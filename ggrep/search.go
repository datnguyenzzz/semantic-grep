package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	peekSize      = 8000
	readerBufSize = 1 << 20 // 1 MiB
	scanBufSize   = 1 << 20 // 1 MiB whole-file pool buffer
)

var (
	cpuWorkers = max(2, runtime.GOMAXPROCS(-1))
	// ioWorkers is set higher to overlap blocking disk read latencies.
	ioWorkers = cpuWorkers * 4
	// walkWorkers scales BFS crawling concurrently across directory structures.
	walkWorkers = cpuWorkers * 2

	readerPool = sync.Pool{
		New: func() any {
			return bufio.NewReaderSize(nil, readerBufSize)
		},
	}

	bufPool = sync.Pool{
		New: func() any {
			b := make([]byte, scanBufSize)
			return &b
		},
	}
)

type SearchKind int

const (
	Literal SearchKind = iota
	Regex
)

type SearchOption struct {
	Kind    SearchKind
	Regex   *Regexp
	Literal []byte
	Pattern string
}

func trimCR(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\r' {
		return line[:n-1]
	}
	return line
}

type fileJob struct {
	path   string
	data   []byte
	bufp   *[]byte // pointer to return to sync.Pool after scanning!
	stream bool    // true if file exceeds 1MB and needs streaming directly from disk
}

func Search(pwds []string, opt *SearchOption, onMatch func(path string, line int, text []byte)) int {
	jobCh := make(chan string, 1024)
	scanCh := make(chan fileJob, cpuWorkers*4)

	var totalMatches int64
	var ioWg sync.WaitGroup
	var cpuWg sync.WaitGroup

	// Do pure literal and regex scanning with no I/O wait times
	for range cpuWorkers {
		cpuWg.Go(func() {
			// Clone and compile a private thread-local CodeSearch regexp per CPU worker!
			workerOpt := *opt
			if opt.Kind == Regex {
				localRe, _ := CompileRegex("(?m)" + opt.Pattern)
				workerOpt.Regex = localRe
			}

			for job := range scanCh {
				if job.stream {
					// Large file streaming bypass: Open and stream directly without using any pool buffer
					f, err := os.Open(job.path)
					if err == nil {
						scanFileStream(job.path, f, &workerOpt, &totalMatches, onMatch)
						f.Close()
					}
					continue
				}

				scanBody(job.path, job.data, &workerOpt, &totalMatches, onMatch)
				// Return buffer back to pool
				if job.bufp != nil {
					bufPool.Put(job.bufp)
				}
			}
		})
	}

	// I/O Producers handle all blocking disk syscalls (os.Open, Read)
	for range ioWorkers {
		ioWg.Go(func() {
			br := readerPool.Get().(*bufio.Reader)
			defer readerPool.Put(br)

			for path := range jobCh {
				info, err := os.Stat(path)
				if err != nil {
					continue
				}
				size := info.Size()
				if size == 0 {
					continue
				}

				if size > scanBufSize {
					// File exceeds 1MB! Do NOT allocate/put it into the buffer pool.
					// Push a direct stream job to the CPU queue instead!
					scanCh <- fileJob{
						path:   path,
						stream: true,
					}
					continue
				}

				// File is small enough to pre-fetch using a pool buffer!
				f, err := os.Open(path)
				if err != nil {
					continue
				}

				br.Reset(f)
				bufp := bufPool.Get().(*[]byte)
				buf := *bufp

				n, err := io.ReadFull(br, buf)
				if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || err == nil {
					scanCh <- fileJob{
						path: path,
						data: buf[:n],
						bufp: bufp,
					}
				} else {
					bufPool.Put(bufp)
				}
				f.Close()
			}
		})
	}

	// Traversal Producer pushes file paths asynchronously
	go func() {
		for _, pwd := range pwds {
			traverseWithGitIgnore(pwd, jobCh)
		}
		close(jobCh)
	}()

	// Sequenced Shutdown
	go func() {
		ioWg.Wait()
		close(scanCh)
	}()

	cpuWg.Wait()
	return int(totalMatches)
}

// scanBody finds matches by sliding bytes.Index over data. Cheap when a
// file has no matches at all — one bytes.Index call returns -1 and we're done.
func scanBody(path string, data []byte, opt *SearchOption, totalMatches *int64, onMatch func(path string, line int, text []byte)) {
	// Binary file check: Look for NUL byte in the first 8000 bytes
	headLimit := min(len(data), peekSize)
	if bytes.IndexByte(data[:headLimit], 0) >= 0 {
		return
	}

	if opt.Kind == Regex {
		scanRegex(path, data, opt, totalMatches, onMatch)
		return
	}

	// High-speed literal match via bytes.Index (Go native SIMD)
	lit := opt.Literal
	lineNum := 1
	cursor := 0
	for {
		idx := bytes.Index(data[cursor:], lit)
		if idx < 0 {
			break
		}
		matchPos := cursor + idx
		// Count newlines from the cursor to the match to calculate the current line number!
		lineNum += bytes.Count(data[cursor:matchPos], []byte{'\n'})

		lineStart := 0
		if i := bytes.LastIndexByte(data[:matchPos], '\n'); i >= 0 {
			lineStart = i + 1
		}
		lineEnd := len(data)
		if i := bytes.IndexByte(data[matchPos:], '\n'); i >= 0 {
			lineEnd = matchPos + i
		}

		line := trimCR(data[lineStart:lineEnd])
		onMatch(path, lineNum, line)
		atomic.AddInt64(totalMatches, 1)

		// Advance past this line so we don't re-match on it
		cursor = lineEnd
		if cursor < len(data) {
			cursor++ // skip the '\n'
			lineNum++
		}
	}
}

func scanRegex(path string, data []byte, opt *SearchOption, totalMatches *int64, onMatch func(path string, line int, text []byte)) {
	lineNum := 1
	cursor := 0

	for cursor < len(data) {
		loc := opt.Regex.FindIndex(data[cursor:])
		if loc == nil {
			break // No more matches in this file
		}
		end := loc[1]

		// The match position relative to the entire file 'data'
		matchEndPos := cursor + end

		// Find the start of the line containing the match
		lineStart := 0
		if i := bytes.LastIndexByte(data[:matchEndPos], '\n'); i >= 0 {
			lineStart = i + 1
		}

		// Find the end of the line containing the match
		lineEnd := len(data)
		if i := bytes.IndexByte(data[matchEndPos-1:], '\n'); i >= 0 {
			lineEnd = matchEndPos - 1 + i
		}

		line := trimCR(data[lineStart:lineEnd])
		// Count newlines from the cursor to lineStart to update lineNum accurately
		lineNum += bytes.Count(data[cursor:lineStart], []byte{'\n'})

		onMatch(path, lineNum, line)
		atomic.AddInt64(totalMatches, 1)

		// Advance past this line so we don't re-match on it
		cursor = lineEnd
		if cursor < len(data) {
			cursor++ // skip the '\n'
			lineNum++
		}
	}
}

// loadGitIgnore reads a .gitignore file and returns a simple list of raw string patterns.
func loadGitIgnore(pwd string) []string {
	var ignores []string
	// Always ignore the .git directory implicitly!
	ignores = append(ignores, ".git")

	f, err := os.Open(filepath.Join(pwd, ".gitignore"))
	if err != nil {
		return ignores
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			line = strings.TrimPrefix(line, "/")
			line = strings.TrimSuffix(line, "/")
			ignores = append(ignores, line)
		}
	}
	return ignores
}

// isIgnored performs a check against the loaded gitignore patterns
// supporting basic wildcards via filepath.Match and path substring matching.
func isIgnored(path string, ignores []string) bool {
	base := filepath.Base(path)
	for _, ig := range ignores {
		// Exact or directory-substring match
		if strings.Contains(path, "/"+ig+"/") || strings.HasSuffix(path, "/"+ig) || path == ig || base == ig {
			return true
		}
		// Glob/wildcard matching for things like "*.log"
		if matched, _ := filepath.Match(ig, base); matched {
			return true
		}
		if matched, _ := filepath.Match(ig, path); matched {
			return true
		}
	}
	return false
}

func traverseWithGitIgnore(pwd string, jobCh chan string) {
	info, err := os.Stat(pwd)
	if err != nil {
		return
	}
	if !info.IsDir() {
		jobCh <- pwd
		return
	}

	var walkWg sync.WaitGroup
	dirCh := make(chan string, 256)

	walkWg.Add(1)
	dirCh <- pwd

	for range walkWorkers {
		go func() {
			for dir := range dirCh {
				ignores := loadGitIgnore(dir)
				entries, err := os.ReadDir(dir)
				if err != nil {
					walkWg.Done()
					continue
				}

				for _, entry := range entries {
					path := filepath.Join(dir, entry.Name())

					if entry.IsDir() {
						if isIgnored(path, ignores) {
							continue
						}
						// Spin off to guarantee zero deadlock on channel block
						walkWg.Add(1)
						go func() {
							dirCh <- path
						}()

						continue
					}

					if isIgnored(path, ignores) {
						continue
					}
					jobCh <- path
				}
				walkWg.Done()
			}
		}()
	}

	walkWg.Wait()
	close(dirCh)
}

// scanFileStream handles files exceeding 1MB by streaming them line-by-line via bufio.Reader
func scanFileStream(path string, f *os.File, opt *SearchOption, totalMatches *int64, onMatch func(path string, line int, text []byte)) {
	br := readerPool.Get().(*bufio.Reader)
	defer readerPool.Put(br)
	br.Reset(f)

	head, _ := br.Peek(peekSize)
	if bytes.IndexByte(head, 0) >= 0 {
		return // Skip binary files
	}

	lineNum := 0
	for {
		line, err := br.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			return // Line exceeds 1MB buffer safely skip it to prevent OOM
		}
		if len(line) > 0 || err == nil {
			lineNum++
			if n := len(line); n > 0 && line[n-1] == '\n' {
				line = line[:n-1]
			}
			line = trimCR(line)

			matched := false
			if opt.Kind == Literal {
				matched = bytes.Contains(line, opt.Literal)
			} else {
				matched = opt.Regex.Match(line)
			}

			if matched {
				onMatch(path, lineNum, line)
				atomic.AddInt64(totalMatches, 1)
			}
		}
		if err != nil {
			break
		}
	}
}

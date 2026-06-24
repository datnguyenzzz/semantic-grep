package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	regexp "github.com/google/codesearch/regexp"
)

const (
	peekSize      = 8000
	readerBufSize = 1 << 20 // 1 MiB
	scanBufSize   = 1 << 20 // 1 MiB whole-file pool buffer
)

var (
	workers = runtime.GOMAXPROCS(-1) * 4

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
	Regex   *regexp.Regexp
	Literal []byte
	Pattern string
}

type SearchResponse struct {
	Path, Text string
	Line       int
}

func trimCR(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\r' {
		return line[:n-1]
	}
	return line
}

// Note: []SearchResponse is non-deterministic
func Search(pwds []string, opt *SearchOption) []SearchResponse {
	jobCh := make(chan string, workers*4)

	// Create a dedicated, private results slice for each worker
	workerResults := make([][]SearchResponse, workers)
	var workerWg sync.WaitGroup

	for i := range workers {
		workerWg.Add(1)
		go func(workerID int) {
			defer workerWg.Done()

			localResp := make([]SearchResponse, 0, 64)

			// Google CodeSearch regex is not thread-safe. Compile a private,
			// dedicated compiled instance per worker!
			workerOpt := *opt
			if opt.Kind == Regex {
				localRe, _ := regexp.Compile(opt.Pattern)
				workerOpt.Regex = localRe
			}

			for path := range jobCh {
				processFile(path, &workerOpt, &localResp)
			}

			// Save the worker's private results back to the global array lock-free
			workerResults[workerID] = localResp
		}(i)
	}

	go func() {
		for _, pwd := range pwds {
			traverseWithGitIgnore(pwd, jobCh)
		}
		close(jobCh)
	}()

	workerWg.Wait()

	// Merge the workers' private slices sequentially in the main thread
	var resp []SearchResponse
	for _, res := range workerResults {
		resp = append(resp, res...)
	}

	return resp
}

func processFile(path string, opt *SearchOption, localResp *[]SearchResponse) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	bufp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bufp)
	buf := *bufp

	n, err := io.ReadFull(f, buf)
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF):
		// File fit in the buffer (possibly empty).
		scanWholeBody(path, buf[:n], opt, localResp)
	case err == nil:
		var probe [1]byte
		if m, _ := f.Read(probe[:]); m == 0 {
			// File was exactly scanBufSize.
			scanWholeBody(path, buf, opt, localResp)
			return
		}
		// File is larger than the pool buffer (1MB); rewind and stream.
		if _, e := f.Seek(0, io.SeekStart); e != nil {
			return
		}
		scanFileStream(path, f, opt, localResp)
	default:
		return
	}
}

// scanWholeBody finds matches by sliding bytes.Index over data. Cheap when a
// file has no matches at all — one bytes.Index call returns -1 and we're done.
func scanWholeBody(path string, data []byte, opt *SearchOption, localResp *[]SearchResponse) {
	// Binary check: Look for NUL byte in the first 8000 bytes
	headLimit := min(len(data), peekSize)
	if bytes.IndexByte(data[:headLimit], 0) >= 0 {
		return
	}

	if opt.Kind == Regex {
		scanWholeRegex(path, data, opt, localResp)
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
		*localResp = append(*localResp, SearchResponse{
			Path: path,
			Line: lineNum,
			Text: string(line),
		})

		// Advance past this line so we don't re-match on it
		cursor = lineEnd
		if cursor < len(data) {
			cursor++ // skip the '\n'
			lineNum++
		}
	}
}

func scanWholeRegex(path string, data []byte, opt *SearchOption, localResp *[]SearchResponse) {
	lineNum := 1
	cursor := 0

	for cursor < len(data) {
		end := opt.Regex.Match(data[cursor:], cursor == 0, true)
		if end < 0 {
			break // No more matches in this file
		}

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

		*localResp = append(*localResp, SearchResponse{
			Path: path,
			Line: lineNum,
			Text: string(line),
		})

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
	ignores := loadGitIgnore(pwd)

	err := filepath.WalkDir(pwd, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		if d.IsDir() {
			if isIgnored(path, ignores) {
				return filepath.SkipDir
			}
			return nil
		}

		if isIgnored(path, ignores) {
			return nil // Skip ignored files
		}

		jobCh <- path
		return nil
	})

	if err != nil {
		log.Printf("error walking directory %s: %s\n", pwd, err)
	}
}

// scanFileStream handles files exceeding 1MB by streaming them line-by-line via bufio.Reader
func scanFileStream(path string, f *os.File, opt *SearchOption, localResp *[]SearchResponse) {
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
				matched = opt.Regex.Match(line, true, true) >= 0
			}

			if matched {
				*localResp = append(*localResp, SearchResponse{
					Path: path,
					Line: lineNum,
					Text: string(line), // allocates string to unpin large buffer slice
				})
			}
		}
		if err != nil {
			break
		}
	}
}

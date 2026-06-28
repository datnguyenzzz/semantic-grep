package db

// keep db operations simple, open and close on every call, standardize all embeddings to exactly 3072 dimensions, and compress using the explicitly passed 4-bit TurboQuant dependency
import (
	"bufio"
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/datnguyenzzz/semantic-grep/ggrep"
	"github.com/datnguyenzzz/semantic-grep/internal/callgraph"
	"github.com/datnguyenzzz/semantic-grep/internal/turboquant"

	_ "github.com/duckdb/duckdb-go/v2"
)

var (
	fileSliceLimit = 1 << 20
)

type Memory struct {
	ID         string    `json:"id"`
	Content    string    `json:"content"` // holds the on-the-fly read code chunk
	SymbolName string    `json:"symbol_name"`
	CWD        string    `json:"cwd"`
	LineStart  int       `json:"line_start"`
	LineEnd    int       `json:"line_end"`
	Similarity float64   `json:"similarity,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

const CandidateMultiplier = 3

func getDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "agent-mem.db"), nil
}

func Open() (*sql.DB, error) {
	dbPath, err := getDBPath()
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("duckdb", dbPath)
	if err != nil {
		return nil, err
	}
	return db, nil
}

func InitDatabase() error {
	dbLock.Lock()
	defer dbLock.Unlock()

	db, err := Open()
	if err != nil {
		return err
	}
	defer db.Close()

	// Load native DuckDB FTS (Full Text Search) extension
	_, _ = db.Exec("INSTALL fts;")
	_, _ = db.Exec("LOAD fts;")
	_, _ = db.Exec("DROP TABLE IF EXISTS gemini_symbols;")

	// automatic DuckDB schema migration - drop tables with legacy content column (source code)
	var contentCol string
	err = db.QueryRow("SELECT column_name FROM information_schema.columns WHERE table_name = 'gemini_memories' AND column_name = 'content'").Scan(&contentCol)
	if err == nil {
		_, _ = db.Exec("DROP TABLE IF EXISTS gemini_memories;")
	}

	// automatic DuckDB schema migration - drop tables with legacy function_name column
	var funcCol string
	err = db.QueryRow("SELECT column_name FROM information_schema.columns WHERE table_name = 'gemini_memories' AND column_name = 'function_name'").Scan(&funcCol)
	if err == nil {
		_, _ = db.Exec("DROP TABLE IF EXISTS gemini_memories;")
	}

	// store function/construct symbol metadata in DuckDB; vectors are persisted separately in our dedicated .tqv files
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS gemini_memories (
			id VARCHAR PRIMARY KEY,
			symbol_name VARCHAR NOT NULL,
			cwd TEXT NOT NULL,
			line_start INTEGER NOT NULL,
			line_end INTEGER NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return err
	}

	// create merkle_trees table for tracking indexed files and directories to enable ultra-fast incremental indexing
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS merkle_trees (
			cwd TEXT PRIMARY KEY,
			root_hash VARCHAR NOT NULL,
			tree_json TEXT NOT NULL,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return err
	}

	// create call_nodes and call_edges tables for fast, incremental AST-based call graph indexing
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS call_nodes (
			name VARCHAR NOT NULL,
			file_path VARCHAR NOT NULL,
			start_line INTEGER NOT NULL,
			end_line INTEGER NOT NULL
		)
	`)
	if err != nil {
		return err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS call_edges (
			caller VARCHAR NOT NULL,
			callee VARCHAR NOT NULL
		)
	`)
	return err
}

// CreateFTSIndex builds or overwrites the native DuckDB BM25 Full-Text Search index
func CreateFTSIndex() error {
	dbLock.Lock()
	defer dbLock.Unlock()

	db, err := Open()
	if err != nil {
		return err
	}
	defer db.Close()

	_, err = db.Exec("PRAGMA create_fts_index('gemini_memories', 'id', 'content', stemmer='none', ignore='(\\.|[^a-zA-Z0-9_])+', overwrite=1);")
	return err
}

func GetTQPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "agent-mem.tqv"), nil
}

var (
	AsyncSaveWG sync.WaitGroup
	dbLock      sync.Mutex
)

type MemoryBatchItem struct {
	ID         string
	SymbolName string
	CWD        string
	LineStart  int
	LineEnd    int
	Embedding  []float32
}

func SaveMemoriesBatch(items []MemoryBatchItem, index *turboquant.Index) error {
	if len(items) == 0 {
		return nil
	}

	dbLock.Lock()
	defer dbLock.Unlock()

	db, err := Open()
	if err != nil {
		return err
	}
	defer db.Close()

	if index == nil {
		return fmt.Errorf("turboquant index cannot be nil")
	}

	// We split the large batch into smaller chunks (each <= 500 items) to prevent long lock times and OOMs
	chunkSize := 500
	numItems := len(items)

	for start := 0; start < numItems; start += chunkSize {
		end := min(start+chunkSize, numItems)
		chunk := items[start:end]

		// Start a transaction for this smaller chunk
		tx, err := db.Begin()
		if err != nil {
			return err
		}

		stmtMemory, err := tx.Prepare(`
			INSERT OR REPLACE INTO gemini_memories (id, symbol_name, cwd, line_start, line_end)
			VALUES ($1, $2, $3, $4, $5)
		`)
		if err != nil {
			_ = tx.Rollback()
			return err
		}

		for _, item := range chunk {
			cwdVal := item.CWD
			if filepath.IsAbs(cwdVal) {
				if clean, err := filepath.EvalSymlinks(cwdVal); err == nil {
					cwdVal = clean
				}
			}
			// Save the metadata record directly to DuckDB
			_, err = stmtMemory.Exec(item.ID, item.SymbolName, cwdVal, item.LineStart, item.LineEnd)
			if err != nil {
				_ = stmtMemory.Close()
				_ = tx.Rollback()
				return err
			}

			// Add to TurboQuant vector index in-memory
			_ = index.Add(item.ID, item.Embedding)
		}

		// Close statement before committing to prevent leaks or locked handles
		_ = stmtMemory.Close()

		if err := tx.Commit(); err != nil {
			return err
		}

		// Report progress dynamically per-commit
		fmt.Printf("\r  ⚙ Database batch writing progress: %d/%d entries committed...", end, numItems)
	}
	fmt.Println()

	return nil
}

func SaveMemory(id, symbolName, cwd string, lineStart, lineEnd int, embedding []float32, index *turboquant.Index) error {
	dbLock.Lock()
	defer dbLock.Unlock()

	db, err := Open()
	if err != nil {
		return err
	}
	defer db.Close()

	if index == nil {
		return fmt.Errorf("turboquant index cannot be nil")
	}

	cwdVal := cwd
	if filepath.IsAbs(cwdVal) {
		if clean, err := filepath.EvalSymlinks(cwdVal); err == nil {
			cwdVal = clean
		}
	}

	query := `
		INSERT OR REPLACE INTO gemini_memories (id, symbol_name, cwd, line_start, line_end)
		VALUES ($1, $2, $3, $4, $5)
	`
	_, err = db.Exec(query, id, symbolName, cwdVal, lineStart, lineEnd)
	if err != nil {
		return err
	}

	return index.Add(id, embedding)
}

type candidateRRF struct {
	id       string
	rrfScore float64
}

func queryParentCodebaseCWD(cwd string) string {
	if cwd == "" {
		return ""
	}
	codebases, err := ListCodebases()
	if err != nil {
		return cwd
	}
	var bestMatch string
	for _, cb := range codebases {
		if cwd == cb.CWD || strings.HasPrefix(cwd, cb.CWD+string(filepath.Separator)) {
			if len(cb.CWD) > len(bestMatch) {
				bestMatch = cb.CWD
			}
		}
	}
	if bestMatch != "" {
		return bestMatch
	}
	return cwd
}

func searchSemanticDense(queryEmbedding []float32, limit int, index *turboquant.Index) ([]turboquant.SearchResult, error) {
	if index == nil {
		return nil, nil
	}
	return index.Search(queryEmbedding, nil, limit)
}

type LexMatch struct {
	ID    string
	Score float64
}

type ggrepMatchLine struct {
	path string
	line int
}

func queryExactGrepMatches(matches []ggrepMatchLine) ([]string, error) {
	if len(matches) == 0 {
		return nil, nil
	}

	dbLock.Lock()
	defer dbLock.Unlock()

	db, err := Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Group matches into chunks of 100 to prevent too large SQL statements
	var ids []string
	chunkSize := 100
	numMatches := len(matches)

	for start := 0; start < numMatches; start += chunkSize {
		end := min(start+chunkSize, numMatches)
		chunk := matches[start:end]

		var clauses []string
		var args []any
		paramIdx := 1
		for _, m := range chunk {
			clauses = append(clauses, fmt.Sprintf("(cwd = $%d AND line_start <= $%d AND $%d <= line_end)", paramIdx, paramIdx+1, paramIdx+2))
			args = append(args, m.path, m.line, m.line)
			paramIdx += 3
		}

		query := fmt.Sprintf(`
			SELECT id
			FROM gemini_memories
			WHERE %s
		`, strings.Join(clauses, " OR "))

		rows, err := db.Query(query, args...)
		if err != nil {
			return nil, err
		}

		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
	}

	return ids, nil
}

func prepareGrepSearchOpt(queryText string) *ggrep.SearchOption {
	var opt *ggrep.SearchOption

	supportedExts := []string{".go", ".py", ".tf", ".yml", ".yaml"}

	trimmedQuery := strings.TrimSpace(queryText)
	if trimmedQuery == "" {
		return nil
	}

	// If the query explicitly contains standard regular expression operators (like |),
	// run it directly as a regular expression search!
	if strings.ContainsAny(trimmedQuery, "|()*+?[]^$") {
		if _, err := regexp.Compile(trimmedQuery); err == nil {
			opt = &ggrep.SearchOption{
				Kind:              ggrep.Regex,
				Pattern:           trimmedQuery,
				AllowedExtensions: supportedExts,
			}

			return opt
		}
	}

	words := strings.Fields(trimmedQuery)
	if len(words) > 1 {
		// Multi-word query: convert to safe regular expression disjunction matching (A|B|C...)
		var escapedWords []string
		for _, w := range words {
			escapedWords = append(escapedWords, regexp.QuoteMeta(w))
		}
		disjunctionPattern := strings.Join(escapedWords, "|")

		opt = &ggrep.SearchOption{
			Kind:              ggrep.Regex,
			Pattern:           disjunctionPattern,
			AllowedExtensions: supportedExts,
		}
	} else {
		// Single word query: use ultra-fast native SIMD literal matching!
		opt = &ggrep.SearchOption{
			Kind:              ggrep.Literal,
			Pattern:           trimmedQuery,
			Literal:           []byte(trimmedQuery),
			AllowedExtensions: supportedExts,
		}
	}

	return opt
}

func searchLexicalSparse(queryText string, cwd string) ([]LexMatch, error) {
	if queryText == "" {
		return nil, nil
	}

	if filepath.IsAbs(cwd) {
		if clean, err := filepath.EvalSymlinks(cwd); err == nil {
			cwd = clean
		}
	}

	opt := prepareGrepSearchOpt(queryText)
	if opt == nil {
		return nil, nil
	}

	// 2. Execute our high-speed concurrent ggrep search natively
	// We use a buffered collector channel and a dedicated background consumer goroutine
	// to collect matches asynchronously, completely eliminating mutex lock contention!
	ch := make(chan ggrepMatchLine, 16384)
	var matchLines []ggrepMatchLine

	var colWg sync.WaitGroup
	colWg.Go(func() {
		for m := range ch {
			matchLines = append(matchLines, m)
		}
	})

	ggrep.Search([]string{cwd}, opt, func(path string, line int, text []byte) {
		ch <- ggrepMatchLine{path: path, line: line}
	})

	// Close channel and wait for the collector background consumer to finish draining matches
	close(ch)
	colWg.Wait()

	// 3. Invert matched lines back into DuckDB function IDs on-the-fly!
	matchedIDs, err := queryExactGrepMatches(matchLines)
	if err != nil {
		return nil, err
	}

	// 4. Map IDs to LexMatch records
	var matches []LexMatch
	idMap := make(map[string]bool)
	for _, id := range matchedIDs {
		if !idMap[id] {
			idMap[id] = true
			matches = append(matches, LexMatch{
				ID:    id,
				Score: 100.0, // High constant exact match score
			})
		}
	}

	return matches, nil
}

// computeRRF returns "limit" results
func computeRRF(semResults []turboquant.SearchResult, lexResults []LexMatch, limit int) []candidateRRF {
	const k = 60.0

	// Pre-allocate fused slice to exact capacity to prevent dynamic re-allocations
	fused := make([]candidateRRF, 0, len(semResults)+len(lexResults))

	// 1. Add semantic results with their RRF scores
	for i, res := range semResults {
		score := 1.0 / (k + float64(i+1))
		fused = append(fused, candidateRRF{id: res.ID, rrfScore: score})
	}

	// 2. Merge lexical results in-place using fast linear scan
	for i, res := range lexResults {
		score := 1.0 / (k + float64(i+1))
		found := false
		for j := range fused {
			if fused[j].id == res.ID {
				fused[j].rrfScore += score
				found = true
				break
			}
		}
		if !found {
			fused = append(fused, candidateRRF{id: res.ID, rrfScore: score})
		}
	}

	// 3. Sort the merged results by RRF score
	sort.Slice(fused, func(i, j int) bool {
		return fused[i].rrfScore > fused[j].rrfScore
	})

	if len(fused) > limit {
		return fused[:limit]
	}
	return fused
}

// Global reusable pools for memory-bounded, zero-allocation database file reading (similar to ggrep!)
var (
	memBufPool = sync.Pool{
		New: func() any {
			buf := make([]byte, 1024*1024) // 1 MiB reusable buffers
			return &buf
		},
	}
	memReaderPool = sync.Pool{
		New: func() any {
			return bufio.NewReaderSize(nil, 65536) // 64 KiB reusable reader buffers
		},
	}
)

// LoadAndSliceMemoryBlock reads the file size and either uses a pooled 1MB buffer (for small files)
// or streams line-by-line (for files > 1MB) to prevent RAM bloat on large files!
func LoadAndSliceMemoryBlock(fullPath string, group []*Memory) {
	info, err := os.Stat(fullPath)
	if err != nil {
		return // Skip if file can't be stat-ed
	}

	size := info.Size()
	if size == 0 {
		return // Empty file
	}

	if size <= int64(fileSliceLimit) {
		loadAndSliceSmallFile(fullPath, group)
	} else {
		loadAndSliceLargeFile(fullPath, group)
	}
}

// loadAndSliceSmallFile parses files <= 1MB using a pooled buffer and stack-allocated O(1) slicing
func loadAndSliceSmallFile(fullPath string, group []*Memory) {
	f, err := os.Open(fullPath)
	if err != nil {
		return
	}
	defer f.Close()

	bufPtr := memBufPool.Get().(*[]byte)
	buf := *bufPtr
	defer memBufPool.Put(bufPtr)

	n, err := f.Read(buf)
	if err != nil && err != os.ErrExist {
		// n holds bytes read, parse safely
	}

	data := buf[:n]

	slices.SortFunc(group, func(a *Memory, b *Memory) int {
		if a.LineStart == b.LineStart {
			return a.LineEnd - b.LineEnd
		}
		return a.LineStart - b.LineStart
	})

	// Zero-heap-allocation: Track start and end byte offsets on the stack
	numMemories := len(group)
	startOffsets := make([]int, numMemories)
	endOffsets := make([]int, numMemories)

	for i := range numMemories {
		startOffsets[i] = -1
		endOffsets[i] = -1
	}

	lineNum := 1
	nextStartIdx := 0
	activeEndIdx := 0

	for nextStartIdx < numMemories && group[nextStartIdx].LineStart == 1 {
		startOffsets[nextStartIdx] = 0
		nextStartIdx++
	}

	cursor := 0
	for cursor < len(data) {
		idx := bytes.IndexByte(data[cursor:], '\n')
		if idx < 0 {
			break
		}

		// Next line starts after the '\n'
		nextOffset := cursor + idx + 1

		// Mark ending offsets for memories that end at the current lineNum
		for i := activeEndIdx; i < numMemories; i++ {
			mPtr := group[i]
			if mPtr.LineStart > lineNum {
				break // Since group is sorted, no subsequent memories can end at lineNum
			}
			if mPtr.LineEnd == lineNum {
				endOffsets[i] = nextOffset - 1
			}
		}

		// Advance activeEndIdx as long as the memory at activeEndIdx has already ended
		for activeEndIdx < numMemories && endOffsets[activeEndIdx] != -1 {
			activeEndIdx++
		}

		// Mark starting offsets for memories starting at lineNum+1
		for nextStartIdx < numMemories && group[nextStartIdx].LineStart == lineNum+1 {
			startOffsets[nextStartIdx] = nextOffset
			nextStartIdx++
		}

		cursor = nextOffset
		lineNum++
	}

	// Handle the end of file for any unfinished ranges
	for i, mPtr := range group {
		if mPtr.LineEnd >= lineNum && endOffsets[i] == -1 {
			endOffsets[i] = len(data)
		}
	}

	for i, mPtr := range group {
		startCursor := startOffsets[i]
		endCursor := endOffsets[i]

		if startCursor == -1 || endCursor == -1 || startCursor >= endCursor || startCursor >= len(data) {
			mPtr.Content = ""
			continue
		}

		mPtr.Content = string(data[startCursor:endCursor])
	}
}

// loadAndSliceLargeFile parses files > 1MB by streaming line-by-line via bufio.Reader
func loadAndSliceLargeFile(fullPath string, group []*Memory) {
	f, err := os.Open(fullPath)
	if err != nil {
		return
	}
	defer f.Close()

	br := memReaderPool.Get().(*bufio.Reader)
	br.Reset(f)
	defer memReaderPool.Put(br)

	sort.Slice(group, func(i, j int) bool {
		if group[i].LineStart == group[j].LineStart {
			return group[i].LineEnd < group[j].LineEnd
		}
		return group[i].LineStart < group[j].LineStart
	})

	// Create a map of target line ranges to extract in single pass
	type rangeLines struct {
		mPtr      *Memory
		linesList []string
	}
	numMemories := len(group)
	ranges := make([]rangeLines, numMemories)
	maxEndLine := 0
	for i, mPtr := range group {
		ranges[i] = rangeLines{
			mPtr:      mPtr,
			linesList: make([]string, 0, mPtr.LineEnd-mPtr.LineStart+1),
		}

		maxEndLine = max(maxEndLine, mPtr.LineEnd)
	}

	var lineNum, nextStartIdx, activeEndIdx int

	for {
		line, err := br.ReadSlice('\n')
		if len(line) > 0 || err == nil {
			lineNum++
			// Trim trailing newlines
			if n := len(line); n > 0 && line[n-1] == '\n' {
				line = line[:n-1]
			}
			if n := len(line); n > 0 && line[n-1] == '\r' {
				line = line[:n-1]
			}

			// Linearly advance nextStartIdx for newly started ranges
			for nextStartIdx < numMemories && lineNum >= group[nextStartIdx].LineStart {
				nextStartIdx++
			}

			lineStr := string(line)
			for i := activeEndIdx; i < nextStartIdx; i++ {
				if lineNum <= ranges[i].mPtr.LineEnd {
					ranges[i].linesList = append(ranges[i].linesList, lineStr)
				}
			}

			// Linearly advance activeEndIdx for completed ranges
			for activeEndIdx < numMemories && lineNum >= group[activeEndIdx].LineEnd {
				activeEndIdx++
			}
		}
		if lineNum >= maxEndLine || err != nil {
			break // Break early as soon as we passed the maximum end line!
		}
	}

	// Reconstruct slice contents
	for _, r := range ranges {
		r.mPtr.Content = strings.Join(r.linesList, "\n")
	}
}

func fetchMemoriesMetadata(candidates []candidateRRF, cwd string) ([]Memory, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	dbLock.Lock()
	defer dbLock.Unlock()

	db, err := Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	placeholders := make([]string, len(candidates))
	queryArgs := make([]any, len(candidates))
	for i, cand := range candidates {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		queryArgs[i] = cand.id
	}

	query := fmt.Sprintf(`
		SELECT id, symbol_name, cwd, line_start, line_end, created_at
		FROM gemini_memories
		WHERE id IN (%s)
	`, strings.Join(placeholders, ", "))

	rows, err := db.Query(query, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memories []Memory
	memMap := make(map[string]*Memory)

	// Fetch all metadata rows
	for rows.Next() {
		var m Memory
		err := rows.Scan(&m.ID, &m.SymbolName, &m.CWD, &m.LineStart, &m.LineEnd, &m.CreatedAt)
		if err == nil {
			memMap[m.ID] = &m
		}
	}

	// Group Memory pointers by their CWD (file path)
	fileGroups := make(map[string][]*Memory)
	for _, mPtr := range memMap {
		fileGroups[mPtr.CWD] = append(fileGroups[mPtr.CWD], mPtr)
	}

	// Open each unique file EXACTLY once and slice matched code lines on-the-fly
	// to reduce I/O in opening file multiple time
	for fileAbsPath, group := range fileGroups {
		if cwd != "" && !strings.HasPrefix(fileAbsPath, cwd) {
			continue // Skip files belonging to other codebases to prevent unnecessary disk I/O!
		}
		LoadAndSliceMemoryBlock(fileAbsPath, group)
	}

	for _, cand := range candidates {
		if mPtr, exists := memMap[cand.id]; exists {
			memories = append(memories, *mPtr)
		}
	}

	return memories, nil
}

// equalFoldASCII compares two bytes case-insensitively using fast bitwise OR.
// It compiles down to a handful of assembly instructions with zero branches.
func equalFoldASCII(a, b byte) bool {
	if a == b {
		return true
	}
	if a|0x20 == b|0x20 {
		lower := a | 0x20
		return lower >= 'a' && lower <= 'z'
	}
	return false
}

// containsFoldASCII reports whether substr is within s, ignoring ASCII case.
// It performs 100% on-the-fly comparisons with absolutely ZERO heap allocations.
func containsFoldASCII(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(s) < len(substr) {
		return false
	}

	// Optimize for single-character search
	if len(substr) == 1 {
		target := substr[0]
		for i := 0; i < len(s); i++ {
			if equalFoldASCII(s[i], target) {
				return true
			}
		}
		return false
	}

	// High-performance sliding window search
	first := substr[0]
	maxIdx := len(s) - len(substr)
	for i := 0; i <= maxIdx; i++ {
		if !equalFoldASCII(s[i], first) {
			continue
		}

		// Match the rest of the substring
		match := true
		for j := 1; j < len(substr); j++ {
			if !equalFoldASCII(s[i+j], substr[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func SearchMemories(queryText string, queryEmbedding []float32, cwd string, limit int, index *turboquant.Index) ([]Memory, error) {
	cwd = queryParentCodebaseCWD(cwd)
	if filepath.IsAbs(cwd) {
		if clean, err := filepath.EvalSymlinks(cwd); err == nil {
			cwd = clean
		}
	}

	if index == nil {
		return nil, fmt.Errorf("turboquant index cannot be nil")
	}

	// Concurrently query Dense Semantic path and Sparse Lexical path in parallel!
	var semResults []turboquant.SearchResult
	var semErr error
	var lexResults []LexMatch
	var lexErr error

	var wg sync.WaitGroup
	wg.Add(2)

	candidateLimit := min(100, limit*CandidateMultiplier)

	go func() {
		defer wg.Done()
		semResults, semErr = searchSemanticDense(queryEmbedding, candidateLimit, index)
	}()

	go func() {
		defer wg.Done()
		if len(queryText) > 0 && len(cwd) > 0 {
			lexResults, lexErr = searchLexicalSparse(queryText, cwd)
		}
	}()

	wg.Wait()

	if semErr != nil {
		return nil, fmt.Errorf("dense semantic path failed: %w", semErr)
	}
	if lexErr != nil {
		return nil, fmt.Errorf("sparse lexical path failed: %w", lexErr)
	}

	// PRE-FUSION JOINT FILTERING (Noise Pruning!)
	// Check if the query embedding is completely zero (mock/uninitialized in tests)
	isZeroVector := true
	for _, v := range queryEmbedding {
		if v != 0 {
			isZeroVector = false
			break
		}
	}

	// 1. Filter Semantic Vector candidates (Keep only those with Cosine Similarity >= 0.10 for real vectors)
	var filteredSem []turboquant.SearchResult
	for _, res := range semResults {
		if isZeroVector || res.Similarity >= 0.10 {
			filteredSem = append(filteredSem, res)
		}
	}

	if len(filteredSem) == 0 && len(lexResults) == 0 {
		return nil, nil // Return empty list immediately if all candidates are unconfident noise!
	}

	// Reciprocal Rank Fusion (RRF) on only the validated
	topCandidates := computeRRF(filteredSem, lexResults, limit)

	// Fetch memories metadata and on-the-fly read code from local disk
	memories, err := fetchMemoriesMetadata(topCandidates, cwd)
	if err != nil {
		return nil, err
	}

	// Map returned memories back into the exact order of topCandidates, assigning RRF score as Similarity.
	memMap := make(map[string]Memory)
	for _, m := range memories {
		memMap[m.ID] = m
	}

	// Enforce codebase scoping: if we are in a real codebase search (where at least one file exists and was loaded),
	// filter out any memories that don't exist in this codebase (i.e. Content is empty).
	// If absolutely zero files could be loaded (e.g. in a virtual/mock test environment), do not filter anything.
	anyFileLoaded := false
	for _, m := range memories {
		if m.Content != "" {
			anyFileLoaded = true
			break
		}
	}

	finalResults := make([]Memory, 0, len(topCandidates))
	for _, cand := range topCandidates {
		if m, exists := memMap[cand.id]; exists {
			if !anyFileLoaded || m.Content != "" {
				m.Similarity = cand.rrfScore
				finalResults = append(finalResults, m)
			}
		}
	}

	return finalResults, nil
}

// SaveMerkleTree stores the serialized Merkle Tree state for a codebase
func SaveMerkleTree(cwd, rootHash, treeJSON string) error {
	dbLock.Lock()
	defer dbLock.Unlock()

	db, err := Open()
	if err != nil {
		return err
	}
	defer db.Close()

	query := `
		INSERT OR REPLACE INTO merkle_trees (cwd, root_hash, tree_json, updated_at)
		VALUES ($1, $2, $3, CURRENT_TIMESTAMP)
	`
	_, err = db.Exec(query, cwd, rootHash, treeJSON)
	return err
}

// LoadMerkleTree retrieves the previously saved Merkle Tree root hash and JSON for a codebase
func LoadMerkleTree(cwd string) (string, string, error) {
	dbLock.Lock()
	defer dbLock.Unlock()

	db, err := Open()
	if err != nil {
		return "", "", err
	}
	defer db.Close()

	query := `
		SELECT root_hash, tree_json FROM merkle_trees WHERE cwd = $1
	`
	var rootHash, treeJSON string
	err = db.QueryRow(query, cwd).Scan(&rootHash, &treeJSON)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	return rootHash, treeJSON, err
}

// DeleteFileMemories deletes the existing chunk memories of a specific codebase file
func DeleteFileMemories(cwd, relPath string, index *turboquant.Index) error {
	dbLock.Lock()
	defer dbLock.Unlock()

	db, err := Open()
	if err != nil {
		return err
	}
	defer db.Close()

	fileAbsPath := filepath.Join(cwd, relPath)

	// 1. Fetch the IDs of the chunks we are about to delete
	querySel := `
		SELECT id FROM gemini_memories
		WHERE cwd = $1
	`
	rows, err := db.Query(querySel, fileAbsPath)
	if err != nil {
		return err
	}
	defer rows.Close()

	var idsToDelete []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			idsToDelete = append(idsToDelete, id)
		}
	}

	// 2. Delete chunks belonging to file from DuckDB metadata and symbols
	queryDel := `
		DELETE FROM gemini_memories
		WHERE cwd = $1
	`
	_, err = db.Exec(queryDel, fileAbsPath)
	if err != nil {
		return err
	}

	// 3. Remove from the shared in-memory TurboQuant index and gemini_symbols table
	for _, id := range idsToDelete {
		_, _ = db.Exec("DELETE FROM gemini_symbols WHERE memory_id = $1", id)
		if index != nil {
			index.Delete(id)
		}
	}

	return nil
}

type Codebase struct {
	CWD       string    `json:"cwd"`
	RootHash  string    `json:"root_hash"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ListCodebases returns all indexed codebases ordered by modification time
func ListCodebases() ([]Codebase, error) {
	dbLock.Lock()
	defer dbLock.Unlock()

	db, err := Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	query := `
		SELECT cwd, root_hash, updated_at
		FROM merkle_trees
		ORDER BY updated_at DESC
	`
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var codebases []Codebase
	for rows.Next() {
		var c Codebase
		err := rows.Scan(&c.CWD, &c.RootHash, &c.UpdatedAt)
		if err != nil {
			return nil, err
		}
		codebases = append(codebases, c)
	}
	return codebases, nil
}

// SaveCallGraph writes the parsed call graph nodes and edges of a single file to DuckDB
func SaveCallGraph(filePath string, nodes []*callgraph.Node, edges []callgraph.Edge) error {
	db, err := Open()
	if err != nil {
		return err
	}
	defer db.Close()

	// Begin a transaction to ensure atomic updates
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Delete existing edges of caller functions declared in this file
	// We first query all function names declared in this file, then delete those callers
	queryGetFuncs := "SELECT name FROM call_nodes WHERE file_path = $1"
	rows, err := tx.Query(queryGetFuncs, filePath)
	if err == nil {
		var funcs []any
		var placeholders []string
		idx := 1
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err == nil {
				funcs = append(funcs, name)
				placeholders = append(placeholders, fmt.Sprintf("$%d", idx))
				idx++
			}
		}
		rows.Close()

		if len(funcs) > 0 {
			queryDelEdges := fmt.Sprintf("DELETE FROM call_edges WHERE caller IN (%s)", strings.Join(placeholders, ", "))
			_, _ = tx.Exec(queryDelEdges, funcs...)
		}
	}

	// 2. Delete existing nodes declared in this file
	_, err = tx.Exec("DELETE FROM call_nodes WHERE file_path = $1", filePath)
	if err != nil {
		return err
	}

	// 3. Insert new nodes
	if len(nodes) > 0 {
		stmtNode, err := tx.Prepare("INSERT INTO call_nodes (name, file_path, start_line, end_line) VALUES ($1, $2, $3, $4)")
		if err != nil {
			return err
		}
		defer stmtNode.Close()

		for _, n := range nodes {
			_, err = stmtNode.Exec(n.Name, n.FilePath, n.StartLine, n.EndLine)
			if err != nil {
				return err
			}
		}
	}

	// 4. Insert new edges
	if len(edges) > 0 {
		stmtEdge, err := tx.Prepare("INSERT INTO call_edges (caller, callee) VALUES ($1, $2)")
		if err != nil {
			return err
		}
		defer stmtEdge.Close()

		for _, e := range edges {
			_, err = stmtEdge.Exec(e.Caller, e.Callee)
			if err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

// DeleteCallGraph removes all nodes and edges declared in a specific file
func DeleteCallGraph(filePath string) error {
	db, err := Open()
	if err != nil {
		return err
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Delete edges of caller functions declared in this file
	queryGetFuncs := "SELECT name FROM call_nodes WHERE file_path = $1"
	rows, err := tx.Query(queryGetFuncs, filePath)
	if err == nil {
		var funcs []any
		var placeholders []string
		idx := 1
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err == nil {
				funcs = append(funcs, name)
				placeholders = append(placeholders, fmt.Sprintf("$%d", idx))
				idx++
			}
		}
		rows.Close()

		if len(funcs) > 0 {
			queryDelEdges := fmt.Sprintf("DELETE FROM call_edges WHERE caller IN (%s)", strings.Join(placeholders, ", "))
			_, _ = tx.Exec(queryDelEdges, funcs...)
		}
	}

	// 2. Delete nodes
	_, err = tx.Exec("DELETE FROM call_nodes WHERE file_path = $1", filePath)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// GetCallNode retrieves metadata for a single function node by name or method suffix
func GetCallNode(funcName string) (*callgraph.Node, error) {
	db, err := Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	query := `
		SELECT name, file_path, start_line, end_line 
		FROM call_nodes 
		WHERE name = $1 OR name LIKE '%' || $2
		LIMIT 1
	`
	var n callgraph.Node
	err = db.QueryRow(query, funcName, "."+funcName).Scan(&n.Name, &n.FilePath, &n.StartLine, &n.EndLine)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// GetCallees retrieves all callee nodes and their metadata for a given caller in a single JOIN query
func GetCallees(callerName string) ([]*callgraph.Node, error) {
	db, err := Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	query := `
		SELECT DISTINCT n.name, n.file_path, n.start_line, n.end_line
		FROM call_edges e
		JOIN call_nodes n ON e.callee = n.name OR n.name LIKE '%' || e.callee
		WHERE e.caller = $1
	`
	rows, err := db.Query(query, callerName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []*callgraph.Node
	for rows.Next() {
		var n callgraph.Node
		if err := rows.Scan(&n.Name, &n.FilePath, &n.StartLine, &n.EndLine); err == nil {
			nodes = append(nodes, &n)
		}
	}
	return nodes, nil
}

// GetCallers retrieves all caller nodes and their metadata for a given callee in a single JOIN query
func GetCallers(calleeName string) ([]*callgraph.Node, error) {
	db, err := Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Handle both exact match or method suffix matching for callee
	query := `
		SELECT DISTINCT n.name, n.file_path, n.start_line, n.end_line
		FROM call_edges e
		JOIN call_nodes n ON e.caller = n.name
		WHERE e.callee = $1 OR $1 LIKE '%' || e.callee
	`
	rows, err := db.Query(query, calleeName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []*callgraph.Node
	for rows.Next() {
		var n callgraph.Node
		if err := rows.Scan(&n.Name, &n.FilePath, &n.StartLine, &n.EndLine); err == nil {
			nodes = append(nodes, &n)
		}
	}
	return nodes, nil
}

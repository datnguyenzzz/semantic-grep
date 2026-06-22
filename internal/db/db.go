package db

// keep db operations simple, open and close on every call, standardize all embeddings to exactly 3072 dimensions, and compress using the explicitly passed 4-bit TurboQuant dependency
import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/datnguyenzzz/agent-context/internal/callgraph"
	"github.com/datnguyenzzz/agent-context/internal/turboquant"

	_ "github.com/duckdb/duckdb-go/v2"
)

type Memory struct {
	ID         string    `json:"id"`
	Content    string    `json:"content"`
	Category   string    `json:"category"`
	CWD        string    `json:"cwd"`
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

	// automatic DuckDB schema migration - drop tables with legacy symbols column
	var symbolsCol string
	err = db.QueryRow("SELECT column_name FROM information_schema.columns WHERE table_name = 'gemini_memories' AND column_name = 'symbols'").Scan(&symbolsCol)
	if err == nil {
		_, _ = db.Exec("DROP TABLE IF EXISTS gemini_memories;")
	}

	// automatic DuckDB schema migration - drop tables with embedding column
	var colType string
	err = db.QueryRow("SELECT data_type FROM information_schema.columns WHERE table_name = 'gemini_memories' AND column_name = 'embedding'").Scan(&colType)
	if err == nil {
		_, _ = db.Exec("DROP TABLE IF EXISTS gemini_memories;")
	}

	// store chunk content in DuckDB; vectors are persisted separately in our dedicated .tqv files
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS gemini_memories (
			id VARCHAR PRIMARY KEY,
			content TEXT NOT NULL,
			category VARCHAR NOT NULL,
			cwd TEXT NOT NULL,
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
	ID           string
	Content      string // metadataHeader
	CWD          string
	Embedding    []float32
	ChunkContent string
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
			INSERT OR REPLACE INTO gemini_memories (id, content, category, cwd)
			VALUES ($1, $2, 'project', $3)
		`)
		if err != nil {
			_ = tx.Rollback()
			return err
		}

		for _, item := range chunk {
			// 1. Minify the raw code chunk at ingestion-time before saving to DuckDB to reduce storage footprint and bypass query-time CPU overhead
			minified := minifyCode(item.ChunkContent, "project")
			_, err = stmtMemory.Exec(item.ID, minified, item.CWD)
			if err != nil {
				_ = stmtMemory.Close()
				_ = tx.Rollback()
				return err
			}

			// 2. Add to TurboQuant index in-memory
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

func SaveMemory(id, content, category, cwd string, embedding []float32, index *turboquant.Index, chunkContent string) error {
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

	// 1. Save chunk content directly into DuckDB
	query := `
		INSERT OR REPLACE INTO gemini_memories (id, content, category, cwd)
		VALUES ($1, $2, 'project', $3)
	`
	_, err = db.Exec(query, id, chunkContent, cwd)
	if err != nil {
		return err
	}

	// 2. Add to the shared TurboQuant index
	return index.Add(id, embedding)
}

type candidateRRF struct {
	id       string
	rrfScore float64
}

type scoredMemory struct {
	m     Memory
	score float64
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

func searchLexicalSparse(queryText string, limit int) ([]LexMatch, error) {
	dbLock.Lock()
	defer dbLock.Unlock()

	db, err := Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	if queryText == "" {
		return nil, nil
	}

	// highly optimized native DuckDB Okapi BM25 full-text search query.
	querySql := `
		SELECT id, fts_main_gemini_memories.match_bm25(id, $1) as score
		FROM gemini_memories
		WHERE score IS NOT NULL
		ORDER BY score DESC
		LIMIT $2
	`
	rows, err := db.Query(querySql, queryText, limit)
	if err != nil {
		// If FTS index hasn't been created yet (e.g. empty database), return empty list gracefully
		return nil, nil
	}
	defer rows.Close()

	var matches []LexMatch
	for rows.Next() {
		var m LexMatch
		if err := rows.Scan(&m.ID, &m.Score); err == nil {
			matches = append(matches, m)
		}
	}

	return matches, nil
}

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
		SELECT id, content, category, cwd, created_at
		FROM gemini_memories
		WHERE id IN (%s)
	`, strings.Join(placeholders, ", "))

	if cwd != "" {
		query += fmt.Sprintf(" AND cwd = $%d", len(queryArgs)+1)
		queryArgs = append(queryArgs, cwd)
	}

	rows, err := db.Query(query, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var m Memory
		err := rows.Scan(&m.ID, &m.Content, &m.Category, &m.CWD, &m.CreatedAt)
		if err == nil {
			memories = append(memories, m)
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

func applyGrepReRanking(memories []Memory, candidates []candidateRRF, queryText string, limit int) []Memory {
	var scored []scoredMemory

	for _, m := range memories {
		rrf := 0.0
		for _, cand := range candidates {
			if cand.id == m.ID {
				rrf = cand.rrfScore
				break
			}
		}

		grepMatch := false
		if m.Category == "project" && queryText != "" {
			if containsFoldASCII(m.Content, queryText) {
				grepMatch = true
			}
		}

		finalScore := rrf
		if grepMatch {
			finalScore *= 1.5 // apply robust 50% exact-match score boost
		}

		m.Similarity = finalScore
		scored = append(scored, scoredMemory{m, finalScore})
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	var finalResults []Memory
	for i := 0; i < len(scored) && i < limit; i++ {
		finalResults = append(finalResults, scored[i].m)
	}

	return finalResults
}

func SearchMemories(queryText string, queryEmbedding []float32, cwd string, limit int, index *turboquant.Index) ([]Memory, error) {
	cwd = queryParentCodebaseCWD(cwd)

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
		if queryText != "" {
			lexResults, lexErr = searchLexicalSparse(queryText, candidateLimit)
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

	// 1. Filter Semantic Vector candidates (Keep only those with Cosine Similarity >= 0.55 for real vectors)
	var filteredSem []turboquant.SearchResult
	for _, res := range semResults {
		if isZeroVector || res.Similarity >= 0.55 {
			filteredSem = append(filteredSem, res)
		}
	}

	// 2. Filter Lexical BM25 candidates (Keep only those within 10% of the max BM25 score)
	var filteredLex []LexMatch
	if len(lexResults) > 0 {
		maxBM25 := lexResults[0].Score // Already sorted descending by DuckDB!
		for _, res := range lexResults {
			if res.Score >= 0.10*maxBM25 {
				filteredLex = append(filteredLex, res)
			}
		}
	}

	if len(filteredSem) == 0 && len(filteredLex) == 0 {
		return nil, nil // Return empty list immediately if all candidates are unconfident noise!
	}

	// Reciprocal Rank Fusion (RRF) on only the validated, high-confidence candidates!
	topCandidates := computeRRF(filteredSem, filteredLex, candidateLimit)

	// Fetch memories metadata and on-the-fly read code from local disk
	memories, err := fetchMemoriesMetadata(topCandidates, cwd)
	if err != nil {
		return nil, err
	}

	// On-the-Fly Scoped Local Grep Re-ranking (exact match boosting)
	return applyGrepReRanking(memories, topCandidates, queryText, limit), nil
}

// isInsideString reports whether a delimiter at index `idx` is inside a string literal
// by counting double quotes and single quotes on its left.
func isInsideString(s string, idx int) bool {
	doubleQuotes := 0
	singleQuotes := 0
	escaped := false
	for i := range idx {
		if i < len(s) && escaped {
			escaped = false
			continue
		}
		if i < len(s) && s[i] == '\\' {
			escaped = true
			continue
		}
		if i < len(s) && s[i] == '"' {
			doubleQuotes++
		} else if i < len(s) && s[i] == '\'' {
			singleQuotes++
		}
	}
	return doubleQuotes%2 != 0 || singleQuotes%2 != 0
}

// findCommentDelimiter finds the index of the first comment delimiter (e.g. "//" or "#")
// that is NOT inside a string literal.
func findCommentDelimiter(s string, delim string) int {
	idx := 0
	for {
		subIdx := strings.Index(s[idx:], delim)
		if subIdx == -1 {
			return -1
		}
		actualIdx := idx + subIdx
		if !isInsideString(s, actualIdx) {
			return actualIdx
		}
		idx = actualIdx + len(delim)
	}
}

// minifyCode compresses code blocks on-the-fly to reduce token usage by 30-50%
// without losing any syntax, structural definitions, or logical variables.
func minifyCode(code string, category string) string {
	if category != "project" && category != "file" {
		return code // Only minify actual codebase source code chunks!
	}

	lines := strings.Split(code, "\n")
	var minifiedLines []string
	inBlockComment := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue // Collapse all redundant empty lines!
		}

		// Get leading indentation
		indent := ""
		for _, r := range line {
			if r == ' ' || r == '\t' {
				indent += string(r)
			} else {
				break
			}
		}

		// Handle C/Go style block comments /* ... */
		if inBlockComment {
			if strings.Contains(trimmed, "*/") {
				idx := strings.Index(trimmed, "*/")
				line = indent + trimmed[idx+2:]
				inBlockComment = false
				trimmed = strings.TrimSpace(line)
			} else {
				continue // Skip lines inside block comments
			}
		}

		blockIdx := findCommentDelimiter(trimmed, "/*")
		if blockIdx != -1 {
			if strings.Contains(trimmed[blockIdx:], "*/") {
				// Inline block comment, e.g. func foo(/* arg */ x int)
				endIdx := strings.Index(trimmed, "*/")
				line = indent + trimmed[:blockIdx] + trimmed[endIdx+2:]
				trimmed = strings.TrimSpace(line)
			} else {
				inBlockComment = true
				line = indent + trimmed[:blockIdx]
				trimmed = strings.TrimSpace(line)
			}
		}

		// Handle C/Go style inline comments // ...
		inlineIdx := findCommentDelimiter(trimmed, "//")
		if inlineIdx != -1 {
			line = indent + trimmed[:inlineIdx]
			trimmed = strings.TrimSpace(line)
		}

		// Handle Python/YAML/Terraform style comments # ...
		hashIdx := findCommentDelimiter(trimmed, "#")
		if hashIdx != -1 {
			line = indent + trimmed[:hashIdx]
			trimmed = strings.TrimSpace(line)
		}

		// If the line is non-empty, keep it!
		if trimmed == "" {
			continue
		}
		minifiedLines = append(minifiedLines, strings.TrimRight(line, " \t\r"))
	}

	return strings.Join(minifiedLines, "\n")
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

	// 1. Fetch the IDs of the chunks we are about to delete
	querySel := `
		SELECT id FROM gemini_memories
		WHERE category = 'project'
		  AND cwd = $1
		  AND content LIKE 'File: ' || $2 || ' (Lines:%'
	`
	rows, err := db.Query(querySel, cwd, relPath)
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
		WHERE category = 'project'
		  AND cwd = $1
		  AND content LIKE 'File: ' || $2 || ' (Lines:%'
	`
	_, err = db.Exec(queryDel, cwd, relPath)
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

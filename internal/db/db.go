package db

// ponytail: keep db operations simple, open and close on every call, standardize all embeddings to exactly 3072 dimensions, and compress using the explicitly passed 4-bit TurboQuant dependency
// ponytail: privacy preservation - codebase file contents are NEVER stored in the database, only their metadata. Code is read on-demand during searches from local disk.
import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

// normalizeVectorTo3072 ensures that every vector is exactly target dimensions by slicing or padding
func normalizeVectorTo3072(vec []float32) []float32 {
	targetDim := turboquant.DefaultDimension
	if len(vec) == targetDim {
		return vec
	}
	if len(vec) > targetDim {
		return vec[:targetDim]
	}
	// Pad with zeros to exactly 3072
	padded := make([]float32, targetDim)
	copy(padded, vec)
	return padded
}

func parseMetadataHeader(content string) (relPath string, startLine, endLine int, ok bool) {
	// Format: "File: <relPath> (Lines: <start>-<end>)"
	if !strings.HasPrefix(content, "File: ") {
		return "", 0, 0, false
	}
	parts := strings.SplitN(content[6:], " (Lines: ", 2)
	if len(parts) != 2 {
		return "", 0, 0, false
	}
	relPath = parts[0]

	rangeParts := strings.SplitN(strings.TrimSuffix(parts[1], ")"), "-", 2)
	if len(rangeParts) != 2 {
		return "", 0, 0, false
	}

	var err error
	startLine, err = strconv.Atoi(rangeParts[0])
	if err != nil {
		return "", 0, 0, false
	}
	endLine, err = strconv.Atoi(rangeParts[1])
	if err != nil {
		return "", 0, 0, false
	}

	return relPath, startLine, endLine, true
}

func readCodeLines(absPath, relPath string, startLine, endLine int) (string, error) {
	fullPath := filepath.Join(absPath, relPath)
	contentBytes, err := os.ReadFile(fullPath)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(contentBytes), "\n")

	if startLine < 1 {
		startLine = 1
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	if startLine > len(lines) || startLine > endLine {
		return "", nil
	}

	return strings.Join(lines[startLine-1:endLine], "\n"), nil
}

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

	// ponytail: automatic DuckDB schema migration - drop tables with embedding column as DuckDB now only stores metadata
	var colType string
	err = db.QueryRow("SELECT data_type FROM information_schema.columns WHERE table_name = 'gemini_memories' AND column_name = 'embedding'").Scan(&colType)
	if err == nil {
		_, _ = db.Exec("DROP TABLE gemini_memories")
	}

	// ponytail: store ONLY metadata in DuckDB; vectors are persisted separately in our dedicated .tqv files
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

	// ponytail: create merkle_trees table for tracking indexed files and directories to enable ultra-fast incremental indexing
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

	// ponytail: create call_nodes and call_edges tables for fast, incremental AST-based call graph indexing
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
	if err != nil {
		return err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS gemini_symbols (
			memory_id VARCHAR NOT NULL,
			token VARCHAR NOT NULL,
			count INTEGER NOT NULL,
			PRIMARY KEY (memory_id, token)
		)
	`)
	return err
}

func GetTQPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "agent-mem.tqv"), nil
}

func tokenize(content string) []string {
	var tokens []string
	var current strings.Builder
	for _, r := range content {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			current.WriteRune(r)
		} else {
			if current.Len() > 2 {
				tokens = append(tokens, strings.ToLower(current.String()))
			}
			current.Reset()
		}
	}
	if current.Len() > 2 {
		tokens = append(tokens, strings.ToLower(current.String()))
	}
	return tokens
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

	// Start a single, robust ACID transaction to write all data in milliseconds
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmtMemory, err := tx.Prepare(`
		INSERT OR REPLACE INTO gemini_memories (id, content, category, cwd)
		VALUES ($1, $2, 'project', $3)
	`)
	if err != nil {
		return err
	}
	defer stmtMemory.Close()

	stmtSymbol, err := tx.Prepare(`
		INSERT OR REPLACE INTO gemini_symbols (memory_id, token, count)
		VALUES ($1, $2, $3)
	`)
	if err != nil {
		return err
	}
	defer stmtSymbol.Close()

	for _, item := range items {
		// 1. Save metadata
		_, err = stmtMemory.Exec(item.ID, item.Content, item.CWD)
		if err != nil {
			return err
		}

		// 2. Tokenize and save symbol frequencies
		tokens := tokenize(item.ChunkContent)
		freq := make(map[string]int)
		for _, t := range tokens {
			freq[t]++
		}

		for token, count := range freq {
			_, err = stmtSymbol.Exec(item.ID, token, count)
			if err != nil {
				return err
			}
		}

		// 3. Add to TurboQuant index in-memory
		normalized := normalizeVectorTo3072(item.Embedding)
		_ = index.Add(item.ID, normalized)
	}

	return tx.Commit()
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

	// 1. Save metadata to DuckDB (always save as project category)
	query := `
		INSERT OR REPLACE INTO gemini_memories (id, content, category, cwd)
		VALUES ($1, $2, 'project', $3)
	`
	_, err = db.Exec(query, id, content, cwd)
	if err != nil {
		return err
	}

	// 2. Tokenize chunkContent, building inverted index and save token/symbol frequencies for fast Lexical search
	tokens := tokenize(chunkContent)
	freq := make(map[string]int)
	for _, t := range tokens {
		freq[t]++
	}

	for token, count := range freq {
		_, err = db.Exec(`
			INSERT OR REPLACE INTO gemini_symbols (memory_id, token, count)
			VALUES ($1, $2, $3)
		`, id, token, count)
		if err != nil {
			return err
		}
	}

	// 3. Normalize and add to the shared TurboQuant index
	embedding = normalizeVectorTo3072(embedding)
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
	queryEmbedding = normalizeVectorTo3072(queryEmbedding)
	return index.Search(queryEmbedding, nil, limit)
}

func searchLexicalSparse(qTokens []string, limit int) (map[string]float64, error) {
	dbLock.Lock()
	defer dbLock.Unlock()

	db, err := Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	lexMap := make(map[string]float64)
	placeholders := make([]string, len(qTokens))
	args := make([]any, len(qTokens))
	for i, tok := range qTokens {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = tok
	}
	querySql := fmt.Sprintf(`
		SELECT memory_id, SUM(count) as match_count
		FROM gemini_symbols
		WHERE token IN (%s)
		GROUP BY memory_id
		ORDER BY match_count DESC
		LIMIT %d
	`, strings.Join(placeholders, ", "), limit)

	rows, err := db.Query(querySql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type lexMatch struct {
		id    string
		count int
	}
	var matches []lexMatch
	maxCount := 1.0

	for rows.Next() {
		var m lexMatch
		if err := rows.Scan(&m.id, &m.count); err == nil {
			matches = append(matches, m)
			if float64(m.count) > maxCount {
				maxCount = float64(m.count)
			}
		}
	}

	for _, m := range matches {
		lexMap[m.id] = float64(m.count) / maxCount
	}

	return lexMap, nil
}

func computeRRF(semResults []turboquant.SearchResult, lexMap map[string]float64, limit int) []candidateRRF {
	allCandIDs := make(map[string]bool)
	semRank := make(map[string]int)
	for i, res := range semResults {
		allCandIDs[res.ID] = true
		semRank[res.ID] = i + 1
	}

	type idScore struct {
		id    string
		score float64
	}
	var lexList []idScore
	for id, sc := range lexMap {
		lexList = append(lexList, idScore{id, sc})
	}
	sort.Slice(lexList, func(i, j int) bool {
		return lexList[i].score > lexList[j].score
	})

	lexRank := make(map[string]int)
	for i, ls := range lexList {
		allCandIDs[ls.id] = true
		lexRank[ls.id] = i + 1
	}

	const k = 60.0
	var fused []candidateRRF
	for id := range allCandIDs {
		score := 0.0
		if r, ok := semRank[id]; ok {
			score += 1.0 / (k + float64(r))
		}
		if r, ok := lexRank[id]; ok {
			score += 1.0 / (k + float64(r))
		}
		fused = append(fused, candidateRRF{id, score})
	}

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
			if m.Category == "project" {
				if relPath, start, end, ok := parseMetadataHeader(m.Content); ok {
					code, err := readCodeLines(m.CWD, relPath, start, end)
					if err == nil && code != "" {
						m.Content = fmt.Sprintf("File: %s (Lines: %d-%d)\nContent:\n%s", relPath, start, end, code)
					}
				}
			}
			memories = append(memories, m)
		}
	}
	return memories, nil
}

func applyGrepReRanking(memories []Memory, candidates []candidateRRF, queryText string, limit int) []Memory {
	var scored []scoredMemory
	lowerQuery := strings.ToLower(queryText)

	for _, m := range memories {
		rrf := 0.0
		for _, cand := range candidates {
			if cand.id == m.ID {
				rrf = cand.rrfScore
				break
			}
		}

		grepMatch := false
		if m.Category == "project" && lowerQuery != "" {
			if strings.Contains(strings.ToLower(m.Content), lowerQuery) {
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

	qTokens := tokenize(queryText)

	// Concurrently query Dense Semantic path and Sparse Lexical path in parallel!
	var semResults []turboquant.SearchResult
	var semErr error
	lexMap := make(map[string]float64)
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
		if len(qTokens) > 0 {
			lexMap, lexErr = searchLexicalSparse(qTokens, candidateLimit)
		}
	}()

	wg.Wait()

	if semErr != nil {
		return nil, fmt.Errorf("dense semantic path failed: %w", semErr)
	}
	if lexErr != nil {
		return nil, fmt.Errorf("sparse lexical path failed: %w", lexErr)
	}

	if len(semResults) == 0 && len(lexMap) == 0 {
		return nil, nil
	}

	// Reciprocal Rank Fusion (RRF) to mathematically merge Dense and Sparse rankings
	topCandidates := computeRRF(semResults, lexMap, candidateLimit)

	// Fetch memories metadata and on-the-fly read code from local disk
	memories, err := fetchMemoriesMetadata(topCandidates, cwd)
	if err != nil {
		return nil, err
	}

	// On-the-Fly Scoped Local Grep Re-ranking (exact match boosting)
	return applyGrepReRanking(memories, topCandidates, queryText, limit), nil
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

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
	"time"

	"agent-mem/internal/callgraph"
	"agent-mem/internal/turboquant"

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

// normalizeVectorTo3072 ensures that every vector is exactly target dimensions by slicing or padding
func normalizeVectorTo3072(vec []float32) []float32 {
	const targetDim = turboquant.DefaultDimension
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
	dbDir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return "", err
	}
	return filepath.Join(dbDir, "agent-mem.db"), nil
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
	return err
}

func GetTQPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dbDir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return "", err
	}
	return filepath.Join(dbDir, "agent-mem.tqv"), nil
}

func SaveMemory(id, content, category, cwd string, embedding []float32, index *turboquant.Index) error {
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

	// 2. Normalize and add to the shared TurboQuant index
	embedding = normalizeVectorTo3072(embedding)
	return index.Add(id, embedding)
}

func SearchMemories(queryEmbedding []float32, cwd string, limit int, index *turboquant.Index) ([]Memory, error) {
	if cwd != "" {
		// Try to find if cwd is inside any indexed codebase (parent path resolution)
		codebases, err := ListCodebases()
		if err == nil {
			var bestMatch string
			for _, cb := range codebases {
				if cwd == cb.CWD || strings.HasPrefix(cwd, cb.CWD+string(filepath.Separator)) {
					if len(cb.CWD) > len(bestMatch) {
						bestMatch = cb.CWD
					}
				}
			}
			if bestMatch != "" {
				cwd = bestMatch
			}
		}
	}

	if index == nil {
		return nil, fmt.Errorf("turboquant index cannot be nil")
	}

	// 1. Normalize query embedding to exactly 3072 dimensions
	queryEmbedding = normalizeVectorTo3072(queryEmbedding)

	// 2. Search vector store FIRST using the shared turboquant.Index (score all records)
	const candidateLimit = 200
	searchResults, err := index.Search(queryEmbedding, nil, candidateLimit)
	if err != nil {
		return nil, err
	}

	if len(searchResults) == 0 {
		return nil, nil
	}

	// Build a map of candidate ID -> similarity score, and a list of candidate IDs for the SQL query
	simMap := make(map[string]float64)
	idPlaceholders := make([]string, len(searchResults))
	queryArgs := make([]any, 0, len(searchResults)+2)

	for i, res := range searchResults {
		simMap[res.ID] = res.Similarity
		idPlaceholders[i] = fmt.Sprintf("$%d", i+1)
		queryArgs = append(queryArgs, res.ID)
	}

	// 2. Query DuckDB to retrieve and filter metadata for these top candidate IDs
	db, err := Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	query := fmt.Sprintf(`
		SELECT id, content, category, cwd, created_at
		FROM gemini_memories
		WHERE id IN (%s)
	`, strings.Join(idPlaceholders, ", "))

	var conditions []string

	if cwd != "" {
		conditions = append(conditions, fmt.Sprintf("cwd = $%d", len(queryArgs)+1))
		queryArgs = append(queryArgs, cwd)
	}

	if len(conditions) > 0 {
		query += " AND " + strings.Join(conditions, " AND ")
	}

	rows, err := db.Query(query, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// 3. Map retrieved active metadata rows to their similarity scores
	var memories []Memory
	for rows.Next() {
		var m Memory
		err := rows.Scan(&m.ID, &m.Content, &m.Category, &m.CWD, &m.CreatedAt)
		if err != nil {
			return nil, err
		}

		m.Similarity = simMap[m.ID]

		// ponytail: privacy preservation - if project category, load raw code content on the fly from local disk
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

	// 4. Sort results descending by similarity
	sort.Slice(memories, func(i, j int) bool {
		return memories[i].Similarity > memories[j].Similarity
	})

	if len(memories) > limit {
		memories = memories[:limit]
	}

	return memories, nil
}

// SaveMerkleTree stores the serialized Merkle Tree state for a codebase
func SaveMerkleTree(cwd, rootHash, treeJSON string) error {
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

	// 2. Delete chunks belonging to file from DuckDB metadata
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

	if index == nil {
		return nil // skip if index dependency is nil
	}

	// 3. Remove from the shared in-memory TurboQuant index
	for _, id := range idsToDelete {
		index.Delete(id)
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

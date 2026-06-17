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

// normalizeVectorTo3072 ensures that every vector is exactly 3072 dimensions by slicing or padding
func normalizeVectorTo3072(vec []float32) []float32 {
	const targetDim = 3072
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

	// ponytail: automatic DuckDB schema migration - drop old FLOAT[] tables and recreate with BLOB for TurboQuant compression
	var colType string
	err = db.QueryRow("SELECT data_type FROM information_schema.columns WHERE table_name = 'gemini_memories' AND column_name = 'embedding'").Scan(&colType)
	if err == nil {
		if strings.Contains(strings.ToUpper(colType), "FLOAT") || strings.Contains(strings.ToUpper(colType), "ARRAY") {
			_, _ = db.Exec("DROP TABLE gemini_memories")
		}
	}

	// ponytail: store embeddings as BLOB for extremely efficient 4-bit vector quantization
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS gemini_memories (
			id VARCHAR PRIMARY KEY,
			content TEXT NOT NULL,
			category VARCHAR NOT NULL,
			cwd TEXT NOT NULL,
			embedding BLOB,
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
	return err
}

func SaveMemory(id, content, category, cwd string, embedding []float32, tq *turboquant.TurboQuant) error {
	db, err := Open()
	if err != nil {
		return err
	}
	defer db.Close()

	if tq == nil {
		return fmt.Errorf("turboquant dependency cannot be nil")
	}

	// ponytail: normalize embedding vector to exactly 3072 dimensions
	embedding = normalizeVectorTo3072(embedding)

	qv, err := tq.Quantize(embedding)
	if err != nil {
		return fmt.Errorf("failed to quantize embedding: %w", err)
	}

	serializedBytes, err := tq.Serialize(qv)
	if err != nil {
		return fmt.Errorf("failed to serialize quantized vector: %w", err)
	}

	query := `
		INSERT OR REPLACE INTO gemini_memories (id, content, category, cwd, embedding)
		VALUES ($1, $2, $3, $4, $5)
	`

	_, err = db.Exec(query, id, content, category, cwd, serializedBytes)
	return err
}

func SearchMemories(queryEmbedding []float32, category, cwd string, limit int, tq *turboquant.TurboQuant) ([]Memory, error) {
	db, err := Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	if tq == nil {
		return nil, fmt.Errorf("turboquant dependency cannot be nil")
	}

	// ponytail: normalize embedding vector to exactly 3072 dimensions
	queryEmbedding = normalizeVectorTo3072(queryEmbedding)

	// ponytail: retrieve quantized vector BLOBs, dequantize on the fly, and score using Go-level CosineSimilarity
	query := `
		SELECT id, content, category, cwd, created_at, embedding
		FROM gemini_memories
	`

	var conditions []string
	var args []any

	if category != "" {
		conditions = append(conditions, fmt.Sprintf("category = $%d", len(args)+1))
		args = append(args, category)
	}

	if cwd != "" {
		conditions = append(conditions, fmt.Sprintf("(cwd = $%d OR category = 'personal')", len(args)+1))
		args = append(args, cwd)
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var m Memory
		var embeddingBytes []byte
		err := rows.Scan(&m.ID, &m.Content, &m.Category, &m.CWD, &m.CreatedAt, &embeddingBytes)
		if err != nil {
			return nil, err
		}

		if len(embeddingBytes) > 0 {
			qv, err := tq.Deserialize(embeddingBytes)
			if err != nil {
				continue
			}

			dequantized, err := tq.Dequantize(qv)
			if err != nil {
				continue
			}

			sim, err := turboquant.CosineSimilarity(queryEmbedding, dequantized)
			if err != nil {
				continue
			}
			m.Similarity = sim
		}

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

	// Sort results by similarity descending
	sort.Slice(memories, func(i, j int) bool {
		return memories[i].Similarity > memories[j].Similarity
	})

	if len(memories) > limit {
		memories = memories[:limit]
	}

	return memories, nil
}

func GetRecentMemories(cwd string, limit int) ([]Memory, error) {
	db, err := Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	query := `
		SELECT id, content, category, cwd, created_at
		FROM gemini_memories
		WHERE (category = 'project' AND cwd = $1) OR (category = 'personal')
		ORDER BY created_at DESC
		LIMIT $2
	`

	rows, err := db.Query(query, cwd, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var m Memory
		err := rows.Scan(&m.ID, &m.Content, &m.Category, &m.CWD, &m.CreatedAt)
		if err != nil {
			return nil, err
		}

		// ponytail: privacy preservation - load raw code content on the fly from local disk
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

	return memories, nil
}

// GetRecentPersonalMemories fetches the most recent personal memories up to the limit
func GetRecentPersonalMemories(limit int) ([]Memory, error) {
	db, err := Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	query := `
		SELECT id, content, category, cwd, created_at
		FROM gemini_memories
		WHERE category = 'personal'
		ORDER BY created_at DESC
		LIMIT $1
	`

	rows, err := db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var m Memory
		err := rows.Scan(&m.ID, &m.Content, &m.Category, &m.CWD, &m.CreatedAt)
		if err != nil {
			return nil, err
		}
		memories = append(memories, m)
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
func DeleteFileMemories(cwd, relPath string) error {
	db, err := Open()
	if err != nil {
		return err
	}
	defer db.Close()

	// ponytail: delete chunks belonging to file using the standard "File: <relPath> (Lines: %" prefix convention
	query := `
		DELETE FROM gemini_memories
		WHERE category = 'project'
		  AND cwd = $1
		  AND content LIKE 'File: ' || $2 || ' (Lines:%'
	`
	_, err = db.Exec(query, cwd, relPath)
	return err
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

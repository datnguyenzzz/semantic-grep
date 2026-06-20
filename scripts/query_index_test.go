//go:build integration

package scripts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/datnguyenzzz/agent-context/internal/db"
	"github.com/datnguyenzzz/agent-context/internal/llm"
	"github.com/datnguyenzzz/agent-context/internal/turboquant"
)

func Test_QueryIndex(t *testing.T) {
	query := "how many percentage of non-interesting traces the otelcol-tail-sampling sampler export ?"

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("failed to retrieve user home: %v", err)
	}

	// 2. Resolve default storage paths
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(home, "agent-mem.db")
	}

	tqvPath := os.Getenv("TQV_PATH")
	if tqvPath == "" {
		tqvPath = filepath.Join(home, "agent-mem.tqv")
	}

	// Ensure target database/index files exist before querying
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatalf("database file not found at: %s", dbPath)
	}
	if _, err := os.Stat(tqvPath); os.IsNotExist(err) {
		t.Fatalf("TurboQuant vector file not found at: %s", tqvPath)
	}

	t.Logf("Querying index using DB: %s, Vectors: %s", dbPath, tqvPath)
	t.Logf("Executing query: %s", query)

	// 3. Fetch Query Embedding via LiteLLM
	embedding, err := llm.GetEmbedding(query, turboquant.DefaultDimension)
	if err != nil {
		t.Fatalf("failed to get embedding: %v", err)
	}

	// 4. Load TurboQuant Vector Index
	tq, err := turboquant.NewTurboQuant(turboquant.DefaultDimension, turboquant.DefaultBitWidth, 42)
	if err != nil {
		t.Fatalf("failed to init TurboQuant: %v", err)
	}

	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		t.Fatalf("failed to load TurboQuant index: %v", err)
	}

	// 5. Query matching memories
	results, err := db.SearchMemories(query, embedding, "", 5, index)
	if err != nil {
		t.Fatalf("failed to search memories: %v", err)
	}

	// 6. Output raw JSON representation simply
	jsonBytes, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal results: %v", err)
	}

	fmt.Printf("\n--- SEMANTIC SEARCH DUMP FOR: %q ---\n", query)
	fmt.Println(string(jsonBytes))
	fmt.Println("------------------------------------")
}

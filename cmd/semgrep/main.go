package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/datnguyenzzz/semantic-grep/internal/db"
	"github.com/datnguyenzzz/semantic-grep/internal/llm"
	"github.com/datnguyenzzz/semantic-grep/internal/turboquant"
)

type SemgrepResult struct {
	AbsolutePath string `json:"absolute_path"`
	SymbolName   string `json:"symbol_name"`
	StartLine    int    `json:"start_line"`
	EndLine      int    `json:"end_line"`
	Content      string `json:"content"`
}

type SemgrepResponse struct {
	Results []SemgrepResult `json:"results"`
}

func main() {
	// 1. Define command-line flags
	limitFlag := flag.Int("n", 10, "Maximum number of search results to return")
	cwdFlag := flag.String("cwd", "", "The absolute codebase path to search in (leave empty for global/all codebases)")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Println("Usage: semgrep [options] <query>")
		fmt.Println("\nOptions:")
		flag.PrintDefaults()
		os.Exit(1)
	}
	query := args[0]
	cwd := *cwdFlag

	// 3. Initialize/Load Database
	if err := db.InitDatabase(); err != nil {
		log.Fatalf("❌ Error: Failed to initialize local DuckDB database: %v", err)
	}

	// 4. Resolve TQV path and load TurboQuant Index
	tqvPath, err := db.GetTQPath()
	if err != nil {
		log.Fatalf("❌ Error: Failed to resolve TurboQuant index path: %v", err)
	}

	// Read or create TurboQuant configurations matching DefaultDimension
	tq, err := turboquant.NewTurboQuant(turboquant.DefaultDimension, turboquant.DefaultBitWidth, turboquant.DefaultSeed)
	if err != nil {
		log.Fatalf("❌ Error: Failed to initialize TurboQuant: %v", err)
	}

	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		log.Fatalf("❌ Error: Failed to load TurboQuant index: %v", err)
	}

	// 5. Generate query embedding using real non-mocked LLM
	embedding, err := llm.GetEmbedding(query, turboquant.DefaultDimension)
	if err != nil {
		log.Fatalf("❌ Error: Failed to generate vector embedding for query: %v.\nMake sure your embedding model server (LiteLLM/OpenAI) is running.", err)
	}

	// 6. Execute our ultra-optimized Hybrid Search
	results, err := db.SearchMemories(query, embedding, cwd, *limitFlag, index)
	if err != nil {
		log.Fatalf("❌ Error: Hybrid search execution failed: %v", err)
	}

	// 7. Print beautiful results to stdout
	var finalResults []SemgrepResult
	for _, m := range results {
		finalResults = append(finalResults, SemgrepResult{
			AbsolutePath: m.CWD,
			SymbolName:   m.SymbolName,
			StartLine:    m.LineStart,
			EndLine:      m.LineEnd,
			Content:      m.Content,
		})
	}

	resp := SemgrepResponse{
		Results: finalResults,
	}

	jsonBytes, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		log.Fatalf("❌ Error: Failed to format search results: %v", err)
	}

	fmt.Println(string(jsonBytes))
}

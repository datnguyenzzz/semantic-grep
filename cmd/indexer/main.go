package main

// codebase indexer walks directories, filters Go, Terraform, and YAML files, splits them, embeds, and stores in DuckDB using a Merkle Tree

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/datnguyenzzz/agent-context/internal/db"
	"github.com/datnguyenzzz/agent-context/internal/merkle"
	"github.com/datnguyenzzz/agent-context/internal/turboquant"
)

func loadEnv() {
	if home, err := os.UserHomeDir(); err == nil {
		envPath := filepath.Join(home, ".agent-mem.env")
		if content, err := os.ReadFile(envPath); err == nil {
			for line := range strings.SplitSeq(string(content), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					key := strings.TrimSpace(parts[0])
					val := strings.TrimSpace(parts[1])
					os.Setenv(key, val)
				}
			}
		}
	}
}

func main() {
	loadEnv()
	targetDir := "."
	if len(os.Args) > 1 {
		targetDir = os.Args[1]
	}

	absPath, err := filepath.Abs(targetDir)
	if err != nil {
		log.Fatalf("Failed to resolve absolute path of %s: %v", targetDir, err)
	}

	fmt.Println("================================================================================")
	fmt.Println("         📂  GEMINI PERSISTENT MEMORY INDEXER (Merkle Tree Sync)  📂            ")
	fmt.Println("================================================================================")
	fmt.Printf("  • Target Path: %s\n", absPath)
	fmt.Println("  • Supported File Extensions: .go, .tf, .yaml, .yml")
	fmt.Println("--------------------------------------------------------------------------------")
	fmt.Println("  🔍 Scanning local workspace files...")

	// Initialize TurboQuant once on startup (using default configurations)
	tq, err := turboquant.NewTurboQuant(turboquant.DefaultDimension, turboquant.DefaultBitWidth, turboquant.DefaultSeed)
	if err != nil {
		log.Fatalf("Failed to initialize TurboQuant: %v", err)
	}

	tqvPath, err := db.GetTQPath()
	if err != nil {
		log.Fatalf("Failed to resolve vector storage path: %v", err)
	}
	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		log.Fatalf("Failed to initialize TurboQuant index: %v", err)
	}

	added, modified, deleted, err := merkle.UpdateIndex(absPath, index)
	if err != nil {
		fmt.Println("  ✗ Indexing failed.")
		log.Fatalf("  Error details: %v", err)
	}

	// Wait for all background database writes to complete successfully
	db.AsyncSaveWG.Wait()

	// Persist the shared index changes back to the .tqv storage file before exiting
	if err := index.Save(); err != nil {
		log.Fatalf("Failed to save TurboQuant index to disk: %v", err)
	}

	fmt.Println("--------------------------------------------------------------------------------")
	if added == 0 && modified == 0 && deleted == 0 {
		fmt.Println("  ✓ Codebase index is already up to date!")
	} else {
		fmt.Println("┌──────────────────────────────────────────────────────────────────────────────┐")
		fmt.Println("│                         INCREMENTAL RUN SYNC COMPLETE                        │")
		fmt.Println("├──────────────────────────────────────────────┬───────────────────────────────┤")
		fmt.Printf("│  Added Files                                 │ %-29d │\n", added)
		fmt.Printf("│  Modified Files                              │ %-29d │\n", modified)
		fmt.Printf("│  Deleted Files                               │ %-29d │\n", deleted)
		fmt.Println("└──────────────────────────────────────────────┴───────────────────────────────┘")
		fmt.Println("  ✓ All changes synced successfully to DuckDB persistent memory!")
	}
	fmt.Println("================================================================================")
}

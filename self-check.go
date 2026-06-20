package main

// ponytail: keep test extremely simple, run offline with mock embeddings, verify DuckDB logic with explicit TurboQuant dependencies

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/datnguyenzzz/agent-context/internal/db"
	"github.com/datnguyenzzz/agent-context/internal/turboquant"
)

func main() {
	fmt.Println("Setting up Go DuckDB self-check...")

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("✗ Failed to get user home directory: %v\n", err)
		os.Exit(1)
	}

	dbFile := filepath.Join(home, "agent-mem.db")
	backupFile := filepath.Join(home, "agent-mem.db.backup")
	tqvFile := filepath.Join(home, "agent-mem.tqv")
	backupTqvFile := filepath.Join(home, "agent-mem.tqv.backup")

	// Backup existing DB and vector store if any
	if _, err := os.Stat(dbFile); err == nil {
		if err := os.Rename(dbFile, backupFile); err != nil {
			fmt.Printf("✗ Failed to backup existing database: %v\n", err)
			os.Exit(1)
		}
		var hasTqvBackup bool
		if _, err := os.Stat(tqvFile); err == nil {
			if err := os.Rename(tqvFile, backupTqvFile); err == nil {
				hasTqvBackup = true
			}
		}
		defer func() {
			os.Remove(dbFile)
			os.Rename(backupFile, dbFile)
			os.Remove(tqvFile)
			if hasTqvBackup {
				os.Rename(backupTqvFile, tqvFile)
			}
			fmt.Println("Restored original database.")
		}()
	} else {
		defer func() {
			os.Remove(dbFile)
			os.Remove(tqvFile)
		}()
	}

	// Initialize DB
	if err := db.InitDatabase(); err != nil {
		fmt.Printf("✗ Failed to initialize database: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Database initialized.")

	// Initialize TurboQuant once on startup (using default configurations)
	tq, err := turboquant.NewTurboQuant(turboquant.DefaultDimension, turboquant.DefaultBitWidth, turboquant.DefaultSeed)
	if err != nil {
		fmt.Printf("✗ Failed to initialize TurboQuant: %v\n", err)
		os.Exit(1)
	}

	tqvPath, err := db.GetTQPath()
	if err != nil {
		fmt.Printf("✗ Failed to resolve vector storage path: %v\n", err)
		os.Exit(1)
	}
	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		fmt.Printf("✗ Failed to initialize TurboQuant index: %v\n", err)
		os.Exit(1)
	}

	// Mock embeddings
	mockEmbed1 := make([]float32, 768)
	mockEmbed2 := make([]float32, 768)
	for i := 0; i < 768; i++ {
		mockEmbed1[i] = 0.1
		mockEmbed2[i] = -0.1
	}
	mockEmbed1[0] = 0.5
	mockEmbed2[0] = -0.5

	// Save memories (all automatically mapped as project category)
	err = db.SaveMemory("test-g1", "Project uses React for frontend", "project", "/Users/thanh.nguyen/test-project", mockEmbed1, index, "Project uses React for frontend")
	if err != nil {
		fmt.Printf("✗ Failed to save project memory 1: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Saved project memory 1.")

	err = db.SaveMemory("test-g2", "Project uses DuckDB Node 'Neo' client", "project", "/Users/thanh.nguyen/test-project", mockEmbed2, index, "Project uses DuckDB Node 'Neo' client")
	if err != nil {
		fmt.Printf("✗ Failed to save project memory 2: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Saved project memory 2.")

	// Save the in-memory index to disk to verify the file persistence path works
	if err := index.Save(); err != nil {
		fmt.Printf("✗ Failed to save TurboQuant index: %v\n", err)
		os.Exit(1)
	}

	// Search memories for first project memory
	results, err := db.SearchMemories("React for frontend", mockEmbed1, "/Users/thanh.nguyen/test-project", 1, index)
	if err != nil {
		fmt.Printf("✗ Failed to search project memory 1: %v\n", err)
		os.Exit(1)
	}

	if len(results) != 1 {
		fmt.Printf("✗ Expected 1 result, got %d\n", len(results))
		os.Exit(1)
	}

	if results[0].ID != "test-g1" {
		fmt.Printf("✗ Expected test-g1, got %s\n", results[0].ID)
		os.Exit(1)
	}
	fmt.Printf("✓ Correct project memory 1 found: %s\n", results[0].Content)

	// Search memories for second project memory
	resultsProject, err := db.SearchMemories("DuckDB Node client", mockEmbed2, "/Users/thanh.nguyen/test-project", 1, index)
	if err != nil {
		fmt.Printf("✗ Failed to search project memory 2: %v\n", err)
		os.Exit(1)
	}

	if len(resultsProject) != 1 {
		fmt.Printf("✗ Expected 1 result, got %d\n", len(resultsProject))
		os.Exit(1)
	}

	if resultsProject[0].ID != "test-g2" {
		fmt.Printf("✗ Expected test-g2, got %s\n", resultsProject[0].ID)
		os.Exit(1)
	}
	fmt.Printf("✓ Correct project memory found: %s\n", resultsProject[0].Content)

	fmt.Println("✓ All Go DuckDB database checks passed successfully!")
}

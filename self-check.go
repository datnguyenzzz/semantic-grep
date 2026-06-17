package main

// ponytail: keep test extremely simple, run offline with mock embeddings, verify DuckDB logic with explicit TurboQuant dependencies

import (
	"fmt"
	"os"
	"path/filepath"

	"agent-mem/internal/db"
	"agent-mem/internal/turboquant"
)

func main() {
	fmt.Println("Setting up Go DuckDB self-check...")

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("✗ Failed to get user home directory: %v\n", err)
		os.Exit(1)
	}

	dbDir := filepath.Join(home, ".gemini")
	dbFile := filepath.Join(dbDir, "agent-mem.db")
	backupFile := filepath.Join(dbDir, "agent-mem.db.backup")

	// Backup existing DB if any
	if _, err := os.Stat(dbFile); err == nil {
		if err := os.Rename(dbFile, backupFile); err != nil {
			fmt.Printf("✗ Failed to backup existing database: %v\n", err)
			os.Exit(1)
		}
		defer func() {
			os.Remove(dbFile)
			os.Rename(backupFile, dbFile)
			fmt.Println("Restored original database.")
		}()
	} else {
		defer func() {
			os.Remove(dbFile)
		}()
	}

	// Initialize DB
	if err := db.InitDatabase(); err != nil {
		fmt.Printf("✗ Failed to initialize database: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Database initialized.")

	// Initialize TurboQuant once on startup (3072 dimension, 4-bit, seed 42)
	tq, err := turboquant.NewTurboQuant(3072, 4, 42)
	if err != nil {
		fmt.Printf("✗ Failed to initialize TurboQuant: %v\n", err)
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

	// Save memories
	err = db.SaveMemory("test-g1", "User prefers TypeScript over JavaScript", "personal", "/Users/thanh.nguyen/test-project", mockEmbed1, tq)
	if err != nil {
		fmt.Printf("✗ Failed to save personal memory: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Saved personal memory.")

	err = db.SaveMemory("test-g2", "Project uses DuckDB Node 'Neo' client", "project", "/Users/thanh.nguyen/test-project", mockEmbed2, tq)
	if err != nil {
		fmt.Printf("✗ Failed to save project memory: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Saved project memory.")

	// Search memories
	results, err := db.SearchMemories(mockEmbed1, "personal", "/Users/thanh.nguyen/test-project", 1, tq)
	if err != nil {
		fmt.Printf("✗ Failed to search personal memory: %v\n", err)
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
	fmt.Printf("✓ Correct personal memory found: %s\n", results[0].Content)

	// Search project memories
	resultsProject, err := db.SearchMemories(mockEmbed2, "project", "/Users/thanh.nguyen/test-project", 1, tq)
	if err != nil {
		fmt.Printf("✗ Failed to search project memory: %v\n", err)
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

//go:build integration

package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-mem/internal/db"
	"agent-mem/internal/llm"
	"agent-mem/internal/merkle"
	"agent-mem/internal/turboquant"
)

func TestEndToEndIndexerIntegration(t *testing.T) {
	// ponytail: skip test gracefully if the real LiteLLM server is not running in the current test environment
	conn, err := net.DialTimeout("tcp", "localhost:36253", 100*time.Millisecond)
	if err != nil {
		t.Skip("Skipping live integration test: local LiteLLM server on localhost:36253 is unreachable")
	}
	conn.Close()

	// Initialize TurboQuant once on startup (3072 dimension, 4-bit, seed 42)
	tq, err := turboquant.NewTurboQuant(3072, 4, 42)
	if err != nil {
		t.Fatalf("failed to initialize turboquant: %v", err)
	}

	// 1. Setup an isolated User Home directory so that we write to a brand new temp DuckDB file
	tmpHome, err := os.MkdirTemp("", "agent-mem-home-*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tmpHome)

	os.Setenv("HOME", tmpHome)
	defer os.Unsetenv("HOME")

	// 2. Setup a mock local codebase folder
	tmpCodebase, err := os.MkdirTemp("", "agent-mem-codebase-*")
	if err != nil {
		t.Fatalf("failed to create temp codebase: %v", err)
	}
	defer os.RemoveAll(tmpCodebase)

	// Create subfolders and Go, Terraform, YAML, Markdown files
	_ = os.MkdirAll(filepath.Join(tmpCodebase, "internal"), 0755)

	mainGoPath := filepath.Join(tmpCodebase, "main.go")
	_ = os.WriteFile(mainGoPath, []byte("package main\n\n// Run starts the app\nfunc Run() {\n\tprintln(\"App\")\n}"), 0644)

	configTfPath := filepath.Join(tmpCodebase, "internal", "config.tf")
	_ = os.WriteFile(configTfPath, []byte("resource \"aws_s3_bucket\" \"test\" {}"), 0644)

	manifestYamlPath := filepath.Join(tmpCodebase, "deployment.yaml")
	_ = os.WriteFile(manifestYamlPath, []byte("apiVersion: apps/v1\nkind: Deployment\n"), 0644)

	readmeMdPath := filepath.Join(tmpCodebase, "README.md")
	_ = os.WriteFile(readmeMdPath, []byte("# Project\n\nThis is an integration test."), 0644)

	// 3. Run the Indexer (First Full Index)
	added, modified, deleted, err := merkle.UpdateIndex(tmpCodebase, tq)
	if err != nil {
		t.Fatalf("UpdateIndex first run failed: %v", err)
	}

	// We expect 3 added files (main.go, config.tf, deployment.yaml)
	if added != 3 || modified != 0 || deleted != 0 {
		t.Errorf("expected 3 added files, got: added=%d, modified=%d, deleted=%d", added, modified, deleted)
	}

	// 4. Verify Database Content after indexing
	codebases, err := db.ListCodebases()
	if err != nil {
		t.Fatalf("ListCodebases failed: %v", err)
	}

	if len(codebases) != 1 {
		t.Errorf("expected 1 indexed codebase, got %d", len(codebases))
	} else if codebases[0].CWD != tmpCodebase {
		t.Errorf("expected indexed codebase path to be %s, got %s", tmpCodebase, codebases[0].CWD)
	}

	// 5. Incremental Index (No Changes)
	added, modified, deleted, err = merkle.UpdateIndex(tmpCodebase, tq)
	if err != nil {
		t.Fatalf("UpdateIndex second run failed: %v", err)
	}

	if added != 0 || modified != 0 || deleted != 0 {
		t.Errorf("expected no changes on second run, got: added=%d, modified=%d, deleted=%d", added, modified, deleted)
	}

	// 6. Modification Run
	// Edit main.go
	_ = os.WriteFile(mainGoPath, []byte("package main\n\n// Run starts the app\nfunc Run() {\n\tprintln(\"App modified\")\n}"), 0644)

	added, modified, deleted, err = merkle.UpdateIndex(tmpCodebase, tq)
	if err != nil {
		t.Fatalf("UpdateIndex modification run failed: %v", err)
	}

	if added != 0 || modified != 1 || deleted != 0 {
		t.Errorf("expected 1 modified file, got: added=%d, modified=%d, deleted=%d", added, modified, deleted)
	}

	// 7. Deletion Run
	// Delete config.tf
	_ = os.Remove(configTfPath)

	added, modified, deleted, err = merkle.UpdateIndex(tmpCodebase, tq)
	if err != nil {
		t.Fatalf("UpdateIndex deletion run failed: %v", err)
	}

	if added != 0 || modified != 0 || deleted != 1 {
		t.Errorf("expected 1 deleted file, got: added=%d, modified=%d, deleted=%d", added, modified, deleted)
	}
}

func TestMultiCodebaseAndPersonalMemoriesIntegration(t *testing.T) {
	// ponytail: skip test gracefully if the real LiteLLM server is not running in the current test environment
	conn, err := net.DialTimeout("tcp", "localhost:36253", 100*time.Millisecond)
	if err != nil {
		t.Skip("Skipping live integration test: local LiteLLM server on localhost:36253 is unreachable")
	}
	conn.Close()

	// Initialize TurboQuant once on startup (3072 dimension, 4-bit, seed 42)
	tq, err := turboquant.NewTurboQuant(3072, 4, 42)
	if err != nil {
		t.Fatalf("failed to initialize turboquant: %v", err)
	}

	// 1. Isolated HOME dir
	tmpHome, err := os.MkdirTemp("", "agent-mem-home-multi-*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tmpHome)

	os.Setenv("HOME", tmpHome)
	defer os.Unsetenv("HOME")

	// Initialize database
	if err := db.InitDatabase(); err != nil {
		t.Fatalf("failed to init db: %v", err)
	}

	// 2. Setup Codebase A
	tmpCodebaseA, err := os.MkdirTemp("", "mock-project-A-*")
	if err != nil {
		t.Fatalf("failed to create codebase A: %v", err)
	}
	defer os.RemoveAll(tmpCodebaseA)

	mainGoPath := filepath.Join(tmpCodebaseA, "main.go")
	_ = os.WriteFile(mainGoPath, []byte("package main\n\nfunc Main() {}"), 0644)

	// 3. Setup Codebase B
	tmpCodebaseB, err := os.MkdirTemp("", "mock-project-B-*")
	if err != nil {
		t.Fatalf("failed to create codebase B: %v", err)
	}
	defer os.RemoveAll(tmpCodebaseB)

	readmeMdPath := filepath.Join(tmpCodebaseB, "README.md")
	_ = os.WriteFile(readmeMdPath, []byte("# Readme\n\nProject B description"), 0644)

	configYamlPath := filepath.Join(tmpCodebaseB, "config.yaml")
	_ = os.WriteFile(configYamlPath, []byte("port: 8080\n"), 0644)

	// 4. Index both codebases
	_, _, _, err = merkle.UpdateIndex(tmpCodebaseA, tq)
	if err != nil {
		t.Fatalf("failed to index codebase A: %v", err)
	}

	_, _, _, err = merkle.UpdateIndex(tmpCodebaseB, tq)
	if err != nil {
		t.Fatalf("failed to index codebase B: %v", err)
	}

	// 5. Save more than 1 Personal Memories
	mockEmbed1, err := llm.GetEmbedding("User prefers functional programming in Go")
	if err != nil {
		t.Fatalf("failed to get real embedding: %v", err)
	}
	err = db.SaveMemory("p1", "User prefers functional programming in Go", "personal", "", mockEmbed1, tq)
	if err != nil {
		t.Fatalf("failed to save personal memory 1: %v", err)
	}

	mockEmbed2, err := llm.GetEmbedding("User prefers space indentation of size 2")
	if err != nil {
		t.Fatalf("failed to get real embedding: %v", err)
	}
	err = db.SaveMemory("p2", "User prefers space indentation of size 2", "personal", "", mockEmbed2, tq)
	if err != nil {
		t.Fatalf("failed to save personal memory 2: %v", err)
	}

	// 6. Assertions:
	// Verify that exactly 2 codebases are stored in our portfolio
	codebases, err := db.ListCodebases()
	if err != nil {
		t.Fatalf("failed to list codebases: %v", err)
	}

	if len(codebases) != 2 {
		t.Errorf("expected 2 indexed codebases in portfolio, got %d", len(codebases))
	}

	// Verify we can find both personal memories in global searches
	results, err := db.SearchMemories(mockEmbed1, "personal", tmpCodebaseA, 5, tq)
	if err != nil {
		t.Fatalf("failed to search memories: %v", err)
	}

	foundP1 := false
	foundP2 := false
	for _, m := range results {
		if m.ID == "p1" {
			foundP1 = true
		}
		if m.ID == "p2" {
			foundP2 = true
		}
	}

	if !foundP1 {
		t.Error("failed to find personal memory p1 in search results")
	}
	if !foundP2 {
		t.Error("failed to find personal memory p2 in search results")
	}

	// Verify project chunks search correctly separates codebase origin
	resultsA, err := db.SearchMemories(mockEmbed1, "project", tmpCodebaseA, 5, tq)
	if err != nil {
		t.Fatalf("failed to search codebase A memories: %v", err)
	}

	for _, m := range resultsA {
		if !strings.Contains(m.Content, "File: main.go") {
			t.Errorf("expected search in Codebase A to yield main.go chunk, got: %s", m.Content)
		}
	}

	// 7. Modification Step for both Codebases A and B
	// Modify main.go in Codebase A
	_ = os.WriteFile(mainGoPath, []byte("package main\n\nfunc Main() {\n\tprintln(\"A modified!\")\n}"), 0644)

	// Modify config.yaml in Codebase B
	_ = os.WriteFile(configYamlPath, []byte("port: 9090\n"), 0644)

	// Run incremental sync on Codebase A
	addedA, modifiedA, deletedA, err := merkle.UpdateIndex(tmpCodebaseA, tq)
	if err != nil {
		t.Fatalf("UpdateIndex on Codebase A failed: %v", err)
	}
	if addedA != 0 || modifiedA != 1 || deletedA != 0 {
		t.Errorf("expected 1 modified file in Codebase A, got: added=%d, modified=%d, deleted=%d", addedA, modifiedA, deletedA)
	}

	// Run incremental sync on Codebase B
	addedB, modifiedB, deletedB, err := merkle.UpdateIndex(tmpCodebaseB, tq)
	if err != nil {
		t.Fatalf("UpdateIndex on Codebase B failed: %v", err)
	}
	if addedB != 0 || modifiedB != 1 || deletedB != 0 {
		t.Errorf("expected 1 modified file in Codebase B, got: added=%d, modified=%d, deleted=%d", addedB, modifiedB, deletedB)
	}

	// Verify search results dynamically load the newly modified code on the fly from disk
	resultsA_updated, err := db.SearchMemories(mockEmbed1, "project", tmpCodebaseA, 5, tq)
	if err != nil {
		t.Fatalf("failed to search updated codebase A memories: %v", err)
	}

	foundUpdatedA := false
	for _, m := range resultsA_updated {
		if strings.Contains(m.Content, "A modified!") {
			foundUpdatedA = true
			break
		}
	}
	if !foundUpdatedA {
		t.Error("expected search results for Codebase A to dynamically load modified content from disk")
	}

	resultsB_updated, err := db.SearchMemories(mockEmbed2, "project", tmpCodebaseB, 5, tq)
	if err != nil {
		t.Fatalf("failed to search updated codebase B memories: %v", err)
	}

	foundUpdatedB := false
	for _, m := range resultsB_updated {
		if strings.Contains(m.Content, "modified!") {
			foundUpdatedB = true
			break
		}
	}
	if !foundUpdatedB {
		t.Error("expected search results for Codebase B to dynamically load modified content from disk")
	}
}

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

	// Initialize TurboQuant once on startup (using default configurations)
	tq, err := turboquant.NewTurboQuant(turboquant.DefaultDimension, turboquant.DefaultBitWidth, turboquant.DefaultSeed)
	if err != nil {
		t.Fatalf("failed to initialize turboquant: %v", err)
	}

	// Setup isolated home first to resolve tqvPath correctly
	tmpHome, err := os.MkdirTemp("", "agent-mem-home-*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tmpHome)

	os.Setenv("HOME", tmpHome)
	defer os.Unsetenv("HOME")

	tqvPath, err := db.GetTQPath()
	if err != nil {
		t.Fatalf("failed to get tqv path: %v", err)
	}
	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		t.Fatalf("failed to initialize index: %v", err)
	}

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
	added, modified, deleted, err := merkle.UpdateIndex(tmpCodebase, index)
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
	added, modified, deleted, err = merkle.UpdateIndex(tmpCodebase, index)
	if err != nil {
		t.Fatalf("UpdateIndex second run failed: %v", err)
	}

	if added != 0 || modified != 0 || deleted != 0 {
		t.Errorf("expected no changes on second run, got: added=%d, modified=%d, deleted=%d", added, modified, deleted)
	}

	// 6. Modification Run
	// Edit main.go
	_ = os.WriteFile(mainGoPath, []byte("package main\n\n// Run starts the app\nfunc Run() {\n\tprintln(\"App modified\")\n}"), 0644)

	added, modified, deleted, err = merkle.UpdateIndex(tmpCodebase, index)
	if err != nil {
		t.Fatalf("UpdateIndex modification run failed: %v", err)
	}

	if added != 0 || modified != 1 || deleted != 0 {
		t.Errorf("expected 1 modified file, got: added=%d, modified=%d, deleted=%d", added, modified, deleted)
	}

	// 7. Deletion Run
	// Delete config.tf
	_ = os.Remove(configTfPath)

	added, modified, deleted, err = merkle.UpdateIndex(tmpCodebase, index)
	if err != nil {
		t.Fatalf("UpdateIndex deletion run failed: %v", err)
	}

	if added != 0 || modified != 0 || deleted != 1 {
		t.Errorf("expected 1 deleted file, got: added=%d, modified=%d, deleted=%d", added, modified, deleted)
	}
}

func TestMultiCodebaseIntegration(t *testing.T) {
	// ponytail: skip test gracefully if the real LiteLLM server is not running in the current test environment
	conn, err := net.DialTimeout("tcp", "localhost:36253", 100*time.Millisecond)
	if err != nil {
		t.Skip("Skipping live integration test: local LiteLLM server on localhost:36253 is unreachable")
	}
	conn.Close()

	// Initialize TurboQuant once on startup (using default configurations)
	tq, err := turboquant.NewTurboQuant(turboquant.DefaultDimension, turboquant.DefaultBitWidth, turboquant.DefaultSeed)
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

	tqvPath, err := db.GetTQPath()
	if err != nil {
		t.Fatalf("failed to get tqv path: %v", err)
	}
	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		t.Fatalf("failed to initialize index: %v", err)
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
	_, _, _, err = merkle.UpdateIndex(tmpCodebaseA, index)
	if err != nil {
		t.Fatalf("failed to index codebase A: %v", err)
	}

	_, _, _, err = merkle.UpdateIndex(tmpCodebaseB, index)
	if err != nil {
		t.Fatalf("failed to index codebase B: %v", err)
	}

	// 5. Assertions:
	// Verify that exactly 2 codebases are stored in our portfolio
	codebases, err := db.ListCodebases()
	if err != nil {
		t.Fatalf("failed to list codebases: %v", err)
	}

	if len(codebases) != 2 {
		t.Errorf("expected 2 indexed codebases in portfolio, got %d", len(codebases))
	}

	mockEmbed1, err := llm.GetEmbedding("func Main")
	if err != nil {
		t.Fatalf("failed to get real embedding: %v", err)
	}

	mockEmbed2, err := llm.GetEmbedding("port: 8080")
	if err != nil {
		t.Fatalf("failed to get real embedding: %v", err)
	}

	// Verify project chunks search correctly separates codebase origin
	resultsA, err := db.SearchMemories(mockEmbed1, tmpCodebaseA, 5, index)
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
	addedA, modifiedA, deletedA, err := merkle.UpdateIndex(tmpCodebaseA, index)
	if err != nil {
		t.Fatalf("UpdateIndex on Codebase A failed: %v", err)
	}
	if addedA != 0 || modifiedA != 1 || deletedA != 0 {
		t.Errorf("expected 1 modified file in Codebase A, got: added=%d, modified=%d, deleted=%d", addedA, modifiedA, deletedA)
	}

	// Run incremental sync on Codebase B
	addedB, modifiedB, deletedB, err := merkle.UpdateIndex(tmpCodebaseB, index)
	if err != nil {
		t.Fatalf("UpdateIndex on Codebase B failed: %v", err)
	}
	if addedB != 0 || modifiedB != 1 || deletedB != 0 {
		t.Errorf("expected 1 modified file in Codebase B, got: added=%d, modified=%d, deleted=%d", addedB, modifiedB, deletedB)
	}

	// Verify search results dynamically load the newly modified code on the fly from disk
	resultsA_updated, err := db.SearchMemories(mockEmbed1, tmpCodebaseA, 5, index)
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

	resultsB_updated, err := db.SearchMemories(mockEmbed2, tmpCodebaseB, 5, index)
	if err != nil {
		t.Fatalf("failed to search updated codebase B memories: %v", err)
	}

	foundUpdatedB := false
	for _, m := range resultsB_updated {
		if strings.Contains(m.Content, "9090") {
			foundUpdatedB = true
			break
		}
	}
	if !foundUpdatedB {
		t.Error("expected search results for Codebase B to dynamically load modified content (9090) from disk")
	}
}

func TestSearchWithExactContentIntegration(t *testing.T) {
	// ponytail: skip test gracefully if the real LiteLLM server is not running in the current test environment
	conn, err := net.DialTimeout("tcp", "localhost:36253", 100*time.Millisecond)
	if err != nil {
		t.Skip("Skipping live integration test: local LiteLLM server on localhost:36253 is unreachable")
	}
	conn.Close()

	// Initialize TurboQuant once on startup (using default configurations)
	tq, err := turboquant.NewTurboQuant(turboquant.DefaultDimension, turboquant.DefaultBitWidth, turboquant.DefaultSeed)
	if err != nil {
		t.Fatalf("failed to initialize turboquant: %v", err)
	}

	// 1. Setup isolated environment
	tmpHome, err := os.MkdirTemp("", "exact-search-home-*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tmpHome)

	os.Setenv("HOME", tmpHome)
	defer os.Unsetenv("HOME")

	if err := db.InitDatabase(); err != nil {
		t.Fatalf("failed to init db: %v", err)
	}

	tqvPath, err := db.GetTQPath()
	if err != nil {
		t.Fatalf("failed to get tqv path: %v", err)
	}
	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		t.Fatalf("failed to initialize index: %v", err)
	}

	// 2. Setup codebase
	tmpCodebase, err := os.MkdirTemp("", "exact-search-codebase-*")
	if err != nil {
		t.Fatalf("failed to create temp codebase: %v", err)
	}
	defer os.RemoveAll(tmpCodebase)

	mathGoPath := filepath.Join(tmpCodebase, "math.go")
	mathCode := "package math\n\n// Add computes the sum of two integers.\nfunc Add(a, b int) int {\n\treturn a + b\n}"
	_ = os.WriteFile(mathGoPath, []byte(mathCode), 0644)

	networkGoPath := filepath.Join(tmpCodebase, "network.go")
	networkCode := "package network\n\n// Connect opens a TCP connection to the server.\nfunc Connect(address string) error {\n\treturn nil\n}"
	_ = os.WriteFile(networkGoPath, []byte(networkCode), 0644)

	// 3. Index codebase
	_, _, _, err = merkle.UpdateIndex(tmpCodebase, index)
	if err != nil {
		t.Fatalf("failed to index codebase: %v", err)
	}

	// 4. Perform Search for Math exact content
	mathQuery := "computes the sum of two integers"
	mathEmbed, err := llm.GetEmbedding(mathQuery)
	if err != nil {
		t.Fatalf("failed to get embedding for math query: %v", err)
	}

	resultsMath, err := db.SearchMemories(mathEmbed, tmpCodebase, 5, index)
	if err != nil {
		t.Fatalf("failed to search math memory: %v", err)
	}

	if len(resultsMath) == 0 {
		t.Fatalf("expected search results for math query, got 0")
	}

	bestMathResult := resultsMath[0]
	if !strings.Contains(bestMathResult.Content, "math.go") {
		t.Errorf("expected best match to be math.go, got content: %s", bestMathResult.Content)
	}
	if !strings.Contains(bestMathResult.Content, "func Add") {
		t.Errorf("expected best match to contain function body of Add, got content: %s", bestMathResult.Content)
	}

	// 5. Perform Search for Network exact content
	networkQuery := "opens a TCP connection to the server"
	networkEmbed, err := llm.GetEmbedding(networkQuery)
	if err != nil {
		t.Fatalf("failed to get embedding for network query: %v", err)
	}

	resultsNetwork, err := db.SearchMemories(networkEmbed, tmpCodebase, 5, index)
	if err != nil {
		t.Fatalf("failed to search network memory: %v", err)
	}

	if len(resultsNetwork) == 0 {
		t.Fatalf("expected search results for network query, got 0")
	}

	bestNetworkResult := resultsNetwork[0]
	if !strings.Contains(bestNetworkResult.Content, "network.go") {
		t.Errorf("expected best match to be network.go, got content: %s", bestNetworkResult.Content)
	}
	if !strings.Contains(bestNetworkResult.Content, "func Connect") {
		t.Errorf("expected best match to contain function body of Connect, got content: %s", bestNetworkResult.Content)
	}
}

package merkle

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/datnguyenzzz/semantic-grep/internal/db"
	"github.com/datnguyenzzz/semantic-grep/internal/llm"
	mockllm "github.com/datnguyenzzz/semantic-grep/internal/llm/mocks"
	"github.com/datnguyenzzz/semantic-grep/internal/turboquant"
	"github.com/stretchr/testify/mock"
)

func TestMerkleHashingAndDiffing(t *testing.T) {
	// Create a temporary directory for test workspace
	tmpDir, err := os.MkdirTemp("", "merkle-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create subdirectories and some indexable files
	err = os.MkdirAll(filepath.Join(tmpDir, "src"), 0755)
	if err != nil {
		t.Fatalf("failed to create src dir: %v", err)
	}

	file1Path := filepath.Join(tmpDir, "main.go")
	file2Path := filepath.Join(tmpDir, "src", "helper.go")
	file3Path := filepath.Join(tmpDir, "unrelated.txt")

	err = os.WriteFile(file1Path, []byte("package main\n\nfunc main() {\n\tprintln(\"hello\")\n}"), 0644)
	if err != nil {
		t.Fatalf("failed to write main.go: %v", err)
	}

	err = os.WriteFile(file2Path, []byte("package src\n\nfunc Help() {}"), 0644)
	if err != nil {
		t.Fatalf("failed to write helper.go: %v", err)
	}

	err = os.WriteFile(file3Path, []byte("some unrelated text file"), 0644)
	if err != nil {
		t.Fatalf("failed to write unrelated.txt: %v", err)
	}

	// 1. Build Merkle Tree for the initial state
	tree1, err := BuildMerkleTree(tmpDir, "")
	if err != nil {
		t.Fatalf("failed to build tree1: %v", err)
	}

	if tree1 == nil {
		t.Fatalf("tree1 should not be nil")
	}

	// Verify only Go files are present in the tree
	files := collectFiles(tree1)
	if len(files) != 2 {
		t.Errorf("expected 2 files in tree1, got %d: %v", len(files), files)
	}

	// Ensure main.go and src/helper.go are mapped, but not unrelated.txt
	hasMain := false
	hasHelper := false
	for _, f := range files {
		if f == "main.go" {
			hasMain = true
		}
		if f == "src/helper.go" {
			hasHelper = true
		}
	}
	if !hasMain || !hasHelper {
		t.Errorf("tree1 missing expected files. files: %v", files)
	}

	// 2. Diffing with same tree should return empty results
	added, modified, deleted := DiffTrees(tree1, tree1)
	if len(added) != 0 || len(modified) != 0 || len(deleted) != 0 {
		t.Errorf("diffing identical trees should yield no changes, got: added=%v, modified=%v, deleted=%v", added, modified, deleted)
	}

	// 3. Modifying a file
	err = os.WriteFile(file2Path, []byte("package src\n\nfunc Help() {\n\tprintln(\"modified\")\n}"), 0644)
	if err != nil {
		t.Fatalf("failed to update helper.go: %v", err)
	}

	tree2, err := BuildMerkleTree(tmpDir, "")
	if err != nil {
		t.Fatalf("failed to build tree2: %v", err)
	}

	added, modified, deleted = DiffTrees(tree1, tree2)
	if len(added) != 0 || len(modified) != 1 || len(deleted) != 0 || modified[0] != "src/helper.go" {
		t.Errorf("expected src/helper.go to be modified, got: added=%v, modified=%v, deleted=%v", added, modified, deleted)
	}

	// 4. Adding a file
	file4Path := filepath.Join(tmpDir, "src", "config.tf")
	err = os.WriteFile(file4Path, []byte(`resource "aws_s3_bucket" "test" {}`), 0644)
	if err != nil {
		t.Fatalf("failed to write config.tf: %v", err)
	}

	tree3, err := BuildMerkleTree(tmpDir, "")
	if err != nil {
		t.Fatalf("failed to build tree3: %v", err)
	}

	added, modified, deleted = DiffTrees(tree2, tree3)
	if len(added) != 1 || len(modified) != 0 || len(deleted) != 0 || added[0] != "src/config.tf" {
		t.Errorf("expected src/config.tf to be added, got: added=%v, modified=%v, deleted=%v", added, modified, deleted)
	}

	// 5. Deleting a file
	err = os.Remove(file1Path)
	if err != nil {
		t.Fatalf("failed to delete main.go: %v", err)
	}

	tree4, err := BuildMerkleTree(tmpDir, "")
	if err != nil {
		t.Fatalf("failed to build tree4: %v", err)
	}

	added, modified, deleted = DiffTrees(tree3, tree4)
	if len(added) != 0 || len(modified) != 0 || len(deleted) != 1 || deleted[0] != "main.go" {
		t.Errorf("expected main.go to be deleted, got: added=%v, modified=%v, deleted=%v", added, modified, deleted)
	}

	// 6. Adding a YAML file
	file5Path := filepath.Join(tmpDir, "config.yaml")
	err = os.WriteFile(file5Path, []byte("env: production\nport: 8080\n"), 0644)
	if err != nil {
		t.Fatalf("failed to write config.yaml: %v", err)
	}

	tree5, err := BuildMerkleTree(tmpDir, "")
	if err != nil {
		t.Fatalf("failed to build tree5: %v", err)
	}

	added, modified, deleted = DiffTrees(tree4, tree5)
	if len(added) != 1 || len(modified) != 0 || len(deleted) != 0 || added[0] != "config.yaml" {
		t.Errorf("expected config.yaml to be added, got: added=%v, modified=%v, deleted=%v", added, modified, deleted)
	}
}

func Test_UpdateIndexConcurrency(t *testing.T) {
	// Set target dimension to 16 for test execution
	originalDim := turboquant.DefaultDimension
	turboquant.DefaultDimension = 16
	defer func() {
		turboquant.DefaultDimension = originalDim
	}()

	// 1. Setup temporary home
	tmpDir, err := os.MkdirTemp("", "merkle-concurrency-test-*")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", originalHome)

	if err := db.InitDatabase(); err != nil {
		t.Fatalf("failed: %v", err)
	}

	// 2. Setup mock TQ index
	tq, err := turboquant.NewTurboQuant(16, 4, 42)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	tqvPath := filepath.Join(tmpDir, "test_merkle_concurrency.tqv")
	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	// 3. Register MockILLM
	mockLLM := mockllm.NewMockILLM(t)
	mockLLM.On("GetEmbedding", mock.Anything, mock.Anything).Return(func(text string, dim int) []float32 {
		mockVec := make([]float32, dim)
		mockVec[0] = 1.0
		return mockVec
	}, nil)

	llm.DefaultClient = mockLLM
	defer func() {
		llm.DefaultClient = &llm.LiteLLM{}
	}()

	// 4. Create multiple concurrent workspaces on disk
	workers := 5
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			// Create workspace and a go file inside
			workspaceDir := filepath.Join(tmpDir, fmt.Sprintf("workspace-%d", workerID))
			_ = os.MkdirAll(workspaceDir, 0755)
			fileGo := filepath.Join(workspaceDir, "main.go")
			_ = os.WriteFile(fileGo, []byte(fmt.Sprintf("package main\n\nfunc main() {\n\tprintln(\"Hello worker %d!\")\n}", workerID)), 0644)

			// Save codebase CWD in database
			_ = db.SaveMerkleTree(workspaceDir, "initial_hash", "{}")

			// Run concurrent incremental index sweeps
			_, _, _, _ = UpdateIndex(workspaceDir, index)
		}(i)
	}

	// Wait for all concurrent index sweeps to return
	wg.Wait()

	// Wait for any background async transaction database writes to complete successfully
	db.AsyncSaveWG.Wait()

	// 5. Query matching memories globally to verify they were saved cleanly without race conditions!
	results, err := db.SearchMemories("Hello worker", make([]float32, 16), "", workers*2, index)
	if err != nil {
		t.Fatalf("failed to query memories: %v", err)
	}

	if len(results) < workers {
		t.Errorf("expected at least %d memory results, got %d", workers, len(results))
	}
}

package main

import (
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datnguyenzzz/semantic-grep/internal/db"
	"github.com/datnguyenzzz/semantic-grep/internal/llm"
	mockllm "github.com/datnguyenzzz/semantic-grep/internal/llm/mocks"
	"github.com/datnguyenzzz/semantic-grep/internal/turboquant"
	"github.com/stretchr/testify/mock"
)

func Test_PeriodicIndexUpdate(t *testing.T) {
	// Set target dimension to 16 for test execution
	originalDim := turboquant.DefaultDimension
	turboquant.DefaultDimension = 16
	defer func() {
		turboquant.DefaultDimension = originalDim
	}()

	// 1. Set up a temporary home directory so we use a clean test database
	tmpHome, err := os.MkdirTemp("", "mcp-server-test-*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer func() {
		os.Setenv("HOME", originalHome)
		os.RemoveAll(tmpHome)
	}()

	// 2. Initialize the test database
	if err := db.InitDatabase(); err != nil {
		t.Fatalf("failed to init database: %v", err)
	}

	// 3. Create a temporary workspace to scan
	tmpWorkspace, err := os.MkdirTemp("", "workspace-*")
	if err != nil {
		t.Fatalf("failed to create temp workspace: %v", err)
	}
	defer os.RemoveAll(tmpWorkspace)

	// Create a dummy Go file inside the workspace so it is indexable
	dummyFile := filepath.Join(tmpWorkspace, "main.go")
	_ = os.WriteFile(dummyFile, []byte("package main\n\nfunc main() {\n\tprintln(\"Hello test!\")\n}"), 0644)

	// 4. Save codebase CWD in database
	if err := db.SaveMerkleTree(tmpWorkspace, "initial_hash", "{}"); err != nil {
		t.Fatalf("failed to save codebase: %v", err)
	}

	// Mock the LLM embedding client using the generated MockILLM
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

	// 5. Initialize TurboQuant and a test index
	tq, err := turboquant.NewTurboQuant(16, 4, 42) // small dimension 16 for faster execution
	if err != nil {
		t.Fatalf("failed to init TurboQuant: %v", err)
	}

	tqvPath := filepath.Join(tmpHome, "agent-mem.tqv")
	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		t.Fatalf("failed to init Index: %v", err)
	}

	// 6. Set short interval environment variable so background updates trigger quickly
	os.Setenv("BACKGROUND_SYNC_INTERVAL", "15ms")
	defer os.Unsetenv("BACKGROUND_SYNC_INTERVAL")

	// Disable verbose logs during test to keep output clean
	log.SetOutput(os.NewFile(0, os.DevNull))

	// 7. Start periodic background updates
	startPeriodicIndexUpdate(index)

	// Wait for 50 milliseconds to allow startup sweep and at least one periodic sweep to occur
	time.Sleep(50 * time.Millisecond)

	// Verify that the codebase main.go was successfully scanned and indexed into DuckDB gemini_memories!
	memories, err := db.SearchMemories("Hello test!", make([]float32, 16), tmpWorkspace, 5, index)
	if err != nil {
		t.Fatalf("failed to search memories: %v", err)
	}

	foundMain := false
	for _, m := range memories {
		if m.ID != "" && m.CWD == "main.go" {
			foundMain = true
			break
		}
	}

	if !foundMain {
		t.Errorf("expected background periodic update to successfully index main.go from workspace %s", tmpWorkspace)
	}
}

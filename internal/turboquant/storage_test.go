package turboquant

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStorage_LoadSave(t *testing.T) {
	// Create a temp directory for our storage file
	tmpDir, err := os.MkdirTemp("", "turboquant-storage-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	filePath := filepath.Join(tmpDir, "test_storage.tqlm")

	dim := 128
	bw := 4
	tq, err := NewTurboQuant(dim, bw, 42)
	if err != nil {
		t.Fatalf("failed to initialize TurboQuant: %v", err)
	}

	storage := NewStorage(dim, bw)

	// Create some mock quantized vectors
	v1 := make([]float32, dim)
	v1[0] = 1.0
	qv1, _ := tq.Quantize(v1)

	v2 := make([]float32, dim)
	v2[0] = -1.0
	qv2, _ := tq.Quantize(v2)

	vectors := map[string]*QuantizedVector{
		"id-1": qv1,
		"id-2": qv2,
	}

	// 1. Test Save
	err = storage.Save(filePath, tq, vectors)
	if err != nil {
		t.Fatalf("failed to save vectors: %v", err)
	}

	// 2. Test Load
	loaded, err := storage.Load(filePath, tq)
	if err != nil {
		t.Fatalf("failed to load vectors: %v", err)
	}

	if len(loaded) != 2 {
		t.Fatalf("expected 2 loaded vectors, got %d", len(loaded))
	}

	if _, ok := loaded["id-1"]; !ok {
		t.Errorf("expected id-1 to be loaded")
	}

	if _, ok := loaded["id-2"]; !ok {
		t.Errorf("expected id-2 to be loaded")
	}

	// Verify loaded vector fields match original
	if loaded["id-1"].Norm != qv1.Norm {
		t.Errorf("loaded vector norm mismatch: expected %v, got %v", qv1.Norm, loaded["id-1"].Norm)
	}
}

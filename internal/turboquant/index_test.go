package turboquant

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestIndex_AddSearchDelete(t *testing.T) {
	// Create a temp directory for our storage file
	tmpDir, err := os.MkdirTemp("", "turboquant-index-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	filePath := filepath.Join(tmpDir, "test.tqv")

	dim := 128
	tq, err := NewTurboQuant(dim, 4, 42)
	if err != nil {
		t.Fatalf("failed to initialize TurboQuant: %v", err)
	}

	index, err := NewIndex(filePath, tq)
	if err != nil {
		t.Fatalf("failed to initialize Index: %v", err)
	}

	rng := rand.New(rand.NewSource(42))

	// Generate a main vector that we will search for
	v1 := make([]float32, dim)
	for i := 0; i < dim; i++ {
		v1[i] = float32(rng.NormFloat64())
	}

	// Generate a random vector (unrelated)
	v2 := make([]float32, dim)
	for i := 0; i < dim; i++ {
		v2[i] = float32(rng.NormFloat64())
	}

	// 1. Test Add
	err = index.Add("id-1", v1)
	if err != nil {
		t.Fatalf("failed to add vector 1: %v", err)
	}

	err = index.Add("id-2", v2)
	if err != nil {
		t.Fatalf("failed to add vector 2: %v", err)
	}

	// 2. Test Search (query created by adding slight noise to v1)
	query := make([]float32, dim)
	for i := 0; i < dim; i++ {
		query[i] = v1[i] + float32(rng.NormFloat64()*0.05) // Add slight noise
	}

	results, err := index.Search(query, nil, 5)
	if err != nil {
		t.Fatalf("failed to search index: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Since query is v1 + noise, "id-1" should have a much higher similarity score and be the best match!
	if results[0].ID != "id-1" {
		t.Errorf("expected best match to be id-1, got %s", results[0].ID)
	}

	if results[0].Similarity < 0.90 {
		t.Errorf("expected high similarity for best match, got %.4f", results[0].Similarity)
	}

	// 3. Test Save & Load persistence (rebuild index from on-disk file)
	err = index.Save()
	if err != nil {
		t.Fatalf("failed to save index: %v", err)
	}

	loadedIndex, err := NewIndex(filePath, tq)
	if err != nil {
		t.Fatalf("failed to load index: %v", err)
	}

	loadedResults, err := loadedIndex.Search(query, nil, 5)
	if err != nil {
		t.Fatalf("failed to search loaded index: %v", err)
	}

	if len(loadedResults) != 2 || loadedResults[0].ID != "id-1" {
		t.Errorf("loaded index search results do not match")
	}

	// 4. Test Delete
	index.Delete("id-1")

	resultsAfterDelete, err := index.Search(query, nil, 5)
	if err != nil {
		t.Fatalf("failed to search after deletion: %v", err)
	}

	if len(resultsAfterDelete) != 1 {
		t.Fatalf("expected 1 result after delete, got %d", len(resultsAfterDelete))
	}

	if resultsAfterDelete[0].ID != "id-2" {
		t.Errorf("expected id-2 to remain after deleting id-1, got %s", resultsAfterDelete[0].ID)
	}
}

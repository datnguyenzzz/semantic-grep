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

func TestIndex_NoisyQueryAccuracy(t *testing.T) {
	// Create a temp directory for our storage file
	tmpDir, err := os.MkdirTemp("", "turboquant-noisy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	filePath := filepath.Join(tmpDir, "noisy_test.tqv")

	dim := 256
	tq, err := NewTurboQuant(dim, 4, 123) // different seed
	if err != nil {
		t.Fatalf("failed to initialize TurboQuant: %v", err)
	}

	index, err := NewIndex(filePath, tq)
	if err != nil {
		t.Fatalf("failed to initialize Index: %v", err)
	}

	rng := rand.New(rand.NewSource(12345))

	numVectors := 10
	sourceVecs := make([][]float32, numVectors)

	// Generate and index 10 distinct, high-dimensional vectors
	for i := 0; i < numVectors; i++ {
		v := make([]float32, dim)
		for j := 0; j < dim; j++ {
			v[j] = float32(rng.NormFloat64())
		}
		sourceVecs[i] = v

		id := string(rune('A' + i)) // "A", "B", "C", ...
		err = index.Add(id, v)
		if err != nil {
			t.Fatalf("failed to add vector %s: %v", id, err)
		}
	}

	// For each vector, create a noisy query and verify the search finds the exact source vector
	for i := range numVectors {
		targetID := string(rune('A' + i))

		// Create query by adding small random noise (e.g. 0.05 scaling factor) to the source vector
		query := make([]float32, dim)
		for j := range dim {
			query[j] = sourceVecs[i][j] + float32(rng.NormFloat64()*0.05)
		}

		// Search with limit 1
		results, err := index.Search(query, nil, 1)
		if err != nil {
			t.Fatalf("failed to search index for query %s: %v", targetID, err)
		}

		if len(results) != 1 {
			t.Fatalf("expected exactly 1 search result, got %d", len(results))
		}

		// Assert that the best matching ID is exactly the original source vector ID!
		if results[0].ID != targetID {
			t.Errorf("accuracy check failed: queried noisy version of %s but search matched %s", targetID, results[0].ID)
		}

		// Also assert that the similarity score is high (typically > 0.90)
		if results[0].Similarity < 0.90 {
			t.Errorf("expected high similarity for exact noisy match of %s, got %.4f", targetID, results[0].Similarity)
		}
	}
}

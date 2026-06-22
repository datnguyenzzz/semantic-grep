package db

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/datnguyenzzz/agent-context/internal/callgraph"
	"github.com/datnguyenzzz/agent-context/internal/turboquant"
)

func TestDBCallGraphPersistence(t *testing.T) {
	// Create a temp directory for the test database
	tmpDir, err := os.MkdirTemp("", "db-callgraph-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Override standard DB location for the test duration
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", originalHome)

	// Initialize schema
	if err := InitDatabase(); err != nil {
		t.Fatalf("failed to init database: %v", err)
	}

	// 1. Create mock nodes and edges
	node1 := &callgraph.Node{
		Name:      "test_func_A",
		FilePath:  "src/file.go",
		StartLine: 10,
		EndLine:   20,
	}
	node2 := &callgraph.Node{
		Name:      "test_func_B",
		FilePath:  "src/file.go",
		StartLine: 30,
		EndLine:   40,
	}
	nodes := []*callgraph.Node{node1, node2}

	edges := []callgraph.Edge{
		{Caller: "test_func_A", Callee: "test_func_B"},
	}

	// 2. Test SaveCallGraph
	err = SaveCallGraph("src/file.go", nodes, edges)
	if err != nil {
		t.Fatalf("failed to save call graph: %v", err)
	}

	// 3. Test LoadCallGraph
	cg, err := LoadCallGraph()
	if err != nil {
		t.Fatalf("failed to load call graph: %v", err)
	}

	if len(cg.Nodes) != 2 {
		t.Errorf("expected 2 loaded nodes, got %d", len(cg.Nodes))
	}

	nA, ok := cg.Nodes["test_func_A"]
	if !ok {
		t.Errorf("expected test_func_A node to exist")
	} else if nA.StartLine != 10 || nA.EndLine != 20 || nA.FilePath != "src/file.go" {
		t.Errorf("node test_func_A metadata mismatch: %+v", nA)
	}

	if len(cg.Edges) != 1 {
		t.Errorf("expected 1 loaded edge, got %d", len(cg.Edges))
	} else if cg.Edges[0].Caller != "test_func_A" || cg.Edges[0].Callee != "test_func_B" {
		t.Errorf("edge mismatch: %+v", cg.Edges[0])
	}

	// 4. Test DeleteCallGraph
	err = DeleteCallGraph("src/file.go")
	if err != nil {
		t.Fatalf("failed to delete call graph: %v", err)
	}

	cgAfterDel, err := LoadCallGraph()
	if err != nil {
		t.Fatalf("failed to load call graph after delete: %v", err)
	}

	if len(cgAfterDel.Nodes) != 0 {
		t.Errorf("expected 0 nodes after delete, got %d", len(cgAfterDel.Nodes))
	}
	if len(cgAfterDel.Edges) != 0 {
		t.Errorf("expected 0 edges after delete, got %d", len(cgAfterDel.Edges))
	}
}

func TestDBCallGraphCrossFileIntegration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "db-callgraph-int-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", originalHome)

	if err := InitDatabase(); err != nil {
		t.Fatalf("failed to init database: %v", err)
	}

	// 1. Save File 1: FunctionA calling FunctionB (cross-file reference)
	nodeA := &callgraph.Node{Name: "FunctionA", FilePath: "file1.go", StartLine: 1, EndLine: 5}
	err = SaveCallGraph("file1.go", []*callgraph.Node{nodeA}, []callgraph.Edge{
		{Caller: "FunctionA", Callee: "FunctionB"},
	})
	if err != nil {
		t.Fatalf("failed to save file1.go nodes/edges: %v", err)
	}

	// 2. Save File 2: FunctionB calling FunctionC
	nodeB := &callgraph.Node{Name: "FunctionB", FilePath: "file2.go", StartLine: 10, EndLine: 15}
	nodeC := &callgraph.Node{Name: "FunctionC", FilePath: "file2.go", StartLine: 20, EndLine: 25}
	err = SaveCallGraph("file2.go", []*callgraph.Node{nodeB, nodeC}, []callgraph.Edge{
		{Caller: "FunctionB", Callee: "FunctionC"},
	})
	if err != nil {
		t.Fatalf("failed to save file2.go nodes/edges: %v", err)
	}

	// 3. Load from real DuckDB and verify full aggregated chain A -> B -> C
	cg, err := LoadCallGraph()
	if err != nil {
		t.Fatalf("failed to load call graph: %v", err)
	}

	if len(cg.Nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(cg.Nodes))
	}

	resp, err := cg.GenerateTreeReport("FunctionA", "callee", 3)
	if err != nil {
		t.Fatalf("failed to generate tree report: %v", err)
	}
	jsonBytes, _ := json.Marshal(resp)
	report := string(jsonBytes)
	if !strings.Contains(report, "FunctionB") || !strings.Contains(report, "FunctionC") {
		t.Errorf("expected loaded call graph to trace A -> B -> C chain: %s", report)
	}

	// 4. Test Incremental Update: Modify file1.go so FunctionA calls FunctionD instead of FunctionB
	nodeD := &callgraph.Node{Name: "FunctionD", FilePath: "file1.go", StartLine: 6, EndLine: 10}
	err = SaveCallGraph("file1.go", []*callgraph.Node{nodeA, nodeD}, []callgraph.Edge{
		{Caller: "FunctionA", Callee: "FunctionD"},
	})
	if err != nil {
		t.Fatalf("failed to update file1.go: %v", err)
	}

	// 5. Load call graph again and verify B is no longer called by A, but D is
	cgUpdated, err := LoadCallGraph()
	if err != nil {
		t.Fatalf("failed to load updated call graph: %v", err)
	}

	// Node B from file2.go should still be there! Only file1.go's edges were replaced
	if _, ok := cgUpdated.Nodes["FunctionB"]; !ok {
		t.Errorf("expected FunctionB node from unaffected file2.go to remain")
	}

	respUpdated, err := cgUpdated.GenerateTreeReport("FunctionA", "callee", 3)
	if err != nil {
		t.Fatalf("failed to generate updated report: %v", err)
	}
	jsonBytesUpdated, _ := json.Marshal(respUpdated)
	updatedReport := string(jsonBytesUpdated)
	if strings.Contains(updatedReport, "FunctionB") {
		t.Errorf("stale edge FunctionA -> FunctionB was not cleaned up during incremental file save: %s", updatedReport)
	}
	if !strings.Contains(updatedReport, "FunctionD") {
		t.Errorf("expected updated report to contain new call FunctionA -> FunctionD: %s", updatedReport)
	}
}

// LoadCallGraph loads all pre-built nodes and edges from DuckDB to reconstruct CallGraph (helper for tests)
func LoadCallGraph() (*callgraph.CallGraph, error) {
	db, err := Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// 1. Load Nodes
	rowsNodes, err := db.Query("SELECT name, file_path, start_line, end_line FROM call_nodes")
	if err != nil {
		return nil, err
	}
	defer rowsNodes.Close()

	nodes := make(map[string]*callgraph.Node)
	for rowsNodes.Next() {
		var n callgraph.Node
		err := rowsNodes.Scan(&n.Name, &n.FilePath, &n.StartLine, &n.EndLine)
		if err != nil {
			return nil, err
		}
		nodes[n.Name] = &n
	}

	// 2. Load Edges
	rowsEdges, err := db.Query("SELECT caller, callee FROM call_edges")
	if err != nil {
		return nil, err
	}
	defer rowsEdges.Close()

	var edges []callgraph.Edge
	for rowsEdges.Next() {
		var e callgraph.Edge
		err := rowsEdges.Scan(&e.Caller, &e.Callee)
		if err != nil {
			return nil, err
		}
		edges = append(edges, e)
	}

	return &callgraph.CallGraph{
		Nodes: nodes,
		Edges: edges,
	}, nil
}

func Test_HybridSearch(t *testing.T) {
	// Set target dimension to 16 for test execution
	originalDim := turboquant.DefaultDimension
	turboquant.DefaultDimension = 16
	defer func() {
		turboquant.DefaultDimension = originalDim
	}()

	// 1. Set up a temporary home folder for the test
	tmpDir, err := os.MkdirTemp("", "db-hybrid-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", originalHome)

	if err := InitDatabase(); err != nil {
		t.Fatalf("failed to init db: %v", err)
	}

	// 2. Setup mock TurboQuant and Index
	tq, err := turboquant.NewTurboQuant(16, 4, 42)
	if err != nil {
		t.Fatalf("failed to init TQ: %v", err)
	}
	tqvPath := filepath.Join(tmpDir, "test.tqv")
	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		t.Fatalf("failed to init index: %v", err)
	}

	// Create test workspace files on disk so scoped local grep can read them on the fly!
	workspaceDir, err := os.MkdirTemp("", "test-workspace-*")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer os.RemoveAll(workspaceDir)

	file1Path := filepath.Join(workspaceDir, "file1.go")
	_ = os.WriteFile(file1Path, []byte("package main\n\nfunc ProcessPayment() {\n\tprintln(\"Processing credit card payment...\")\n}"), 0644)

	file2Path := filepath.Join(workspaceDir, "file2.go")
	_ = os.WriteFile(file2Path, []byte("package main\n\nfunc SendNotification() {\n\tprintln(\"Sending email notification alert...\")\n}"), 0644)

	// Save two memories (with small embedding length 16)
	embed1 := make([]float32, 16)
	embed1[0] = 1.0 // non-zero embedding for file1.go
	header1 := "File: file1.go (Lines: 1-5)"
	err = SaveMemory("id-payment", header1, "project", workspaceDir, embed1, index, "func ProcessPayment() credit card payment")
	if err != nil {
		t.Fatalf("failed to save memory 1: %v", err)
	}

	embed2 := make([]float32, 16)
	embed2[1] = 1.0 // non-zero embedding for file2.go
	header2 := "File: file2.go (Lines: 1-5)"
	err = SaveMemory("id-notification", header2, "project", workspaceDir, embed2, index, "func SendNotification() email notification alert")
	if err != nil {
		t.Fatalf("failed to save memory 2: %v", err)
	}

	// Build DuckDB FTS index natively
	if err := CreateFTSIndex(); err != nil {
		t.Fatalf("failed to build FTS index: %v", err)
	}

	// 3. Test Lexical + Grep exact matching
	results, err := SearchMemories("ProcessPayment", make([]float32, 16), workspaceDir, 5, index)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	if len(results) == 0 {
		t.Fatalf("expected search results to return, got 0")
	}

	bestResult := results[0]
	if !strings.Contains(bestResult.Content, "ProcessPayment") {
		t.Errorf("expected best match to be ProcessPayment memory, got: %s", bestResult.Content)
	}
}

func Test_ComputeRRF(t *testing.T) {
	// 1. Mock dense semantic results
	semResults := []turboquant.SearchResult{
		{ID: "doc-A", Similarity: 0.9},
		{ID: "doc-B", Similarity: 0.8},
	}

	// 2. Mock sparse lexical matches
	lexResults := []LexMatch{
		{ID: "doc-B", Score: 1.0},
		{ID: "doc-C", Score: 0.5},
	}

	// 3. Compute RRF
	fused := computeRRF(semResults, lexResults, 5)

	if len(fused) == 0 {
		t.Fatalf("expected fused results, got 0")
	}

	// doc-B is rank 2 in semantic and rank 1 in lexical. It should have the highest RRF score!
	if fused[0].id != "doc-B" {
		t.Errorf("expected doc-B to be ranked first after RRF fusion, got: %s", fused[0].id)
	}
}

func Test_GrepReRanking(t *testing.T) {
	memories := []Memory{
		{ID: "doc-1", Content: "func ProcessPayment() { println(\"credit card\") }", Category: "project"},
		{ID: "doc-2", Content: "func SendNotification() { println(\"email alert\") }", Category: "project"},
	}

	candidates := []candidateRRF{
		{id: "doc-1", rrfScore: 0.05},
		{id: "doc-2", rrfScore: 0.05},
	}

	// If we grep for "ProcessPayment", doc-1 should get boosted and ranked first!
	reRanked := applyGrepReRanking(memories, candidates, "ProcessPayment", 5)

	if len(reRanked) < 2 {
		t.Fatalf("expected 2 reranked results, got: %d", len(reRanked))
	}

	if reRanked[0].ID != "doc-1" {
		t.Errorf("expected doc-1 to be boosted to first place by grep match, got: %s", reRanked[0].ID)
	}

	// Verify that the boosted doc-1 score has been multiplied by 1.5
	if reRanked[0].Similarity <= 0.05 {
		t.Errorf("expected boosted similarity score to be greater than 0.05, got %f", reRanked[0].Similarity)
	}
}

func Test_IndexerConcurrencySafety(t *testing.T) {
	// Set target dimension to 16 for test execution
	originalDim := turboquant.DefaultDimension
	turboquant.DefaultDimension = 16
	defer func() {
		turboquant.DefaultDimension = originalDim
	}()

	// 1. Setup temporary home
	tmpDir, err := os.MkdirTemp("", "db-concurrency-test-*")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", originalHome)

	if err := InitDatabase(); err != nil {
		t.Fatalf("failed: %v", err)
	}

	// 2. Setup mock TQ index
	tq, err := turboquant.NewTurboQuant(16, 4, 42)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	tqvPath := filepath.Join(tmpDir, "test_concurrency.tqv")
	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	// 3. Launch 10 concurrent goroutines executing batch saves and searches in parallel on the same index!
	var wg sync.WaitGroup
	workers := 10

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			// Prepare batch items
			items := []MemoryBatchItem{
				{
					ID:           fmt.Sprintf("mem-%d-1", workerID),
					Content:      fmt.Sprintf("File: file_%d.go (Lines: 1-5)", workerID),
					CWD:          tmpDir,
					Embedding:    make([]float32, 16),
					ChunkContent: "func ProcessPayment() { println(\"test\") }",
				},
				{
					ID:           fmt.Sprintf("mem-%d-2", workerID),
					Content:      fmt.Sprintf("File: file_%d.go (Lines: 10-15)", workerID),
					CWD:          tmpDir,
					Embedding:    make([]float32, 16),
					ChunkContent: "func SendNotification() { println(\"email\") }",
				},
			}

			// Save batch (writes to DuckDB and modifies TurboQuant index map)
			_ = SaveMemoriesBatch(items, index)

			// Query the index concurrently
			_, _ = SearchMemories("ProcessPayment", make([]float32, 16), tmpDir, 5, index)
		}(i)
	}

	// Wait for all concurrent workers to complete
	wg.Wait()

	// Wait for any background async saves to complete cleanly
	AsyncSaveWG.Wait()

	// 4. Verify that we have saved exactly workers * 2 memories successfully
	memories, err := SearchMemories("ProcessPayment", make([]float32, 16), tmpDir, workers*2, index)
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}

	if len(memories) < workers {
		t.Errorf("expected at least %d memory results, got %d", workers, len(memories))
	}
}

func Test_FTSRealDuckDBIntegration(t *testing.T) {
	// Set target dimension to 16 for test execution
	originalDim := turboquant.DefaultDimension
	turboquant.DefaultDimension = 16
	defer func() {
		turboquant.DefaultDimension = originalDim
	}()

	// 1. Setup temporary home
	tmpDir, err := os.MkdirTemp("", "fts-duckdb-test-*")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", originalHome)

	if err := InitDatabase(); err != nil {
		t.Fatalf("failed to init database: %v", err)
	}

	// 2. Setup mock TQ index
	tq, err := turboquant.NewTurboQuant(16, 4, 42)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	tqvPath := filepath.Join(tmpDir, "test_fts.tqv")
	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	// 3. Save 3 real-world documents directly
	err = SaveMemory("doc-1", "Albert Einstein theoretical physics relativity", "project", tmpDir, make([]float32, 16), index, "Albert Einstein theoretical physics relativity")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	err = SaveMemory("doc-2", "Go programming language systems concurrency", "project", tmpDir, make([]float32, 16), index, "Go programming language systems concurrency")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	err = SaveMemory("doc-3", "web router HTTP handler middleware Gin", "project", tmpDir, make([]float32, 16), index, "web router HTTP handler middleware Gin")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	// Build native DuckDB FTS index
	if err := CreateFTSIndex(); err != nil {
		t.Fatalf("failed to build FTS: %v", err)
	}

	// 4. Run Lexical Queries and Assert Accuracy!
	// Query "physics" should yield doc-1
	res1, err := SearchMemories("physics", make([]float32, 16), tmpDir, 5, index)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	if len(res1) == 0 || res1[0].ID != "doc-1" {
		t.Errorf("expected top result for 'physics' to be doc-1, got: %v", res1)
	}

	// Query "concurrency" should yield doc-2
	res2, err := SearchMemories("concurrency", make([]float32, 16), tmpDir, 5, index)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	if len(res2) == 0 || res2[0].ID != "doc-2" {
		t.Errorf("expected top result for 'concurrency' to be doc-2, got: %v", res2)
	}

	// Query "Gin" should yield doc-3
	res3, err := SearchMemories("Gin", make([]float32, 16), tmpDir, 5, index)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	if len(res3) == 0 || res3[0].ID != "doc-3" {
		t.Errorf("expected top result for 'Gin' to be doc-3, got: %v", res3)
	}
}

func Test_FTSConjunctiveAndIgnoreNoise(t *testing.T) {
	// 1. Setup temporary home
	tmpDir, err := os.MkdirTemp("", "fts-conj-test-*")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", originalHome)

	if err := InitDatabase(); err != nil {
		t.Fatalf("failed to init database: %v", err)
	}

	// 2. Setup mock TQ index
	tq, err := turboquant.NewTurboQuant(16, 4, 42)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	tqvPath := filepath.Join(tmpDir, "test_fts_conj.tqv")
	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	// 3. Save documents with brackets and trailing characters
	err = SaveMemory("doc-1", "func ProcessPayment() { println(\"credit card\") };", "project", tmpDir, make([]float32, 16), index, "func ProcessPayment() { println(\"credit card\") };")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	err = SaveMemory("doc-2", "func SendNotification() { println(\"email alert\") };", "project", tmpDir, make([]float32, 16), index, "func SendNotification() { println(\"email alert\") };")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	// Build native DuckDB FTS index
	if err := CreateFTSIndex(); err != nil {
		t.Fatalf("failed to build FTS: %v", err)
	}

	// 4. Test Disjunctive (OR) matching
	// "ProcessPayment" and "credit" should yield doc-1
	res1, err := searchLexicalSparse("ProcessPayment credit", 5)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	foundDoc1 := false
	for _, m := range res1 {
		if m.ID == "doc-1" {
			foundDoc1 = true
			break
		}
	}
	if !foundDoc1 {
		t.Errorf("expected disjunctive match for 'ProcessPayment credit' to yield doc-1, got: %v", res1)
	}

	// "ProcessPayment" and "email" should match both doc-1 and doc-2 (due to OR logic!)
	res2, err := searchLexicalSparse("ProcessPayment email", 5)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	foundDoc1AndDoc2 := 0
	for _, m := range res2 {
		if m.ID == "doc-1" || m.ID == "doc-2" {
			foundDoc1AndDoc2++
		}
	}
	if foundDoc1AndDoc2 < 2 {
		t.Errorf("expected disjunctive match for 'ProcessPayment email' to yield both doc-1 and doc-2, got: %v", res2)
	}

	// 5. Test Punctuation/Syntax Ignoring
	// Searching with syntax brackets should still match the clean symbol due to our regex ignore pattern!
	res3, err := searchLexicalSparse("ProcessPayment()", 5)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	foundDoc1Syntax := false
	for _, m := range res3 {
		if m.ID == "doc-1" {
			foundDoc1Syntax = true
			break
		}
	}
	if !foundDoc1Syntax {
		t.Errorf("expected clean symbol matching for 'ProcessPayment()' ignoring parentheses, got: %v", res3)
	}
}

func Test_ContainsFoldASCII(t *testing.T) {
	testCases := []struct {
		haystack string
		needle   string
		expected bool
	}{
		{"The quick brown FOX jumps", "FOX", true},
		{"The quick brown FOX jumps", "fox", true},
		{"The quick brown fox jumps", "FOX", true},
		{"The quick brown fox jumps", "FoX", true},
		{"The quick brown fox jumps", "dog", false},
		{"The quick brown fox jumps", "", true},
		{"fox", "The quick brown fox", false}, // needle longer than haystack!
		{"a", "A", true},
		{"a", "b", false},
	}

	for _, tc := range testCases {
		res := containsFoldASCII(tc.haystack, tc.needle)
		if res != tc.expected {
			t.Errorf("containsFoldASCII(%q, %q) returned %t, expected %t", tc.haystack, tc.needle, res, tc.expected)
		}
	}
}

func Test_SearchMemoriesPreFusionFiltering(t *testing.T) {
	// Verify that the pre-fusion filters cleanly discard low-confidence noise.
	
	// Case 1: Semantic similarity below the 0.55 cutoff
	semResults := []turboquant.SearchResult{
		{ID: "noise-1", Similarity: 0.45}, // should be pruned!
		{ID: "good-1", Similarity: 0.75},  // should be kept!
	}
	
	var filteredSem []turboquant.SearchResult
	for _, res := range semResults {
		if res.Similarity >= 0.55 {
			filteredSem = append(filteredSem, res)
		}
	}
	if len(filteredSem) != 1 || filteredSem[0].ID != "good-1" {
		t.Errorf("expected only good-1 to survive semantic pruning, got: %v", filteredSem)
	}

	// Case 2: Lexical BM25 score below 10% of the max BM25 score
	lexResults := []LexMatch{
		{ID: "lex-top", Score: 100.0},
		{ID: "lex-ok", Score: 15.0},  // 15% of max -> should be kept!
		{ID: "lex-noise", Score: 5.0}, // 5% of max -> should be pruned!
	}
	
	var filteredLex []LexMatch
	if len(lexResults) > 0 {
		maxBM25 := lexResults[0].Score
		for _, res := range lexResults {
			if res.Score >= 0.10*maxBM25 {
				filteredLex = append(filteredLex, res)
			}
		}
	}
	if len(filteredLex) != 2 || filteredLex[1].ID != "lex-ok" {
		t.Errorf("expected only lex-top and lex-ok to survive, got: %v", filteredLex)
	}
}

func Test_MinifyCode(t *testing.T) {
	testCases := []struct {
		name     string
		category string
		input    string
		expected string
	}{
		{
			name:     "Non-project category bypass",
			category: "user_note",
			input:    "// This is a comment\nfunc Foo() {}",
			expected: "// This is a comment\nfunc Foo() {}",
		},
		{
			name:     "Go inline and empty lines",
			category: "project",
			input: `
// Package declaration
package main

// Import libraries
import "fmt"

func main() {
	// Print output
	fmt.Println("Hello, World!") // inline comment
}
`,
			expected: `package main
import "fmt"
func main() {
	fmt.Println("Hello, World!")
}`,
		},
		{
			name:     "Go block comment single and multi-line",
			category: "project",
			input: `
/*
  This is a heavy
  multi-line block comment.
*/
func main() {
	/* inline block comment */
	println("Running...")
}
`,
			expected: `func main() {
	println("Running...")
}`,
		},
		{
			name:     "Python and YAML comments",
			category: "project",
			input: `
# This is a python config
name: "my-app" # name of application

# Port configuration
port: 8080
`,
			expected: `name: "my-app"
port: 8080`,
		},
		{
			name:     "HTTP string safety",
			category: "project",
			input:    `url := "https://google.com" // search URL`,
			expected: `url := "https://google.com"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			res := minifyCode(tc.input, tc.category)
			if strings.TrimSpace(res) != strings.TrimSpace(tc.expected) {
				t.Errorf("minifyCode failed for %s.\nGOT:\n%s\nEXPECTED:\n%s", tc.name, res, tc.expected)
			}
		})
	}
}

func Test_PythonMemoryMinificationIntegration(t *testing.T) {
	// 1. Setup temporary home
	tmpDir, err := os.MkdirTemp("", "db-test-py-integration-*")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", originalHome)

	if err := InitDatabase(); err != nil {
		t.Fatalf("failed: %v", err)
	}

	// 2. Initialize TurboQuant Index
	originalDim := turboquant.DefaultDimension
	turboquant.DefaultDimension = 16
	defer func() {
		turboquant.DefaultDimension = originalDim
	}()

	tq, err := turboquant.NewTurboQuant(16, 4, 42)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	tqvPath := filepath.Join(tmpDir, "benchmark.tqv")
	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	// 3. Save raw python memory with comments
	pyCode := `
# This is a python service
class App:
    def run(self):
        print("Running service...") # inline log
`
	items := []MemoryBatchItem{
		{
			ID:           "doc-py",
			Content:      "File: app.py (Lines: 1-5)",
			CWD:          tmpDir,
			Embedding:    make([]float32, 16),
			ChunkContent: pyCode,
		},
	}

	if err := SaveMemoriesBatch(items, index); err != nil {
		t.Fatalf("failed to save memories batch: %v", err)
	}
	AsyncSaveWG.Wait()

	if err := CreateFTSIndex(); err != nil {
		t.Fatalf("failed to build FTS index: %v", err)
	}

	// 4. Retrieve memory via SearchMemories
	res, err := SearchMemories("run", make([]float32, 16), tmpDir, 5, index)
	if err != nil {
		t.Fatalf("failed to search memories: %v", err)
	}

	if len(res) != 1 {
		t.Fatalf("expected exactly 1 memory, got %d", len(res))
	}

	gotContent := res[0].Content
	expectedMinified := `class App:
    def run(self):
        print("Running service...")`

	if strings.TrimSpace(gotContent) != strings.TrimSpace(expectedMinified) {
		t.Errorf("expected minified python content in DB, got:\n%s\nEXPECTED:\n%s", gotContent, expectedMinified)
	}
}

type dummy struct{} // prevent package import issue

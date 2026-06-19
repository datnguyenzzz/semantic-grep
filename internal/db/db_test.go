package db

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"agent-mem/internal/callgraph"
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

type dummy struct{} // prevent package import issue

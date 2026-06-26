//go:build integration

package scripts

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/datnguyenzzz/semantic-grep/internal/callgraph"
	"github.com/datnguyenzzz/semantic-grep/internal/db"
	"github.com/datnguyenzzz/semantic-grep/internal/llm"
	"github.com/datnguyenzzz/semantic-grep/internal/merkle"
	"github.com/datnguyenzzz/semantic-grep/internal/turboquant"
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

	mockEmbed1, err := llm.GetEmbedding("func Main", turboquant.DefaultDimension)
	if err != nil {
		t.Fatalf("failed to get real embedding: %v", err)
	}

	mockEmbed2, err := llm.GetEmbedding("port: 8080", turboquant.DefaultDimension)
	if err != nil {
		t.Fatalf("failed to get real embedding: %v", err)
	}

	// Verify project chunks search correctly separates codebase origin
	resultsA, err := db.SearchMemories("func Main", mockEmbed1, tmpCodebaseA, 5, index)
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
	resultsA_updated, err := db.SearchMemories("A modified", mockEmbed1, tmpCodebaseA, 5, index)
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

	resultsB_updated, err := db.SearchMemories("port: 9090", mockEmbed2, tmpCodebaseB, 5, index)
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
	mathEmbed, err := llm.GetEmbedding(mathQuery, turboquant.DefaultDimension)
	if err != nil {
		t.Fatalf("failed to get embedding for math query: %v", err)
	}

	resultsMath, err := db.SearchMemories(mathQuery, mathEmbed, tmpCodebase, 5, index)
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
	networkEmbed, err := llm.GetEmbedding(networkQuery, turboquant.DefaultDimension)
	if err != nil {
		t.Fatalf("failed to get embedding for network query: %v", err)
	}

	resultsNetwork, err := db.SearchMemories(networkQuery, networkEmbed, tmpCodebase, 5, index)
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

func TestCallGraphE2EIntegration(t *testing.T) {
	// 1. Skip test gracefully if LiteLLM is offline
	conn, err := net.DialTimeout("tcp", "localhost:36253", 100*time.Millisecond)
	if err != nil {
		t.Skip("Skipping live integration test: local LiteLLM server on localhost:36253 is unreachable")
	}
	conn.Close()

	// 2. Setup isolated Home & DB
	tmpHome, err := os.MkdirTemp("", "callgraph-e2e-home-*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tmpHome)

	os.Setenv("HOME", tmpHome)
	defer os.Unsetenv("HOME")

	if err := db.InitDatabase(); err != nil {
		t.Fatalf("failed to init database: %v", err)
	}

	tqvPath, err := db.GetTQPath()
	if err != nil {
		t.Fatalf("failed to get tqv path: %v", err)
	}
	tq, err := turboquant.NewTurboQuant(turboquant.DefaultDimension, turboquant.DefaultBitWidth, turboquant.DefaultSeed)
	if err != nil {
		t.Fatalf("failed to init TurboQuant: %v", err)
	}
	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		t.Fatalf("failed to init Index: %v", err)
	}

	// 3. Setup mock codebase directory
	tmpCodebase, err := os.MkdirTemp("", "callgraph-e2e-codebase-*")
	if err != nil {
		t.Fatalf("failed to create temp codebase: %v", err)
	}
	defer os.RemoveAll(tmpCodebase)

	// File 1: Go (A -> B)
	goContent := `package main
func FunctionA() {
	FunctionB()
}
func FunctionB() {}
`
	_ = os.WriteFile(filepath.Join(tmpCodebase, "main.go"), []byte(goContent), 0644)

	// File 2: Terraform (module referencing subnet)
	tfContent := `
resource "aws_subnet" "public" {
  vpc_id = module.vpc.vpc_id
}
module "vpc" {
  source = "./vpc"
}
`
	_ = os.WriteFile(filepath.Join(tmpCodebase, "main.tf"), []byte(tfContent), 0644)

	// File 3: YAML (Deploy needs Build)
	yamlContent := `
steps:
  - name: Build
    id: build_step
  - name: Deploy
    needs: [Build]
`
	_ = os.WriteFile(filepath.Join(tmpCodebase, "pipeline.yaml"), []byte(yamlContent), 0644)

	// 4. Run full Merkle tree index update
	_, _, _, err = merkle.UpdateIndex(tmpCodebase, index)
	if err != nil {
		t.Fatalf("failed to execute full indexer sync: %v", err)
	}

	// 5. Query and verify Go Call Graph Node & Edge from real DuckDB lazily
	nodeA, err := db.GetCallNode("FunctionA")
	if err != nil {
		t.Fatalf("failed to query Go call node FunctionA from DuckDB: %v", err)
	}
	if nodeA == nil || nodeA.Name != "FunctionA" {
		t.Errorf("FunctionA node metadata was not saved or loaded correctly: %+v", nodeA)
	}

	calleesA, err := db.GetCallees("FunctionA")
	if err != nil {
		t.Fatalf("failed to query callees of FunctionA: %v", err)
	}
	if len(calleesA) != 1 || calleesA[0].Name != "FunctionB" {
		t.Errorf("expected 1 callee (FunctionB), got: %+v", calleesA)
	}

	// 6. Query and verify Terraform Dependency Graph Node & Edge from real DuckDB lazily
	tfSubnetNode, err := db.GetCallNode("aws_subnet.public")
	if err != nil {
		t.Fatalf("failed to query TF subnet node: %v", err)
	}
	if tfSubnetNode == nil {
		t.Errorf("expected TF subnet node to be indexed and stored in DuckDB")
	}

	calleesSubnet, err := db.GetCallees("resource.aws_subnet.public")
	if err != nil {
		t.Fatalf("failed to query TF subnet callees: %v", err)
	}
	if len(calleesSubnet) != 1 || calleesSubnet[0].Name != "module.vpc" {
		t.Errorf("expected subnet callee to be module.vpc, got: %+v", calleesSubnet)
	}

	// 7. Query and verify YAML Dependency Graph Node & Edge from real DuckDB lazily
	yamlDeployNode, err := db.GetCallNode("step.Deploy")
	if err != nil {
		t.Fatalf("failed to query YAML Deploy step: %v", err)
	}
	if yamlDeployNode == nil {
		t.Errorf("expected YAML step.Deploy node to be indexed and stored in DuckDB")
	}

	calleesDeploy, err := db.GetCallees("step.Deploy")
	if err != nil {
		t.Fatalf("failed to query YAML Deploy callees: %v", err)
	}
	if len(calleesDeploy) != 1 || calleesDeploy[0].Name != "step.Build" {
		t.Errorf("expected step.Deploy to depend on step.Build, got: %+v", calleesDeploy)
	}
}

func TestCallGraphOnDemandDBQuerying(t *testing.T) {
	// Create an isolated temp directory for our DuckDB database file
	tmpHome, err := os.MkdirTemp("", "callgraph-lazy-db-*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tmpHome)

	// Set HOME environment so the DB path resolves inside tmpHome
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", originalHome)

	if err := db.InitDatabase(); err != nil {
		t.Fatalf("failed to init database: %v", err)
	}

	// 1. Populate nodes and edges inside our real DuckDB instance
	nodeA := &callgraph.Node{Name: "FunctionA", FilePath: "file.go", StartLine: 1, EndLine: 5}
	nodeB := &callgraph.Node{Name: "FunctionB", FilePath: "file.go", StartLine: 10, EndLine: 15}
	nodeC := &callgraph.Node{Name: "FunctionC", FilePath: "file.go", StartLine: 20, EndLine: 25}

	err = db.SaveCallGraph("file.go", []*callgraph.Node{nodeA, nodeB, nodeC}, []callgraph.Edge{
		{Caller: "FunctionA", Callee: "FunctionB"},
		{Caller: "FunctionB", Callee: "FunctionC"},
	})
	if err != nil {
		t.Fatalf("failed to save mock call graph: %v", err)
	}

	// 2. Perform target node lookup using db.GetCallNode
	targetNode, err := db.GetCallNode("FunctionA")
	if err != nil {
		t.Fatalf("failed to fetch target call node: %v", err)
	}
	if targetNode == nil || targetNode.Name != "FunctionA" {
		t.Fatalf("expected to retrieve targetNode for FunctionA, got: %+v", targetNode)
	}

	// 3. Execute the actual search_call_graph tool's lazy database-querying report logic
	resp, err := callgraph.GenerateOnDemandTreeReport(targetNode, "both", 3, db.GetCallees, db.GetCallers)
	if err != nil {
		t.Fatalf("failed to generate on demand report: %v", err)
	}
	jsonBytes, _ := json.Marshal(resp)
	report := string(jsonBytes)

	// 4. Assert that the lazy callbacks successfully queried DuckDB and reconstructed the full call chain!
	if !strings.Contains(report, "FunctionA") {
		t.Errorf("expected report to contain FunctionA header")
	}
	if !strings.Contains(report, "FunctionB") {
		t.Errorf("expected report to contain lazy callee FunctionB")
	}
	if !strings.Contains(report, "FunctionC") {
		t.Errorf("expected report to contain lazy transitive callee FunctionC")
	}
}

func Test_HybridSearchIntegration(t *testing.T) {
	// Setup custom DB paths inside a temporary environment to keep it clean
	tmpHome, err := os.MkdirTemp("", "hybrid-integration-test-*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tmpHome)

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", originalHome)

	if err := db.InitDatabase(); err != nil {
		t.Fatalf("failed to init database: %v", err)
	}

	// Create real temporary workspace for integration files
	tmpWorkspace, err := os.MkdirTemp("", "hybrid-workspace-*")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer os.RemoveAll(tmpWorkspace)

	file1Path := filepath.Join(tmpWorkspace, "fibonacci.go")
	fibonacciCode := `package math

// CalculateFibonacciSequence recursively computes the fibonacci value of n.
func CalculateFibonacciSequence(n int) int {
	if n <= 1 {
		return n
	}
	return CalculateFibonacciSequence(n-1) + CalculateFibonacciSequence(n-2)
}`
	_ = os.WriteFile(file1Path, []byte(fibonacciCode), 0644)

	// Initialize real TurboQuant and test index
	tq, err := turboquant.NewTurboQuant(turboquant.DefaultDimension, turboquant.DefaultBitWidth, turboquant.DefaultSeed)
	if err != nil {
		t.Fatalf("failed to init TurboQuant: %v", err)
	}

	tqvPath := filepath.Join(tmpHome, "agent-mem.tqv")
	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		t.Fatalf("failed to init Index: %v", err)
	}

	// Save codebase CWD in database
	if err := db.SaveMerkleTree(tmpWorkspace, "initial_hash", "{}"); err != nil {
		t.Fatalf("failed to save codebase: %v", err)
	}

	// 1. Run live incremental indexing sweep which triggers real LLM embeddings and symbol parsing
	_, _, _, err = merkle.UpdateIndex(tmpWorkspace, index)
	if err != nil {
		t.Fatalf("failed to index workspace: %v", err)
	}

	// 2. Perform live hybrid search
	query := "FibonacciSequence"
	queryEmbed, err := llm.GetEmbedding(query, turboquant.DefaultDimension)
	if err != nil {
		t.Fatalf("failed to fetch embedding: %v. Is LiteLLM running?", err)
	}

	results, err := db.SearchMemories(query, queryEmbed, tmpWorkspace, 5, index)
	if err != nil {
		t.Fatalf("failed to query hybrid search: %v", err)
	}

	if len(results) == 0 {
		t.Fatalf("expected search results to return matches, got 0")
	}

	bestResult := results[0]
	if !strings.Contains(bestResult.Content, "CalculateFibonacciSequence") {
		t.Errorf("expected best match to be the Fibonacci memory chunk, got content: %s", bestResult.Content)
	}
}

func TestZeroStorageFTSMigrationAndColumnStructure(t *testing.T) {
	// Setup isolated DB paths inside a temporary environment to keep it clean
	tmpHome, err := os.MkdirTemp("", "zero-storage-test-*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tmpHome)

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", originalHome)

	if err := db.InitDatabase(); err != nil {
		t.Fatalf("failed to init database: %v", err)
	}

	// 1. Verify schema columns structure in DuckDB directly (prove zero raw code storage columns!)
	conn, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open DuckDB: %v", err)
	}
	defer conn.Close()

	rows, err := conn.Query("PRAGMA table_info('gemini_memories')")
	if err != nil {
		t.Fatalf("failed to query table info for gemini_memories: %v", err)
	}
	defer rows.Close()

	columns := make(map[string]string)
	for rows.Next() {
		var cid int
		var name, dType string
		var notNull, pk int
		var dfltVal *string
		if err := rows.Scan(&cid, &name, &dType, &notNull, &dfltVal, &pk); err == nil {
			columns[name] = dType
		}
	}

	// Verify legacy content column is completely gone
	if _, exists := columns["content"]; exists {
		t.Errorf("Security/Database Violation: legacy 'content' column still exists in DuckDB!")
	}
	// Verify legacy category column is completely gone
	if _, exists := columns["category"]; exists {
		t.Errorf("legacy 'category' column still exists in DuckDB!")
	}

	// Verify new metadata columns exist and have correct types
	expectedCols := map[string]string{
		"id":            "VARCHAR",
		"function_name": "VARCHAR",
		"cwd":           "VARCHAR", // VARCHAR/TEXT maps identically in DuckDB
		"line_start":    "INTEGER",
		"line_end":      "INTEGER",
	}
	for name, expectedType := range expectedCols {
		dType, exists := columns[name]
		if !exists {
			t.Errorf("missing expected metadata column: %s", name)
		} else if !strings.HasPrefix(dType, expectedType) {
			t.Errorf("column %s has type %s, expected type starting with %s", name, dType, expectedType)
		}
	}
}

func TestLockFreeGgrepCollectorSearchIntegrity(t *testing.T) {
	// Setup custom DB paths inside a temporary environment to keep it clean
	tmpHome, err := os.MkdirTemp("", "lock-free-ggrep-test-*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tmpHome)

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", originalHome)

	if err := db.InitDatabase(); err != nil {
		t.Fatalf("failed to init database: %v", err)
	}

	// Create real temporary workspace for integration files
	tmpWorkspace, err := os.MkdirTemp("", "lock-free-workspace-*")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer os.RemoveAll(tmpWorkspace)

	// Write 5 concurrent files with clear distinct functions
	for i := 1; i <= 5; i++ {
		fileCode := fmt.Sprintf("package payment\n\nfunc ProcessPaymentMethod%d() {\n\tprintln(\"method%d\")\n}", i, i)
		_ = os.WriteFile(filepath.Join(tmpWorkspace, fmt.Sprintf("pay%d.go", i)), []byte(fileCode), 0644)
	}

	// Initialize real TurboQuant and test index
	tq, err := turboquant.NewTurboQuant(16, 4, 42)
	if err != nil {
		t.Fatalf("failed to init TurboQuant: %v", err)
	}
	tqvPath := filepath.Join(tmpHome, "agent-mem.tqv")
	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		t.Fatalf("failed to init Index: %v", err)
	}

	// Index workspace
	if err := db.SaveMerkleTree(tmpWorkspace, "initial_hash", "{}"); err != nil {
		t.Fatalf("failed to save codebase: %v", err)
	}
	_, _, _, err = merkle.UpdateIndex(tmpWorkspace, index)
	if err != nil {
		t.Fatalf("failed to index workspace: %v", err)
	}

	// Spin up 10 concurrent reader routines querying SearchMemories simultaneously
	// to verify that our lock-free buffered channel collector pipeline operates
	// with 100% thread safety under heavy concurrent access!
	var wg sync.WaitGroup
	errCh := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			// Each worker searches for a specific payment method using a regex pattern
			query := fmt.Sprintf("ProcessPaymentMethod[1-5]")
			results, err := db.SearchMemories(query, make([]float32, 16), tmpWorkspace, 5, index)
			if err != nil {
				errCh <- err
				return
			}
			// Verify that we found matches and they dynamically loaded the raw code on-the-fly!
			if len(results) == 0 {
				errCh <- fmt.Errorf("worker %d: expected to find matching payment memories, got 0", workerID)
				return
			}
			for _, m := range results {
				if m.Content == "" {
					errCh <- fmt.Errorf("worker %d: expected match content to be read on-the-fly from disk, got empty", workerID)
					return
				}
				if !strings.Contains(m.Content, "ProcessPaymentMethod") {
					errCh <- fmt.Errorf("worker %d: expected match content to contain function name, got: %s", workerID, m.Content)
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	// Check if any goroutine reported an error
	for err := range errCh {
		t.Errorf("Concurrency error: %v", err)
	}
}

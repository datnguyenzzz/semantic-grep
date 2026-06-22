package callgraph

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildCallGraph_Go(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "callgraph-go-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Math utils file containing local call: Add -> ValidateInput
	mathUtils := `package math
func Add(a, b int) int {
	ValidateInput(a)
	ValidateInput(b)
	return a + b
}

func ValidateInput(v int) {
	// mock validation
}
`

	// Main execution file containing cross-file call: MainProcess -> Add
	mainGo := `package main

import "math"

func MainProcess() {
	result := math.Add(5, 10)
	println(result)
}
`

	_ = os.WriteFile(filepath.Join(tmpDir, "math_utils.go"), []byte(mathUtils), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(mainGo), 0644)

	// Build Call Graph
	cg, err := BuildCallGraph(tmpDir)
	if err != nil {
		t.Fatalf("failed to build call graph: %v", err)
	}

	// Verify all Go function nodes are registered
	expectedNodes := []string{"Add", "ValidateInput", "MainProcess"}
	for _, expected := range expectedNodes {
		if _, ok := cg.Nodes[expected]; !ok {
			t.Errorf("expected Go function node %s to be registered", expected)
		}
	}

	// Verify the complete, multi-file nested callee chain report
	resp, err := cg.GenerateTreeReport("MainProcess", "callee", 3)
	if err != nil {
		t.Fatalf("failed to generate tree report: %v", err)
	}
	jsonBytes, _ := json.Marshal(resp)
	report := string(jsonBytes)
	if !strings.Contains(report, "Add") {
		t.Errorf("expected MainProcess callee chain to contain Add: %s", report)
	}
	if !strings.Contains(report, "ValidateInput") {
		t.Errorf("expected MainProcess callee chain to contain ValidateInput: %s", report)
	}

	// Verify reverse caller chain
	respUpward, err := cg.GenerateTreeReport("ValidateInput", "caller", 3)
	if err != nil {
		t.Fatalf("failed to generate upward report: %v", err)
	}
	jsonBytesUpward, _ := json.Marshal(respUpward)
	upwardReport := string(jsonBytesUpward)
	if !strings.Contains(upwardReport, "Add") {
		t.Errorf("expected ValidateInput callers to contain Add: %s", upwardReport)
	}
	if !strings.Contains(upwardReport, "MainProcess") {
		t.Errorf("expected ValidateInput callers to contain MainProcess: %s", upwardReport)
	}
}

func TestBuildCallGraph_Terraform(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "callgraph-tf-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// File 1: VPC module and main VPC resource
	vpcTf := `
module "vpc" {
  source = "./modules/vpc"
}

resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}
`

	// File 2: Subnets & Route tables referencing VPC across files
	subnetsTf := `
resource "aws_subnet" "public_a" {
  vpc_id = aws_vpc.main.id
}

resource "aws_route_table" "route_a" {
  vpc_id   = aws_vpc.main.id
  route_id = module.vpc.route_table_id
}
`

	_ = os.WriteFile(filepath.Join(tmpDir, "vpc.tf"), []byte(vpcTf), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "subnets.tf"), []byte(subnetsTf), 0644)

	// Build Call Graph
	cg, err := BuildCallGraph(tmpDir)
	if err != nil {
		t.Fatalf("failed to build call graph: %v", err)
	}

	// Assert registered nodes
	expectedNodes := []string{
		"module.vpc",
		"resource.aws_vpc.main",
		"resource.aws_subnet.public_a",
		"resource.aws_route_table.route_a",
	}
	for _, expected := range expectedNodes {
		if _, ok := cg.Nodes[expected]; !ok {
			t.Errorf("expected TF node %s to be registered", expected)
		}
	}

	// Verify that the route_table callee chain contains both its cross-file module and resource dependencies
	resp, err := cg.GenerateTreeReport("aws_route_table.route_a", "callee", 2)
	if err != nil {
		t.Fatalf("failed to generate tree report: %v", err)
	}
	jsonBytes, _ := json.Marshal(resp)
	report := string(jsonBytes)
	if !strings.Contains(report, "aws_vpc.main") {
		t.Errorf("expected route_table callee chain to contain aws_vpc.main: %s", report)
	}
	if !strings.Contains(report, "module.vpc") {
		t.Errorf("expected route_table callee chain to contain module.vpc: %s", report)
	}
}

func TestBuildCallGraph_YAML(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "callgraph-yaml-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// File 1: CI workflows
	ciYaml := `
steps:
  - name: Build
    run_task: go build
  - name: Test
    needs: [Build]
`

	// File 2: CD workflow referencing test completion across files
	cdYaml := `
steps:
  - name: Deploy
    needs: [Test]
`

	_ = os.WriteFile(filepath.Join(tmpDir, "ci.yaml"), []byte(ciYaml), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "cd.yaml"), []byte(cdYaml), 0644)

	// Build Call Graph
	cg, err := BuildCallGraph(tmpDir)
	if err != nil {
		t.Fatalf("failed to build call graph: %v", err)
	}

	// Assert registered nodes
	expectedNodes := []string{"step.Build", "step.Test", "step.Deploy"}
	for _, expected := range expectedNodes {
		if _, ok := cg.Nodes[expected]; !ok {
			t.Errorf("expected YAML step %s to be registered", expected)
		}
	}

	// Verify step.Deploy depends on step.Test (cross-file) which depends on step.Build (within-file)
	respDeploy, err := cg.GenerateTreeReport("step.Deploy", "callee", 3)
	if err != nil {
		t.Fatalf("failed to generate tree report: %v", err)
	}
	jsonBytesDeploy, _ := json.Marshal(respDeploy)
	report := string(jsonBytesDeploy)
	if !strings.Contains(report, "step.Test") {
		t.Errorf("expected Deploy to depend on Test: %s", report)
	}
	if !strings.Contains(report, "step.Build") {
		t.Errorf("expected Deploy to transitively depend on Build: %s", report)
	}
}

func TestGenerateOnDemandTreeReport(t *testing.T) {
	nodeA := &Node{Name: "FunctionA", FilePath: "file1.go", StartLine: 1, EndLine: 5}
	nodeB := &Node{Name: "FunctionB", FilePath: "file2.go", StartLine: 10, EndLine: 15}
	nodeC := &Node{Name: "FunctionC", FilePath: "file2.go", StartLine: 20, EndLine: 25}

	// Mock lazy callee retriever
	mockGetCallees := func(caller string) ([]*Node, error) {
		switch caller {
		case "FunctionA":
			return []*Node{nodeB}, nil
		case "FunctionB":
			return []*Node{nodeC}, nil
		}
		return nil, nil
	}

	// Mock lazy caller retriever
	mockGetCallers := func(callee string) ([]*Node, error) {
		switch callee {
		case "FunctionC":
			return []*Node{nodeB}, nil
		case "FunctionB":
			return []*Node{nodeA}, nil
		}
		return nil, nil
	}

	// 1. Verify on-demand report generation
	resp, err := GenerateOnDemandTreeReport(nodeA, "both", 3, mockGetCallees, mockGetCallers)
	if err != nil {
		t.Fatalf("failed to generate on demand report: %v", err)
	}

	if resp.TargetNode.Name != "FunctionA" {
		t.Errorf("expected target FunctionA, got %s", resp.TargetNode.Name)
	}

	// Downward Chain (A -> B -> C)
	if len(resp.Callees) != 1 || resp.Callees[0].Name != "FunctionB" {
		t.Errorf("expected callee FunctionB")
	}
	if len(resp.Callees[0].Children) != 1 || resp.Callees[0].Children[0].Name != "FunctionC" {
		t.Errorf("expected nested callee FunctionC")
	}

	// Upward Chain (C <- B <- A)
	respC, err := GenerateOnDemandTreeReport(nodeC, "caller", 3, mockGetCallees, mockGetCallers)
	if err != nil {
		t.Fatalf("failed to generate on demand caller report: %v", err)
	}

	if len(respC.Callers) != 1 || respC.Callers[0].Name != "FunctionB" {
		t.Errorf("expected caller FunctionB")
	}
	if len(respC.Callers[0].Children) != 1 || respC.Callers[0].Children[0].Name != "FunctionA" {
		t.Errorf("expected nested caller FunctionA")
	}
}

func TestBuildCallGraph_Python(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "callgraph-test-py-*")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pyCode := `
class MyService:
    def execute(self):
        self.log_message()

    def log_message(self):
        print("Done")

def main():
    service = MyService()
    service.execute()
`

	_ = os.WriteFile(filepath.Join(tmpDir, "service.py"), []byte(pyCode), 0644)

	// Build Call Graph
	cg, err := BuildCallGraph(tmpDir)
	if err != nil {
		t.Fatalf("failed to build call graph: %v", err)
	}

	// Assert registered nodes (class and methods)
	expectedNodes := []string{
		"MyService",
		"MyService.execute",
		"MyService.log_message",
		"main",
	}
	for _, expected := range expectedNodes {
		if _, ok := cg.Nodes[expected]; !ok {
			t.Errorf("expected Python node %s to be registered", expected)
		}
	}

	// Assert registered edges
	// execute should call log_message natively mapped to class!
	foundEdge := false
	for _, e := range cg.Edges {
		if e.Caller == "MyService.execute" && e.Callee == "MyService.log_message" {
			foundEdge = true
			break
		}
	}
	if !foundEdge {
		t.Errorf("expected edge MyService.execute -> MyService.log_message, got edges: %v", cg.Edges)
	}
}

// BuildCallGraph builds a recursive AST call/dependency graph for all files inside the specified root (helper for tests)
func BuildCallGraph(root string) (*CallGraph, error) {
	nodes := make(map[string]*Node)
	var edges []Edge

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "dist" {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".go" && ext != ".tf" && ext != ".yaml" && ext != ".yml" && ext != ".py" {
			return nil
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			relPath = path
		}

		fileNodes, fileEdges, err := ParseFile(path, relPath)
		if err == nil {
			for _, n := range fileNodes {
				nodes[n.Name] = n
			}
			edges = append(edges, fileEdges...)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return &CallGraph{
		Nodes: nodes,
		Edges: edges,
	}, nil
}

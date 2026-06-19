package callgraph

import (
	"fmt"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
)

type Node struct {
	Name      string `json:"name"`
	FilePath  string `json:"file_path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

type Edge struct {
	Caller string
	Callee string
}

type CallGraph struct {
	Nodes map[string]*Node
	Edges []Edge
}

func ParseFile(path, relPath string) ([]*Node, []Edge, error) {
	fset := token.NewFileSet()
	nodes := make(map[string]*Node)
	var edges []Edge

	ext := strings.ToLower(filepath.Ext(path))
	var err error
	switch ext {
	case ".go":
		err = parseGoFile(path, relPath, fset, nodes, &edges)
	case ".tf":
		err = parseTerraformFile(path, relPath, nodes, &edges)
	case ".yaml", ".yml":
		err = parseYamlFile(path, relPath, nodes, &edges)
	}

	if err != nil {
		return nil, nil, err
	}

	nodeList := make([]*Node, 0, len(nodes))
	for _, n := range nodes {
		nodeList = append(nodeList, n)
	}

	return nodeList, edges, nil
}

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
		if ext != ".go" && ext != ".tf" && ext != ".yaml" && ext != ".yml" {
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

type CallNode struct {
	Name      string      `json:"name"`
	FilePath  string      `json:"file_path"`
	StartLine int         `json:"start_line"`
	EndLine   int         `json:"end_line"`
	Children  []*CallNode `json:"children,omitempty"`
}

type CallGraphResponse struct {
	TargetNode *Node       `json:"target_node"`
	Callers    []*CallNode `json:"callers,omitempty"`
	Callees    []*CallNode `json:"callees,omitempty"`
}

// GenerateTreeReport creates a structured bi-directional call tree report
func (cg *CallGraph) GenerateTreeReport(targetFunc string, direction string, maxDepth int) (*CallGraphResponse, error) {
	// Find target node (matching exactly, or matching as a suffix, e.g. resource suffix or method suffix)
	var targetNode *Node
	for name, node := range cg.Nodes {
		if name == targetFunc || strings.HasSuffix(name, "."+targetFunc) {
			targetNode = node
			targetFunc = name // normalize
			break
		}
	}

	if targetNode == nil {
		return nil, fmt.Errorf("block/function '%s' not found", targetFunc)
	}

	resp := &CallGraphResponse{
		TargetNode: targetNode,
	}

	if direction == "caller" || direction == "both" {
		resp.Callers = cg.buildCallersJSON(targetFunc, 0, maxDepth, make(map[string]bool))
	}

	if direction == "callee" || direction == "both" {
		resp.Callees = cg.buildCalleesJSON(targetFunc, 0, maxDepth, make(map[string]bool))
	}

	return resp, nil
}

func (cg *CallGraph) buildCallersJSON(funcName string, depth, maxDepth int, visited map[string]bool) []*CallNode {
	if depth >= maxDepth || visited[funcName] {
		return nil
	}
	visited[funcName] = true
	defer func() { visited[funcName] = false }()

	var nodes []*CallNode
	for _, edge := range cg.Edges {
		if edge.Callee == funcName || strings.HasSuffix(edge.Caller, "."+edge.Callee) && edge.Callee == funcName || strings.HasSuffix(funcName, "."+edge.Callee) && edge.Callee != "" {
			callerNode, exists := cg.Nodes[edge.Caller]
			if exists {
				cn := &CallNode{
					Name:      callerNode.Name,
					FilePath:  callerNode.FilePath,
					StartLine: callerNode.StartLine,
					EndLine:   callerNode.EndLine,
				}
				cn.Children = cg.buildCallersJSON(edge.Caller, depth+1, maxDepth, visited)
				nodes = append(nodes, cn)
			}
		}
	}
	return nodes
}

func (cg *CallGraph) buildCalleesJSON(funcName string, depth, maxDepth int, visited map[string]bool) []*CallNode {
	if depth >= maxDepth || visited[funcName] {
		return nil
	}
	visited[funcName] = true
	defer func() { visited[funcName] = false }()

	var nodes []*CallNode
	for _, edge := range cg.Edges {
		if edge.Caller == funcName {
			var calleeNode *Node
			for name, node := range cg.Nodes {
				if name == edge.Callee || strings.HasSuffix(name, "."+edge.Callee) {
					calleeNode = node
					break
				}
			}

			if calleeNode != nil {
				cn := &CallNode{
					Name:      calleeNode.Name,
					FilePath:  calleeNode.FilePath,
					StartLine: calleeNode.StartLine,
					EndLine:   calleeNode.EndLine,
				}
				cn.Children = cg.buildCalleesJSON(calleeNode.Name, depth+1, maxDepth, visited)
				nodes = append(nodes, cn)
			}
		}
	}
	return nodes
}

// GenerateOnDemandTreeReport creates a structured call tree report by fetching callers and callees lazily on the fly via callbacks
func GenerateOnDemandTreeReport(
	targetNode *Node,
	direction string,
	maxDepth int,
	getCallees func(caller string) ([]*Node, error),
	getCallers func(callee string) ([]*Node, error),
) (*CallGraphResponse, error) {
	resp := &CallGraphResponse{
		TargetNode: targetNode,
	}

	if direction == "caller" || direction == "both" {
		resp.Callers = buildOnDemandCallersJSON(targetNode.Name, 0, maxDepth, make(map[string]bool), getCallers)
	}

	if direction == "callee" || direction == "both" {
		resp.Callees = buildOnDemandCalleesJSON(targetNode.Name, 0, maxDepth, make(map[string]bool), getCallees)
	}

	return resp, nil
}

func buildOnDemandCallersJSON(
	funcName string,
	depth, maxDepth int,
	visited map[string]bool,
	getCallers func(callee string) ([]*Node, error),
) []*CallNode {
	if depth >= maxDepth || visited[funcName] {
		return nil
	}
	visited[funcName] = true
	defer func() { visited[funcName] = false }()

	callers, err := getCallers(funcName)
	if err != nil || len(callers) == 0 {
		return nil
	}

	var nodes []*CallNode
	for _, callerNode := range callers {
		cn := &CallNode{
			Name:      callerNode.Name,
			FilePath:  callerNode.FilePath,
			StartLine: callerNode.StartLine,
			EndLine:   callerNode.EndLine,
		}
		cn.Children = buildOnDemandCallersJSON(callerNode.Name, depth+1, maxDepth, visited, getCallers)
		nodes = append(nodes, cn)
	}
	return nodes
}

func buildOnDemandCalleesJSON(
	funcName string,
	depth, maxDepth int,
	visited map[string]bool,
	getCallees func(caller string) ([]*Node, error),
) []*CallNode {
	if depth >= maxDepth || visited[funcName] {
		return nil
	}
	visited[funcName] = true
	defer func() { visited[funcName] = false }()

	callees, err := getCallees(funcName)
	if err != nil || len(callees) == 0 {
		return nil
	}

	var nodes []*CallNode
	for _, calleeNode := range callees {
		cn := &CallNode{
			Name:      calleeNode.Name,
			FilePath:  calleeNode.FilePath,
			StartLine: calleeNode.StartLine,
			EndLine:   calleeNode.EndLine,
		}
		cn.Children = buildOnDemandCalleesJSON(calleeNode.Name, depth+1, maxDepth, visited, getCallees)
		nodes = append(nodes, cn)
	}
	return nodes
}

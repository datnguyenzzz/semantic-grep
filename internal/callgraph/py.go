package callgraph

import (
	"os"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
)

// parsePythonFile parses a Python file using Tree-sitter to build nodes and call edges.
func parsePythonFile(path string, nodes map[string]*Node, edges *[]Edge) error {
	contentBytes, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// 1. Initialize Tree-sitter parser
	parser := tree_sitter.NewParser()
	defer parser.Close()

	lang := tree_sitter.NewLanguage(tree_sitter_python.Language())
	parser.SetLanguage(lang)

	// 2. Parse content into an AST
	tree := parser.Parse(contentBytes, nil)
	defer tree.Close()

	root := tree.RootNode()

	// 3. Recursive traverser to register definition nodes and extract call edges
	var traverse func(node *tree_sitter.Node, currentClass string, currentFunc string)
	traverse = func(node *tree_sitter.Node, currentClass string, currentFunc string) {
		if node == nil {
			return
		}

		kind := node.Kind()

		if kind == "class_definition" {
			nameNode := node.ChildByFieldName("name")
			if nameNode != nil {
				className := string(contentBytes[nameNode.StartByte():nameNode.EndByte()])
				startLine := int(node.StartPosition().Row) + 1
				endLine := int(node.EndPosition().Row) + 1

				nodes[className] = &Node{
					Name:      className,
					FilePath:  path,
					StartLine: startLine,
					EndLine:   endLine,
				}

				// Traverse class children with class context
				for i := uint(0); i < node.ChildCount(); i++ {
					traverse(node.Child(i), className, currentFunc)
				}
				return
			}
		}

		if kind == "function_definition" {
			nameNode := node.ChildByFieldName("name")
			if nameNode != nil {
				funcName := string(contentBytes[nameNode.StartByte():nameNode.EndByte()])
				startLine := int(node.StartPosition().Row) + 1
				endLine := int(node.EndPosition().Row) + 1

				nodeName := funcName
				if currentClass != "" {
					nodeName = currentClass + "." + funcName
				}

				nodes[nodeName] = &Node{
					Name:      nodeName,
					FilePath:  path,
					StartLine: startLine,
					EndLine:   endLine,
				}

				// Traverse function body with function context
				for i := uint(0); i < node.ChildCount(); i++ {
					traverse(node.Child(i), currentClass, nodeName)
				}
				return
			}
		}

		if kind == "call" && currentFunc != "" {
			funcNode := node.ChildByFieldName("function")
			if funcNode != nil {
				callee := ""

				if funcNode.Kind() == "identifier" {
					callee = string(contentBytes[funcNode.StartByte():funcNode.EndByte()])
				} else if funcNode.Kind() == "attribute" {
					// Handle self.method() calls inside classes
					objNode := funcNode.ChildByFieldName("object")
					attrNode := funcNode.ChildByFieldName("attribute")
					if objNode != nil && attrNode != nil {
						objName := string(contentBytes[objNode.StartByte():objNode.EndByte()])
						attrName := string(contentBytes[attrNode.StartByte():attrNode.EndByte()])
						if objName == "self" && currentClass != "" {
							callee = currentClass + "." + attrName
						} else {
							callee = attrName
						}
					}
				}

				if callee != "" {
					// Exclude standard control flows or keywords
					if callee != "print" && callee != "len" && callee != "range" {
						*edges = append(*edges, Edge{
							Caller: currentFunc,
							Callee: callee,
						})
					}
				}
			}
		}

		// Standard child traversal
		for i := uint(0); i < node.ChildCount(); i++ {
			traverse(node.Child(i), currentClass, currentFunc)
		}
	}

	traverse(root, "", "")
	return nil
}

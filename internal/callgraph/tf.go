package callgraph

import (
	"fmt"
	"os"
	"strings"
)

func parseTerraformFile(path string, nodes map[string]*Node, edges *[]Edge) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	lines := strings.Split(string(content), "\n")
	var activeNode *Node
	var braceCount int

	for i, line := range lines {
		lineNum := i + 1
		trimmed := strings.TrimSpace(line)

		// Detect block declarations, e.g. "resource \"aws_instance\" \"web\" {" or "module \"vpc\" {"
		if strings.HasPrefix(trimmed, "resource ") || strings.HasPrefix(trimmed, "module ") || strings.HasPrefix(trimmed, "variable ") || strings.HasPrefix(trimmed, "output ") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				blockType := parts[0]
				blockName := strings.Trim(parts[1], `"{`)
				if blockType == "resource" && len(parts) >= 3 {
					resourceType := strings.Trim(parts[1], `"`)
					resourceName := strings.Trim(parts[2], `"{`)
					blockName = fmt.Sprintf("%s.%s", resourceType, resourceName)
				}

				fullName := fmt.Sprintf("%s.%s", blockType, blockName)
				activeNode = &Node{
					Name:      fullName,
					FilePath:  path,
					StartLine: lineNum,
				}
				nodes[fullName] = activeNode
				braceCount = strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
			}
			continue
		}

		// If inside an active block, scan for references/dependencies
		if activeNode != nil {
			braceCount += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
			if braceCount <= 0 {
				activeNode.EndLine = lineNum
				activeNode = nil
				continue
			}

			// Find references, e.g. "aws_security_group.web.id" or "module.vpc.id"
			words := strings.Fields(line)
			for _, word := range words {
				word = strings.Trim(word, `",()[]{}=`)
				if strings.HasPrefix(word, "module.") {
					parts := strings.Split(word, ".")
					if len(parts) >= 2 {
						*edges = append(*edges, Edge{
							Caller: activeNode.Name,
							Callee: "module." + parts[1],
						})
					}
				} else {
					parts := strings.Split(word, ".")
					if len(parts) >= 2 {
						if strings.Contains(parts[0], "_") {
							*edges = append(*edges, Edge{
								Caller: activeNode.Name,
								Callee: "resource." + parts[0] + "." + parts[1],
							})
						}
					}
				}
			}
		}
	}

	if activeNode != nil {
		activeNode.EndLine = len(lines)
	}

	return nil
}

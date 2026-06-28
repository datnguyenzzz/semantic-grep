package callgraph

import (
	"fmt"
	"os"
	"strings"
)

func parseYamlFile(path string, nodes map[string]*Node, edges *[]Edge) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	lines := strings.Split(string(content), "\n")
	var activeNode *Node

	for i, line := range lines {
		lineNum := i + 1
		trimmed := strings.TrimSpace(line)

		// Detect name declarations, e.g. "- name: Build App" or "id: build_step"
		if strings.HasPrefix(trimmed, "- name:") || strings.HasPrefix(trimmed, "id:") || strings.HasPrefix(trimmed, "name:") {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				val := strings.Trim(parts[1], ` '"`)
				if val != "" {
					if activeNode != nil {
						activeNode.EndLine = lineNum - 1
					}

					fullName := fmt.Sprintf("step.%s", val)
					activeNode = &Node{
						Name:      fullName,
						FilePath:  path,
						StartLine: lineNum,
					}
					nodes[fullName] = activeNode
				}
			}
			continue
		}

		// Scan inside active task/step for dependencies/calls
		if activeNode != nil {
			if strings.Contains(line, "needs:") || strings.Contains(line, "uses:") || strings.Contains(line, "run_task:") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					refVal := strings.Trim(parts[1], ` '"[](),`)
					if refVal != "" {
						*edges = append(*edges, Edge{
							Caller: activeNode.Name,
							Callee: "step." + refVal,
						})
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

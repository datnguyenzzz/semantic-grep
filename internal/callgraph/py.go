package callgraph

import (
	"bufio"
	"os"
	"regexp"
	"strings"
)

var (
	pyFuncRegex  = regexp.MustCompile(`^\s*def\s+([a-zA-Z0-9_]+)\s*\(`)
	pyClassRegex = regexp.MustCompile(`^\s*class\s+([a-zA-Z0-9_]+)`)
	pyCallRegex  = regexp.MustCompile(`([a-zA-Z0-9_]+)\s*\(`)
)

func parsePythonFile(path, relPath string, nodes map[string]*Node, edges *[]Edge) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	type pyBlock struct {
		name      string
		startLine int
		endLine   int
	}

	var blocks []pyBlock
	currentClass := ""
	currentBlock := ""
	var currentBlockStart int

	// First pass: locate all function and class definitions (nodes)
	for idx, line := range lines {
		lineNum := idx + 1

		// 1. Check for class definition
		if match := pyClassRegex.FindStringSubmatch(line); len(match) > 1 {
			currentClass = match[1]
			// Register class as a node
			nodeName := currentClass
			nodes[nodeName] = &Node{
				Name:      nodeName,
				FilePath:  relPath,
				StartLine: lineNum,
				EndLine:   lineNum, // Will update at end of file
			}
			continue
		}

		// 2. Check for function definition
		if match := pyFuncRegex.FindStringSubmatch(line); len(match) > 1 {
			funcName := match[1]
			nodeName := funcName
			if currentClass != "" {
				// Check indentation to see if function is actually inside the class
				indentation := len(line) - len(strings.TrimLeft(line, " \t"))
				if indentation > 0 {
					nodeName = currentClass + "." + funcName
				} else {
					currentClass = "" // Reset class context if top-level def
				}
			}

			// Finalize previous block if exists
			if currentBlock != "" {
				blocks = append(blocks, pyBlock{
					name:      currentBlock,
					startLine: currentBlockStart,
					endLine:   lineNum - 1,
				})
			}

			currentBlock = nodeName
			currentBlockStart = lineNum

			nodes[nodeName] = &Node{
				Name:      nodeName,
				FilePath:  relPath,
				StartLine: lineNum,
				EndLine:   lineNum, // Will update when block ends
			}
		}
	}

	// Finalize last block
	if currentBlock != "" {
		blocks = append(blocks, pyBlock{
			name:      currentBlock,
			startLine: currentBlockStart,
			endLine:   len(lines),
		})
	}

	// Update EndLine for nodes
	for _, b := range blocks {
		if n, ok := nodes[b.name]; ok {
			n.EndLine = b.endLine
		}
	}

	// Second pass: scan function bodies to extract call edges
	for _, b := range blocks {
		for lineNum := b.startLine + 1; lineNum <= b.endLine; lineNum++ {
			if lineNum > len(lines) {
				break
			}
			line := lines[lineNum-1]
			trimmed := strings.TrimSpace(line)

			// Skip comments
			if strings.HasPrefix(trimmed, "#") {
				continue
			}

			// Find matches for function calls like foo() or self.bar()
			calls := pyCallRegex.FindAllStringSubmatch(line, -1)
			for _, match := range calls {
				if len(match) > 1 {
					callee := match[1]
					// Exclude standard Python control flow/builtins
					if callee == "def" || callee == "class" || callee == "if" || callee == "elif" || callee == "while" || callee == "for" || callee == "print" {
						continue
					}
					// If calling a method in same class (e.g. self.foo()), map it to the full class name!
					if strings.Contains(line, "self."+callee) && strings.Contains(b.name, ".") {
						classParts := strings.Split(b.name, ".")
						callee = classParts[0] + "." + callee
					}
					*edges = append(*edges, Edge{
						Caller: b.name,
						Callee: callee,
					})
				}
			}
		}
	}

	return nil
}

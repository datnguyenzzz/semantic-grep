package splitter

import (
	"os"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
)

// parsePythonFile parses a Python file using Tree-sitter and splits it into logical AST-guided chunks.
func parsePythonFile(filePath string) ([]Chunk, error) {
	contentBytes, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
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
	var chunks []Chunk

	// Read lines to help reconstruct arbitrary non-class/non-function blocks safely
	lines := strings.Split(string(contentBytes), "\n")

	var currentGroup []string
	groupStart := 1

	for i := uint(0); i < root.ChildCount(); i++ {
		child := root.Child(i)
		kind := child.Kind()

		if kind == "class_definition" || kind == "function_definition" {
			startLine := int(child.StartPosition().Row) + 1
			endLine := int(child.EndPosition().Row) + 1

			// If we have accumulated preceding global lines, save them as a chunk first
			if len(currentGroup) > 0 {
				chunks = append(chunks, Chunk{
					FilePath:   filePath,
					Content:    strings.Join(currentGroup, "\n"),
					StartLine:  groupStart,
					EndLine:    startLine - 1,
					SymbolName: "",
				})
				currentGroup = nil
			}

			startByte := child.StartByte()
			endByte := child.EndByte()
			content := string(contentBytes[startByte:endByte])

			// Extract the class or function name using Tree-sitter ChildByFieldName
			var funcName string
			nameNode := child.ChildByFieldName("name")
			if nameNode != nil {
				funcName = string(contentBytes[nameNode.StartByte():nameNode.EndByte()])
			}

			chunks = append(chunks, Chunk{
				FilePath:   filePath,
				Content:    content,
				StartLine:  startLine,
				EndLine:    endLine,
				SymbolName: funcName,
			})
			groupStart = endLine + 1
		} else {
			// Accumulate lines for miscellaneous top-level statements (imports, variables, etc.)
			startRow := child.StartPosition().Row
			endRow := child.EndPosition().Row
			for r := startRow; r <= endRow; r++ {
				if r < uint(len(lines)) {
					// We only add the line if we haven't already captured it in currentGroup
					lineStr := lines[r]
					if len(currentGroup) == 0 || currentGroup[len(currentGroup)-1] != lineStr {
						currentGroup = append(currentGroup, lineStr)
					}
				}
			}
		}
	}

	// Save any remaining trailing global lines
	if len(currentGroup) > 0 {
		chunks = append(chunks, Chunk{
			FilePath:   filePath,
			Content:    strings.Join(currentGroup, "\n"),
			StartLine:  groupStart,
			EndLine:    len(lines),
			SymbolName: "",
		})
	}

	// Return raw chunks (splitter.go handles refining at the top level!)
	return chunks, nil
}

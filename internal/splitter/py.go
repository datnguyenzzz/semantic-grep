package splitter

import (
	"bufio"
	"os"
	"strings"
)

// parsePythonFile parses a Python file and splits it into logical AST-guided chunks (classes and functions).
func parsePythonFile(filePath string) ([]Chunk, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var chunks []Chunk
	var currentChunk []string
	startLine := 1
	currentLine := 1

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Identify a new boundary (class or top-level function definition)
		// Top-level definitions usually have no leading whitespace before 'class ' or 'def '
		isNewBoundary := (strings.HasPrefix(line, "class ") || strings.HasPrefix(line, "def ")) && trimmed != ""

		if isNewBoundary && len(currentChunk) > 0 {
			// Save previous chunk
			chunks = append(chunks, Chunk{
				FilePath:  filePath,
				Content:   strings.Join(currentChunk, "\n"),
				StartLine: startLine,
				EndLine:   currentLine - 1,
			})
			currentChunk = nil
			startLine = currentLine
		}

		currentChunk = append(currentChunk, line)
		currentLine++
	}

	if len(currentChunk) > 0 {
		chunks = append(chunks, Chunk{
			FilePath:  filePath,
			Content:   strings.Join(currentChunk, "\n"),
			StartLine: startLine,
			EndLine:   currentLine - 1,
		})
	}

	// Return raw chunks (splitter.go handles refining at the top level!)
	return chunks, nil
}

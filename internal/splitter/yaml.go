package splitter

// parse YAML documents semantically by splitting along multi-document boundary separators (---), keeping documents intact for maximum context cohesion

import (
	"os"
	"strings"
)

func parseYamlFile(filePath string) ([]Chunk, error) {
	contentBytes, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	content := string(contentBytes)
	var docs []string
	var sep string

	if strings.Contains(content, "\r\n---\r\n") {
		docs = strings.Split(content, "\r\n---\r\n")
		sep = "\r\n---\r\n"
	} else {
		docs = strings.Split(content, "\n---\n")
		sep = "\n---\n"
	}

	var chunks []Chunk
	currentLine := 1

	for i, doc := range docs {
		trimmedDoc := strings.TrimSpace(doc)
		linesCount := strings.Count(doc, "\n") + 1

		if trimmedDoc != "" {
			chunks = append(chunks, Chunk{
				FilePath:  filePath,
				Content:   trimmedDoc,
				StartLine: currentLine,
				EndLine:   currentLine + linesCount - 1,
			})
		}

		currentLine += linesCount
		if i < len(docs)-1 {
			currentLine += strings.Count(sep, "\n")
		}
	}

	// Fallback if no chunks found
	if len(chunks) == 0 {
		lines := strings.Split(content, "\n")
		chunks = append(chunks, Chunk{
			FilePath:  filePath,
			Content:   content,
			StartLine: 1,
			EndLine:   len(lines),
		})
	}

	return chunks, nil
}

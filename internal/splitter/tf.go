package splitter

// parse Terraform lexical blocks using standard strings manipulation, completely self-contained

import (
	"os"
	"strings"
)

func parseTerraformFile(filePath string) ([]Chunk, error) {
	contentBytes, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(contentBytes), "\n")
	var chunks []Chunk

	var currentBlock []string
	blockStartLine := 0
	depth := 0
	inBlock := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if !inBlock {
			isBlockHeader := false
			headers := []string{"resource", "data", "variable", "output", "provider", "module", "locals", "terraform"}
			for _, h := range headers {
				if strings.HasPrefix(trimmed, h) {
					isBlockHeader = true
					break
				}
			}

			if isBlockHeader && strings.Contains(trimmed, "{") {
				inBlock = true
				blockStartLine = i + 1
				depth = 0
				currentBlock = nil
			}
		}

		if inBlock {
			currentBlock = append(currentBlock, line)
			depth += strings.Count(line, "{")
			depth -= strings.Count(line, "}")

			if depth <= 0 {
				chunks = append(chunks, Chunk{
					FilePath:  filePath,
					Content:   strings.Join(currentBlock, "\n"),
					StartLine: blockStartLine,
					EndLine:   i + 1,
				})
				inBlock = false
			}
		}
	}

	// Fallback if no blocks found
	if len(chunks) == 0 {
		chunks = append(chunks, Chunk{
			FilePath:  filePath,
			Content:   string(contentBytes),
			StartLine: 1,
			EndLine:   len(lines),
		})
	}

	return chunks, nil
}

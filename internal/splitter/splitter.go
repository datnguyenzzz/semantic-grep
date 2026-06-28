package splitter

// AST-based splitter manager for Go and Terraform, orchestrating the file-specific parsers and chunk refining

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Chunk struct {
	FilePath   string `json:"file_path"`
	Content    string `json:"content"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	SymbolName string `json:"symbol_name"`
}

// Default chunking sizes with environment overrides
var (
	MaxChunkSize = intEnv("SPLITTER_MAX_CHUNK_SIZE", 20000)
	ChunkOverlap = intEnv("SPLITTER_CHUNK_OVERLAP", 200)
)

func intEnv(key string, fallback int) int {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	var i int
	if _, err := fmt.Sscanf(val, "%d", &i); err != nil {
		return fallback
	}
	return i
}

func SplitFile(filePath string) ([]Chunk, error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	var rawChunks []Chunk
	var err error

	switch ext {
	case ".go":
		rawChunks, err = parseGoFile(filePath)
	case ".tf":
		rawChunks, err = parseTerraformFile(filePath)
	case ".yaml", ".yml":
		rawChunks, err = parseYamlFile(filePath)
	case ".py":
		rawChunks, err = parsePythonFile(filePath)
	default:
		// Non-supported fallback (treat whole file as single chunk)
		contentBytes, err := os.ReadFile(filePath)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(contentBytes), "\n")
		rawChunks = []Chunk{{
			FilePath:  filePath,
			Content:   string(contentBytes),
			StartLine: 1,
			EndLine:   len(lines),
		}}
	}

	if err != nil {
		return nil, err
	}

	// Refine large chunks
	var finalChunks []Chunk
	for _, rc := range rawChunks {
		if len(rc.Content) <= MaxChunkSize {
			finalChunks = append(finalChunks, rc)
		} else {
			subChunks := splitLargeChunk(rc.Content, rc.StartLine, MaxChunkSize)
			for _, sc := range subChunks {
				finalChunks = append(finalChunks, Chunk{
					FilePath:  filePath,
					Content:   sc.Content,
					StartLine: sc.StartLine,
					EndLine:   sc.EndLine,
				})
			}
		}
	}

	// TODO: Do we need to adding overlap between 2 consecutive chunk

	return finalChunks, nil
}

func splitLargeChunk(content string, startLine int, maxSize int) []Chunk {
	lines := strings.Split(content, "\n")
	var subChunks []Chunk

	var currentChunk strings.Builder
	currentStartLine := startLine
	currentLineCount := 0

	for i, line := range lines {
		lineWithNewline := line
		if i < len(lines)-1 {
			lineWithNewline = line + "\n"
		}

		if currentChunk.Len()+len(lineWithNewline) > maxSize && currentChunk.Len() > 0 {
			subChunks = append(subChunks, Chunk{
				Content:   strings.TrimSpace(currentChunk.String()),
				StartLine: currentStartLine,
				EndLine:   currentStartLine + currentLineCount - 1,
			})

			currentChunk.Reset()
			currentStartLine = startLine + i
			currentLineCount = 0
		}

		currentChunk.WriteString(lineWithNewline)
		currentLineCount++
	}

	if currentChunk.Len() > 0 && strings.TrimSpace(currentChunk.String()) != "" {
		subChunks = append(subChunks, Chunk{
			Content:   strings.TrimSpace(currentChunk.String()),
			StartLine: currentStartLine,
			EndLine:   currentStartLine + currentLineCount - 1,
		})
	}

	return subChunks
}

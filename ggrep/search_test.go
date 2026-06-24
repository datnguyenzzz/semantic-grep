package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	regexp "github.com/google/codesearch/regexp"
)

func Benchmark_GgrepLiteral(b *testing.B) {
	// Standard workspace directory to simulate a real-world scale load
	workspacePath := "/Users/thanh.nguyen/Documents/dhse/opentelemetry-collector-contrib"
	pattern := "createMetricsProcessor"

	opt := &SearchOption{
		Kind:    Literal,
		Literal: []byte(pattern),
	}

	b.ReportAllocs()

	for b.Loop() {
		// Run a full recursive search over the entire workspace directory
		_ = Search([]string{workspacePath}, opt)
	}
}

func Test_GgrepRealFiles(t *testing.T) {
	// 1. Setup a temporary directory
	tmpDir, err := os.MkdirTemp("", "ggrep-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir) // Clean up automatically!

	// 2. Create a mock workspace
	files := map[string]string{
		"main.go": `package main
import "fmt"
func main() {
	fmt.Println("Hello, World!")
	// TODO: implement logic
}
`,
		"utils.go": `package main
// This is a test file
func isTest() bool {
	return true
}
`,
		".gitignore": `node_modules/
dist/
*.log
`,
		"node_modules/library/index.js": `function log() { console.log("implement me"); }`,
		"dist/bundle.js":                `console.log("implement");`,
		"ignored.log":                   `[ERROR] Failed to implement something.`,
	}

	for relPath, content := range files {
		fullPath := filepath.Join(tmpDir, relPath)
		_ = os.MkdirAll(filepath.Dir(fullPath), 0755)
		err := os.WriteFile(fullPath, []byte(content), 0644)
		if err != nil {
			t.Fatalf("failed to write %s: %v", relPath, err)
		}
	}

	// =========================================================================
	// TEST A: Literal Search
	// We search for "implement". It appears in main.go (line 5).
	// It also appears in node_modules, dist, and ignored.log, but those MUST BE IGNORED!
	// =========================================================================
	t.Run("Literal Search with GitIgnore", func(t *testing.T) {
		opt := &SearchOption{
			Kind:    Literal,
			Literal: []byte("implement"),
		}

		results := Search([]string{tmpDir}, opt)

		if len(results) != 1 {
			t.Fatalf("expected exactly 1 literal match, got %d: %v", len(results), results)
		}

		res := results[0]
		if !strings.HasSuffix(res.Path, "main.go") {
			t.Errorf("expected match in main.go, got %s", res.Path)
		}
		if res.Line != 5 {
			t.Errorf("expected match on line 5, got line %d", res.Line)
		}
		if !strings.Contains(res.Text, "TODO: implement logic") {
			t.Errorf("unexpected matched text: %s", res.Text)
		}
	})

	// =========================================================================
	// TEST B: Regex Search
	// We search for "func [a-zA-Z]+\(". It should match "func main(" in main.go (line 3)
	// and "func isTest(" in utils.go (line 3).
	// =========================================================================
	t.Run("Regex Search", func(t *testing.T) {
		importRegexp, err := regexp.Compile(`func [a-zA-Z]+\(`)
		if err != nil {
			t.Fatalf("failed to compile regex: %v", err)
		}
		opt := &SearchOption{
			Kind:    Regex,
			Regex:   importRegexp,
			Pattern: `func [a-zA-Z]+\(`,
		}

		results := Search([]string{tmpDir}, opt)

		if len(results) != 2 {
			t.Fatalf("expected exactly 2 regex matches, got %d: %v", len(results), results)
		}

		matchedMain := false
		matchedUtils := false

		for _, res := range results {
			if strings.HasSuffix(res.Path, "main.go") {
				matchedMain = true
				if res.Line != 3 {
					t.Errorf("expected main.go match on line 3, got line %d", res.Line)
				}
			} else if strings.HasSuffix(res.Path, "utils.go") {
				matchedUtils = true
				if res.Line != 3 {
					t.Errorf("expected utils.go match on line 3, got line %d", res.Line)
				}
			}
		}

		if !matchedMain || !matchedUtils {
			t.Errorf("missing expected regex matches. main.go: %v, utils.go: %v", matchedMain, matchedUtils)
		}
	})
}

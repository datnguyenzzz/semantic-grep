package ggrep

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type SearchResponse struct {
	Path, Text string
	Line       int
}

func Benchmark_GgrepLiteral(b *testing.B) {
	workspacePath := "/Users/thanh.nguyen/Documents/dhse/opentelemetry/opentelemetry-collector-contrib"
	pattern := "createMetricsProcessor"

	opt := &SearchOption{
		Kind:    Literal,
		Literal: []byte(pattern),
	}

	b.ReportAllocs()

	for b.Loop() {
		_ = Search([]string{workspacePath}, opt, func(path string, line int, text []byte) {})
	}
}

func Benchmark_GgrepRegex(b *testing.B) {
	workspacePath := "/Users/thanh.nguyen/Documents/dhse/opentelemetry/opentelemetry-collector-contrib"
	pattern := "(http|grpc)://[a-zA-Z0-9.-]+"

	opt := &SearchOption{
		Kind:    Regex,
		Pattern: pattern,
	}

	b.ReportAllocs()

	for b.Loop() {
		// Run a full recursive search over the entire workspace directory (with empty dummy closure)
		_ = Search([]string{workspacePath}, opt, func(path string, line int, text []byte) {})
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

		var results []SearchResponse
		count := Search([]string{tmpDir}, opt, func(path string, line int, text []byte) {
			results = append(results, SearchResponse{
				Path: path,
				Line: line,
				Text: string(text),
			})
		})

		if count != 1 || len(results) != 1 {
			t.Fatalf("expected exactly 1 literal match, got %d", count)
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
		importRegexp, err := CompileRegex("(?m)" + `func [a-zA-Z]+\(`)
		if err != nil {
			t.Fatalf("failed to compile regex: %v", err)
		}
		opt := &SearchOption{
			Kind:    Regex,
			Regex:   importRegexp,
			Pattern: `func [a-zA-Z]+\(`,
		}

		var results []SearchResponse
		count := Search([]string{tmpDir}, opt, func(path string, line int, text []byte) {
			results = append(results, SearchResponse{
				Path: path,
				Line: line,
				Text: string(text),
			})
		})

		if count != 2 || len(results) != 2 {
			t.Fatalf("expected exactly 2 regex matches, got %d", count)
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

func Test_GgrepVsOSGrep(t *testing.T) {
	targetFile := "search_test.go"

	// 1. LITERAL COMPARISON
	t.Run("Literal Matching Parity", func(t *testing.T) {
		pattern := "Benchmark_GgrepLiteral"

		// Run ggrep (with empty dummy onMatch closure)
		opt := &SearchOption{
			Kind:    Literal,
			Literal: []byte(pattern),
		}
		ggrepCount := Search([]string{targetFile}, opt, func(path string, line int, text []byte) {})

		// Run OS grep
		cmd := exec.Command("grep", "-n", "-I", pattern, targetFile)
		var out bytes.Buffer
		cmd.Stdout = &out
		_ = cmd.Run() // grep exits with 1 if 0 matches, so we ignore exit code

		// Parse OS grep lines
		var grepCount int
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.TrimSpace(line) != "" {
				grepCount++
			}
		}

		if ggrepCount != grepCount {
			t.Fatalf("Literal matching deviation too high! ggrep found %d, OS grep found %d", ggrepCount, grepCount)
		}
		t.Logf("✓ Literal parity verified! Both found exactly %d matches.", grepCount)
	})

	// 2. REGEX COMPARISON (Multi-pattern Conformance Suite)
	t.Run("Regex Matching Parity", func(t *testing.T) {
		patterns := []string{
			"func Test_[a-zA-Z0-9_]+\\(",            // 1. Standard word/char classes and grouping
			"Benchmark_(Ggrep|Grrep)[a-zA-Z]*",      // 2. Group alternation and repetition
			"^[ \t]*targetFile[ \t]*:=",             // 3. Line anchors and tab character classes
			"✓ [a-zA-Z]+ parity",                    // 4. UTF-8 unicode matching with ✓ symbol
			"grepCount\\+\\+",                       // 5. Backslash escapes of operator metacharacters
			"matched(Main|Utils)",                   // 6. Parenthesis alternation groupings
			"expected exactly [0-9]+ regex matches", // 7. Numeric character classes
			"\"[a-zA-Z0-9_ ]+parity verified!\"",    // 8. Quoted string literals
			"opt[ \t]*:=[ \t]*&SearchOption",        // 9. Structure pointers and address escape matching
			"grep -E -n -I",                         // 10. Standard CLI execution flag strings
		}

		for idx, pattern := range patterns {
			t.Run(fmt.Sprintf("Pattern_%d", idx+1), func(t *testing.T) {
				// Run ggrep (with multiline (?m) prepended and empty dummy closure)
				compiledRegex, err := CompileRegex("(?m)" + pattern)
				if err != nil {
					t.Fatalf("failed to compile regex %q: %v", pattern, err)
				}
				opt := &SearchOption{
					Kind:    Regex,
					Regex:   compiledRegex,
					Pattern: pattern,
				}
				ggrepCount := Search([]string{targetFile}, opt, func(path string, line int, text []byte) {})

				// Run OS grep (POSIX ERE mode)
				cmd := exec.Command("grep", "-E", "-n", "-I", pattern, targetFile)
				var out bytes.Buffer
				cmd.Stdout = &out
				_ = cmd.Run()

				// Parse OS grep lines
				var grepCount int
				for _, line := range strings.Split(out.String(), "\n") {
					if strings.TrimSpace(line) != "" {
						grepCount++
					}
				}

				if ggrepCount != grepCount {
					t.Fatalf("Parity deviation too high for pattern %q! ggrep found %d, OS grep found %d", pattern, ggrepCount, grepCount)
				}
				t.Logf("✓ Parity verified for %q! Both found exactly %d matches.", pattern, grepCount)
			})
		}
	})
}

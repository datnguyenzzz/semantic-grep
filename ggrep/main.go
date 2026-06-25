package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
)

// writeMatch formats and writes a match directly and atomically to os.Stdout
// using a stack-allocated buffer (no heap allocations or line interleaving!).
func writeMatch(path string, lineNum int, text []byte) {
	var buf [512]byte
	needed := len(path) + 1 + 20 + 1 + len(text) + 1
	var b []byte
	if needed <= len(buf) {
		b = buf[:0]
	} else {
		b = make([]byte, 0, needed) // fallback for long lines
	}

	b = append(b, path...)
	b = append(b, ':')
	b = strconv.AppendInt(b, int64(lineNum), 10)
	b = append(b, ':')
	b = append(b, text...)
	b = append(b, '\n')

	_, _ = os.Stdout.Write(b)
}

func main() {
	// Define CLI flags
	useRegex := flag.Bool("r", false, "Use regular expressions instead of literal string matching")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: ggrep [-r] <pattern> <path1> [path2 ...]\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	args := flag.Args()
	if len(args) < 2 {
		flag.Usage()
		os.Exit(1)
	}

	pattern := args[0]
	paths := args[1:]

	// Initialize search options
	opt := &SearchOption{
		Pattern: pattern,
	}

	if *useRegex {
		opt.Kind = Regex
		// the regex will be compiled later
	} else {
		opt.Kind = Literal
		opt.Literal = []byte(pattern)
	}

	// Execute high-speed parallel search (writes directly and atomically to os.Stdout)
	Search(paths, opt, writeMatch)
}

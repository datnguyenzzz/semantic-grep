package main

import (
	"flag"
	"fmt"
	"os"
)

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

	// Execute high-speed parallel search
	results := Search(paths, opt)

	// Print results elegantly (matching standard grep format)
	for _, res := range results {
		fmt.Printf("%s:%d:%s\n", res.Path, res.Line, res.Text)
	}
}

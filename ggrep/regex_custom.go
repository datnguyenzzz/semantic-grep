//go:build !stdregexp

package main

import regexp "github.com/datnguyenzzz/agent-context/ggrep/regexp"

// Regexp is the representation of our compiled custom DFA codesearch regular expression.
type Regexp = regexp.Regexp

// CompileRegex compiles our custom regular expression natively.
func CompileRegex(pattern string) (*Regexp, error) {
	return regexp.Compile(pattern)
}

//go:build !stdregexp

package ggrep

import regexp "github.com/datnguyenzzz/semantic-grep/ggrep/regexp"

// Regexp is the representation of our compiled custom DFA codesearch regular expression.
type Regexp = regexp.Regexp

// CompileRegex compiles our custom regular expression natively.
func CompileRegex(pattern string) (*Regexp, error) {
	return regexp.Compile(pattern)
}

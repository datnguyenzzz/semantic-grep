//go:build stdregexp

package ggrep

import "regexp"

// Regexp is the representation of our compiled Go standard library regular expression.
type Regexp = regexp.Regexp

// CompileRegex compiles Go's standard library regular expression natively.
func CompileRegex(pattern string) (*Regexp, error) {
	return regexp.Compile(pattern)
}

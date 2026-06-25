// Copyright 2011 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Forked from https://github.com/google/codesearch's regexp/syntax.
// And made some modifications for our needed

package regexp

import (
	"bytes"
	"regexp/syntax"
	"strings"
)

func bug() {
	panic("codesearch/regexp: internal error")
}

// Regexp is the representation of a compiled regular expression.
// A Regexp is NOT SAFE for concurrent use by multiple goroutines.
type Regexp struct {
	Syntax    *syntax.Regexp
	expr      string // original expression
	m         matcher
	hasPrefix bool
	prefix    []byte
}

// String returns the source text used to compile the regular expression.
func (re *Regexp) String() string {
	return re.expr
}

// Compile parses a regular expression and returns, if successful,
// a Regexp object that can be used to match against lines of text.
func Compile(expr string) (*Regexp, error) {
	re, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return nil, err
	}
	sre := re.Simplify()
	prog, err := syntax.Compile(sre)
	if err != nil {
		return nil, err
	}
	if err := toByteProg(prog); err != nil {
		return nil, err
	}
	r := &Regexp{
		Syntax: re,
		expr:   expr,
	}
	if err := r.m.init(prog); err != nil {
		return nil, err
	}
	r.initPrefix()
	return r, nil
}

func (re *Regexp) initPrefix() {
	if re.Syntax == nil {
		return
	}

	// Traverse the syntax tree to find a leading literal
	s := re.Syntax
	for s != nil {
		switch s.Op {
		case syntax.OpLiteral:
			// Case-folding safety check: Case-folded literals are not safe for exact SIMD byte search!
			if s.Flags&syntax.FoldCase != 0 {
				return
			}
			var b []byte
			for _, r := range s.Rune {
				if r < 128 { // ASCII characters
					b = append(b, byte(r))
				} else {
					break
				}
			}
			if len(b) > 0 {
				re.hasPrefix = true
				re.prefix = b
			}
			return
		case syntax.OpConcat:
			if len(s.Sub) > 0 {
				s = s.Sub[0]
			} else {
				return
			}
		case syntax.OpCapture:
			if len(s.Sub) > 0 {
				s = s.Sub[0]
			} else {
				return
			}
		default:
			return
		}
	}
}

// Match reports whether the Regexp matches the byte slice b.
func (re *Regexp) Match(b []byte) bool {
	if re.hasPrefix {
		idx := bytes.Index(b, re.prefix)
		if idx < 0 {
			return false
		}
		return re.m.match(b[idx:], idx == 0, true) >= 0
	}
	return re.m.match(b, true, true) >= 0
}

// MatchString reports whether the Regexp matches the string s.
func (re *Regexp) MatchString(s string) bool {
	if re.hasPrefix {
		idx := strings.Index(s, string(re.prefix))
		if idx < 0 {
			return false
		}
		return re.m.matchString(s[idx:], idx == 0, true) >= 0
	}
	return re.m.matchString(s, true, true) >= 0
}

// FindIndex returns a two-element slice of integers defining the location
// of the leftmost match in b. (Drop-in stdlib signature!)
func (re *Regexp) FindIndex(b []byte) (loc []int) {
	if re.hasPrefix {
		idx := bytes.Index(b, re.prefix)
		if idx < 0 {
			return nil
		}
		end := re.m.match(b[idx:], idx == 0, true)
		if end < 0 {
			return nil
		}
		return []int{idx, idx + end}
	}

	end := re.m.match(b, true, true)
	if end < 0 {
		return nil
	}
	return []int{0, end}
}

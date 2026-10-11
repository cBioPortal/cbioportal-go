package core

import (
	"regexp"
	"strings"
)

// ILikeMatcher returns a test of whether a string matches the pattern as
// ClickHouse's ILIKE matches it: % stands for any run of characters, _ for
// any one character, a backslash makes the next character literal, and
// case is ignored.
func ILikeMatcher(pattern string) func(string) bool {
	var b strings.Builder
	b.WriteString(`(?is)\A`)
	escaped := false
	for _, c := range pattern {
		switch {
		case escaped:
			b.WriteString(regexp.QuoteMeta(string(c)))
			escaped = false
		case c == '\\':
			escaped = true
		case c == '%':
			b.WriteString(`.*`)
		case c == '_':
			b.WriteString(`.`)
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	if escaped {
		b.WriteString(`\\`)
	}
	b.WriteString(`\z`)
	return regexp.MustCompile(b.String()).MatchString
}

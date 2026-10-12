// Package cli holds the conventions the command-line programs share: exit
// statuses, "did you mean" suggestions and JSON output.
package cli

import (
	"encoding/json"
	"io"
	"strings"
)

// Exit statuses of every command: success, a failure while running, and a
// malformed command line (an unknown command or flag, a missing argument).
const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

// Suggest returns the candidate closest to name, written as it would be
// typed ("--" + candidate for flags is up to the caller), or "" when none is
// close: within two edits, or one that starts with name.
func Suggest(name string, candidates []string) string {
	name = strings.ToLower(name)
	best, bestDist := "", 3
	for _, c := range candidates {
		d := distance(name, strings.ToLower(c))
		if len(name) >= 3 && strings.HasPrefix(strings.ToLower(c), name) && d > 1 {
			d = 1
		}
		if d < bestDist {
			best, bestDist = c, d
		}
	}
	if bestDist >= len(name) && len(name) <= 2 {
		return "" // "ab" is two edits from every two-letter name
	}
	return best
}

// DidYouMean is ` (did you mean <s>?)`, or "" for no suggestion.
func DidYouMean(s string) string {
	if s == "" {
		return ""
	}
	return " (did you mean " + s + "?)"
}

// distance is the Levenshtein distance of a and b.
func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// WriteJSON writes v as indented JSON followed by a newline.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// JSONFlag reports whether args hold --json (or -json, --json=true), and
// returns args without it.
func JSONFlag(args []string) (bool, []string) {
	on := false
	var rest []string
	for i, a := range args {
		if a == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		switch a {
		case "--json", "-json", "--json=true", "-json=true":
			on = true
		case "--json=false", "-json=false":
			on = false
		default:
			rest = append(rest, a)
		}
	}
	return on, rest
}

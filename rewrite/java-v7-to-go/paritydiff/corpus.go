package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// request is one corpus entry.
type request struct {
	Name   string
	Method string
	Path   string // path and query, starting with /
	Body   string // JSON body, empty for none
	Opts   requestOptions
}

// requestOptions are the per-request comparison settings written after the
// name in the corpus.
type requestOptions struct {
	Unordered     bool
	UnorderedKeys []string
	Ties          []string
	NullIsMissing bool
	StatusOnly    bool
	Ignore        []string
	Known         string // a known, accepted difference and its reason
	// KnownIfEmpty is the reason of a difference accepted only when A
	// answers an empty JSON array.
	KnownIfEmpty string
	// KnownIfBFails is the reason of a status difference accepted only when
	// A succeeds (2xx) and B fails with a 4xx or a 5xx other than a
	// gateway's.
	KnownIfBFails string
	// KnownIfBEmpty is the reason of a difference accepted only when B
	// answers 200 with an empty body.
	KnownIfBEmpty string
}

// parseCorpus reads the corpus format:
//
//	# comment
//	## name [options]
//	METHOD /api/path?query
//	optional JSON body, any number of lines
//
// Options: unordered (sort every array), unordered=key,key (sort the arrays
// held by these keys), ties=field,field (elements equal in these fields may
// come in any order), ignore=key,key (drop these keys), nullmissing (null
// equals absent), status-only (compare the status code only),
// known="reason" (an accepted difference), known-if-empty="reason" (a
// difference accepted when A answers an empty array), known-if-b-empty="reason"
// (a difference accepted when B answers 200 with an empty body),
// known-if-b-fails="reason"
// (a status difference accepted when A succeeds and B fails).
//
// Entries are separated by their "## " header lines; lines starting with "#"
// (but not "## ") are comments; blank lines are ignored.
func parseCorpus(r io.Reader) ([]request, error) {
	var out []request
	var cur *request
	var body []string
	names := map[string]int{}
	flush := func() error {
		if cur == nil {
			return nil
		}
		if cur.Method == "" {
			return fmt.Errorf("%s: missing request line", cur.Name)
		}
		cur.Body = strings.TrimSpace(strings.Join(body, "\n"))
		out = append(out, *cur)
		cur, body = nil, nil
		return nil
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	line := 0
	for sc.Scan() {
		line++
		s := sc.Text()
		t := strings.TrimSpace(s)
		switch {
		case strings.HasPrefix(t, "## "):
			if err := flush(); err != nil {
				return nil, err
			}
			req, err := parseHeader(strings.TrimSpace(t[3:]))
			if err != nil {
				return nil, fmt.Errorf("line %d: %v", line, err)
			}
			if prev, dup := names[req.Name]; dup {
				return nil, fmt.Errorf("line %d: duplicate name %q (first on line %d)", line, req.Name, prev)
			}
			names[req.Name] = line
			cur = &req
		case t == "" || strings.HasPrefix(t, "#"):
			continue
		case cur == nil:
			return nil, fmt.Errorf("line %d: request outside an entry", line)
		case cur.Method == "":
			method, path, ok := strings.Cut(t, " ")
			path = strings.TrimSpace(path)
			if !ok || !strings.HasPrefix(path, "/") {
				return nil, fmt.Errorf("line %d: want \"METHOD /path\", got %q", line, t)
			}
			cur.Method, cur.Path = strings.ToUpper(method), path
		default:
			body = append(body, s)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

// parseHeader parses "name opt opt ..." where the quoted options
// (known="...", known-if-empty="...", known-if-b-fails="...",
// known-if-b-empty="...") may contain
// spaces.
func parseHeader(h string) (request, error) {
	name, rest, _ := strings.Cut(h, " ")
	if name == "" {
		return request{}, fmt.Errorf("entry without a name")
	}
	req := request{Name: name}
	rest = strings.TrimSpace(rest)
	quoted := map[string]*string{`known="`: &req.Opts.Known, `known-if-empty="`: &req.Opts.KnownIfEmpty,
		`known-if-b-fails="`: &req.Opts.KnownIfBFails, `known-if-b-empty="`: &req.Opts.KnownIfBEmpty}
	for rest != "" {
		var tok string
		if prefix, _, ok := strings.Cut(rest, `"`); ok {
			if dst := quoted[prefix+`"`]; dst != nil {
				start := len(prefix) + 1
				end := strings.Index(rest[start:], `"`)
				if end < 0 {
					return req, fmt.Errorf("%s: unterminated %s", name, prefix+`"`)
				}
				*dst = rest[start : start+end]
				rest = strings.TrimSpace(rest[start+end+1:])
				continue
			}
		}
		tok, rest, _ = strings.Cut(rest, " ")
		rest = strings.TrimSpace(rest)
		switch {
		case tok == "unordered":
			req.Opts.Unordered = true
		case tok == "nullmissing":
			req.Opts.NullIsMissing = true
		case tok == "status-only":
			req.Opts.StatusOnly = true
		case strings.HasPrefix(tok, "unordered="):
			req.Opts.UnorderedKeys = append(req.Opts.UnorderedKeys, strings.Split(tok[len("unordered="):], ",")...)
		case strings.HasPrefix(tok, "ties="):
			req.Opts.Ties = append(req.Opts.Ties, strings.Split(tok[len("ties="):], ",")...)
		case strings.HasPrefix(tok, "ignore="):
			req.Opts.Ignore = append(req.Opts.Ignore, strings.Split(tok[len("ignore="):], ",")...)
		default:
			return req, fmt.Errorf("%s: unknown option %q", name, tok)
		}
	}
	return req, nil
}

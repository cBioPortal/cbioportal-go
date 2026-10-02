package parser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// YAMLMap is a YAML mapping that keeps its keys in document order, so its JSON
// encoding matches cbioportal-core's Jackson YAML→JSON conversion (which reads
// mappings into insertion-ordered LinkedHashMaps).
type YAMLMap []YAMLEntry

// YAMLEntry is one key/value pair of a YAMLMap.
type YAMLEntry struct {
	Key   string
	Value any
}

// MarshalJSON encodes the mapping as a JSON object in document order.
func (m YAMLMap) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, e := range m {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(e.Key)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		v, err := json.Marshal(e.Value)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

type yamlLine struct {
	indent int
	text   string
	num    int
}

type yamlParser struct {
	lines []yamlLine
	pos   int
}

// ParseYAML parses the block-style YAML subset used by study tags files:
// nested block mappings and sequences, plain/single-quoted/double-quoted
// scalars, flow sequences/mappings, literal (|) and folded (>) block scalars,
// and comments. Mappings decode to YAMLMap, sequences to []any, and scalars to
// nil, bool, json.Number, or string (YAML core-schema resolution, as Jackson
// does for an untyped read).
func ParseYAML(text string) (any, error) {
	p := &yamlParser{}
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimRight(raw, " \t")
		content := strings.TrimLeft(trimmed, " ")
		if content == "" || strings.HasPrefix(content, "#") {
			p.lines = append(p.lines, yamlLine{indent: -1, text: trimmed, num: i + 1})
			continue
		}
		if content == "---" || content == "..." {
			continue
		}
		p.lines = append(p.lines, yamlLine{indent: len(trimmed) - len(content), text: content, num: i + 1})
	}
	p.skipBlank()
	if p.pos >= len(p.lines) {
		return nil, nil
	}
	v, err := p.parseBlock(p.lines[p.pos].indent)
	if err != nil {
		return nil, err
	}
	p.skipBlank()
	if p.pos < len(p.lines) {
		l := p.lines[p.pos]
		return nil, fmt.Errorf("yaml line %d: unexpected content %q", l.num, l.text)
	}
	return v, nil
}

// ReadYAMLAsJSON reads a YAML file and returns its JSON encoding.
func ReadYAMLAsJSON(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	v, err := ParseYAML(string(b))
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (p *yamlParser) skipBlank() {
	for p.pos < len(p.lines) && p.lines[p.pos].indent < 0 {
		p.pos++
	}
}

// parseBlock parses the mapping or sequence whose entries start at indent.
func (p *yamlParser) parseBlock(indent int) (any, error) {
	p.skipBlank()
	if p.pos >= len(p.lines) {
		return nil, nil
	}
	if isSeqItem(p.lines[p.pos].text) {
		return p.parseSeq(indent)
	}
	return p.parseMap(indent)
}

func isSeqItem(text string) bool { return text == "-" || strings.HasPrefix(text, "- ") }

func (p *yamlParser) parseSeq(indent int) ([]any, error) {
	out := []any{}
	for {
		p.skipBlank()
		if p.pos >= len(p.lines) {
			return out, nil
		}
		l := p.lines[p.pos]
		if l.indent < indent || !isSeqItem(l.text) {
			return out, nil
		}
		if l.indent > indent {
			return nil, fmt.Errorf("yaml line %d: bad indentation", l.num)
		}
		rest := strings.TrimLeft(strings.TrimPrefix(l.text, "-"), " ")
		if rest == "" {
			p.pos++
			v, err := p.parseNested(indent)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			continue
		}
		childIndent := l.indent + (len(l.text) - len(rest))
		if isSeqItem(rest) || mappingKeyEnd(rest) >= 0 {
			// "- key: v" / "- - x": the remainder starts a nested block at
			// the column where its content begins.
			p.lines[p.pos] = yamlLine{indent: childIndent, text: rest, num: l.num}
			v, err := p.parseBlock(childIndent)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			continue
		}
		p.pos++
		v, err := p.parseValue(rest, indent)
		if err != nil {
			return nil, fmt.Errorf("yaml line %d: %w", l.num, err)
		}
		out = append(out, v)
	}
}

func (p *yamlParser) parseMap(indent int) (YAMLMap, error) {
	out := YAMLMap{}
	for {
		p.skipBlank()
		if p.pos >= len(p.lines) {
			return out, nil
		}
		l := p.lines[p.pos]
		if l.indent < indent {
			return out, nil
		}
		if l.indent > indent {
			return nil, fmt.Errorf("yaml line %d: bad indentation", l.num)
		}
		if isSeqItem(l.text) {
			return out, nil
		}
		end := mappingKeyEnd(l.text)
		if end < 0 {
			return nil, fmt.Errorf("yaml line %d: expected 'key: value', got %q", l.num, l.text)
		}
		key, err := scalarKey(strings.TrimSpace(l.text[:end]))
		if err != nil {
			return nil, fmt.Errorf("yaml line %d: %w", l.num, err)
		}
		rest := strings.TrimSpace(stripComment(l.text[end+1:]))
		p.pos++
		var v any
		if rest == "" {
			v, err = p.parseNestedOrSameIndentSeq(indent)
		} else {
			v, err = p.parseValue(rest, indent)
		}
		if err != nil {
			return nil, fmt.Errorf("yaml line %d: %w", l.num, err)
		}
		replaced := false
		for i := range out {
			if out[i].Key == key {
				out[i].Value, replaced = v, true
			}
		}
		if !replaced {
			out = append(out, YAMLEntry{Key: key, Value: v})
		}
	}
}

// parseNested parses the block (if any) indented deeper than parent.
func (p *yamlParser) parseNested(parent int) (any, error) {
	p.skipBlank()
	if p.pos >= len(p.lines) || p.lines[p.pos].indent <= parent {
		return nil, nil
	}
	return p.parseBlock(p.lines[p.pos].indent)
}

// parseNestedOrSameIndentSeq handles a mapping value given on the following
// lines: a deeper block, or a sequence at the key's own indent ("key:\n- a").
func (p *yamlParser) parseNestedOrSameIndentSeq(indent int) (any, error) {
	p.skipBlank()
	if p.pos < len(p.lines) && p.lines[p.pos].indent == indent && isSeqItem(p.lines[p.pos].text) {
		return p.parseSeq(indent)
	}
	return p.parseNested(indent)
}

// parseValue parses an inline value; block scalars consume following lines.
func (p *yamlParser) parseValue(text string, parent int) (any, error) {
	if text[0] == '|' || text[0] == '>' {
		return p.blockScalar(text, parent), nil
	}
	if text[0] == '[' || text[0] == '{' {
		v, rest, err := parseFlow(text)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(stripComment(rest)) != "" {
			return nil, fmt.Errorf("trailing content after flow value: %q", rest)
		}
		return v, nil
	}
	return parseScalar(stripComment(text))
}

func (p *yamlParser) blockScalar(header string, parent int) string {
	folded := header[0] == '>'
	chomp := "clip"
	if strings.Contains(header, "-") {
		chomp = "strip"
	} else if strings.Contains(header, "+") {
		chomp = "keep"
	}
	var raw []yamlLine
	for p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if l.indent >= 0 && l.indent <= parent {
			break
		}
		raw = append(raw, l)
		p.pos++
	}
	blockIndent := -1
	for _, l := range raw {
		if l.indent >= 0 {
			blockIndent = l.indent
			break
		}
	}
	var lines []string
	for _, l := range raw {
		if l.indent < 0 {
			// Blank lines and lines that only looked like comments are content.
			t := strings.TrimLeft(l.text, " ")
			if t == "" || blockIndent < 0 {
				lines = append(lines, "")
				continue
			}
			lines = append(lines, strings.Repeat(" ", max(0, len(l.text)-len(t)-blockIndent))+t)
			continue
		}
		lines = append(lines, strings.Repeat(" ", l.indent-blockIndent)+l.text)
	}
	trailing := 0
	for i := len(lines) - 1; i >= 0 && lines[i] == ""; i-- {
		trailing++
	}
	body := lines[:len(lines)-trailing]
	var s string
	if folded {
		var b strings.Builder
		for i, ln := range body {
			if i > 0 {
				prev := body[i-1]
				switch {
				case ln == "":
					b.WriteByte('\n')
				case prev == "":
				case strings.HasPrefix(ln, " ") || strings.HasPrefix(prev, " "):
					b.WriteByte('\n')
				default:
					b.WriteByte(' ')
				}
			}
			b.WriteString(ln)
		}
		s = b.String()
	} else {
		s = strings.Join(body, "\n")
	}
	switch chomp {
	case "clip":
		if len(body) > 0 {
			s += "\n"
		}
	case "keep":
		s += "\n" + strings.Repeat("\n", trailing)
	}
	return s
}

// mappingKeyEnd returns the index of the ':' that ends a mapping key (a colon
// followed by a space or end of line, outside quotes), or -1.
func mappingKeyEnd(text string) int {
	if strings.HasPrefix(text, "#") {
		return -1
	}
	start := 0
	if text != "" && (text[0] == '"' || text[0] == '\'') {
		q := text[0]
		i := 1
		for i < len(text) {
			if text[i] == q {
				if q == '\'' && i+1 < len(text) && text[i+1] == '\'' {
					i += 2
					continue
				}
				break
			}
			if q == '"' && text[i] == '\\' {
				i++
			}
			i++
		}
		start = i + 1
	} else if text != "" && (text[0] == '[' || text[0] == '{') {
		return -1
	}
	for i := start; i < len(text); i++ {
		if text[i] == ' ' && i+1 < len(text) && text[i+1] == '#' {
			return -1
		}
		if text[i] == ':' && (i+1 == len(text) || text[i+1] == ' ' || text[i+1] == '\t') {
			return i
		}
	}
	return -1
}

// stripComment removes a trailing " #..." comment outside quotes.
func stripComment(s string) string {
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			} else if q == '"' && c == '\\' {
				i++
			}
		case c == '"' || c == '\'':
			if i == 0 || s[i-1] == ' ' || s[i-1] == '[' || s[i-1] == ',' || s[i-1] == '{' {
				q = c
			}
		case c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t'):
			return strings.TrimRight(s[:i], " \t")
		}
	}
	return s
}

func scalarKey(s string) (string, error) {
	v, err := parseScalar(s)
	if err != nil {
		return "", err
	}
	switch t := v.(type) {
	case nil:
		return "null", nil
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case json.Number:
		return t.String(), nil
	}
	return fmt.Sprint(v), nil
}

var (
	yamlInt   = regexp.MustCompile(`^[-+]?(0|[1-9][0-9]*)$`)
	yamlFloat = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$`)
)

// parseScalar resolves one inline scalar.
func parseScalar(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	switch s[0] {
	case '\'':
		if len(s) < 2 || s[len(s)-1] != '\'' {
			return nil, fmt.Errorf("unterminated single-quoted scalar %q", s)
		}
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'"), nil
	case '"':
		if len(s) < 2 || s[len(s)-1] != '"' {
			return nil, fmt.Errorf("unterminated double-quoted scalar %q", s)
		}
		return unescapeDouble(s[1 : len(s)-1]), nil
	}
	switch s {
	case "~", "null", "Null", "NULL":
		return nil, nil
	case "true", "True", "TRUE":
		return true, nil
	case "false", "False", "FALSE":
		return false, nil
	}
	if yamlInt.MatchString(s) {
		return json.Number(strings.TrimPrefix(s, "+")), nil
	}
	if yamlFloat.MatchString(s) {
		f, err := strconv.ParseFloat(s, 64)
		if err == nil {
			return json.Number(strconv.FormatFloat(f, 'g', -1, 64)), nil
		}
	}
	return s, nil
}

func unescapeDouble(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 == len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '0':
			b.WriteByte(0)
		case 'u':
			if i+4 < len(s) {
				if r, err := strconv.ParseUint(s[i+1:i+5], 16, 32); err == nil {
					b.WriteRune(rune(r))
					i += 4
					continue
				}
			}
			b.WriteString(`\u`)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// parseFlow parses a flow sequence/mapping at the start of s, returning the
// value and the unconsumed remainder.
func parseFlow(s string) (any, string, error) {
	s = strings.TrimLeft(s, " ")
	if s == "" {
		return nil, s, fmt.Errorf("unexpected end of flow value")
	}
	switch s[0] {
	case '[':
		out := []any{}
		s = strings.TrimLeft(s[1:], " ")
		for {
			if s == "" {
				return nil, s, fmt.Errorf("unterminated flow sequence")
			}
			if s[0] == ']' {
				return out, s[1:], nil
			}
			v, rest, err := parseFlow(s)
			if err != nil {
				return nil, rest, err
			}
			out = append(out, v)
			s = strings.TrimLeft(rest, " ")
			if strings.HasPrefix(s, ",") {
				s = strings.TrimLeft(s[1:], " ")
			}
		}
	case '{':
		out := YAMLMap{}
		s = strings.TrimLeft(s[1:], " ")
		for {
			if s == "" {
				return nil, s, fmt.Errorf("unterminated flow mapping")
			}
			if s[0] == '}' {
				return out, s[1:], nil
			}
			k, rest, err := flowToken(s, true)
			if err != nil {
				return nil, rest, err
			}
			key, err := scalarKey(k)
			if err != nil {
				return nil, rest, err
			}
			s = strings.TrimLeft(rest, " ")
			var v any
			if strings.HasPrefix(s, ":") {
				v, rest, err = parseFlow(strings.TrimLeft(s[1:], " "))
				if err != nil {
					return nil, rest, err
				}
				s = strings.TrimLeft(rest, " ")
			}
			out = append(out, YAMLEntry{Key: key, Value: v})
			if strings.HasPrefix(s, ",") {
				s = strings.TrimLeft(s[1:], " ")
			}
		}
	}
	tok, rest, err := flowToken(s, false)
	if err != nil {
		return nil, rest, err
	}
	v, err := parseScalar(tok)
	return v, rest, err
}

// flowToken reads one scalar inside a flow collection.
func flowToken(s string, isKey bool) (string, string, error) {
	if s != "" && (s[0] == '"' || s[0] == '\'') {
		q := s[0]
		for i := 1; i < len(s); i++ {
			if s[i] == q {
				if q == '\'' && i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				return s[:i+1], s[i+1:], nil
			}
			if q == '"' && s[i] == '\\' {
				i++
			}
		}
		return "", s, fmt.Errorf("unterminated quoted scalar in flow value")
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ',', ']', '}':
			return strings.TrimSpace(s[:i]), s[i:], nil
		case ':':
			if isKey && (i+1 == len(s) || s[i+1] == ' ') {
				return strings.TrimSpace(s[:i]), s[i:], nil
			}
		}
	}
	return strings.TrimSpace(s), "", nil
}

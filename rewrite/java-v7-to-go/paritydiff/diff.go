package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// compareOptions controls how two JSON documents are normalized before they
// are compared.
type compareOptions struct {
	// Unordered sorts every array by the canonical form of its elements, for
	// endpoints whose element order Java leaves unspecified.
	Unordered bool
	// UnorderedKeys sorts only the arrays held by object keys of these names.
	UnorderedKeys map[string]bool
	// Ties lists the fields an array of objects is ordered by: runs of
	// adjacent elements equal in these fields may come in any order, so they
	// are sorted, while the order of the runs is still compared.
	Ties []string
	// Ignore drops object keys with these names at any depth.
	Ignore map[string]bool
	// NullIsMissing treats a key with a null value like an absent key.
	NullIsMissing bool
	// RelTol is the relative tolerance for numbers; AbsTol the absolute one
	// (for values that should be zero).
	RelTol, AbsTol float64
	// KeyRelTol is the relative tolerance of numbers held by these keys,
	// in place of RelTol.
	KeyRelTol map[string]float64
	// MaxDiffs caps the differences recorded (all are counted).
	MaxDiffs int
}

func defaultOptions() compareOptions {
	return compareOptions{RelTol: 1e-9, AbsTol: 1e-15, MaxDiffs: 5}
}

// diffResult is the outcome of comparing two documents.
type diffResult struct {
	Count int      // number of differences found
	Diffs []string // the first MaxDiffs of them, as "path: description"
	// Lengths reports top-level arrays of different lengths; OnlyA and
	// OnlyB then count the elements found on one side only.
	Lengths      bool
	OnlyA, OnlyB int
}

// aInB reports whether the only differences are top-level elements B has
// and A lacks; bInA the reverse.
func (r diffResult) aInB() bool { return r.onlyLengths() && r.OnlyA == 0 }
func (r diffResult) bInA() bool { return r.onlyLengths() && r.OnlyB == 0 }

func (r diffResult) onlyLengths() bool { return r.Lengths && r.Count == 1+r.OnlyA+r.OnlyB }

func (r *diffResult) add(o *compareOptions, path, format string, args ...any) {
	r.Count++
	if len(r.Diffs) < o.MaxDiffs {
		if path == "" {
			path = "$"
		}
		r.Diffs = append(r.Diffs, path+": "+fmt.Sprintf(format, args...))
	}
}

// decodeJSON parses a document keeping numbers as json.Number.
func decodeJSON(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if d.More() {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return v, nil
}

// normalize applies the options that rewrite a document: dropped keys,
// null-as-missing and array sorting.
func normalize(v any, o *compareOptions) any {
	return normalizeIn(v, "", o)
}

func normalizeIn(v any, key string, o *compareOptions) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			if o.Ignore[k] || (o.NullIsMissing && x == nil) {
				continue
			}
			m[k] = normalizeIn(x, k, o)
		}
		return m
	case []any:
		a := make([]any, len(t))
		for i, x := range t {
			a[i] = normalizeIn(x, "", o)
		}
		switch {
		case o.Unordered || o.UnorderedKeys[key]:
			sortCanonical(a)
		case len(o.Ties) > 0:
			sortTies(a, o.Ties)
		}
		return a
	}
	return v
}

// sortTies sorts each run of adjacent elements that agree on the tie fields.
func sortTies(a []any, fields []string) {
	tieKey := func(v any) (string, bool) {
		m, ok := v.(map[string]any)
		if !ok {
			return "", false
		}
		k := map[string]any{}
		for _, f := range fields {
			k[f] = m[f]
		}
		return canonical(k), true
	}
	for start := 0; start < len(a); {
		k, ok := tieKey(a[start])
		end := start + 1
		for ok && end < len(a) {
			if k2, ok2 := tieKey(a[end]); !ok2 || k2 != k {
				break
			}
			end++
		}
		sortCanonical(a[start:end])
		start = end
	}
}

// identityFields are object keys that identify a record in cBioPortal
// responses; besides these, every key ending in "Id" or "Key" does.
var identityFields = map[string]bool{
	"hugoGeneSymbol": true, "name": true, "value": true, "treatment": true, "eventType": true,
	"stableId": true, "keyword": true, "chromosome": true, "start": true, "end": true,
	"proteinChange": true, "proteinPosStart": true, "alteration": true, "time": true, "key": true,
}

func isIdentityField(k string) bool {
	return identityFields[k] || strings.HasSuffix(k, "Id") || strings.HasSuffix(k, "Key")
}

// sortKey orders array elements: objects by their identity fields first, so a
// record whose other fields differ still lines up with its counterpart, then
// by their whole canonical form.
func sortKey(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return canonical(v)
	}
	id := map[string]any{}
	for k, x := range m {
		if isIdentityField(k) {
			if _, nested := x.(map[string]any); !nested {
				id[k] = x
			}
		}
	}
	return canonical(id) + "\x00" + canonical(v)
}

// sortCanonical sorts elements by their sort key, stably.
func sortCanonical(a []any) {
	keys := make([]string, len(a))
	idx := make([]int, len(a))
	for i, x := range a {
		keys[i] = sortKey(x)
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool { return keys[idx[i]] < keys[idx[j]] })
	out := make([]any, len(a))
	for i, j := range idx {
		out[i] = a[j]
	}
	copy(a, out)
}

// canonical renders a value with sorted keys and numbers rounded to 9
// significant digits, so values equal within tolerance usually share a key.
func canonical(v any) string {
	var sb strings.Builder
	writeCanonical(&sb, v)
	return sb.String()
}

func writeCanonical(sb *strings.Builder, v any) {
	switch t := v.(type) {
	case nil:
		sb.WriteString("null")
	case bool:
		sb.WriteString(strconv.FormatBool(t))
	case json.Number:
		if f, err := t.Float64(); err == nil {
			sb.WriteString(strconv.FormatFloat(f, 'g', 9, 64))
		} else {
			sb.WriteString(t.String())
		}
	case string:
		sb.WriteString(strconv.Quote(t))
	case []any:
		sb.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				sb.WriteByte(',')
			}
			writeCanonical(sb, x)
		}
		sb.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(strconv.Quote(k))
			sb.WriteByte(':')
			writeCanonical(sb, t[k])
		}
		sb.WriteByte('}')
	default:
		fmt.Fprintf(sb, "%v", t)
	}
}

// compareJSON normalizes both documents and returns their differences.
func compareJSON(a, b any, o compareOptions) diffResult {
	var r diffResult
	walk(normalize(a, &o), normalize(b, &o), "", &o, &r)
	return r
}

func numbersEqual(x, y json.Number, o *compareOptions) bool {
	if x == y {
		return true
	}
	fx, ex := x.Float64()
	fy, ey := y.Float64()
	if ex != nil || ey != nil {
		return false
	}
	d := math.Abs(fx - fy)
	return d <= o.AbsTol || d <= o.RelTol*math.Max(math.Abs(fx), math.Abs(fy))
}

func equal(a, b any, o *compareOptions) bool {
	var r diffResult
	quiet := *o
	quiet.MaxDiffs = 0
	walk(a, b, "", &quiet, &r)
	return r.Count == 0
}

func walk(a, b any, path string, o *compareOptions, r *diffResult) {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			r.add(o, path, "a=%s b=%s", short(a), short(b))
			return
		}
		keys := make([]string, 0, len(x)+len(y))
		for k := range x {
			keys = append(keys, k)
		}
		for k := range y {
			if _, ok := x[k]; !ok {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			va, ina := x[k]
			vb, inb := y[k]
			p := path + "." + k
			switch {
			case !inb:
				r.add(o, p, "only in a (%s)", short(va))
			case !ina:
				r.add(o, p, "only in b (%s)", short(vb))
			default:
				if tol, ok := o.KeyRelTol[k]; ok {
					if na, ok := va.(json.Number); ok {
						loose := *o
						loose.RelTol = tol
						if nb, ok := vb.(json.Number); !ok || !numbersEqual(na, nb, &loose) {
							r.add(o, p, "a=%s b=%s", short(va), short(vb))
						}
						continue
					}
				}
				walk(va, vb, p, o, r)
			}
		}
	case []any:
		y, ok := b.([]any)
		if !ok {
			r.add(o, path, "a=%s b=%s", short(a), short(b))
			return
		}
		walkArray(x, y, path, o, r)
	case json.Number:
		y, ok := b.(json.Number)
		if !ok || !numbersEqual(x, y, o) {
			r.add(o, path, "a=%s b=%s", short(a), short(b))
		}
	default:
		if !scalarEqual(a, b) {
			r.add(o, path, "a=%s b=%s", short(a), short(b))
		}
	}
}

func scalarEqual(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	}
	return false
}

// walkArray compares arrays element by element when their lengths match. When
// they hold the same elements in another order, that is reported once instead
// of per element; when the lengths differ, the elements found on only one side
// are reported.
func walkArray(x, y []any, path string, o *compareOptions, r *diffResult) {
	if len(x) == len(y) {
		var sub diffResult
		walkElems(x, y, path, o, &sub)
		if sub.Count == 0 {
			return
		}
		if !o.Unordered && sameMultiset(x, y, o) {
			r.add(o, path, "same %d elements in a different order", len(x))
			return
		}
		r.Count += sub.Count
		for _, d := range sub.Diffs {
			if len(r.Diffs) < o.MaxDiffs {
				r.Diffs = append(r.Diffs, d)
			}
		}
		return
	}
	r.add(o, path, "length a=%d b=%d", len(x), len(y))
	onlyA, onlyB := multisetDiff(x, y)
	if path == "" {
		r.Lengths, r.OnlyA, r.OnlyB = true, len(onlyA), len(onlyB)
	}
	for _, v := range onlyA {
		r.add(o, path+"[]", "only in a: %s", short(v))
	}
	for _, v := range onlyB {
		r.add(o, path+"[]", "only in b: %s", short(v))
	}
}

func walkElems(x, y []any, path string, o *compareOptions, r *diffResult) {
	for i := range x {
		walk(x[i], y[i], path+"["+strconv.Itoa(i)+"]", o, r)
	}
}

func sameMultiset(x, y []any, o *compareOptions) bool {
	xs := append([]any(nil), x...)
	ys := append([]any(nil), y...)
	sortCanonical(xs)
	sortCanonical(ys)
	for i := range xs {
		if !equal(xs[i], ys[i], o) {
			return false
		}
	}
	return true
}

// multisetDiff returns the elements of x missing from y and vice versa, by
// canonical key.
func multisetDiff(x, y []any) (onlyX, onlyY []any) {
	count := map[string]int{}
	for _, v := range y {
		count[canonical(v)]++
	}
	for _, v := range x {
		k := canonical(v)
		if count[k] > 0 {
			count[k]--
		} else {
			onlyX = append(onlyX, v)
		}
	}
	count = map[string]int{}
	for _, v := range x {
		count[canonical(v)]++
	}
	for _, v := range y {
		k := canonical(v)
		if count[k] > 0 {
			count[k]--
		} else {
			onlyY = append(onlyY, v)
		}
	}
	return onlyX, onlyY
}

// short renders a value for a diff line, truncated.
func short(v any) string {
	s := canonical(v)
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}

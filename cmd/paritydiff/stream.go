package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"sort"
	"strconv"
)

// Large responses (a whole study's copy-number calls can be 100 MB) are
// compared element by element: decoding both documents whole, normalizing
// and sorting them takes about ten times their size. For top-level arrays
// compareStream keeps per element only its position in the body and its
// sort keys, and decodes again the elements whose canonical forms differ.

// element is a top-level array element of a response body.
type element struct {
	start, end int      // its bytes in the body
	tie        string   // canonical form of the Ties fields (Ties only)
	id         string   // canonical form of its identity fields
	sum        [16]byte // hash of its canonical form
}

// sortsBefore orders elements by identity, then content, as sortKey does
// (with a hash standing in for the canonical form).
func sortsBefore(x, y element) bool {
	if x.id != y.id {
		return x.id < y.id
	}
	return bytes.Compare(x.sum[:], y.sum[:]) < 0
}

// scanElements reads the elements of the top-level array in body, each
// normalized as compareJSON normalizes it; ok is false when body is not a
// JSON array.
func scanElements(body []byte, o *compareOptions) (els []element, ok bool) {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if t, err := d.Token(); err != nil || t != json.Delim('[') {
		return nil, false
	}
	for d.More() {
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return nil, false
		}
		end := int(d.InputOffset())
		v, err := decodeJSON(raw)
		if err != nil {
			return nil, false
		}
		v = normalizeIn(v, "", o)
		e := element{start: end - len(raw), end: end}
		full := canonical(v)
		sum := sha256.Sum256([]byte(full))
		copy(e.sum[:], sum[:16])
		if m, isObj := v.(map[string]any); isObj {
			id := map[string]any{}
			for k, x := range m {
				if isIdentityField(k) {
					if _, nested := x.(map[string]any); !nested {
						id[k] = x
					}
				}
			}
			e.id = canonical(id)
			if len(o.Ties) > 0 {
				t := map[string]any{}
				for _, f := range o.Ties {
					t[f] = m[f]
				}
				e.tie = canonical(t)
			}
		} else {
			e.id = full
		}
		els = append(els, e)
	}
	if t, err := d.Token(); err != nil || t != json.Delim(']') {
		return nil, false
	}
	if d.More() {
		return nil, false
	}
	return els, true
}

// compareStream compares two top-level JSON arrays as compareJSON does,
// holding one pair of elements decoded at a time; ok is false when either
// body is not an array.
func compareStream(a, b []byte, o compareOptions) (r diffResult, ok bool) {
	xs, okA := scanElements(a, &o)
	if !okA {
		return r, false
	}
	ys, okB := scanElements(b, &o)
	if !okB {
		return r, false
	}
	order := func(els []element) {
		switch {
		case o.Unordered || o.UnorderedKeys[""]:
			sort.SliceStable(els, func(i, j int) bool { return sortsBefore(els[i], els[j]) })
		case len(o.Ties) > 0:
			for start := 0; start < len(els); {
				end := start + 1
				for end < len(els) && els[end].tie == els[start].tie {
					end++
				}
				run := els[start:end]
				sort.SliceStable(run, func(i, j int) bool { return sortsBefore(run[i], run[j]) })
				start = end
			}
		}
	}
	order(xs)
	order(ys)
	decode := func(body []byte, e element) any {
		v, _ := decodeJSON(body[e.start:e.end])
		return normalizeIn(v, "", &o)
	}
	if len(xs) == len(ys) {
		var sub diffResult
		for i := range xs {
			if xs[i].sum != ys[i].sum {
				walk(decode(a, xs[i]), decode(b, ys[i]), "["+strconv.Itoa(i)+"]", &o, &sub)
			}
		}
		if sub.Count == 0 {
			return r, true
		}
		if !o.Unordered && sameSums(xs, ys) {
			r.add(&o, "", "same %d elements in a different order", len(xs))
			return r, true
		}
		r.Count += sub.Count
		for _, d := range sub.Diffs {
			if len(r.Diffs) < o.MaxDiffs {
				r.Diffs = append(r.Diffs, d)
			}
		}
		return r, true
	}
	r.add(&o, "", "length a=%d b=%d", len(xs), len(ys))
	onlyA, onlyB := sumDiff(xs, ys), sumDiff(ys, xs)
	for _, e := range onlyA {
		r.add(&o, "[]", "only in a: %s", short(decode(a, e)))
	}
	for _, e := range onlyB {
		r.add(&o, "[]", "only in b: %s", short(decode(b, e)))
	}
	return r, true
}

// sameSums reports whether two element lists hold the same canonical forms.
func sameSums(xs, ys []element) bool {
	return len(sumDiff(xs, ys)) == 0 && len(sumDiff(ys, xs)) == 0
}

// sumDiff returns the elements of xs whose canonical form ys lacks (counting
// repeats).
func sumDiff(xs, ys []element) []element {
	count := map[[16]byte]int{}
	for _, e := range ys {
		count[e.sum]++
	}
	var out []element
	for _, e := range xs {
		if count[e.sum] > 0 {
			count[e.sum]--
		} else {
			out = append(out, e)
		}
	}
	return out
}

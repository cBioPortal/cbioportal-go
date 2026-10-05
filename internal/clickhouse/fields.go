package clickhouse

import (
	"reflect"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// jsonField is a struct field as encoding/json sees it when decoding: the
// name it matches, where it lives, and how.
type jsonField struct {
	name   string
	tagged bool
	quoted bool // the ",string" option on a scalar
	index  []int
	typ    reflect.Type // the field's declared type
	offset uintptr      // from the start of the outer struct
	viaPtr bool         // reached through an embedded pointer
}

// jsonFields lists the fields encoding/json decodes object keys into for
// struct type t, in index order, following its rules for tags, unexported
// and embedded fields and name conflicts.
func jsonFields(t reflect.Type) []jsonField {
	if f, ok := fieldCache.Load(t); ok {
		return f.([]jsonField)
	}
	f := typeFields(t)
	fieldCache.Store(t, f)
	return f
}

var fieldCache sync.Map // reflect.Type -> []jsonField

// typeFields mirrors encoding/json's typeFields: a breadth-first walk over
// embedded structs, where a shallower field hides deeper ones of the same
// name and equally deep duplicates hide each other unless exactly one is
// tagged.
func typeFields(t reflect.Type) []jsonField {
	type walk struct {
		typ    reflect.Type
		index  []int
		offset uintptr
		viaPtr bool
	}
	var current []walk
	next := []walk{{typ: t}}
	var count, nextCount map[reflect.Type]int
	visited := map[reflect.Type]bool{}
	var fields []jsonField

	for len(next) > 0 {
		current, next = next, current[:0]
		count, nextCount = nextCount, map[reflect.Type]int{}
		for _, f := range current {
			if visited[f.typ] {
				continue
			}
			visited[f.typ] = true
			for i := 0; i < f.typ.NumField(); i++ {
				sf := f.typ.Field(i)
				if sf.Anonymous {
					st := sf.Type
					if st.Kind() == reflect.Pointer {
						st = st.Elem()
					}
					if !sf.IsExported() && st.Kind() != reflect.Struct {
						continue
					}
				} else if !sf.IsExported() {
					continue
				}
				tag := sf.Tag.Get("json")
				if tag == "-" {
					continue
				}
				name, opts, _ := strings.Cut(tag, ",")
				if !isValidTag(name) {
					name = ""
				}
				index := append(slices.Clone(f.index), i)
				ft := sf.Type
				if ft.Name() == "" && ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}
				quoted := false
				if hasOpt(opts, "string") {
					switch ft.Kind() {
					case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
						reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
						reflect.Float32, reflect.Float64, reflect.String:
						quoted = true
					}
				}
				if name != "" || !sf.Anonymous || ft.Kind() != reflect.Struct {
					field := jsonField{
						name: name, tagged: name != "", quoted: quoted, index: index,
						typ: sf.Type, offset: f.offset + sf.Offset, viaPtr: f.viaPtr,
					}
					if field.name == "" {
						field.name = sf.Name
					}
					fields = append(fields, field)
					if count[f.typ] > 1 {
						// Several embeddings of the same struct at this depth:
						// the duplicate makes the conflict rule drop it.
						fields = append(fields, fields[len(fields)-1])
					}
					continue
				}
				nextCount[ft]++
				if nextCount[ft] == 1 {
					next = append(next, walk{
						typ: ft, index: index, offset: f.offset + sf.Offset,
						viaPtr: f.viaPtr || sf.Type.Kind() == reflect.Pointer,
					})
				}
			}
		}
	}

	slices.SortFunc(fields, func(a, b jsonField) int {
		if c := strings.Compare(a.name, b.name); c != 0 {
			return c
		}
		if len(a.index) != len(b.index) {
			return len(a.index) - len(b.index)
		}
		if a.tagged != b.tagged {
			if a.tagged {
				return -1
			}
			return 1
		}
		return slices.Compare(a.index, b.index)
	})
	out := fields[:0]
	for advance, i := 0, 0; i < len(fields); i += advance {
		fi := fields[i]
		for advance = 1; i+advance < len(fields); advance++ {
			if fields[i+advance].name != fi.name {
				break
			}
		}
		if advance == 1 {
			out = append(out, fi)
			continue
		}
		group := fields[i : i+advance]
		if len(group[0].index) == len(group[1].index) && group[0].tagged == group[1].tagged {
			continue
		}
		out = append(out, group[0])
	}
	fields = slices.Clone(out)
	slices.SortFunc(fields, func(a, b jsonField) int { return slices.Compare(a.index, b.index) })
	return fields
}

func hasOpt(opts, name string) bool {
	for opts != "" {
		var o string
		o, opts, _ = strings.Cut(opts, ",")
		if o == name {
			return true
		}
	}
	return false
}

func isValidTag(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", c):
		case !unicode.IsLetter(c) && !unicode.IsDigit(c):
			return false
		}
	}
	return true
}

// matchField returns the field encoding/json decodes key into: the exact
// name, else the first case-insensitive match. ok is false when the key
// cannot be resolved here exactly (non-ASCII names fold by Unicode rules).
func matchField(fields []jsonField, key string) (f *jsonField, ok bool) {
	for i := range fields {
		if fields[i].name == key {
			return &fields[i], true
		}
	}
	if !isASCII(key) {
		return nil, false
	}
	for i := range fields {
		if !isASCII(fields[i].name) {
			return nil, false
		}
	}
	for i := range fields {
		if strings.EqualFold(fields[i].name, key) {
			return &fields[i], true
		}
	}
	return nil, true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

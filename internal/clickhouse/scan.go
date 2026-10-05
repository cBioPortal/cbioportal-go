package clickhouse

import (
	"encoding"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
	"unsafe"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// The driver scans each column into its own Go type (rows.Scan); the plan
// then copies the value into the struct field encoding/json would fill from
// ClickHouse's JSONEachRow output (64-bit integers unquoted), with the same
// value: columns match fields by json name, NULL and non-finite floats (JSON
// null) leave the zero value, Float32 reads back from its shortest decimal
// text, invalid UTF-8 becomes U+FFFD per byte, empty arrays are non-nil, and
// integers out of the field's range are errors. Anything else (tuples, maps,
// dates, decimals, any/RawMessage/unmarshaler fields, ambiguous names) makes
// the query decode from JSON instead.

// plainType matches the column types handled here; the driver scans them
// into Go strings, integers, floats, bools, pointers (Nullable) and slices
// (Array).
var plainType = regexp.MustCompile(`^(Array\()*(LowCardinality\()?(Nullable\()?` +
	`(String|FixedString\(\d+\)|Enum(8|16)\(.*\)|U?Int(8|16|32|64)|Float(32|64)|Bool)\)*$`)

// jsonField is a struct field that a JSON object key can set.
type jsonField struct {
	name   string
	typ    reflect.Type
	offset uintptr
	depth  int // embedding depth
}

// jsonFields lists t's fields by the names encoding/json matches: the json
// tag name, else the Go name, promoted through embedded (non-pointer)
// structs, where a shallower field hides deeper ones of the same name. ok is
// false for shapes resolved here differently than by encoding/json: a
// pointer embedding, a ",string" option, or two candidates for one
// case-folded name.
func jsonFields(t reflect.Type, offset uintptr) (fields []jsonField, ok bool) {
	for i := range t.NumField() {
		sf := t.Field(i)
		tag := sf.Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if tag == "-" || !sf.IsExported() && !sf.Anonymous {
			continue
		}
		if strings.Contains(","+opts+",", ",string,") {
			return nil, false
		}
		if sf.Anonymous && name == "" {
			if sf.Type.Kind() == reflect.Pointer {
				return nil, false
			}
			if sf.Type.Kind() == reflect.Struct {
				sub, ok := jsonFields(sf.Type, offset+sf.Offset)
				if !ok {
					return nil, false
				}
				for _, f := range sub {
					f.depth++
					fields = append(fields, f)
				}
				continue
			}
			if !sf.IsExported() {
				continue
			}
		}
		if name == "" {
			name = sf.Name
		}
		fields = append(fields, jsonField{name, sf.Type, offset + sf.Offset, 0})
	}
	out := fields[:0:0]
	for i, f := range fields {
		hidden := false
		for j, g := range fields {
			switch {
			case i == j || !strings.EqualFold(f.name, g.name):
			case f.name != g.name || f.depth == g.depth:
				return nil, false
			case g.depth < f.depth:
				hidden = true
			}
		}
		if !hidden {
			out = append(out, f)
		}
	}
	return out, true
}

// convFn copies the column value at src (the driver's type) into the field
// at dst. null reports a value JSON shows as null, which leaves the target
// untouched.
type convFn func(src, dst unsafe.Pointer) (null bool, err error)

var (
	jsonUnmarshaler = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// readNumber reads an integer of kind k at p as int64 or uint64.
func readNumber(k reflect.Kind, p unsafe.Pointer) (i int64, u uint64) {
	switch k {
	case reflect.Int8:
		i = int64(*(*int8)(p))
	case reflect.Int16:
		i = int64(*(*int16)(p))
	case reflect.Int32:
		i = int64(*(*int32)(p))
	case reflect.Int64:
		i = *(*int64)(p)
	case reflect.Uint8:
		u = uint64(*(*uint8)(p))
	case reflect.Uint16:
		u = uint64(*(*uint16)(p))
	case reflect.Uint32:
		u = uint64(*(*uint32)(p))
	case reflect.Uint64:
		u = *(*uint64)(p)
	}
	return
}

func isSigned(k reflect.Kind) bool   { return k >= reflect.Int && k <= reflect.Int64 }
func isUnsigned(k reflect.Kind) bool { return k >= reflect.Uint && k <= reflect.Uint64 }

// buildConv returns the conversion of a column scanned as nt into a field of
// type ft, or false when it is not exactly reproducible.
func buildConv(nt, ft reflect.Type) (convFn, bool) {
	if ft.Implements(jsonUnmarshaler) || reflect.PointerTo(ft).Implements(jsonUnmarshaler) ||
		ft.Implements(textUnmarshaler) || reflect.PointerTo(ft).Implements(textUnmarshaler) {
		return nil, false
	}
	nk, fk := nt.Kind(), ft.Kind()
	switch {
	case fk == reflect.Pointer:
		inner, ok := buildConv(nt, ft.Elem())
		et := ft.Elem()
		return func(src, dst unsafe.Pointer) (bool, error) {
			v := reflect.New(et).UnsafePointer()
			if null, err := inner(src, v); err != nil || null {
				return false, err
			}
			*(*unsafe.Pointer)(dst) = v
			return false, nil
		}, ok
	case nk == reflect.Pointer:
		inner, ok := buildConv(nt.Elem(), ft)
		return func(src, dst unsafe.Pointer) (bool, error) {
			if p := *(*unsafe.Pointer)(src); p != nil {
				return inner(p, dst)
			}
			return true, nil
		}, ok
	case nk == reflect.String && fk == reflect.String:
		return func(src, dst unsafe.Pointer) (bool, error) {
			*(*string)(dst) = jsonString(*(*string)(src))
			return false, nil
		}, true
	case nk == reflect.Bool && fk == reflect.Bool:
		return func(src, dst unsafe.Pointer) (bool, error) {
			*(*bool)(dst) = *(*bool)(src)
			return false, nil
		}, true
	case (isSigned(nk) || isUnsigned(nk)) && (isSigned(fk) || isUnsigned(fk) || fk == reflect.Float64):
		return func(src, dst unsafe.Pointer) (bool, error) {
			i, u := readNumber(nk, src)
			v := reflect.NewAt(ft, dst).Elem()
			switch {
			case fk == reflect.Float64 && isSigned(nk):
				v.SetFloat(float64(i))
			case fk == reflect.Float64:
				v.SetFloat(float64(u))
			case isSigned(fk) && isSigned(nk) && !v.OverflowInt(i):
				v.SetInt(i)
			case isSigned(fk) && isUnsigned(nk) && u <= math.MaxInt64 && !v.OverflowInt(int64(u)):
				v.SetInt(int64(u))
			case isUnsigned(fk) && isUnsigned(nk) && !v.OverflowUint(u):
				v.SetUint(u)
			case isUnsigned(fk) && isSigned(nk) && i >= 0 && !v.OverflowUint(uint64(i)):
				v.SetUint(uint64(i))
			default:
				n := strconv.FormatInt(i, 10)
				if isUnsigned(nk) {
					n = strconv.FormatUint(u, 10)
				}
				return false, &json.UnmarshalTypeError{Value: "number " + n, Type: ft}
			}
			return false, nil
		}, true
	case nk == reflect.Float32 && (fk == reflect.Float64 || fk == reflect.Float32):
		return func(src, dst unsafe.Pointer) (bool, error) {
			f := *(*float32)(src)
			if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
				return true, nil
			}
			if fk == reflect.Float32 {
				*(*float32)(dst) = f
				return false, nil
			}
			// ClickHouse writes the shortest text that reads back as the
			// Float32; JSON decoding parses that text as a float64.
			*(*float64)(dst), _ = strconv.ParseFloat(strconv.FormatFloat(float64(f), 'g', -1, 32), 64)
			return false, nil
		}, true
	case nk == reflect.Float64 && fk == reflect.Float64:
		return func(src, dst unsafe.Pointer) (bool, error) {
			f := *(*float64)(src)
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return true, nil
			}
			*(*float64)(dst) = f
			return false, nil
		}, true
	case nk == reflect.Slice && fk == reflect.Slice:
		elem, ok := buildConv(nt.Elem(), ft.Elem())
		return func(src, dst unsafe.Pointer) (bool, error) {
			sv := reflect.NewAt(nt, src).Elem()
			out := reflect.MakeSlice(ft, sv.Len(), sv.Len())
			for i := range sv.Len() {
				if _, err := elem(sv.Index(i).Addr().UnsafePointer(), out.Index(i).Addr().UnsafePointer()); err != nil {
					return false, err
				}
			}
			reflect.NewAt(ft, dst).Elem().Set(out)
			return false, nil
		}, ok
	}
	return nil, false
}

// jsonString returns s as encoding/json decodes it from ClickHouse's JSON
// output, which writes string bytes unchanged: each byte of an invalid UTF-8
// sequence becomes U+FFFD.
func jsonString(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(utf8.RuneError)
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// colPlan is how one result column reaches its field (conv nil: dropped).
type colPlan struct {
	nt     reflect.Type
	offset uintptr
	conv   convFn
}

// scanPlan decodes the rows of one result shape into a struct type.
type scanPlan struct {
	cols []colPlan
}

type planKey struct {
	t   reflect.Type
	sig string
}

var planCache sync.Map // planKey -> *scanPlan, or the error when unsupported

// planFor returns the plan decoding rows with these columns into struct type
// t, or an error naming why the JSON path must decode them.
func planFor(t reflect.Type, names []string, types []driver.ColumnType) (*scanPlan, error) {
	var sig strings.Builder
	for i, n := range names {
		fmt.Fprintf(&sig, "%s\x00%s\x00", n, types[i].DatabaseTypeName())
	}
	key := planKey{t, sig.String()}
	if v, ok := planCache.Load(key); ok {
		if err, bad := v.(error); bad {
			return nil, err
		}
		return v.(*scanPlan), nil
	}
	plan, err := buildPlan(t, names, types)
	if err != nil {
		planCache.Store(key, err)
		return nil, err
	}
	planCache.Store(key, plan)
	return plan, nil
}

func buildPlan(t reflect.Type, names []string, types []driver.ColumnType) (*scanPlan, error) {
	fields, ok := jsonFields(t, 0)
	if !ok {
		return nil, fmt.Errorf("%s: field names not resolved as encoding/json does", t)
	}
	plan := &scanPlan{cols: make([]colPlan, len(names))}
	used := map[int]bool{}
	for i, name := range names {
		c := &plan.cols[i]
		c.nt = types[i].ScanType()
		f := -1
		for j := range fields {
			if fields[j].name == name || f < 0 && strings.EqualFold(fields[j].name, name) {
				f = j
			}
		}
		if f < 0 {
			continue
		}
		if used[f] {
			return nil, fmt.Errorf("column %s: field %s already set", name, fields[f].name)
		}
		used[f] = true
		if !plainType.MatchString(types[i].DatabaseTypeName()) {
			return nil, fmt.Errorf("column %s: type %s", name, types[i].DatabaseTypeName())
		}
		if c.conv, ok = buildConv(c.nt, fields[f].typ); !ok {
			return nil, fmt.Errorf("column %s (%s) into %s", name, types[i].DatabaseTypeName(), fields[f].typ)
		}
		c.offset = fields[f].offset
	}
	return plan, nil
}

// scanState holds one query's scan destinations, one per column.
type scanState struct {
	plan    *scanPlan
	dests   []any
	scratch []unsafe.Pointer
}

func (p *scanPlan) newState() *scanState {
	s := &scanState{plan: p, dests: make([]any, len(p.cols)), scratch: make([]unsafe.Pointer, len(p.cols))}
	for i, c := range p.cols {
		v := reflect.New(c.nt)
		s.dests[i], s.scratch[i] = v.Interface(), v.UnsafePointer()
	}
	return s
}

// scanRow decodes the current row into the struct at base.
func (s *scanState) scanRow(rows driver.Rows, base unsafe.Pointer) error {
	if err := rows.Scan(s.dests...); err != nil {
		return err
	}
	for i, c := range s.plan.cols {
		if c.conv != nil {
			if _, err := c.conv(s.scratch[i], unsafe.Add(base, c.offset)); err != nil {
				return err
			}
		}
	}
	return nil
}

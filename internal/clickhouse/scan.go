package clickhouse

import (
	"encoding"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/shopspring/decimal"
)

// Rows decoded from the driver's columnar results land in the same Go values
// encoding/json produces from ClickHouse's JSONEachRow output (with 64-bit
// integers unquoted): columns map to fields by json name, NULL and non-finite
// floats (JSON null) leave the zero value, Float32 values are read back from
// their shortest decimal text, invalid UTF-8 becomes U+FFFD per byte, and
// empty arrays and maps are empty, not nil. A column/field pairing outside
// that mapping makes the query decode from JSON instead.

// chType is a parsed ClickHouse column type.
type chType struct {
	kind      chKind
	bits      int     // integer width
	elem, key *chType // Nullable/Array element, Map value; Map key
	name      string  // the type as ClickHouse names it
}

type chKind int

const (
	tOther chKind = iota
	tString
	tInt
	tUint
	tFloat32
	tFloat64
	tBool
	tNullable
	tArray
	tMap
	tDecimal
	tDate
	tDateTime
)

func parseCHType(s string) *chType {
	s = strings.TrimSpace(s)
	ct := &chType{name: s}
	inner := func(prefix string) (string, bool) {
		if strings.HasPrefix(s, prefix+"(") && strings.HasSuffix(s, ")") {
			return s[len(prefix)+1 : len(s)-1], true
		}
		return "", false
	}
	if in, ok := inner("LowCardinality"); ok {
		return parseCHType(in)
	}
	if in, ok := inner("Nullable"); ok {
		ct.kind, ct.elem = tNullable, parseCHType(in)
		return ct
	}
	if in, ok := inner("Array"); ok {
		ct.kind, ct.elem = tArray, parseCHType(in)
		return ct
	}
	if in, ok := inner("Map"); ok {
		k, v, found := splitTopLevel(in)
		if !found {
			return ct
		}
		ct.kind, ct.key, ct.elem = tMap, parseCHType(k), parseCHType(v)
		return ct
	}
	switch s {
	case "String":
		ct.kind = tString
	case "Int8", "Int16", "Int32", "Int64":
		ct.kind = tInt
		ct.bits, _ = strconv.Atoi(s[3:])
	case "UInt8", "UInt16", "UInt32", "UInt64":
		ct.kind = tUint
		ct.bits, _ = strconv.Atoi(s[4:])
	case "Float32":
		ct.kind = tFloat32
	case "Float64":
		ct.kind = tFloat64
	case "Bool":
		ct.kind = tBool
	case "Date", "Date32":
		ct.kind = tDate
	case "DateTime":
		ct.kind = tDateTime
	default:
		switch {
		case strings.HasPrefix(s, "FixedString("), strings.HasPrefix(s, "Enum8("), strings.HasPrefix(s, "Enum16("):
			ct.kind = tString
		case strings.HasPrefix(s, "Decimal"):
			ct.kind = tDecimal
		case strings.HasPrefix(s, "DateTime(") && !strings.HasPrefix(s, "DateTime64"):
			ct.kind = tDateTime
		}
	}
	return ct
}

// splitTopLevel splits "K, V" at the first comma outside parentheses and
// quotes.
func splitTopLevel(s string) (string, string, bool) {
	depth, quoted := 0, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quoted:
			if c == '\\' {
				i++
			} else if c == '\'' {
				quoted = false
			}
		case c == '\'':
			quoted = true
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

var (
	timeType   = reflect.TypeFor[time.Time]()
	jsonUnmT   = reflect.TypeFor[json.Unmarshaler]()
	textUnmT   = reflect.TypeFor[encoding.TextUnmarshaler]()
	stringType = reflect.TypeFor[string]()
)

// nativeType is the Go type the driver scans a column of type ct into, or nil
// for types not handled here.
func nativeType(ct *chType) reflect.Type {
	switch ct.kind {
	case tString:
		return stringType
	case tInt:
		return [...]reflect.Type{8: reflect.TypeFor[int8](), 16: reflect.TypeFor[int16](), 32: reflect.TypeFor[int32](), 64: reflect.TypeFor[int64]()}[ct.bits]
	case tUint:
		return [...]reflect.Type{8: reflect.TypeFor[uint8](), 16: reflect.TypeFor[uint16](), 32: reflect.TypeFor[uint32](), 64: reflect.TypeFor[uint64]()}[ct.bits]
	case tFloat32:
		return reflect.TypeFor[float32]()
	case tFloat64:
		return reflect.TypeFor[float64]()
	case tBool:
		return reflect.TypeFor[bool]()
	case tDate, tDateTime:
		return timeType
	case tDecimal:
		return reflect.TypeFor[decimal.Decimal]()
	case tNullable:
		if e := nativeType(ct.elem); e != nil && ct.elem.kind != tNullable && ct.elem.kind != tArray && ct.elem.kind != tMap {
			return reflect.PointerTo(e)
		}
	case tArray:
		if e := nativeType(ct.elem); e != nil {
			return reflect.SliceOf(e)
		}
	case tMap:
		k, v := nativeType(ct.key), nativeType(ct.elem)
		if k != nil && v != nil && ct.key.kind == tString {
			return reflect.MapOf(k, v)
		}
	}
	return nil
}

// plainTarget reports whether encoding/json decodes into t by its kind alone
// (no Unmarshaler/TextUnmarshaler, no interface or struct).
func plainTarget(t reflect.Type) bool {
	if t.Implements(jsonUnmT) || reflect.PointerTo(t).Implements(jsonUnmT) ||
		t.Implements(textUnmT) || reflect.PointerTo(t).Implements(textUnmT) {
		return false
	}
	switch t.Kind() {
	case reflect.Interface, reflect.Struct, reflect.Array, reflect.Func, reflect.Chan,
		reflect.Complex64, reflect.Complex128, reflect.UnsafePointer:
		return false
	}
	return true
}

// convFn writes the column value at src (the driver's native type) into the
// field at dst. null reports a value JSON shows as null, which leaves the
// target untouched.
type convFn func(src, dst unsafe.Pointer) (null bool, err error)

func intOverflow(v int64, bits int) bool {
	if bits == 64 {
		return false
	}
	lim := int64(1) << (bits - 1)
	return v < -lim || v >= lim
}

func uintOverflow(v uint64, bits int) bool {
	return bits < 64 && v >= uint64(1)<<bits
}

func setInt(dst unsafe.Pointer, k reflect.Kind, v int64) {
	switch k {
	case reflect.Int:
		*(*int)(dst) = int(v)
	case reflect.Int8:
		*(*int8)(dst) = int8(v)
	case reflect.Int16:
		*(*int16)(dst) = int16(v)
	case reflect.Int32:
		*(*int32)(dst) = int32(v)
	case reflect.Int64:
		*(*int64)(dst) = v
	}
}

func setUint(dst unsafe.Pointer, k reflect.Kind, v uint64) {
	switch k {
	case reflect.Uint:
		*(*uint)(dst) = uint(v)
	case reflect.Uint8:
		*(*uint8)(dst) = uint8(v)
	case reflect.Uint16:
		*(*uint16)(dst) = uint16(v)
	case reflect.Uint32:
		*(*uint32)(dst) = uint32(v)
	case reflect.Uint64:
		*(*uint64)(dst) = v
	case reflect.Uintptr:
		*(*uintptr)(dst) = uintptr(v)
	}
}

func kindBits(k reflect.Kind) int {
	switch k {
	case reflect.Int8, reflect.Uint8:
		return 8
	case reflect.Int16, reflect.Uint16:
		return 16
	case reflect.Int32, reflect.Uint32, reflect.Float32:
		return 32
	}
	return 64
}

func isIntKind(k reflect.Kind) bool {
	return k >= reflect.Int && k <= reflect.Int64
}

func isUintKind(k reflect.Kind) bool {
	return k >= reflect.Uint && k <= reflect.Uintptr
}

func numberError(v string, t reflect.Type) error {
	return &json.UnmarshalTypeError{Value: "number " + v, Type: t}
}

// readInt reads a native signed integer of the given width.
func readInt(src unsafe.Pointer, bits int) int64 {
	switch bits {
	case 8:
		return int64(*(*int8)(src))
	case 16:
		return int64(*(*int16)(src))
	case 32:
		return int64(*(*int32)(src))
	}
	return *(*int64)(src)
}

func readUint(src unsafe.Pointer, bits int) uint64 {
	switch bits {
	case 8:
		return uint64(*(*uint8)(src))
	case 16:
		return uint64(*(*uint16)(src))
	case 32:
		return uint64(*(*uint32)(src))
	}
	return *(*uint64)(src)
}

// buildConv returns the conversion of a column of type ct (scanned as nt)
// into a field of type ft, or false when it is not exactly reproducible.
func buildConv(ct *chType, nt, ft reflect.Type) (convFn, bool) {
	if !plainTarget(ft) {
		return nil, false
	}
	if ft.Kind() == reflect.Pointer {
		inner, ok := buildConv(ct, nt, ft.Elem())
		if !ok {
			return nil, false
		}
		et := ft.Elem()
		return func(src, dst unsafe.Pointer) (bool, error) {
			v := reflect.New(et).UnsafePointer()
			null, err := inner(src, v)
			if err != nil || null {
				return false, err
			}
			*(*unsafe.Pointer)(dst) = v
			return false, nil
		}, true
	}
	fk := ft.Kind()
	switch ct.kind {
	case tNullable:
		inner, ok := buildConv(ct.elem, nt.Elem(), ft)
		if !ok {
			return nil, false
		}
		return func(src, dst unsafe.Pointer) (bool, error) {
			p := *(*unsafe.Pointer)(src)
			if p == nil {
				return true, nil
			}
			return inner(p, dst)
		}, true
	case tString:
		if fk != reflect.String {
			return nil, false
		}
		return func(src, dst unsafe.Pointer) (bool, error) {
			*(*string)(dst) = jsonString(*(*string)(src))
			return false, nil
		}, true
	case tInt:
		bits := ct.bits
		switch {
		case isIntKind(fk):
			fb := kindBits(fk)
			return func(src, dst unsafe.Pointer) (bool, error) {
				v := readInt(src, bits)
				if intOverflow(v, fb) {
					return false, numberError(strconv.FormatInt(v, 10), ft)
				}
				setInt(dst, fk, v)
				return false, nil
			}, true
		case isUintKind(fk):
			fb := kindBits(fk)
			return func(src, dst unsafe.Pointer) (bool, error) {
				v := readInt(src, bits)
				if v < 0 || uintOverflow(uint64(v), fb) {
					return false, numberError(strconv.FormatInt(v, 10), ft)
				}
				setUint(dst, fk, uint64(v))
				return false, nil
			}, true
		case fk == reflect.Float64:
			return func(src, dst unsafe.Pointer) (bool, error) {
				*(*float64)(dst) = float64(readInt(src, bits))
				return false, nil
			}, true
		}
	case tUint:
		bits := ct.bits
		switch {
		case isIntKind(fk):
			fb := kindBits(fk)
			return func(src, dst unsafe.Pointer) (bool, error) {
				v := readUint(src, bits)
				if v > math.MaxInt64 || intOverflow(int64(v), fb) {
					return false, numberError(strconv.FormatUint(v, 10), ft)
				}
				setInt(dst, fk, int64(v))
				return false, nil
			}, true
		case isUintKind(fk):
			fb := kindBits(fk)
			return func(src, dst unsafe.Pointer) (bool, error) {
				v := readUint(src, bits)
				if uintOverflow(v, fb) {
					return false, numberError(strconv.FormatUint(v, 10), ft)
				}
				setUint(dst, fk, v)
				return false, nil
			}, true
		case fk == reflect.Float64:
			return func(src, dst unsafe.Pointer) (bool, error) {
				*(*float64)(dst) = float64(readUint(src, bits))
				return false, nil
			}, true
		}
	case tFloat32:
		switch fk {
		case reflect.Float64:
			return func(src, dst unsafe.Pointer) (bool, error) {
				f := float64(*(*float32)(src))
				if math.IsNaN(f) || math.IsInf(f, 0) {
					return true, nil
				}
				// ClickHouse writes the shortest text that reads back as the
				// Float32; JSON decoding parses that text as a float64.
				*(*float64)(dst), _ = strconv.ParseFloat(strconv.FormatFloat(f, 'g', -1, 32), 64)
				return false, nil
			}, true
		case reflect.Float32:
			return func(src, dst unsafe.Pointer) (bool, error) {
				f := *(*float32)(src)
				if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
					return true, nil
				}
				*(*float32)(dst) = f
				return false, nil
			}, true
		}
	case tFloat64:
		if fk == reflect.Float64 {
			return func(src, dst unsafe.Pointer) (bool, error) {
				f := *(*float64)(src)
				if math.IsNaN(f) || math.IsInf(f, 0) {
					return true, nil
				}
				*(*float64)(dst) = f
				return false, nil
			}, true
		}
	case tBool:
		if fk == reflect.Bool {
			return func(src, dst unsafe.Pointer) (bool, error) {
				*(*bool)(dst) = *(*bool)(src)
				return false, nil
			}, true
		}
	case tDecimal:
		if fk == reflect.Float64 {
			return func(src, dst unsafe.Pointer) (bool, error) {
				s := reflect.NewAt(nt, src).Elem().Interface().(fmt.Stringer).String()
				f, err := strconv.ParseFloat(s, 64)
				if err != nil {
					return false, numberError(s, ft)
				}
				*(*float64)(dst) = f
				return false, nil
			}, true
		}
	case tDate, tDateTime:
		if fk != reflect.String || nt != timeType {
			return nil, false
		}
		layout := "2006-01-02"
		if ct.kind == tDateTime {
			layout = "2006-01-02 15:04:05"
		}
		return func(src, dst unsafe.Pointer) (bool, error) {
			*(*string)(dst) = (*time.Time)(src).Format(layout)
			return false, nil
		}, true
	case tArray:
		if fk != reflect.Slice {
			return nil, false
		}
		elem, ok := buildConv(ct.elem, nt.Elem(), ft.Elem())
		if !ok {
			return nil, false
		}
		return func(src, dst unsafe.Pointer) (bool, error) {
			sv := reflect.NewAt(nt, src).Elem()
			n := sv.Len()
			out := reflect.MakeSlice(ft, n, n)
			for i := 0; i < n; i++ {
				if _, err := elem(sv.Index(i).Addr().UnsafePointer(), out.Index(i).Addr().UnsafePointer()); err != nil {
					return false, err
				}
			}
			reflect.NewAt(ft, dst).Elem().Set(out)
			return false, nil
		}, true
	case tMap:
		kt := ft.Key()
		if fk != reflect.Map || kt.Kind() != reflect.String || !plainTarget(kt) {
			return nil, false
		}
		val, ok := buildConv(ct.elem, nt.Elem(), ft.Elem())
		if !ok {
			return nil, false
		}
		vt := ft.Elem()
		return func(src, dst unsafe.Pointer) (bool, error) {
			sv := reflect.NewAt(nt, src).Elem()
			out := reflect.MakeMapWithSize(ft, sv.Len())
			it := sv.MapRange()
			for it.Next() {
				k := reflect.ValueOf(jsonString(it.Key().String())).Convert(kt)
				v := reflect.New(vt)
				ev := it.Value()
				tmp := reflect.New(nt.Elem())
				tmp.Elem().Set(ev)
				if _, err := val(tmp.UnsafePointer(), v.UnsafePointer()); err != nil {
					return false, err
				}
				out.SetMapIndex(k, v.Elem())
			}
			reflect.NewAt(ft, dst).Elem().Set(out)
			return false, nil
		}, true
	}
	return nil, false
}

// direct reports whether the driver can scan a column of type ct straight
// into a field of type ft (its own native type), and the fix-up that makes
// the value match JSON decoding (nil when none is needed).
func direct(ct *chType, nt, ft reflect.Type) (fix func(unsafe.Pointer), ok bool) {
	if ft != nt {
		return nil, false
	}
	switch ct.kind {
	case tInt, tUint, tBool:
		return nil, true
	case tString:
		return func(p unsafe.Pointer) {
			s := (*string)(p)
			*s = jsonString(*s)
		}, true
	case tFloat32:
		return func(p unsafe.Pointer) {
			f := (*float32)(p)
			if math.IsNaN(float64(*f)) || math.IsInf(float64(*f), 0) {
				*f = 0
			}
		}, true
	case tFloat64:
		return func(p unsafe.Pointer) {
			f := (*float64)(p)
			if math.IsNaN(*f) || math.IsInf(*f, 0) {
				*f = 0
			}
		}, true
	case tNullable:
		inner, ok := direct(ct.elem, nt.Elem(), ft.Elem())
		if !ok {
			return nil, false
		}
		if inner == nil {
			return nil, true
		}
		float := ct.elem.kind == tFloat32 || ct.elem.kind == tFloat64
		return func(p unsafe.Pointer) {
			v := *(*unsafe.Pointer)(p)
			if v == nil {
				return
			}
			if float && nonFinite(ct.elem.kind, v) {
				*(*unsafe.Pointer)(p) = nil
				return
			}
			inner(v)
		}, true
	case tArray:
		inner, ok := direct(ct.elem, nt.Elem(), ft.Elem())
		if !ok {
			return nil, false
		}
		if ct.elem.kind == tString {
			return func(p unsafe.Pointer) {
				for i, s := range *(*[]string)(p) {
					(*(*[]string)(p))[i] = jsonString(s)
				}
			}, true
		}
		if inner == nil {
			return nil, true
		}
		size := ft.Elem().Size()
		return func(p unsafe.Pointer) {
			sv := reflect.NewAt(ft, p).Elem()
			if sv.Len() == 0 {
				return
			}
			base := sv.Index(0).Addr().UnsafePointer()
			for i := 0; i < sv.Len(); i++ {
				inner(unsafe.Add(base, uintptr(i)*size))
			}
		}, true
	}
	return nil, false
}

func nonFinite(k chKind, p unsafe.Pointer) bool {
	var f float64
	if k == tFloat32 {
		f = float64(*(*float32)(p))
	} else {
		f = *(*float64)(p)
	}
	return math.IsNaN(f) || math.IsInf(f, 0)
}

// jsonString returns s as encoding/json decodes it from ClickHouse's JSON
// output, which writes string bytes unchanged: each byte of an invalid UTF-8
// sequence becomes U+FFFD.
func jsonString(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
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

// destFn returns the Scan destination for a field of type t at p.
func destFn(t reflect.Type) func(unsafe.Pointer) any {
	switch t {
	case reflect.TypeFor[string]():
		return func(p unsafe.Pointer) any { return (*string)(p) }
	case reflect.TypeFor[int64]():
		return func(p unsafe.Pointer) any { return (*int64)(p) }
	case reflect.TypeFor[uint64]():
		return func(p unsafe.Pointer) any { return (*uint64)(p) }
	case reflect.TypeFor[int32]():
		return func(p unsafe.Pointer) any { return (*int32)(p) }
	case reflect.TypeFor[uint32]():
		return func(p unsafe.Pointer) any { return (*uint32)(p) }
	case reflect.TypeFor[uint8]():
		return func(p unsafe.Pointer) any { return (*uint8)(p) }
	case reflect.TypeFor[float64]():
		return func(p unsafe.Pointer) any { return (*float64)(p) }
	case reflect.TypeFor[bool]():
		return func(p unsafe.Pointer) any { return (*bool)(p) }
	case reflect.TypeFor[*string]():
		return func(p unsafe.Pointer) any { return (**string)(p) }
	case reflect.TypeFor[*int64]():
		return func(p unsafe.Pointer) any { return (**int64)(p) }
	case reflect.TypeFor[*float64]():
		return func(p unsafe.Pointer) any { return (**float64)(p) }
	case reflect.TypeFor[[]string]():
		return func(p unsafe.Pointer) any { return (*[]string)(p) }
	}
	return func(p unsafe.Pointer) any { return reflect.NewAt(t, p).Interface() }
}

// colPlan is how one result column reaches its field.
type colPlan struct {
	offset uintptr
	nt     reflect.Type             // the driver's native type
	dest   func(unsafe.Pointer) any // direct scan into the field
	fix    func(unsafe.Pointer)     // after a direct scan
	conv   convFn                   // from a scratch native value
}

// scanPlan decodes the rows of one result shape into struct type T.
type scanPlan struct {
	cols []colPlan
}

type planKey struct {
	t   reflect.Type
	sig string
}

var planCache sync.Map // planKey -> *scanPlan, or error when unsupported

// planFor returns the decoding plan of rows with the given columns into
// struct type t, or an error naming why the JSON path must decode them.
func planFor(t reflect.Type, names []string, types []driver.ColumnType) (*scanPlan, error) {
	var sig strings.Builder
	for i, n := range names {
		sig.WriteString(n)
		sig.WriteByte(0)
		sig.WriteString(types[i].DatabaseTypeName())
		sig.WriteByte(0)
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
	if len(names) != len(types) {
		return nil, fmt.Errorf("%d columns, %d types", len(names), len(types))
	}
	fields := jsonFields(t)
	plan := &scanPlan{cols: make([]colPlan, len(names))}
	used := map[*jsonField]bool{}
	for i, name := range names {
		f, ok := matchField(fields, name)
		if !ok {
			return nil, fmt.Errorf("column %s: non-ASCII name", name)
		}
		c := &plan.cols[i]
		if f == nil {
			// Scanned into its own type and dropped.
			c.nt = types[i].ScanType()
			continue
		}
		ct := parseCHType(types[i].DatabaseTypeName())
		nt := nativeType(ct)
		if nt == nil || nt != types[i].ScanType() {
			return nil, fmt.Errorf("column %s: type %s not decoded natively", name, ct.name)
		}
		c.nt = nt
		if used[f] {
			return nil, fmt.Errorf("column %s: field %s already set", name, f.name)
		}
		used[f] = true
		if f.quoted || f.viaPtr {
			return nil, fmt.Errorf("column %s: field %s is quoted or behind a pointer", name, f.name)
		}
		c.offset = f.offset
		if fix, ok := direct(ct, nt, f.typ); ok {
			c.dest, c.fix = destFn(f.typ), fix
			continue
		}
		conv, ok := buildConv(ct, nt, f.typ)
		if !ok {
			return nil, fmt.Errorf("column %s (%s) into %s", name, ct.name, f.typ)
		}
		c.conv = conv
	}
	return plan, nil
}

// scanState holds one query's scan destinations.
type scanState struct {
	plan    *scanPlan
	dests   []any
	scratch []unsafe.Pointer
}

func (p *scanPlan) newState() *scanState {
	s := &scanState{plan: p, dests: make([]any, len(p.cols)), scratch: make([]unsafe.Pointer, len(p.cols))}
	for i, c := range p.cols {
		if c.dest == nil {
			v := reflect.New(c.nt)
			s.dests[i] = v.Interface()
			s.scratch[i] = v.UnsafePointer()
		}
	}
	return s
}

// scanRow decodes the current row into the struct at base.
func (s *scanState) scanRow(rows driver.Rows, base unsafe.Pointer) error {
	cols := s.plan.cols
	for i := range cols {
		if d := cols[i].dest; d != nil {
			s.dests[i] = d(unsafe.Add(base, cols[i].offset))
		}
	}
	if err := rows.Scan(s.dests...); err != nil {
		return err
	}
	for i := range cols {
		c := &cols[i]
		switch {
		case c.conv != nil:
			if _, err := c.conv(s.scratch[i], unsafe.Add(base, c.offset)); err != nil {
				return err
			}
		case c.fix != nil:
			c.fix(unsafe.Add(base, c.offset))
		}
	}
	return nil
}

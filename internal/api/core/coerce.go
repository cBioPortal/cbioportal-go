package core

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// DecodeJSON decodes raw into v like json.Unmarshal, except that scalars of
// the wrong JSON type are read as Jackson's default coercion reads them
// (coerceScalars) instead of being left out.
func DecodeJSON(raw []byte, v any) error {
	target := reflect.ValueOf(v)
	var before reflect.Value
	if target.Kind() == reflect.Pointer && !target.IsNil() {
		before = reflect.New(target.Elem().Type()).Elem()
		before.Set(target.Elem())
	}
	err := json.NewDecoder(bytes.NewReader(raw)).Decode(v)
	var typ *json.UnmarshalTypeError
	if err == nil || !errors.As(err, &typ) || malformedJSON(err) || !before.IsValid() {
		return err
	}
	// Read the body again, coerced, from the value as it was before the
	// first read, so no half-decoded field remains.
	coerced, ok := coerceScalars(raw, target.Type())
	if !ok {
		return err
	}
	target.Elem().Set(before)
	return json.NewDecoder(bytes.NewReader(coerced)).Decode(v)
}

// coerceScalars rewrites a JSON body the way Jackson's default scalar
// coercion reads it into the Go type t: a string holding a number becomes
// that number where t wants a number ("7157" for an entrez gene id), a
// whole decimal (100.0) becomes an integer where t wants one, an empty string
// becomes null where t wants a number, a "true"/"false" string becomes a
// boolean where t wants one, and a number or boolean becomes a string where
// t wants a string. Everything else is left as it is. ok is false when the
// body is not JSON.
func coerceScalars(raw []byte, t reflect.Type) (out []byte, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	out, err := json.Marshal(coerceValue(v, t))
	return out, err == nil
}

var (
	jsonUnmarshalerType = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
	textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
)

// coerceValue is coerceScalars on a decoded value (UseNumber) and the type
// it is read into.
func coerceValue(v any, t reflect.Type) any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	// A type that reads itself decides what it accepts.
	if reflect.PointerTo(t).Implements(jsonUnmarshalerType) || reflect.PointerTo(t).Implements(textUnmarshalerType) {
		return v
	}
	switch x := v.(type) {
	case string:
		switch t.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			s := strings.TrimSpace(x)
			if s == "" {
				return nil
			}
			if _, err := strconv.ParseFloat(s, 64); err == nil {
				return json.Number(s)
			}
		case reflect.Bool:
			if b, err := strconv.ParseBool(x); err == nil && (x == "true" || x == "false") {
				return b
			}
		}
	case json.Number:
		switch t.Kind() {
		case reflect.String:
			return x.String()
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			// A whole number written as a decimal (100.0, 1e2) is that
			// integer. A fraction stays, so the read fails (notWholeNumber).
			if _, err := x.Int64(); err != nil {
				if f, err := x.Float64(); err == nil && f == math.Trunc(f) && math.Abs(f) < math.MaxInt64 {
					return json.Number(strconv.FormatInt(int64(f), 10))
				}
			}
		}
	case bool:
		if t.Kind() == reflect.String {
			return strconv.FormatBool(x)
		}
	case []any:
		if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			for i := range x {
				x[i] = coerceValue(x[i], t.Elem())
			}
		}
	case map[string]any:
		switch t.Kind() {
		case reflect.Map:
			for k, e := range x {
				x[k] = coerceValue(e, t.Elem())
			}
		case reflect.Struct:
			for k, e := range x {
				if ft, ok := jsonField(t, k); ok {
					x[k] = coerceValue(e, ft)
				}
			}
		}
	}
	return v
}

// jsonField finds the type of the struct field encoding/json decodes the
// object key into: by its json name, else case-insensitively, through
// embedded structs.
func jsonField(t reflect.Type, key string) (reflect.Type, bool) {
	var fold reflect.Type
	var walk func(t reflect.Type) (reflect.Type, bool)
	walk = func(t reflect.Type) (reflect.Type, bool) {
		for i := range t.NumField() {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			if f.Anonymous && name == "" {
				et := f.Type
				for et.Kind() == reflect.Pointer {
					et = et.Elem()
				}
				if et.Kind() == reflect.Struct {
					if ft, ok := walk(et); ok {
						return ft, true
					}
					continue
				}
			}
			if !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			if name == key {
				return f.Type, true
			}
			if fold == nil && strings.EqualFold(name, key) {
				fold = f.Type
			}
		}
		return nil, false
	}
	if ft, ok := walk(t); ok {
		return ft, true
	}
	return fold, fold != nil
}

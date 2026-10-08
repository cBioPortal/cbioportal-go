package core

import (
	"bytes"
	"encoding/json"
	"math"

	"github.com/cbioportal/cbioportal-go/internal/databin"
)

// JSONObject writes a JSON object member by member, for models whose Java
// field order depends on the endpoint that writes them.
type JSONObject struct {
	buf bytes.Buffer
	err error
}

// Add writes the member key: v.
func (o *JSONObject) Add(key string, v any) {
	if o.err != nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		o.err = err
		return
	}
	if o.buf.Len() == 0 {
		o.buf.WriteByte('{')
	} else {
		o.buf.WriteByte(',')
	}
	o.buf.Write(AppendJSONString(nil, []byte(key)))
	o.buf.WriteByte(':')
	o.buf.Write(b)
}

// AddIf writes the member when ok, as Jackson's NON_NULL leaves out a null.
func (o *JSONObject) AddIf(ok bool, key string, v any) {
	if ok {
		o.Add(key, v)
	}
}

// JavaFloat is a java.lang.Float field, which Jackson writes as
// Float.toString does ("1.0", "0.355").
type JavaFloat float64

// MarshalJSON writes f as Float.toString does.
func (f JavaFloat) MarshalJSON() ([]byte, error) {
	v := float32(f)
	if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
		return json.Marshal(float64(f))
	}
	return []byte(databin.JavaFloatToString(v)), nil
}

// JavaStringMap is a Java HashMap<String, String>: it is written in the
// iteration order of a HashMap its entries were put into in insertion order.
type JavaStringMap struct {
	Keys []string
	Vals map[string]string
}

// Set puts k=v, keeping k's first insertion position.
func (m *JavaStringMap) Set(k, v string) {
	if m.Vals == nil {
		m.Vals = map[string]string{}
	}
	if _, ok := m.Vals[k]; !ok {
		m.Keys = append(m.Keys, k)
	}
	m.Vals[k] = v
}

// MarshalJSON writes the entries in Java HashMap iteration order.
func (m JavaStringMap) MarshalJSON() ([]byte, error) {
	var o JSONObject
	for _, k := range JavaHashMapOrder(m.Keys) {
		o.Add(k, m.Vals[k])
	}
	return o.Bytes()
}

// Bytes returns the object's JSON, or the first error adding to it.
func (o *JSONObject) Bytes() ([]byte, error) {
	if o.err != nil {
		return nil, o.err
	}
	if o.buf.Len() == 0 {
		return []byte("{}"), nil
	}
	o.buf.WriteByte('}')
	return o.buf.Bytes(), nil
}

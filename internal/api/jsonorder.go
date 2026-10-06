package api

import (
	"bytes"
	"encoding/json"
	"math"

	"github.com/cbioportal/cbioportal-go/internal/databin"
)

// jsonObject writes a JSON object member by member, for models whose Java
// field order depends on the endpoint that writes them.
type jsonObject struct {
	buf bytes.Buffer
	err error
}

// add writes the member key: v.
func (o *jsonObject) add(key string, v any) {
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
	o.buf.Write(appendJSONString(nil, []byte(key)))
	o.buf.WriteByte(':')
	o.buf.Write(b)
}

// addIf writes the member when ok, as Jackson's NON_NULL leaves out a null.
func (o *jsonObject) addIf(ok bool, key string, v any) {
	if ok {
		o.add(key, v)
	}
}

// javaFloat is a java.lang.Float field, which Jackson writes as
// Float.toString does ("1.0", "0.355").
type javaFloat float64

func (f javaFloat) MarshalJSON() ([]byte, error) {
	v := float32(f)
	if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
		return json.Marshal(float64(f))
	}
	return []byte(databin.JavaFloatToString(v)), nil
}

// javaStringMap is a Java HashMap<String, String>: it is written in the
// iteration order of a HashMap its entries were put into in insertion order.
type javaStringMap struct {
	keys []string
	vals map[string]string
}

func (m *javaStringMap) set(k, v string) {
	if m.vals == nil {
		m.vals = map[string]string{}
	}
	if _, ok := m.vals[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.vals[k] = v
}

func (m javaStringMap) MarshalJSON() ([]byte, error) {
	var o jsonObject
	for _, k := range javaHashMapOrder(m.keys) {
		o.add(k, m.vals[k])
	}
	return o.bytes()
}

func (o *jsonObject) bytes() ([]byte, error) {
	if o.err != nil {
		return nil, o.err
	}
	if o.buf.Len() == 0 {
		return []byte("{}"), nil
	}
	o.buf.WriteByte('}')
	return o.buf.Bytes(), nil
}

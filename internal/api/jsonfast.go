package api

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// appendJSONString appends s as encoding/json encodes a string (with its
// default HTML escaping). Plain printable ASCII is copied directly.
func appendJSONString(dst, s []byte) []byte {
	for _, c := range s {
		if c < 0x20 || c >= 0x7f || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
			b, _ := json.Marshal(string(s))
			return append(dst, b...)
		}
	}
	dst = append(dst, '"')
	dst = append(dst, s...)
	return append(dst, '"')
}

// appendJSONFloat appends f as encoding/json encodes a float64; like it, it
// rejects NaN and infinities.
func appendJSONFloat(dst []byte, f float64) ([]byte, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return dst, fmt.Errorf("json: unsupported value: %s", strconv.FormatFloat(f, 'g', -1, 64))
	}
	format := byte('f')
	if abs := math.Abs(f); abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	dst = strconv.AppendFloat(dst, f, format, -1, 64)
	if format == 'e' {
		// clean up e-09 to e-9
		n := len(dst)
		if n >= 4 && dst[n-4] == 'e' && dst[n-3] == '-' && dst[n-2] == '0' {
			dst[n-2] = dst[n-1]
			dst = dst[:n-1]
		}
	}
	return dst, nil
}

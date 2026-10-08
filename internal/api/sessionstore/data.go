package sessionstore

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Checksum is the MD5 of data's JSON, as the session service stores it.
func Checksum(data map[string]any) string {
	raw, _ := json.Marshal(data)
	sum := md5.Sum(raw)
	return hex.EncodeToString(sum[:])
}

// DecodeJSON decodes keeping numbers as json.Number so ids and timestamps
// round-trip unchanged.
func DecodeJSON(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(v)
}

func deepCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	raw, _ := json.Marshal(m)
	var out map[string]any
	DecodeJSON(raw, &out)
	return out
}

// StringSet reads a JSON array of strings, de-duplicated in input order.
func StringSet(v any) []string {
	arr, _ := v.([]any)
	out := []string{}
	seen := map[string]bool{}
	for _, x := range arr {
		if s, ok := x.(string); ok && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ContainsUser matches a users entry case-insensitively.
func ContainsUser(users []string, name string) bool {
	for _, u := range users {
		if strings.EqualFold(u, name) {
			return true
		}
	}
	return false
}

// SameSet reports whether a and b hold the same strings, in any order.
func SameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]bool{}
	for _, x := range a {
		set[x] = true
	}
	for _, x := range b {
		if !set[x] {
			return false
		}
	}
	return true
}

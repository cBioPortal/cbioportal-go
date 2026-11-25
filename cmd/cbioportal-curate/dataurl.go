package main

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
)

// decodeDataURL returns the bytes of a data: URL, base64 or not.
func decodeDataURL(s string) ([]byte, error) {
	meta, payload, ok := strings.Cut(s, ",")
	if !ok || !strings.HasPrefix(meta, "data:") {
		return nil, errors.New("not a data: URL")
	}
	if strings.HasSuffix(meta, ";base64") {
		return base64.StdEncoding.DecodeString(payload)
	}
	text, err := url.PathUnescape(payload)
	return []byte(text), err
}

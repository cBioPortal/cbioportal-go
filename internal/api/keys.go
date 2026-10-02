package api

import (
	"encoding/base64"
	"strings"
)

// uniqueKey mirrors cBioPortal's Encoder.calculateBase64: URL-safe base64
// without padding of "<studyId>:<id>".
func uniqueKey(studyID, id string) string {
	return base64.RawStdEncoding.EncodeToString([]byte(studyID + ":" + id))
}

// decodeUniqueKey reverses uniqueKey, returning (studyId, id). ok is false if
// the key is not valid base64 or lacks the ':' separator.
func decodeUniqueKey(key string) (studyID, id string, ok bool) {
	b, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(key, "="))
	if err != nil {
		return "", "", false
	}
	s := string(b)
	i := strings.IndexByte(s, ':')
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

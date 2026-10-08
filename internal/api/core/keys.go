package core

import (
	"encoding/base64"
	"strings"
)

// UniqueKey mirrors cBioPortal's Encoder.calculateBase64(id, studyId) as used
// by UniqueKeyInterceptor: standard base64 without padding of "<id>:<studyId>".
func UniqueKey(studyID, id string) string {
	return base64.RawStdEncoding.EncodeToString([]byte(id + ":" + studyID))
}

// DecodeUniqueKey reverses uniqueKey, returning (studyId, id). ok is false if
// the key is not valid base64 or lacks the ':' separator. The study id is the
// part after the last ':' (study ids never contain one; entity ids may).
func DecodeUniqueKey(key string) (studyID, id string, ok bool) {
	b, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(key, "="))
	if err != nil {
		return "", "", false
	}
	s := string(b)
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", "", false
	}
	return s[i+1:], s[:i], true
}

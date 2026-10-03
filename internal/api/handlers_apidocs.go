package api

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"net/http"
	"sync"
)

// legacyAPIJSON is the Swagger 2.0 description of the legacy REST API that
// API clients are generated from (e.g. bravado's
// SwaggerClient.from_url(".../api/v2/api-docs")). It is the Java portal's own
// hand-maintained file, cbioportal src/main/resources/legacy-api.json
// (98600e1), served as its LegacyApiController serves it.
//
//go:embed legacy-api.json
var legacyAPIJSON []byte

// legacyDocs is legacyAPIJSON compacted, split around the value of its
// top-level "host" key.
var legacyDocs struct {
	once          sync.Once
	before, after []byte
	err           error
}

// legacyAPIDocs serves the legacy API description with its host set to the
// host the client asked for.
func (s *State) legacyAPIDocs(w http.ResponseWriter, r *http.Request) {
	legacyDocs.once.Do(func() {
		legacyDocs.before, legacyDocs.after, legacyDocs.err = splitTopLevelValue(legacyAPIJSON, "host")
	})
	if legacyDocs.err != nil {
		writeError(w, http.StatusInternalServerError, "legacy API description unavailable")
		return
	}
	host, _ := json.Marshal(r.Host)
	w.Header().Set("Content-Type", "application/json")
	w.Write(legacyDocs.before)
	w.Write(host)
	w.Write(legacyDocs.after)
}

// splitTopLevelValue compacts the JSON object doc and returns the bytes
// before and after the value of its top-level key (the value itself left
// out). Without that key, the key is appended as the object's last member.
func splitTopLevelValue(doc []byte, key string) (before, after []byte, err error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, doc); err != nil {
		return nil, nil, err
	}
	b := compact.Bytes()
	dec := json.NewDecoder(bytes.NewReader(b))
	if _, err := dec.Token(); err != nil { // {
		return nil, nil, err
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		start := dec.InputOffset() + 1 // past the ':'
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, err
		}
		if t == key {
			end := dec.InputOffset()
			return b[:start:start], b[end:], nil
		}
	}
	// No such key: add it as the last member.
	k, _ := json.Marshal(key)
	before = append(append(append([]byte{}, b[:len(b)-1]...), ','), k...)
	return append(before, ':'), []byte("}"), nil
}

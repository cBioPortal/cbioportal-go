package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// remoteStore keeps sessions in cBioPortal's external session service
// (github.com/cBioPortal/session-service), as the Java portal's
// SessionServiceRequestHandler does: base is the portal instance's URL,
// e.g. http://host:5000/api/sessions/my_portal/, below which the service
// answers {type} (POST: create), {type}/{id} (GET, POST with a chosen id,
// PUT, DELETE) and {type}/query/fetch (POST a MongoDB filter). The service
// generates ids and de-duplicates identical data, and its sessions carry
// its own source (the portal instance) and no checksum.
type remoteStore struct {
	base     string // without the trailing slash
	user     string
	password string
	client   *http.Client
}

// sessionServiceTimeout bounds one request to the session service.
const sessionServiceTimeout = 30 * time.Second

func newRemoteStore(base, user, password string) (*remoteStore, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("session.service.url %q is not an http(s) URL", redactURL(base))
	}
	return &remoteStore{
		base:     strings.TrimRight(base, "/"),
		user:     user,
		password: password,
		client:   &http.Client{Timeout: sessionServiceTimeout},
	}, nil
}

// redactURL hides a password embedded in a URL, for logs and errors.
func redactURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return "<unparsable URL>"
	}
	return u.Redacted()
}

// sessionServiceError is an unexpected answer of the session service.
type sessionServiceError struct {
	method, path string
	status       int
	err          error
}

func (e *sessionServiceError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("session service: %s %s: %v", e.method, e.path, e.err)
	}
	return fmt.Sprintf("session service: %s %s: status %d", e.method, e.path, e.status)
}

func (e *sessionServiceError) Unwrap() error { return e.err }

// do sends one request below base; body, when non-nil, is sent as JSON. It
// answers the status and body of any HTTP response; transport failures
// (refused, timeout) are errors.
func (st *remoteStore) do(method string, body any, segments ...string) (int, []byte, error) {
	path := ""
	for _, s := range segments {
		path += "/" + url.PathEscape(s)
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, st.base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// Basic authentication whenever a password is configured, as in Java.
	if st.password != "" {
		req.SetBasicAuth(st.user, st.password)
	}
	resp, err := st.client.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err // the url.Error repeats the URL, which may hold credentials
		}
		return 0, nil, &sessionServiceError{method: method, path: path, err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, &sessionServiceError{method: method, path: path, err: err}
	}
	return resp.StatusCode, raw, nil
}

// expect fails unless the service answered 2xx.
func expect(method string, status int, err error, segments ...string) error {
	if err != nil {
		return err
	}
	if status/100 != 2 {
		return &sessionServiceError{method: method, path: "/" + strings.Join(segments, "/"), status: status}
	}
	return nil
}

func decodeSession(raw []byte) (*Session, error) {
	var s Session
	if err := decodeJSON(raw, &s); err != nil {
		return nil, fmt.Errorf("session service: decoding session: %w", err)
	}
	if s.ID == "" {
		return nil, fmt.Errorf("session service: session without id")
	}
	if s.Data == nil {
		s.Data = map[string]any{}
	}
	return &s, nil
}

func (st *remoteStore) get(typ, id string) (*Session, error) {
	status, raw, err := st.do(http.MethodGet, nil, typ, id)
	if err == nil && status == http.StatusNotFound {
		return nil, errSessionNotFound
	}
	if err := expect(http.MethodGet, status, err, typ, id); err != nil {
		return nil, err
	}
	return decodeSession(raw)
}

func (st *remoteStore) create(typ string, data map[string]any) (string, error) {
	status, raw, err := st.do(http.MethodPost, data, typ)
	if err := expect(http.MethodPost, status, err, typ); err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ID == "" {
		return "", fmt.Errorf("session service: POST /%s: no id in answer", typ)
	}
	return out.ID, nil
}

func (st *remoteStore) insert(typ, id string, data map[string]any) error {
	status, _, err := st.do(http.MethodPost, data, typ, id)
	if err == nil && status == http.StatusConflict {
		return errSessionConflict
	}
	return expect(http.MethodPost, status, err, typ, id)
}

func (st *remoteStore) update(typ, id string, data map[string]any) error {
	status, _, err := st.do(http.MethodPut, data, typ, id)
	if err == nil && status == http.StatusNotFound {
		return errSessionNotFound
	}
	return expect(http.MethodPut, status, err, typ, id)
}

func (st *remoteStore) remove(typ, id string) error {
	status, _, err := st.do(http.MethodDelete, nil, typ, id)
	if err == nil && status == http.StatusNotFound {
		return errSessionNotFound
	}
	return expect(http.MethodDelete, status, err, typ, id)
}

// find posts the filter as the MongoDB query Java builds and answers the
// sessions in the service's order.
func (st *remoteStore) find(typ string, f sessionFilter) ([]*Session, error) {
	status, raw, err := st.do(http.MethodPost, f.mongoQuery(), typ, "query", "fetch")
	if err := expect(http.MethodPost, status, err, typ, "query", "fetch"); err != nil {
		return nil, err
	}
	var list []*Session
	if err := decodeJSON(raw, &list); err != nil {
		return nil, fmt.Errorf("session service: decoding %s query: %w", typ, err)
	}
	out := make([]*Session, 0, len(list))
	for _, s := range list {
		if s == nil {
			continue
		}
		if s.Data == nil {
			s.Data = map[string]any{}
		}
		out = append(out, s)
	}
	return out, nil
}

// mongoQuery is the session-service filter of SessionServiceRequestHandler:
// users and owner are case-insensitive regular expressions of the quoted
// name — so they match any entry containing it — and the origin matches as a
// set ($all plus $size). Several conditions are joined with $and.
func (f sessionFilter) mongoQuery() map[string]any {
	var conds []map[string]any
	if f.users != "" {
		conds = append(conds, map[string]any{"data.users": javaRegex(f.users)})
	}
	if f.owner != "" {
		conds = append(conds, map[string]any{"data.owner": javaRegex(f.owner)})
	}
	if f.hasOrigin {
		origin := f.origin
		if origin == nil {
			origin = []string{}
		}
		conds = append(conds,
			map[string]any{"data.origin": map[string]any{"$all": origin}},
			map[string]any{"data.origin": map[string]any{"$size": len(origin)}})
	}
	if f.page != "" {
		conds = append(conds, map[string]any{"data.page": f.page})
	}
	switch len(conds) {
	case 0:
		return map[string]any{}
	case 1:
		return conds[0]
	}
	return map[string]any{"$and": conds}
}

// javaRegex is Pattern.compile(Pattern.quote(s), CASE_INSENSITIVE) as the
// MongoDB Java driver serialises it.
func javaRegex(s string) map[string]any {
	return map[string]any{"$regex": javaPatternQuote(s), "$options": "i"}
}

// javaPatternQuote is java.util.regex.Pattern.quote: the literal wrapped in
// \Q…\E, with any \E inside it split out.
func javaPatternQuote(s string) string {
	if !strings.Contains(s, `\E`) {
		return `\Q` + s + `\E`
	}
	return `\Q` + strings.ReplaceAll(s, `\E`, `\E\\E\Q`) + `\E`
}

// logSessionError logs a session store failure other than a missing session.
func logSessionError(err error) {
	if err != nil && !errors.Is(err, errSessionNotFound) {
		log.Print(err)
	}
}

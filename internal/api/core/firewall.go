package core

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Requests the Java backend rejects before any controller sees them: Tomcat
// answers a request target it cannot parse or normalize with its own HTML
// 400 page, and Spring Security's default StrictHttpFirewall rejects
// methods, path parameters and encodings with a 400 that Spring Boot's error
// controller renders (SpringError).

// firewallMethods are the methods StrictHttpFirewall allows.
var firewallMethods = map[string]bool{
	http.MethodDelete: true, http.MethodGet: true, http.MethodHead: true, http.MethodOptions: true,
	http.MethodPatch: true, http.MethodPost: true, http.MethodPut: true,
}

// tomcatBadRequestPage is Tomcat's error report page for a 400.
const tomcatBadRequestPage = `<!doctype html><html lang="en"><head><title>HTTP Status 400 – Bad Request</title>` +
	`<style type="text/css">body {font-family:Tahoma,Arial,sans-serif;} h1, h2, h3, b {color:white;background-color:#525D76;} ` +
	`h1 {font-size:22px;} h2 {font-size:16px;} h3 {font-size:14px;} p {font-size:12px;} a {color:black;} ` +
	`.line {height:1px;background-color:#525D76;border:none;}</style></head><body><h1>HTTP Status 400 – Bad Request</h1></body></html>`

// JavaFirewall answers the requests Tomcat or StrictHttpFirewall reject.
func JavaFirewall(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !firewallMethods[r.Method] {
			// The error dispatch is rejected too: an empty 400.
			if r.Method == http.MethodTrace {
				w.Header().Set("Allow", "HEAD, DELETE, POST, GET, OPTIONS, PUT")
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		path, query, ok := requestTarget(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		decoded, ok := tomcatPath(path, query)
		if !ok {
			h := w.Header()
			h.Set("Content-Type", "text/html;charset=utf-8")
			h.Set("Content-Language", "en")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(tomcatBadRequestPage))
			return
		}
		if firewallRejects(path, decoded) {
			SpringError(w, r, http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requestTarget splits the raw request target into its path and query; ok
// is false for a target without a path (e.g. "*").
func requestTarget(r *http.Request) (path, query string, ok bool) {
	t := r.RequestURI
	if t == "" {
		t = r.URL.RequestURI()
	}
	if !strings.HasPrefix(t, "/") {
		// absolute-form: scheme://authority/path
		i := strings.Index(t, "://")
		if i < 0 {
			return "", "", false
		}
		j := strings.IndexByte(t[i+3:], '/')
		if j < 0 {
			return "", "", false
		}
		t = t[i+3+j:]
	}
	path, query, _ = strings.Cut(t, "?")
	return path, query, true
}

// rawPath is the raw path of the request, as Spring's getRequestURI.
func rawPath(r *http.Request) string {
	if p, _, ok := requestTarget(r); ok {
		return p
	}
	return r.URL.EscapedPath()
}

// invalidTargetByte reports a byte Tomcat does not accept unencoded in the
// path or query of a request target.
func invalidTargetByte(c byte) bool {
	return c <= 0x20 || c >= 0x7f || strings.IndexByte(`"<>[\]^`+"`{|}", c) >= 0
}

// tomcatPath checks the request target as Tomcat parses it and returns the
// decoded, normalized path (the servlet path); ok is false when Tomcat
// rejects the request: an invalid character, an invalid or forbidden
// (%2F, %5C, %00) escape in the path, a path that is not UTF-8, or one
// whose ".." segments climb above the root.
func tomcatPath(path, query string) (string, bool) {
	for i := 0; i < len(path); i++ {
		if invalidTargetByte(path[i]) {
			return "", false
		}
	}
	for i := 0; i < len(query); i++ {
		if invalidTargetByte(query[i]) {
			return "", false
		}
	}
	segs := strings.Split(path, "/")
	out := make([]string, 0, len(segs))
	for _, seg := range segs[1:] {
		// Path parameters (";...") are removed before decoding.
		seg, _, _ = strings.Cut(seg, ";")
		dec, ok := decodeTomcatSegment(seg)
		if !ok {
			return "", false
		}
		switch dec {
		case "", ".":
		case "..":
			if len(out) == 0 {
				return "", false
			}
			out = out[:len(out)-1]
		default:
			out = append(out, dec)
		}
	}
	decoded := "/" + strings.Join(out, "/")
	if strings.HasSuffix(path, "/") && len(out) > 0 {
		decoded += "/"
	}
	return decoded, true
}

// decodeTomcatSegment percent-decodes one path segment as UTF-8.
func decodeTomcatSegment(seg string) (string, bool) {
	if !strings.Contains(seg, "%") {
		return seg, true
	}
	b := make([]byte, 0, len(seg))
	for i := 0; i < len(seg); i++ {
		if seg[i] != '%' {
			b = append(b, seg[i])
			continue
		}
		if i+2 >= len(seg) || !isHex(seg[i+1]) || !isHex(seg[i+2]) {
			return "", false
		}
		c := unhex(seg[i+1])<<4 | unhex(seg[i+2])
		if c == '/' || c == '\\' || c == 0 {
			return "", false
		}
		b = append(b, c)
		i += 2
	}
	if !utf8.Valid(b) {
		return "", false
	}
	return string(b), true
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c <= 'F':
		return c - 'A' + 10
	}
	return c - 'a' + 10
}

// firewallRejects applies StrictHttpFirewall to a path Tomcat accepted: the
// raw path may not hold a path parameter (";"), "//", or an encoded period,
// percent, line feed or carriage return, nor a "." or ".." segment; the
// decoded path may not hold a line or paragraph separator.
func firewallRejects(raw, decoded string) bool {
	if strings.Contains(raw, ";") || strings.Contains(raw, "//") {
		return true
	}
	lower := strings.ToLower(raw)
	for _, s := range []string{"%3b", "%2e", "%25", "%0a", "%0d"} {
		if strings.Contains(lower, s) {
			return true
		}
	}
	for _, seg := range strings.Split(raw, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return strings.ContainsAny(decoded, "  ")
}

// errorPageHTML is the portal's error template (templates/error.html), which
// Spring Boot renders for every error a browser receives.
const errorPageHTML = "<html>\n<head>\n    \n" + `</head>
<body style="font-family: Helvetica Neue, Helvetica, Arial, sans-serif">
<div style="display: flex; align-items: center; justify-content: center; height: 100%;">
    <h1 style="color: #3786C2;text-align: center;">
        Error 400: Invalid Request
    </h1>
</div>

</body>
</html>`

// errorRepresentation is the representation BasicErrorController chooses
// for the Accept header: "html" when it names text/html or text/*, "json"
// when it is absent or accepts JSON, else "" (no body).
func errorRepresentation(r *http.Request) string {
	wantJSON, named := false, false
	for _, v := range r.Header.Values("Accept") {
		for _, part := range strings.Split(v, ",") {
			mt, _, _ := strings.Cut(part, ";")
			switch strings.ToLower(strings.TrimSpace(mt)) {
			case "":
				continue
			case "text/html", "text/*":
				return "html"
			case "*/*", "application/json", "application/*":
				wantJSON = true
			}
			named = true
		}
	}
	if wantJSON || !named {
		return "json"
	}
	return ""
}

// SpringError writes Spring Boot's error response for status: the JSON
// error attributes, the error page for a browser, or no body when neither
// is acceptable.
func SpringError(w http.ResponseWriter, r *http.Request, status int) {
	h := w.Header()
	switch errorRepresentation(r) {
	case "html":
		h.Set("Content-Type", "text/html;charset=UTF-8")
		h.Set("Content-Language", "en-US")
		w.WriteHeader(status)
		w.Write([]byte(errorPageHTML))
	case "json":
		h.Set("Content-Type", "application/json")
		w.WriteHeader(status)
		// Field order and the epoch-millisecond timestamp as Spring Boot
		// writes them; Jackson leaves <, > and & unescaped.
		var b strings.Builder
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		enc.Encode(struct {
			Timestamp int64  `json:"timestamp"`
			Status    int    `json:"status"`
			Error     string `json:"error"`
			Path      string `json:"path"`
		}{time.Now().UnixMilli(), status, http.StatusText(status), rawPath(r)})
		w.Write([]byte(strings.TrimSuffix(b.String(), "\n")))
	default:
		w.WriteHeader(status)
	}
}

package core

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/metrics"
)

// The server's metrics; LoginsTotal counts the OAuth2 logins and DATChecks
// the data access token checks.
var (
	httpRequests = metrics.NewCounterVec("cbioportal_http_requests_total",
		"HTTP requests by route (the matched pattern) and status code.", "route", "code")
	httpDuration = metrics.NewHistogramVec("cbioportal_http_request_duration_seconds",
		"HTTP request time by route.", metrics.DurationBuckets, "route")
	httpInFlight = metrics.NewGauge("cbioportal_http_requests_in_flight", "HTTP requests being served.")

	metaCacheLookups = metrics.NewCounterVec("cbioportal_metadata_cache_lookups_total",
		"Metadata cache lookups by result (hit, miss).", "result")
	LoginsTotal = metrics.NewCounterVec("cbioportal_logins_total",
		"OAuth2 logins by outcome (success, failure).", "outcome")
	DATChecks = metrics.NewCounterVec("cbioportal_data_access_token_checks_total",
		"Data access token checks by result (cached, refreshed at the provider, rejected).", "result")
)

// requestInfo collects what inner handlers learn about a request (the
// authenticated user) for its access log line.
type requestInfo struct {
	user string
}

type requestInfoKey struct{}

// WithUser authenticates the request as u for the handlers after it and the
// access log.
func WithUser(r *http.Request, u User) *http.Request {
	if info, ok := r.Context().Value(requestInfoKey{}).(*requestInfo); ok {
		info.user = u.Name
	}
	return r.WithContext(context.WithValue(r.Context(), userCtxKey{}, u))
}

// newAccessLog writes one JSON line per request to stdout when on (for log
// collection, e.g. Datadog); nil otherwise.
func newAccessLog(on bool) *slog.Logger {
	if !on {
		return nil
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, nil))
}

// Observe records every request in the metrics and the access log. Requests
// are labelled by the route pattern they match, so labels stay bounded.
func (s *State) Observe(next http.Handler) http.Handler {
	return s.observeWith(next, newAccessLog(s.Opts.AccessLog))
}

// observeWith is Observe with the given access log (nil: none).
func (s *State) observeWith(next http.Handler, accessLog *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		route := s.routeOf(r)
		info := &requestInfo{}
		r = r.WithContext(context.WithValue(r.Context(), requestInfoKey{}, info))
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		httpInFlight.Add(1)
		defer func() {
			httpInFlight.Add(-1)
			elapsed := time.Since(start)
			httpRequests.Inc(route, strconv.Itoa(sw.status))
			httpDuration.Observe(elapsed.Seconds(), route)
			if accessLog != nil {
				attrs := []any{
					slog.String("method", r.Method), slog.String("route", route), slog.String("path", r.URL.Path),
					slog.Int("status", sw.status), slog.Int64("bytes", sw.bytes),
					slog.Float64("duration_ms", float64(elapsed.Microseconds())/1000),
					slog.String("remote", clientIP(r)),
				}
				if info.user != "" {
					attrs = append(attrs, slog.String("user", info.user))
				}
				accessLog.Info("request", attrs...)
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

// routeOf is the mux pattern the request matches (e.g.
// "GET /api/studies/{studyId}"); the frontend and unknown paths share theirs.
func (s *State) routeOf(r *http.Request) string {
	if s.mux == nil {
		return "unmatched"
	}
	_, pattern := s.mux.Handler(r)
	if pattern == "" {
		return "unmatched"
	}
	return pattern
}

// clientIP is the original client address: the first X-Forwarded-For entry
// behind a proxy, else the connection's.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(first)
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return host
}

// statusWriter records the status and body size of a response.
type statusWriter struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status, w.wroteHeader = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	w.wroteHeader = true
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// MetricsHandler serves the server's metrics in the Prometheus text format.
func MetricsHandler() http.Handler { return metrics.Default.Handler() }

// IsAPIPath reports whether p is an API path rather than a page of the portal.
func IsAPIPath(p string) bool {
	return strings.HasPrefix(p, "/api/") || p == "/webservice.do"
}

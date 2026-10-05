package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExposition(t *testing.T) {
	Default = &Registry{}
	c := NewCounterVec("test_requests_total", "Requests.", "route", "code")
	c.Inc("GET /api/studies", "200")
	c.Add(2, "GET /api/studies", "200")
	c.Inc(`GET /api/"q"`, "500")
	h := NewHistogramVec("test_duration_seconds", "Durations.", []float64{0.1, 1}, "route")
	h.Observe(0.05, "r")
	h.Observe(0.5, "r")
	h.Observe(5, "r")
	g := NewGauge("test_in_flight", "In flight.")
	g.Add(3)
	g.Add(-1)
	NewGaugeFunc("test_sessions", "Sessions.", func() float64 { return 7 })

	w := httptest.NewRecorder()
	Default.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	out := w.Body.String()
	for _, want := range []string{
		"# TYPE test_requests_total counter\n",
		`test_requests_total{route="GET /api/\"q\"",code="500"} 1` + "\n",
		`test_requests_total{route="GET /api/studies",code="200"} 3` + "\n",
		"# TYPE test_duration_seconds histogram\n",
		`test_duration_seconds_bucket{route="r",le="0.1"} 1` + "\n",
		`test_duration_seconds_bucket{route="r",le="1"} 2` + "\n",
		`test_duration_seconds_bucket{route="r",le="+Inf"} 3` + "\n",
		`test_duration_seconds_sum{route="r"} 5.55` + "\n",
		`test_duration_seconds_count{route="r"} 3` + "\n",
		"test_in_flight 2\n",
		"test_sessions 7\n",
		"# TYPE go_goroutines gauge\n",
		"# TYPE process_cpu_seconds_total counter\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("content type %q", ct)
	}
}

func TestLabelCountMismatchPanics(t *testing.T) {
	Default = &Registry{}
	c := NewCounterVec("test_x_total", "X.", "a", "b")
	defer func() {
		if recover() == nil {
			t.Error("no panic for a wrong number of label values")
		}
	}()
	c.Inc("only-one")
}

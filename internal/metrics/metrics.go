// Package metrics keeps the server's counters, gauges and histograms and
// writes them in the Prometheus text exposition format (version 0.0.4), which
// Prometheus and Datadog's OpenMetrics check scrape. Go runtime and process
// metrics are included under their conventional names.
package metrics

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Registry holds metric families in registration order.
type Registry struct {
	mu       sync.Mutex
	families []family
}

type family interface {
	write(w io.Writer)
}

// Default is the registry the server's metrics register in.
var Default = &Registry{}

func (r *Registry) add(f family) {
	r.mu.Lock()
	r.families = append(r.families, f)
	r.mu.Unlock()
}

// Handler serves the registry and the runtime metrics.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.mu.Lock()
		fams := slices.Clone(r.families)
		r.mu.Unlock()
		for _, f := range fams {
			f.write(w)
		}
		writeRuntime(w)
	})
}

// series is the per-label-values state shared by the vector types.
type series[T any] struct {
	mu     sync.Mutex
	labels []string
	byKey  map[string]*T
	values map[string][]string
}

func (s *series[T]) get(values []string, init func() *T) *T {
	if len(values) != len(s.labels) {
		panic(fmt.Sprintf("metrics: %d label values for %d labels", len(values), len(s.labels)))
	}
	key := strings.Join(values, "\xff")
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.byKey[key]; ok {
		return v
	}
	if s.byKey == nil {
		s.byKey, s.values = map[string]*T{}, map[string][]string{}
	}
	v := init()
	s.byKey[key], s.values[key] = v, slices.Clone(values)
	return v
}

// each visits the series in label-value order.
func (s *series[T]) each(fn func(values []string, v *T)) {
	s.mu.Lock()
	keys := make([]string, 0, len(s.byKey))
	for k := range s.byKey {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	type kv struct {
		values []string
		v      *T
	}
	items := make([]kv, len(keys))
	for i, k := range keys {
		items[i] = kv{s.values[k], s.byKey[k]}
	}
	s.mu.Unlock()
	for _, it := range items {
		fn(it.values, it.v)
	}
}

func header(w io.Writer, name, help, typ string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, strings.ReplaceAll(help, "\n", " "), name, typ)
}

func labelPairs(names, values []string, extra ...string) string {
	if len(names) == 0 && len(extra) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	n := 0
	put := func(k, v string) {
		if n > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v))
		b.WriteByte('"')
		n++
	}
	for i, k := range names {
		put(k, values[i])
	}
	for i := 0; i+1 < len(extra); i += 2 {
		put(extra[i], extra[i+1])
	}
	b.WriteByte('}')
	return b.String()
}

func formatFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// ---- counters ----

// CounterVec is a monotonically increasing count per label values.
type CounterVec struct {
	name, help string
	s          series[atomic.Uint64]
}

// NewCounterVec registers a counter family in Default.
func NewCounterVec(name, help string, labels ...string) *CounterVec {
	c := &CounterVec{name: name, help: help}
	c.s.labels = labels
	Default.add(c)
	return c
}

// Add increases the counter of the label values by n.
func (c *CounterVec) Add(n uint64, values ...string) {
	c.s.get(values, func() *atomic.Uint64 { return new(atomic.Uint64) }).Add(n)
}

// Inc increases the counter of the label values by one.
func (c *CounterVec) Inc(values ...string) { c.Add(1, values...) }

func (c *CounterVec) write(w io.Writer) {
	header(w, c.name, c.help, "counter")
	c.s.each(func(values []string, v *atomic.Uint64) {
		fmt.Fprintf(w, "%s%s %d\n", c.name, labelPairs(c.s.labels, values), v.Load())
	})
}

// ---- gauges ----

// Gauge is a value that goes up and down.
type Gauge struct {
	name, help string
	v          atomic.Int64
}

// NewGauge registers a gauge in Default.
func NewGauge(name, help string) *Gauge {
	g := &Gauge{name: name, help: help}
	Default.add(g)
	return g
}

// Add changes the gauge by d.
func (g *Gauge) Add(d int64) { g.v.Add(d) }

func (g *Gauge) write(w io.Writer) {
	header(w, g.name, g.help, "gauge")
	fmt.Fprintf(w, "%s %d\n", g.name, g.v.Load())
}

// ---- histograms ----

// DurationBuckets are histogram bounds for request and query durations, in
// seconds.
var DurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

type histogram struct {
	mu     sync.Mutex
	counts []uint64 // per bucket, not cumulative; the last is +Inf
	sum    float64
	count  uint64
}

// HistogramVec counts observations into buckets per label values.
type HistogramVec struct {
	name, help string
	buckets    []float64
	s          series[histogram]
}

// NewHistogramVec registers a histogram family with the given upper bounds
// in Default.
func NewHistogramVec(name, help string, buckets []float64, labels ...string) *HistogramVec {
	h := &HistogramVec{name: name, help: help, buckets: slices.Clone(buckets)}
	h.s.labels = labels
	Default.add(h)
	return h
}

// Observe records v for the label values.
func (h *HistogramVec) Observe(v float64, values ...string) {
	st := h.s.get(values, func() *histogram { return &histogram{counts: make([]uint64, len(h.buckets)+1)} })
	i, _ := slices.BinarySearch(h.buckets, v)
	st.mu.Lock()
	st.counts[i]++
	st.sum += v
	st.count++
	st.mu.Unlock()
}

func (h *HistogramVec) write(w io.Writer) {
	header(w, h.name, h.help, "histogram")
	h.s.each(func(values []string, st *histogram) {
		st.mu.Lock()
		counts, sum, count := slices.Clone(st.counts), st.sum, st.count
		st.mu.Unlock()
		var cum uint64
		for i, le := range h.buckets {
			cum += counts[i]
			fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, labelPairs(h.s.labels, values, "le", formatFloat(le)), cum)
		}
		fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, labelPairs(h.s.labels, values, "le", "+Inf"), count)
		fmt.Fprintf(w, "%s_sum%s %s\n", h.name, labelPairs(h.s.labels, values), formatFloat(sum))
		fmt.Fprintf(w, "%s_count%s %d\n", h.name, labelPairs(h.s.labels, values), count)
	})
}

// ---- runtime and process ----

var startTime = time.Now()

// writeRuntime writes the Go runtime and process metrics under the names the
// Prometheus Go client uses, so standard dashboards apply.
func writeRuntime(w io.Writer) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	gauge := func(name, help string, v float64) {
		header(w, name, help, "gauge")
		fmt.Fprintf(w, "%s %s\n", name, formatFloat(v))
	}
	counter := func(name, help string, v float64) {
		header(w, name, help, "counter")
		fmt.Fprintf(w, "%s %s\n", name, formatFloat(v))
	}
	gauge("go_goroutines", "Number of goroutines that currently exist.", float64(runtime.NumGoroutine()))
	gauge("go_memstats_heap_alloc_bytes", "Bytes of allocated heap objects.", float64(m.HeapAlloc))
	gauge("go_memstats_heap_inuse_bytes", "Bytes in in-use heap spans.", float64(m.HeapInuse))
	gauge("go_memstats_sys_bytes", "Bytes of memory obtained from the OS.", float64(m.Sys))
	counter("go_memstats_alloc_bytes_total", "Cumulative bytes allocated for heap objects.", float64(m.TotalAlloc))
	counter("go_gc_cycles_total", "Completed GC cycles.", float64(m.NumGC))
	counter("go_gc_pause_seconds_total", "Cumulative GC stop-the-world pause time.", float64(m.PauseTotalNs)/1e9)

	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) == nil {
		cpu := float64(ru.Utime.Sec+ru.Stime.Sec) + float64(ru.Utime.Usec+ru.Stime.Usec)/1e6
		counter("process_cpu_seconds_total", "Total user and system CPU time spent in seconds.", cpu)
	}
	if rss := residentBytes(); rss > 0 {
		gauge("process_resident_memory_bytes", "Resident memory size in bytes.", float64(rss))
	}
	if fds, err := os.ReadDir("/proc/self/fd"); err == nil {
		gauge("process_open_fds", "Number of open file descriptors.", float64(len(fds)))
	}
	gauge("process_start_time_seconds", "Start time of the process since unix epoch in seconds.", float64(startTime.Unix()))
}

// residentBytes reads the resident set size from /proc (Linux); 0 elsewhere.
func residentBytes() int64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	pages, _ := strconv.ParseInt(f[1], 10, 64)
	return pages * int64(os.Getpagesize())
}

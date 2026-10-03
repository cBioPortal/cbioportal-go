package api

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// routeStatus registers the server-status, static-data timestamp, cache
// eviction and SEO endpoints.
func (s *State) routeStatus(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/timestamps", s.timestamps)
	mux.HandleFunc("GET /api/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain;charset=UTF-8")
		w.Write([]byte("Greetings from Spring Boot!"))
	})
	mux.HandleFunc("DELETE /api/cache", s.clearCache)
	mux.HandleFunc("DELETE /api/cache/{studyId}", s.clearCache)
	mux.HandleFunc("GET /robots.txt", s.robots)
	mux.HandleFunc("GET /sitemap_index.xml", s.sitemapIndex)
	mux.HandleFunc("GET /sitemap_study.xml", s.sitemapStudy)
}

// health serves GET /api/health (ServerStatusServiceImpl): UP while the
// database answers with at least one cancer type, else DOWN.
func (s *State) health(w http.ResponseWriter, r *http.Request) {
	status := "DOWN"
	if v, err := s.CH.Execute("SELECT count() FROM cbioportal_ref.type_of_cancer FORMAT TabSeparated"); err == nil {
		if n, _ := strconv.Atoi(strings.TrimSpace(v)); n > 0 {
			status = "UP"
		}
	}
	writeJSON(w, map[string]string{"status": status})
}

// timestampTables are StaticDataTimestampController.TIMESTAMP_TABLES.
var timestampTables = []string{"gene", "reference_genome_gene"}

// timestamps serves GET /api/timestamps: when each static reference table
// last changed, as MySQL's COALESCE(update_time, create_time) — here the
// newest active part's modification time, else the table's metadata time.
func (s *State) timestamps(w http.ResponseWriter, r *http.Request) {
	type row struct {
		Name string `json:"name"`
		T    string `json:"t"`
	}
	rows, err := ch.Query[row](s.CH, fmt.Sprintf(
		"SELECT t.name AS name, toString(if(p.m > toDateTime(0), p.m, t.metadata_modification_time)) AS t "+
			"FROM system.tables t LEFT JOIN (SELECT table, max(modification_time) AS m FROM system.parts "+
			"WHERE database = 'cbioportal_ref' AND active GROUP BY table) p ON p.table = t.name "+
			"WHERE t.database = 'cbioportal_ref' AND t.name IN (%s)", inList(timestampTables)))
	if err != nil {
		fail(w, err)
		return
	}
	out := map[string]string{}
	for _, rw := range rows {
		out[rw.Name] = rw.T
	}
	writeJSON(w, out)
}

// clearCache serves DELETE /api/cache and /api/cache/{studyId}
// (CacheController): enabled by cache.endpoint.enabled, authorised by the
// X-API-KEY header matching cache.endpoint.api-key. It drops the metadata
// cache; the study id only names the study in the reply.
func (s *State) clearCache(w http.ResponseWriter, r *http.Request) {
	key, ok := r.Header["X-Api-Key"]
	if !ok {
		springError(w, r, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain;charset=UTF-8")
	if s.Props["cache.endpoint.enabled"] != "true" {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("Cache endpoint is disabled for this instance."))
		return
	}
	required, set := s.Props["cache.endpoint.api-key"]
	if !set || required == "not set" || required != key[0] {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	s.meta.mu.Lock()
	s.meta.entries = nil
	s.meta.mu.Unlock()
	if id := r.PathValue("studyId"); id != "" {
		w.Write([]byte("Flushed " + id + " caches!!!"))
		return
	}
	w.Write([]byte("Flushed all caches!!!"))
}

// sitemapsEnabled is the `sitemaps` flag (SitemapFeature); without it the
// SEO endpoints answer 404 with an empty body.
func (s *State) sitemapsEnabled(w http.ResponseWriter) bool {
	if s.Props["sitemaps"] == "true" {
		return true
	}
	w.WriteHeader(http.StatusNotFound)
	return false
}

// seoBaseURL is SeoRequestUtil.resolveBaseUrl: the first X-Forwarded-Proto /
// X-Forwarded-Host values, else the request's scheme and server name (the
// Host header without its port).
func seoBaseURL(r *http.Request) string {
	first := func(h string) string {
		v, _, _ := strings.Cut(r.Header.Get(h), ",")
		return strings.TrimSpace(v)
	}
	scheme := first("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
		if r.TLS != nil {
			scheme = "https"
		}
	}
	host := first("X-Forwarded-Host")
	if host == "" {
		host = r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
	}
	return scheme + "://" + host
}

// robots serves GET /robots.txt (RobotsController), tuned by
// robots.disallow_user_agents, robots.disallow_paths and robots.crawl_delay.
func (s *State) robots(w http.ResponseWriter, r *http.Request) {
	if !s.sitemapsEnabled(w) {
		return
	}
	prop := func(k, def string) string {
		if v, ok := s.Props[k]; ok {
			return v
		}
		return def
	}
	var b strings.Builder
	for _, a := range strings.Split(prop("robots.disallow_user_agents", ""), ",") {
		if a = strings.TrimSpace(a); a != "" {
			b.WriteString("User-agent: " + a + "\nDisallow: /\n\n")
		}
	}
	b.WriteString("User-agent: *\nAllow: /\n")
	for _, p := range strings.Split(prop("robots.disallow_paths", "/proxy/"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			b.WriteString("Disallow: " + p + "\n")
		}
	}
	if d := strings.TrimSpace(prop("robots.crawl_delay", "5")); d != "" {
		b.WriteString("Crawl-delay: " + d + "\n")
	}
	b.WriteString("\nSitemap: " + seoBaseURL(r) + "/sitemap_index.xml\n")
	w.Header().Set("Content-Type", "text/plain;charset=UTF-8")
	w.Write([]byte(b.String()))
}

// patientsPerSitemap keeps a study's page 0 (summary URL plus patients) within
// sitemaps.org's 50,000-URL limit.
const patientsPerSitemap = 50000 - 1

// sitemapIndex serves GET /sitemap_index.xml: one entry per sitemap page of
// every study.
func (s *State) sitemapIndex(w http.ResponseWriter, r *http.Request) {
	if !s.sitemapsEnabled(w) {
		return
	}
	type row struct {
		StudyID  string `json:"studyId"`
		Patients uint64 `json:"patients"`
	}
	rows, err := ch.Query[row](s.CH,
		"SELECT c.study_id AS studyId, ifNull(p.n, 0) AS patients FROM cancer_study c "+
			"LEFT JOIN (SELECT study_id, count() AS n FROM patient GROUP BY study_id) p ON p.study_id = c.study_id "+
			"ORDER BY c.study_id")
	if err != nil {
		fail(w, err)
		return
	}
	base := seoBaseURL(r)
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<sitemapindex xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">\n")
	for _, st := range rows {
		pages := max(1, int((st.Patients+patientsPerSitemap-1)/patientsPerSitemap))
		for p := 0; p < pages; p++ {
			loc := fmt.Sprintf("%s/sitemap_study.xml?studyId=%s&page=%d", base, javaURLEncode(st.StudyID), p)
			b.WriteString("  <sitemap>\n    <loc>" + xmlEscape(loc) + "</loc>\n  </sitemap>\n")
		}
	}
	b.WriteString("</sitemapindex>\n")
	writeSitemap(w, b.String())
}

// sitemapStudy serves GET /sitemap_study.xml?studyId=&page=: the study summary
// URL (page 0) and one patient-view URL per patient of the page, patients
// ordered by id.
func (s *State) sitemapStudy(w http.ResponseWriter, r *http.Request) {
	if !s.sitemapsEnabled(w) {
		return
	}
	q := r.URL.Query()
	studyID, ok := q["studyId"]
	if !ok {
		writeError(w, http.StatusBadRequest, "Request parameter is missing: studyId")
		return
	}
	page := 0
	if v := q.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, msgIntMismatch(v))
			return
		}
		page = n
	}
	id := studyID[0]
	if page < 0 || !s.studyKnown(id) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	type row struct {
		PatientID string `json:"patientId"`
	}
	patients, err := ch.Query[row](s.CH, fmt.Sprintf(
		"SELECT patient_id AS patientId FROM patient WHERE study_id = '%s' ORDER BY patient_id LIMIT %d OFFSET %d",
		sqlLit(id), patientsPerSitemap, page*patientsPerSitemap))
	if err != nil {
		fail(w, err)
		return
	}
	if page > 0 && len(patients) == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	base := seoBaseURL(r)
	var b strings.Builder
	url := func(loc string) { b.WriteString("  <url>\n    <loc>" + xmlEscape(loc) + "</loc>\n  </url>\n") }
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<urlset xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">\n")
	if page == 0 {
		url(base + "/study/summary?id=" + javaURLEncode(id))
	}
	for _, p := range patients {
		url(base + "/patient?studyId=" + javaURLEncode(id) + "&caseId=" + javaURLEncode(p.PatientID))
	}
	b.WriteString("</urlset>\n")
	writeSitemap(w, b.String())
}

func writeSitemap(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/xml;charset=UTF-8")
	w.Header().Set("X-Robots-Tag", "noindex")
	w.Write([]byte(body))
}

// javaURLEncode is java.net.URLEncoder.encode(s, UTF_8): letters, digits and
// ".-*_" stay, a space becomes "+", every other byte is %XX.
func javaURLEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '-', c == '*', c == '_':
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;")

func xmlEscape(s string) string { return xmlEscaper.Replace(s) }

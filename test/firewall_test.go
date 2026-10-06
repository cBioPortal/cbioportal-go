package contract

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Contracts for the requests the Java backend rejects before a controller
// sees them (Tomcat's request-target parsing and Spring Security's
// StrictHttpFirewall), and for the single-study clinical data filter.

// rawTarget sends method and the request target exactly as given (no client
// normalization or escaping) and returns the response and its body.
func rawTarget(t *testing.T, method, target string, header map[string]string) (*http.Response, []byte) {
	t.Helper()
	u, _ := url.Parse(apiBase())
	conn, err := net.DialTimeout("tcp", u.Host, 10*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", u.Host, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	var b strings.Builder
	b.WriteString(method + " " + target + " HTTP/1.1\r\nHost: " + u.Host + "\r\nConnection: close\r\n")
	for k, v := range header {
		b.WriteString(k + ": " + v + "\r\n")
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: method})
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

const tomcatBadRequest = `<!doctype html><html lang="en"><head><title>HTTP Status 400 – Bad Request</title>` +
	`<style type="text/css">body {font-family:Tahoma,Arial,sans-serif;} h1, h2, h3, b {color:white;background-color:#525D76;} ` +
	`h1 {font-size:22px;} h2 {font-size:16px;} h3 {font-size:14px;} p {font-size:12px;} a {color:black;} ` +
	`.line {height:1px;background-color:#525D76;border:none;}</style></head><body><h1>HTTP Status 400 – Bad Request</h1></body></html>`

// wantFirewall asserts Spring Boot's error body for a request the firewall
// rejected: 400, its raw path.
func wantFirewall(t *testing.T, target string) {
	t.Helper()
	resp, body := rawTarget(t, "GET", target, nil)
	var e map[string]any
	json.Unmarshal(body, &e)
	path, _, _ := strings.Cut(target, "?")
	if resp.StatusCode != 400 || resp.Header.Get("Content-Type") != "application/json" ||
		e["status"] != float64(400) || e["error"] != "Bad Request" || e["path"] != path || e["timestamp"] == nil || len(e) != 4 {
		t.Errorf("GET %s: %d %s %s, want the firewall's 400", target, resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if !strings.HasPrefix(string(body), `{"timestamp":`) {
		t.Errorf("GET %s: field order %s", target, body)
	}
}

func TestContractFirewall(t *testing.T) {
	requireAPI(t)
	// StrictHttpFirewall: path parameters, "//", "." and ".." segments, and
	// encoded periods, percents, line feeds, carriage returns and line
	// separators.
	for _, target := range []string{
		"/api/genes/lookup/ACSL1;", "/api/genes/TP53;", "/api/genes/TP53;x=1", "/api/genes/TP%3B53",
		"/api/studies;jsessionid=1", "/;", "/index.html;", "/api/..;/sendgrid/.env",
		"/api/session/main_session/67934d9683e9543d6193db50;", "/api/session/main_session/67934d9683e9543d6193db50;%7B",
		"/api/genes//TP53", "/api/genes/../genes/TP53", "/api/genes/./TP53", "/api/genes/..", "/api/genes/TP53/.",
		"/api/genes/TP%2E53", "/api/genes/%2e%2e", "/api/genes/TP%2553", "/api/genes/TP%0A53", "/api/genes/TP%0d53",
		"/api/genes/TP%E2%80%A853", "/api/genes/TP&53;",
	} {
		wantFirewall(t, target)
	}
	// Tomcat: an invalid character in the path or query, an invalid,
	// encoded slash, backslash or NUL escape, a path that is not UTF-8, or
	// one climbing above the root.
	for _, target := range []string{
		"/api/genes/TP%2F53", "/api/genes/TP%2f%2f53", "/api/genes/TP%5C53", "/api/genes/TP%0053", "/api/genes/TP\\53",
		"/api/genes/TP%FF53", "/api/genes/TP|53", "/api/genes/TP[53", "/api/genes/TP{53", "/api/genes/TP53?x={",
		"/api/genes/TP53?x=|", "/api/genes/TP53?x=^", "/../api/genes/TP53", "/%2e%2e/api/genes/TP53", "/..;/api/genes/TP53",
	} {
		resp, body := rawTarget(t, "GET", target, nil)
		if resp.StatusCode != 400 || resp.Header.Get("Content-Type") != "text/html;charset=utf-8" || string(body) != tomcatBadRequest {
			t.Errorf("GET %s: %d %s %.80s, want Tomcat's 400 page", target, resp.StatusCode, resp.Header.Get("Content-Type"), body)
		}
	}
	// Accepted as they are.
	for target, status := range map[string]int{
		"/api/genes/TP53?x=a;b": 200, "/api/genes/TP53?x=%": 200, "/api/genes/TP53?x=a%2Fb;c": 200,
		"/api/genes/.TP53": 404, "/api/genes/TP%7C53": 404, "/api/genes/TP%7F53": 404, "/api/genes/TP%C3%A953": 404,
	} {
		if resp, body := rawTarget(t, "GET", target, nil); resp.StatusCode != status {
			t.Errorf("GET %s: %d %.100s, want %d", target, resp.StatusCode, body, status)
		}
	}

	// The error representation follows the Accept header.
	for accept, ct := range map[string]string{
		"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8": "text/html;charset=UTF-8",
		"text/*":                            "text/html;charset=UTF-8",
		"application/json, text/plain, */*": "application/json",
		"image/png":                         "",
	} {
		resp, body := rawTarget(t, "GET", "/api/genes/TP53;", map[string]string{"Accept": accept})
		if resp.StatusCode != 400 || resp.Header.Get("Content-Type") != ct {
			t.Errorf("Accept %s: %d %s", accept, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if ct == "" && len(body) != 0 || strings.HasPrefix(ct, "text/html") && !strings.Contains(string(body), "Error 400: Invalid Request") {
			t.Errorf("Accept %s: body %.100q", accept, body)
		}
	}

	// Methods outside StrictHttpFirewall's list: an empty 400.
	for _, m := range []string{"TRACE", "PROPFIND", "FOO"} {
		if resp, body := rawTarget(t, m, "/api/genes/TP53", nil); resp.StatusCode != 400 || len(body) != 0 {
			t.Errorf("%s: %d %q, want an empty 400", m, resp.StatusCode, body)
		}
	}
}

func TestContractClinicalDataInStudyFilter(t *testing.T) {
	requireAPI(t)
	const path = "/api/studies/study_es_0/clinical-data/fetch"
	const badJSON = "There is an error in the JSON format of the request payload"
	// Jackson reads ClinicalDataSingleStudyFilter: ids and attributeIds only,
	// both lists of strings.
	for _, body := range []string{
		`{"sampleListId":"study_es_0_all"}`,
		`{"ids":["TCGA-A1-A0SB-01"],"clinicalDataType":"PATIENT"}`,
		`{"ids":["TCGA-A1-A0SB-01"],"projection":"DETAILED"}`,
		`{"sampleIds":["TCGA-A1-A0SB-01"],"clinicalAttributeIds":["OS_STATUS"]}`,
		`{"ids":[{"value":["TCGA-A1-A0SB-01"]}]}`,
		`{"ids":"TCGA-A1-A0SB-01","attributeIds":["OS_STATUS"]}`,
		`{"ids":["TCGA-A1-A0SB-01"],"attributeIds":[["OS_STATUS"]]}`,
		`"SUBTYPE"`, `[]`, `null`, ``,
	} {
		wantError(t, "POST", path, body, 400, badJSON)
	}
	wantError(t, "POST", path, `{"attributeIds":["OS_STATUS"],"ids":[]}`, 400, "ids size must be between 1 and 10000000")
	wantError(t, "POST", path, `{"attributeIds":[]}`, 400, "attributeIds size must be between 1 and 10000000")
	// clinicalDataType converts before the body, projection after it, and
	// the study is looked up last.
	wantError(t, "POST", path+"?clinicalDataType=FOO", `{"ids":[]}`, 400,
		"Request parameter type mismatch: java.lang.IllegalArgumentException: No enum constant org.cbioportal.legacy.web.parameter.ClinicalDataType.FOO")
	wantError(t, "POST", path+"?projection=FOO", `{"ids":[]}`, 400, "ids size must be between 1 and 10000000")
	wantError(t, "POST", "/api/studies/no_such_study/clinical-data/fetch", `{"ids":[]}`, 400, "ids size must be between 1 and 10000000")
	wantError(t, "POST", "/api/studies/no_such_study/clinical-data/fetch", `{"ids":["x"]}`, 404, "Study not found: no_such_study")

	// Valid filters, including the frontend's.
	var d []map[string]any
	post(t, path+"?clinicalDataType=SAMPLE&projection=SUMMARY", map[string]any{
		"ids": []string{"TCGA-A1-A0SB-01"}, "attributeIds": []string{"MUTATION_COUNT"}}, &d)
	wantEq(t, len(d), 1)
	d = nil
	post(t, path+"?clinicalDataType=PATIENT", map[string]any{"ids": nil, "attributeIds": []string{"OS_STATUS"}}, &d)
	if len(d) == 0 {
		t.Error("PATIENT data with ids null: no rows")
	}
	d = nil
	post(t, path, map[string]any{}, &d)
	if len(d) == 0 {
		t.Error("empty filter: no rows")
	}
}

package clickhouse

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestEncodeParam(t *testing.T) {
	cases := []struct {
		v    any
		want string
	}{
		{"plain", "plain"},
		{"", ""},
		{`it's`, `it's`},
		{`back\slash`, `back\\slash`},
		{"tab\there", `tab\there`},
		{"new\nline\r", `new\nline\r`},
		{"nul\x00", `nul\0`},
		{"ünïcödé ✓🧬", "ünïcödé ✓🧬"},
		{true, "true"},
		{42, "42"},
		{int64(-7), "-7"},
		{uint64(18446744073709551615), "18446744073709551615"},
		{0.5, "0.5"},
		{[]string{}, "[]"},
		{[]string{"a", "b"}, "['a','b']"},
		{[]string{`o'neil`, `c:\x`, "t\tn\n", ""}, `['o\'neil','c:\\x','t\tn\n','']`},
		{[]int{1, -2}, "[1,-2]"},
		{[]int64{}, "[]"},
		{[][2]string{{"s1", "p'1"}, {"s2", `p\2`}}, `[('s1','p\'1'),('s2','p\\2')]`},
		{[][2]string{}, "[]"},
	}
	for _, c := range cases {
		got, err := EncodeParam(c.v)
		if err != nil {
			t.Fatalf("%#v: %v", c.v, err)
		}
		if got != c.want {
			t.Errorf("EncodeParam(%#v) = %q, want %q", c.v, got, c.want)
		}
	}
	if _, err := EncodeParam(struct{}{}); err == nil {
		t.Error("unsupported type accepted")
	}
}

func TestSubstituteTable(t *testing.T) {
	ts, _ := tableParam("ids", []string{"a"})
	sql := "SELECT has({ids:Array(String)}, x) FROM t WHERE x IN {ids:Array(String)} AND y NOT IN { ids : Array(String) } AND z = {idsx:String}"
	want := "SELECT has((SELECT groupArray(v) FROM _param_ids), x) FROM t WHERE x IN (SELECT v FROM _param_ids) AND y NOT IN (SELECT v FROM _param_ids) AND z = {idsx:String}"
	if got := substituteTable(sql, "ids", ts); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	tp, _ := tableParam("p", [][2]string{{"a", "b"}})
	if got := substituteTable("(a, b) IN {p:Array(Tuple(String, String))} OR length({p:Array(Tuple(String, String))}) > 0", "p", tp); got !=
		"(a, b) IN (SELECT c1, c2 FROM _param_p) OR length((SELECT groupArray((c1, c2)) FROM _param_p)) > 0" {
		t.Errorf("tuple substitution: %s", got)
	}
	// MIN is not the IN keyword.
	if got := substituteTable("SELECT MIN {ids:Array(String)}", "ids", ts); !strings.Contains(got, "groupArray") {
		t.Errorf("MIN treated as IN: %s", got)
	}
}

// recorded is what a fake ClickHouse saw of one request.
type recorded struct {
	url         string
	contentType string
	body        string
	form        map[string]string
	files       map[string]string
}

func fakeServer(t *testing.T, rec *recorded) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.url = r.URL.RawQuery
		rec.contentType = r.Header.Get("Content-Type")
		rec.form, rec.files = map[string]string{}, map[string]string{}
		if strings.HasPrefix(rec.contentType, "multipart/") {
			mr, err := r.MultipartReader()
			if err != nil {
				t.Fatal(err)
			}
			for {
				part, err := mr.NextPart()
				if err != nil {
					break
				}
				b, _ := io.ReadAll(part)
				if part.FileName() != "" {
					rec.files[part.FormName()] = string(b)
				} else {
					rec.form[part.FormName()] = string(b)
				}
			}
		} else {
			b, _ := io.ReadAll(r.Body)
			rec.body = string(b)
		}
		w.Write([]byte(`{"n":1}` + "\n"))
	}))
}

func TestRequestShapes(t *testing.T) {
	var rec recorded
	srv := fakeServer(t, &rec)
	defer srv.Close()
	c := New(srv.URL, "db")
	type row struct {
		N int `json:"n"`
	}

	// Small parameters: statement in the body, values in the URL.
	if _, err := QueryP[row](c, "SELECT {s:String}", Params{"s": "a'b"}); err != nil {
		t.Fatal(err)
	}
	if rec.body != "SELECT {s:String} FORMAT JSONEachRow" || !strings.Contains(rec.url, "param_s=a%27b") {
		t.Errorf("small: url=%s body=%s", rec.url, rec.body)
	}

	// Many short ids: multipart form fields.
	mid := make([]string, 2000)
	for i := range mid {
		mid[i] = fmt.Sprintf("sample_%05d", i)
	}
	if _, err := QueryP[row](c, "SELECT 1 WHERE x IN {ids:Array(String)}", Params{"ids": mid}); err != nil {
		t.Fatal(err)
	}
	enc, _ := EncodeParam(mid)
	if !strings.HasPrefix(rec.contentType, "multipart/form-data") || rec.form["param_ids"] != enc ||
		rec.form["query"] != "SELECT 1 WHERE x IN {ids:Array(String)} FORMAT JSONEachRow" {
		t.Errorf("medium: content-type=%s form=%v", rec.contentType, len(rec.form))
	}

	// Ids past the form-field limit: an external table.
	big := make([]string, 20000)
	for i := range big {
		big[i] = fmt.Sprintf("sample_%05d", i)
	}
	big[3] = "tab\there"
	if _, err := QueryP[row](c, "SELECT 1 WHERE x IN {ids:Array(String)} AND y = {s:String}", Params{"ids": big, "s": "z"}); err != nil {
		t.Fatal(err)
	}
	if rec.form["query"] != "SELECT 1 WHERE x IN (SELECT v FROM _param_ids) AND y = {s:String} FORMAT JSONEachRow" ||
		rec.form["param_s"] != "z" || !strings.Contains(rec.url, "_param_ids_structure=v+String") ||
		!strings.HasPrefix(rec.files["_param_ids"], "sample_00000\nsample_00001\nsample_00002\ntab\\there\n") {
		t.Errorf("large: url=%s query=%s", rec.url, rec.form["query"])
	}

	if _, err := QueryP[row](c, "SELECT 1", Params{"bad-name": "x"}); err == nil {
		t.Error("invalid parameter name accepted")
	}
}

// TestLiveRoundTrip checks the encodings against a real server when
// CLICKHOUSE_TEST_URL (e.g. http://localhost:18123) is set.
func TestLiveRoundTrip(t *testing.T) {
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	c := New(base, "default")
	odd := []string{"plain", "", `o'neil`, `c:\x`, "t\tn\nr\r", "ünïcödé ✓🧬", `\'`, "{x:String}", "nul\x00"}

	type strRow struct {
		S string `json:"s"`
	}
	for _, s := range odd {
		rows, err := QueryP[strRow](c, "SELECT {s:String} AS s", Params{"s": s})
		if err != nil || len(rows) != 1 || rows[0].S != s {
			t.Errorf("string %q: %v %v", s, rows, err)
		}
	}

	type arrRow struct {
		A []string `json:"a"`
		N uint64   `json:"n"`
	}
	big := make([]string, 30000)
	for i := range big {
		big[i] = fmt.Sprintf("sample_%05d", i)
	}
	copy(big, odd)
	for _, ids := range [][]string{{}, odd, big} {
		rows, err := QueryP[arrRow](c,
			"SELECT {ids:Array(String)} AS a, countIf(x IN {ids:Array(String)}) AS n FROM (SELECT arrayJoin({ids:Array(String)}) AS x)",
			Params{"ids": ids})
		if err != nil || len(rows) != 1 {
			t.Fatalf("%d ids: %v %v", len(ids), rows, err)
		}
		if !reflect.DeepEqual(append([]string{}, rows[0].A...), append([]string{}, ids...)) || rows[0].N != uint64(len(ids)) {
			t.Errorf("%d ids: round trip got %d values, %d matched", len(ids), len(rows[0].A), rows[0].N)
		}
	}

	pairs := make([][2]string, 10000)
	for i := range pairs {
		pairs[i] = [2]string{fmt.Sprintf("study'%d", i), fmt.Sprintf("s\\%d", i)}
	}
	for _, ps := range [][][2]string{{{"a", "b"}}, pairs} {
		rows, err := QueryP[struct {
			N uint64 `json:"n"`
		}](c, "SELECT countIf((a, b) IN {p:Array(Tuple(String, String))}) AS n FROM (SELECT tupleElement(t, 1) AS a, tupleElement(t, 2) AS b FROM (SELECT arrayJoin({p:Array(Tuple(String, String))}) AS t))",
			Params{"p": ps})
		if err != nil || len(rows) != 1 || rows[0].N != uint64(len(ps)) {
			t.Errorf("%d pairs: %v %v", len(ps), rows, err)
		}
	}
}

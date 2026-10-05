package clickhouse

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unsafe"
)

func TestPlainType(t *testing.T) {
	for typ, want := range map[string]bool{
		"String": true, "LowCardinality(String)": true, "LowCardinality(Nullable(String))": true,
		"FixedString(3)": true, "Enum8('a' = 1, 'b,c' = 2)": true, "Int8": true, "UInt64": true,
		"Float32": true, "Bool": true, "Nullable(Float64)": true, "Array(Nullable(String))": true,
		"Array(Array(UInt32))": true, "Array(LowCardinality(String))": true,
		"Map(String, Int64)": false, "Date": false, "DateTime('UTC')": false, "Decimal(18, 2)": false,
		"UUID": false, "IPv4": false, "Tuple(String, String)": false, "Array(Tuple(String, String))": false,
		"Nullable(Nothing)": false, "Int128": false, "JSON": false,
	} {
		if plainType.MatchString(typ) != want {
			t.Errorf("%s: %v", typ, !want)
		}
	}
}

// TestJSONFields checks the field resolution against encoding/json itself:
// each key set alone in an object must land in the field json picks.
func TestJSONFields(t *testing.T) {
	type Inner struct {
		A    string `json:"a"`
		Deep string
	}
	type inner2 struct {
		Low string `json:"low"`
	}
	type row struct {
		Inner
		inner2
		Deep    string
		Plain   string
		Skipped string `json:"-"`
		hidden  string
		Hyphen  string `json:"-,"`
		Opt     string `json:"opt,omitempty"`
	}
	fields, ok := jsonFields(reflect.TypeFor[row](), 0)
	if !ok {
		t.Fatal("unresolved")
	}
	keys := []string{"a", "A", "deep", "DEEP", "low", "plain", "PLAIN", "skipped", "Skipped", "hidden", "-", "opt", "OPT", "missing"}
	for _, key := range keys {
		var want, got row
		if err := json.Unmarshal([]byte(`{"`+key+`":"x"}`), &want); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		for _, f := range fields {
			if f.name == key || strings.EqualFold(f.name, key) {
				*(*string)(unsafe.Add(unsafe.Pointer(&got), f.offset)) = "x"
				break
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("key %s: got %+v, want %+v", key, got, want)
		}
	}
	type Other struct{ Deep string }
	type conflict struct {
		Inner
		Other
	}
	type foldConflict struct {
		Inner
		DEEP string
	}
	type quoted struct {
		N int64 `json:"n,string"`
	}
	type folded struct {
		A  string `json:"a"`
		A2 string `json:"A"`
	}
	type ptr struct{ *Inner }
	for _, typ := range []reflect.Type{reflect.TypeFor[conflict](), reflect.TypeFor[foldConflict](), reflect.TypeFor[quoted](), reflect.TypeFor[folded](), reflect.TypeFor[ptr]()} {
		if _, ok := jsonFields(typ, 0); ok {
			t.Errorf("%s should go through encoding/json", typ)
		}
	}
}

func TestJSONString(t *testing.T) {
	for _, s := range []string{"plain", "", "ünï ✓🧬", "\xc3(\xffA", "\xed\xa0\x80", "a\xe2\x82", "\x00\x7f"} {
		var want string
		// ClickHouse writes the bytes unchanged, escaping only quotes,
		// backslashes and control characters.
		var b strings.Builder
		b.WriteByte('"')
		for i := 0; i < len(s); i++ {
			switch c := s[i]; {
			case c == '"' || c == '\\':
				b.WriteByte('\\')
				b.WriteByte(c)
			case c < 0x20:
				fmt.Fprintf(&b, `\u%04x`, c)
			default:
				b.WriteByte(c)
			}
		}
		b.WriteByte('"')
		if err := json.Unmarshal([]byte(b.String()), &want); err != nil {
			t.Fatal(err)
		}
		if got := jsonString(s); got != want {
			t.Errorf("%q: %q, want %q", s, got, want)
		}
	}
}

// TestConversions checks each conversion against JSON decoding of the text
// ClickHouse writes for the same value.
func TestConversions(t *testing.T) {
	check := func(ctName string, src any, text string, target any) {
		t.Helper()
		nt := reflect.TypeOf(src)
		ft := reflect.TypeOf(target).Elem()
		sv := reflect.New(nt)
		sv.Elem().Set(reflect.ValueOf(src))
		want := reflect.New(ft)
		wrapped := reflect.New(reflect.StructOf([]reflect.StructField{{Name: "V", Type: ft, Tag: `json:"v"`}}))
		wantErr := json.Unmarshal([]byte(`{"v":`+text+`}`), wrapped.Interface())
		want.Elem().Set(wrapped.Elem().Field(0))
		got := reflect.New(ft)
		conv, ok := buildConv(nt, ft)
		if !ok {
			t.Errorf("%s -> %s: no conversion", ctName, ft)
			return
		}
		_, gotErr := conv(sv.UnsafePointer(), got.UnsafePointer())
		if (gotErr != nil) != (wantErr != nil) {
			t.Errorf("%s %s -> %s: error %v, want %v", ctName, text, ft, gotErr, wantErr)
			return
		}
		g, _ := json.Marshal(got.Interface())
		w, _ := json.Marshal(want.Interface())
		if string(g) != string(w) || !reflect.DeepEqual(got.Interface(), want.Interface()) {
			t.Errorf("%s %s -> %s: %s, want %s", ctName, text, ft, g, w)
		}
	}
	s := func(v string) *string { return &v }
	f := func(v float64) *float64 { return &v }
	check("UInt64", uint64(42), "42", new(int64))
	check("UInt64", uint64(math.MaxUint64), "18446744073709551615", new(int64))
	check("UInt64", uint64(math.MaxUint64), "18446744073709551615", new(float64))
	check("Int64", int64(-9007199254740993), "-9007199254740993", new(float64))
	check("Int64", int64(-1), "-1", new(uint64))
	check("Int32", int32(-70000), "-70000", new(int16))
	check("Int32", int32(-70000), "-70000", new(int))
	check("UInt8", uint8(200), "200", new(int8))
	check("UInt8", uint8(200), "200", new(*int64))
	check("UInt32", uint32(7), "7", new(uint16))
	check("Float32", float32(0.1), "0.1", new(float64))
	check("Float32", float32(1e-45), "1e-45", new(float64))
	check("Float32", float32(16777216), "16777216", new(float64))
	check("Float32", float32(math.NaN()), "null", new(float64))
	check("Float32", float32(0.1), "0.1", new(float32))
	check("Float32", float32(math.Inf(1)), "null", new(*float64))
	check("Float64", 0.1, "0.1", new(float64))
	check("Float64", math.Inf(-1), "null", new(float64))
	check("Float64", math.NaN(), "null", new(*float64))
	check("Float64", math.Copysign(0, -1), "-0", new(float64))
	check("Nullable(Float64)", f(math.NaN()), "null", new(*float64))
	check("Nullable(Float64)", f(2.5), "2.5", new(float64))
	check("Nullable(Float64)", (*float64)(nil), "null", new(float64))
	check("Nullable(String)", (*string)(nil), "null", new(string))
	check("Nullable(String)", s("\xff"), "\"\xff\"", new(string))
	check("Nullable(String)", s("x"), `"x"`, new(*string))
	check("LowCardinality(Nullable(String))", (*string)(nil), "null", new(*string))
	check("String", "\xc3(", "\"\xc3(\"", new(string))
	check("String", "x", `"x"`, new(*string))
	type named string
	check("String", "x", `"x"`, new(named))
	check("Bool", true, "true", new(bool))
	check("Array(String)", []string{}, "[]", new([]string))
	check("Array(String)", []string{"a", "\xff"}, "[\"a\",\"\xff\"]", new([]string))
	check("Array(UInt64)", []uint64{1, 2}, "[1,2]", new([]int64))
	check("Array(UInt64)", []uint64{}, "[]", new([]int64))
	check("Array(Float64)", []float64{0.5, math.NaN()}, "[0.5,null]", new([]float64))
	check("Array(Nullable(Float64))", []*float64{f(0.5), f(math.NaN()), nil}, "[0.5,null,null]", new([]*float64))
	check("Array(Nullable(String))", []*string{nil, s("x")}, `[null,"x"]`, new([]string))
	check("Array(Array(String))", [][]string{{}, {"a"}}, `[[],["a"]]`, new([][]string))
}

func TestDriverQuery(t *testing.T) {
	ids := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "SAMPLE-" + strconv.Itoa(i)
		}
		return out
	}
	const q = "SELECT 1 WHERE x IN {ids:Array(String)} AND y = {s:String}"
	// Up to a form field's size, arrays ride in the URL.
	sql, opts, ok := driverQuery(q, Params{"ids": ids(3000), "s": "v"})
	if !ok || len(opts) != 1 || sql != q {
		t.Fatalf("medium: %v %s %d", ok, sql, len(opts))
	}
	// Longer ones are external tables.
	sql, opts, ok = driverQuery(q, Params{"ids": ids(10000), "s": "v"})
	if !ok || len(opts) != 2 || !strings.Contains(sql, "(SELECT v FROM _param_ids)") || !strings.Contains(sql, "{s:String}") {
		t.Fatalf("big: %v %s %d", ok, sql, len(opts))
	}
	// Past the URL budget, the largest arrays move to external tables.
	p := Params{}
	var sb strings.Builder
	sb.WriteString("SELECT 1")
	for i := range 12 {
		name := "a" + strconv.Itoa(i)
		p[name] = ids(3000 + i)
		sb.WriteString(" AND x IN {" + name + ":Array(String)}")
	}
	sql, opts, ok = driverQuery(sb.String(), p)
	if !ok || len(opts) != 2 || !strings.Contains(sql, "_param_a11") || !strings.Contains(sql, "{a0:Array(String)}") {
		t.Fatalf("budget: %v %s %d", ok, sql, len(opts))
	}
	if _, _, ok = driverQuery("SELECT {s:String}", Params{"s": strings.Repeat("x", maxDriverURLParams+1)}); ok {
		t.Fatal("a scalar past the URL budget should go through newRequest")
	}
	if _, _, ok = driverQuery(q+strings.Repeat(" ", maxFormQuery), Params{"ids": ids(10000)}); ok {
		t.Fatal("a long statement with external tables should go through newRequest")
	}
}

// compareLive runs sql through both decoders and requires identical results
// (and the driver path to be taken unless wantJSON).
func compareLive[T any](t *testing.T, c *Client, sql string, p Params, wantJSON bool) []T {
	t.Helper()
	want, wantErr := queryJSON[T](c, sql, p)
	got, ok := queryNative[T](c, sql, p)
	if ok == wantJSON {
		t.Fatalf("%.80s: native ok=%v", sql, ok)
	}
	if !ok {
		got, err := QueryP[T](c, sql, p)
		if fmt.Sprint(err) != fmt.Sprint(wantErr) || !reflect.DeepEqual(got, want) {
			t.Fatalf("%.80s: fallback differs: %v / %v", sql, err, wantErr)
		}
		return want
	}
	if wantErr != nil {
		t.Fatalf("%.80s: JSON error %v, native succeeded", sql, wantErr)
	}
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) || !reflect.DeepEqual(got, want) {
		gs, ws := string(g), string(w)
		for i := 0; i < len(gs) && i < len(ws); i++ {
			if gs[i] != ws[i] {
				lo := max(0, i-200)
				gs, ws = gs[lo:min(len(gs), i+200)], ws[lo:min(len(ws), i+200)]
				break
			}
		}
		t.Fatalf("%.80s:\nnative %s\njson   %s", sql, gs, ws)
	}
	return got
}

type liveEmbedded struct {
	Emb   string `json:"emb"`
	Plain string
}

type liveRow struct {
	liveEmbedded
	I8       int8       `json:"i8"`
	I16      int        `json:"i16"`
	I32      int32      `json:"i32"`
	I64      int64      `json:"i64"`
	I64F     float64    `json:"i64f"`
	U8       uint8      `json:"u8"`
	U16      uint16     `json:"u16"`
	U32      int64      `json:"u32"`
	U64      uint64     `json:"u64"`
	Cnt      int64      `json:"cnt"`
	CntP     *int64     `json:"cntp"`
	F32      float32    `json:"f32"`
	F32D     float64    `json:"f32d"`
	F64      float64    `json:"f64"`
	F32Nan   *float64   `json:"f32nan"`
	F64Inf   float64    `json:"f64inf"`
	NegZero  float64    `json:"negzero"`
	S        string     `json:"s"`
	Bad      string     `json:"bad"`
	FS       string     `json:"fs"`
	LC       string     `json:"lc"`
	En       string     `json:"en"`
	B        bool       `json:"b"`
	NS       string     `json:"ns"`
	NSP      *string    `json:"nsp"`
	NS2      *string    `json:"ns2"`
	NI       *int64     `json:"ni"`
	NIV      int64      `json:"niv"`
	NF       *float64   `json:"nf"`
	NFV      float64    `json:"nfv"`
	Arr      []string   `json:"arr"`
	EArr     []string   `json:"earr"`
	IArr     []int64    `json:"iarr"`
	UArr     []int64    `json:"uarr"`
	FArr     []float64  `json:"farr"`
	NArr     []*string  `json:"narr"`
	AArr     [][]uint32 `json:"aarr"`
	LCN      *string    `json:"lcn"`
	Missing  string     `json:"missing"`
	MixedCas string
}

const liveSelect = `SELECT
	'e' AS emb, 'p' AS PLAIN, 'mc' AS mixedcas,
	toInt8(-5) AS i8, toInt16(-300) AS i16, toInt32(-70000) AS i32, toInt64(-9007199254740993) AS i64, toInt64(-9007199254740993) AS i64f,
	toUInt8(250) AS u8, toUInt16(65000) AS u16, toUInt32(4000000000) AS u32, toUInt64(18446744073709551615) AS u64,
	toUInt64(7) AS cnt, toUInt64(8) AS cntp,
	toFloat32(0.1) AS f32, toFloat32(0.1) AS f32d, 0.1 AS f64, toFloat32(nan) AS f32nan, -inf AS f64inf, -0. AS negzero,
	'héllo' AS s, unhex('C328FF41') AS bad, toFixedString('ab', 4) AS fs, toLowCardinality('lc') AS lc,
	CAST('b', 'Enum8(\'a\' = 1, \'b\' = 2)') AS en, true AS b,
	CAST(NULL, 'Nullable(String)') AS ns, CAST(NULL, 'Nullable(String)') AS nsp, CAST('x', 'Nullable(String)') AS ns2,
	CAST(NULL, 'Nullable(Int64)') AS ni, CAST(3, 'Nullable(Int64)') AS niv,
	CAST(nan, 'Nullable(Float64)') AS nf, CAST(NULL, 'Nullable(Float64)') AS nfv,
	['a', '', unhex('FF')] AS arr, CAST([], 'Array(String)') AS earr, CAST([1, -2], 'Array(Int64)') AS iarr, CAST([1, 2], 'Array(UInt64)') AS uarr,
	[0.5, nan, inf] AS farr, CAST([NULL, 'x'], 'Array(Nullable(String))') AS narr, CAST([[1], [], [2, 3]], 'Array(Array(UInt32))') AS aarr,
	toLowCardinality(CAST(NULL, 'Nullable(String)')) AS lcn, 'ignored' AS unmatched`

func liveClient(t *testing.T) *Client {
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	return New(base, "default")
}

func TestLiveNativeTypes(t *testing.T) {
	c := liveClient(t)
	rows := compareLive[liveRow](t, c, liveSelect, nil, false)
	if len(rows) != 1 || rows[0].Bad != "\uFFFD(\uFFFDA" || rows[0].F32D != 0.1 || rows[0].EArr == nil || rows[0].Plain != "p" {
		t.Fatalf("unexpected row %+v", rows)
	}
	// Many rows across several blocks.
	type numRow struct {
		N   uint64   `json:"n"`
		S   string   `json:"s"`
		F   float64  `json:"f"`
		F32 float64  `json:"f32"`
		G   float32  `json:"g"`
		NS  *string  `json:"ns"`
		A   []string `json:"a"`
	}
	compareLive[numRow](t, c, `SELECT number AS n, toString(number) AS s,
		reinterpretAsFloat64(sipHash64(number)) AS f, reinterpretAsFloat32(toUInt32(sipHash64(number) % 4294967296)) AS f32,
		reinterpretAsFloat32(toUInt32(cityHash64(number) % 4294967296)) AS g,
		if(number % 3 = 0, NULL, toString(number)) AS ns, arrayMap(x -> toString(x), range(number % 4)) AS a
		FROM numbers(300000)`, nil, false)
	// Columns without a field are dropped whatever their type.
	compareLive[numRow](t, c, "SELECT number AS n, (number, 'a') AS t, toUUID('61f0c404-5cb3-11e7-907b-a6006ad3dba0') AS u FROM numbers(3)", nil, false)
	// Empty result.
	got := compareLive[numRow](t, c, "SELECT number AS n FROM numbers(10) WHERE number > 100", nil, false)
	if got == nil {
		t.Fatal("empty result must be an empty slice")
	}
}

func TestLiveFallback(t *testing.T) {
	c := liveClient(t)
	type anyRow struct {
		V any `json:"v"`
	}
	compareLive[anyRow](t, c, "SELECT 1 AS v", nil, true)
	type rawRow struct {
		V json.RawMessage `json:"v"`
	}
	compareLive[rawRow](t, c, "SELECT [1, 2] AS v", nil, true)
	type strRow struct {
		V string `json:"v"`
	}
	compareLive[strRow](t, c, "SELECT (1, 'a') AS v", nil, true)
	compareLive[strRow](t, c, "SELECT toUUID('61f0c404-5cb3-11e7-907b-a6006ad3dba0') AS v", nil, true)
	compareLive[strRow](t, c, "SELECT 1 AS v", nil, true) // number into string: JSON error
	compareLive[strRow](t, c, "SELECT toDateTime64(0, 3) AS v", nil, true)
	compareLive[strRow](t, c, "SELECT toDate('2024-01-02') AS v", nil, true)
	type floatRow struct {
		V float64 `json:"v"`
	}
	compareLive[floatRow](t, c, "SELECT toDecimal64(1.50, 2) AS v", nil, true)
	type mapRow struct {
		V map[string]int64 `json:"v"`
	}
	compareLive[mapRow](t, c, "SELECT map('k', 1) AS v", nil, true)
	type intRow struct {
		V int64 `json:"v"`
	}
	compareLive[intRow](t, c, "SELECT 1.5 AS v", nil, true)
	type smallRow struct {
		V int8 `json:"v"`
	}
	compareLive[smallRow](t, c, "SELECT 300 AS v", nil, true) // overflow: the JSON path reports it
	compareLive[map[string]any](t, c, "SELECT 1 AS v", nil, true)
	type quoted struct {
		V int64 `json:"v,string"`
	}
	compareLive[quoted](t, c, "SELECT '5' AS v", nil, true)
	// A query error comes from the JSON path with its message.
	_, err := QueryP[strRow](c, "SELECT v FROM no_such_table_xyz", nil)
	if err == nil || !strings.Contains(err.Error(), "UNKNOWN_TABLE") {
		t.Fatalf("error %v", err)
	}
}

func TestLiveNativeParams(t *testing.T) {
	c := liveClient(t)
	type row struct {
		N     uint64 `json:"n"`
		First string `json:"first"`
		Len   uint64 `json:"len"`
	}
	for _, n := range []int{0, 3, 2000, 6000, 20000} {
		ids := make([]string, n)
		for i := range ids {
			ids[i] = "id'\\\t" + strconv.Itoa(i)
		}
		pairs := make([][2]string, n)
		for i := range pairs {
			pairs[i] = [2]string{"s" + strconv.Itoa(i), "p\n" + strconv.Itoa(i)}
		}
		nums := make([]int64, n)
		for i := range nums {
			nums[i] = int64(i * 7)
		}
		p := Params{"ids": ids, "pairs": pairs, "nums": nums, "s": "x'y", "k": int64(3)}
		sql := `SELECT
			(SELECT count() FROM numbers(30000) WHERE concat('id\'\\\t', toString(number)) IN {ids:Array(String)}) AS n,
			arrayStringConcat(arrayMap(t -> concat(t.1, t.2), arraySlice({pairs:Array(Tuple(String, String))}, 1, 3)), '|') || {s:String} AS first,
			length({nums:Array(Int64)}) + {k:Int64} AS len`
		got := compareLive[row](t, c, sql, p, false)
		if got[0].N != uint64(min(n, 30000)) || got[0].Len != uint64(n+3) {
			t.Fatalf("n=%d: %+v", n, got)
		}
	}
}

// BenchmarkLiveDecode compares the decoders on a large result
// (CLICKHOUSE_TEST_URL must be set).
func BenchmarkLiveDecode(b *testing.B) {
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		b.Skip("CLICKHOUSE_TEST_URL not set")
	}
	c := New(base, "default")
	type row struct {
		MolecularProfileID string `json:"molecularProfileId"`
		SampleID           string `json:"sampleId"`
		PatientID          string `json:"patientId"`
		StudyID            string `json:"studyId"`
		GenePanelID        string `json:"genePanelId"`
		Profiled           bool   `json:"profiled"`
		N                  int64  `json:"n"`
	}
	sql := `SELECT 'msk_impact_50k_2026_mutations' AS molecularProfileId, concat('P-', toString(number), '-T01-IM6') AS sampleId,
		concat('P-', toString(number)) AS patientId, 'msk_impact_50k_2026' AS studyId, 'IMPACT468' AS genePanelId,
		true AS profiled, number AS n FROM numbers(300000)`
	for _, name := range []string{"native", "json"} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var err error
				if name == "native" {
					_, ok := queryNative[row](c, sql, nil)
					if !ok {
						b.Fatal("fallback")
					}
				} else {
					_, err = queryJSON[row](c, sql, nil)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

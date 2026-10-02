package databin

import (
	"bufio"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

func readTSV(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows [][]string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		rows = append(rows, strings.Split(sc.Text(), "\t"))
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestIsCreatableAndParseMatchJava checks NumberUtils.isCreatable and
// new BigDecimal(s).toString() against outputs recorded from the JVM
// (testdata/numbers.tsv: input, isCreatable, toString or ERR).
func TestIsCreatableAndParseMatchJava(t *testing.T) {
	rows := readTSV(t, "testdata/numbers.tsv")
	if len(rows) < 1000 {
		t.Fatalf("expected a large corpus, got %d rows", len(rows))
	}
	for _, r := range rows {
		in, wantCreatable, wantParse := r[0], r[1] == "1", r[2]
		if got := isCreatable(in); got != wantCreatable {
			t.Errorf("isCreatable(%q) = %v, want %v", in, got, wantCreatable)
		}
		d, err := ParseDecimal(in)
		got := "ERR"
		if err == nil {
			got = d.String()
		}
		if got != wantParse {
			t.Errorf("BigDecimal(%q) = %s, want %s", in, got, wantParse)
		}
	}
}

// TestDecimalFromFloat64MatchesJava checks BigDecimal.valueOf(double)
// against outputs recorded from the JVM (testdata/doubles.tsv: IEEE bits in
// hex, toString).
func TestDecimalFromFloat64MatchesJava(t *testing.T) {
	for _, r := range readTSV(t, "testdata/doubles.tsv") {
		bits, err := strconv.ParseUint(r[0], 16, 64)
		if err != nil {
			t.Fatal(err)
		}
		f := math.Float64frombits(bits)
		if got := DecimalFromFloat64(f).String(); got != r[1] {
			t.Errorf("valueOf(%v) = %s, want %s", f, got, r[1])
		}
	}
}

func TestDecimalArithmetic(t *testing.T) {
	d := MustParseDecimal
	cases := []struct {
		name string
		got  Decimal
		want string
	}{
		{"median 3+4 / 2.0", d("7").divExact(d("2.0")), "3.5"},
		{"median 10+10 / 2.0", d("20").divExact(d("2.0")), "1E+1"},
		{"median 2+4 / 2.0", d("6").divExact(d("2.0")), "3"},
		{"divide scale up", d("1").divExact(d("8")), "0.125"},
		{"divide preferred scale", d("10.00").divExact(d("2")), "5.00"},
		{"zero dividend", d("0.0").divExact(d("2.00")), "0E+1"},
		{"remainder 45 % 10.0", d("45").rem(d("10.0")), "5.0"},
		{"remainder -62 % 10", d("-62").rem(d("10")), "-2"},
		{"remainder 7.5 % 2", d("7.5").rem(d("2")), "1.5"},
		{"remainder 3 % 10", d("3").rem(d("10")), "3"},
		{"remainder 40 % 5.0", d("40").rem(d("5.0")), "0.0"},
		{"remainder 400 % 5", d("400").rem(d("5")), "0"},
		{"remainder 4000 % 0.5", d("4000").rem(d("0.5")), "0"},
		{"remainder 4000.00 % 50", d("4000.00").rem(d("50")), "0.00"},
		{"remainder 1E+3 % 7", d("1E+3").rem(d("7")), "6"},
		{"remainder 1E+3 % 1E+2", d("1E+3").rem(d("1E+2")), "0E+3"},
		{"remainder -0.05 % 0.02", d("-0.05").rem(d("0.02")), "-0.01"},
		{"setScale half up", d("0.12345").setScale(3, roundHalfUp), "0.123"},
		{"setScale half up .5", d("-0.0125").setScale(3, roundHalfUp), "-0.013"},
		{"setScale ceiling", d("12.01").setScale(1, roundCeiling), "12.1"},
		{"setScale floor negative", d("-12.01").setScale(1, roundFloor), "-12.1"},
		{"setScale up", d("12").setScale(2, roundFloor), "12.00"},
		{"strip zeros", d("100.00").stripTrailingZeros(), "1E+2"},
		{"strip zeros zero", d("0.000").stripTrailingZeros(), "0"},
		{"small plain", d("0.000001"), "0.000001"},
		{"small sci", d("0.0000001"), "1E-7"},
		{"neg scale", d("-1.5E+3"), "-1.5E+3"},
	}
	for _, c := range cases {
		if c.got.String() != c.want {
			t.Errorf("%s: got %s, want %s", c.name, c.got.String(), c.want)
		}
	}
	if p := d("0.00123").precision(); p != 3 {
		t.Errorf("precision = %d", p)
	}
	if e := calcExponent(d("0.00123")); e != -3 {
		t.Errorf("exponent = %d", e)
	}
	if v := d("-12.9").longValue(); v != -12 {
		t.Errorf("longValue = %d", v)
	}
	if d("2").Cmp(d("2.000")) != 0 || d("2").identical(d("2.0")) {
		t.Error("compare/equals semantics")
	}
	if d("123456789012345678901234567890").Cmp(d("1.23456789012345678901234567891E+29")) != -1 {
		t.Error("big compare")
	}
}

func TestDecimalJSON(t *testing.T) {
	var x struct {
		A Decimal   `json:"a"`
		B []Decimal `json:"b"`
	}
	if err := jsonUnmarshal(`{"a": 18.0, "b": [1, 2.50, 1e3]}`, &x); err != nil {
		t.Fatal(err)
	}
	if x.A.String() != "18.0" || x.B[0].String() != "1" || x.B[1].String() != "2.50" || x.B[2].String() != "1E+3" {
		t.Errorf("unexpected decode %v %v", x.A, x.B)
	}
	b, err := jsonMarshal(Bin{AttributeID: "AGE", Start: decPtr(MustParseDecimal("1.0E+8")), Count: 2})
	if err != nil {
		t.Fatal(err)
	}
	if b != `{"attributeId":"AGE","start":1.0E+8,"count":2}` {
		t.Errorf("marshal = %s", b)
	}
}

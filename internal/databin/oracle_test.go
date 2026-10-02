package databin

// TestJavaOracle replays scenarios whose expected bins were recorded by
// running the unmodified Java DataBinner (with its helpers, Guava and
// commons-lang3 3.17) on the JVM. It compares every bin exactly, including
// the BigDecimal string form of each edge.
//
// testdata/oracle_scenarios.txt holds tab-separated directives:
//
//	CASE name | ATTR id | MODE STATIC|DYNAMIC | BINMETHOD m|null
//	START d | END d | CUSTOM d,d,... | GEN binSize,anchor | NOLOG bool
//	THRESH n | U value | F value | UMOCK key | FMOCK key
//	FMOCKIDX key i,j,... | RUN
//
// U/F add one unfiltered/filtered value; the MOCK forms add values from
// testdata/mockdata.json (the Java DataBinnerMocker fixtures). STATIC bins
// the unfiltered values and recounts the filtered ones; DYNAMIC bins the
// filtered values. testdata/oracle_expected.txt holds, per case, the line
// "CASE name", then "BIN special start end count" lines ("null" for absent
// fields) and "DONE", or "ERR exception" when Java threw.

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

type oracleCase struct {
	name       string
	attr       string
	opts       Options
	unfiltered []Value
	filtered   []Value
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

func parseOracleScenarios(t *testing.T, mock map[string][]string) []oracleCase {
	var cases []oracleCase
	var c oracleCase
	for _, line := range readLines(t, "testdata/oracle_scenarios.txt") {
		k, v, _ := strings.Cut(line, "\t")
		switch k {
		case "CASE":
			c = oracleCase{name: v, opts: Options{Method: Dynamic, BinMethod: BinMethodCustom}}
		case "ATTR":
			c.attr = v
		case "MODE":
			c.opts.Method = Method(v)
		case "BINMETHOD":
			if v == "null" {
				c.opts.BinMethod = BinMethodNone
			} else {
				c.opts.BinMethod = BinMethod(v)
			}
		case "START":
			c.opts.Start = dec(v)
		case "END":
			c.opts.End = dec(v)
		case "CUSTOM":
			c.opts.CustomBins = []Decimal{}
			if v != "" {
				c.opts.CustomBins = decs(strings.Split(v, ",")...)
			}
		case "GEN":
			p := strings.Split(v, ",")
			c.opts.BinsGeneratorConfig = &BinsGeneratorConfig{BinSize: dec(p[0]), AnchorValue: dec(p[1])}
		case "NOLOG":
			c.opts.DisableLogScale = v == "true"
		case "THRESH":
			n, err := strconv.Atoi(v)
			if err != nil {
				t.Fatal(err)
			}
			c.opts.DistinctValueThreshold = n
		case "U":
			c.unfiltered = append(c.unfiltered, Value{Value: v, Count: 1})
		case "F":
			c.filtered = append(c.filtered, Value{Value: v, Count: 1})
		case "UMOCK":
			c.unfiltered = append(c.unfiltered, perCase(mock[v])...)
		case "FMOCK":
			c.filtered = append(c.filtered, perCase(mock[v])...)
		case "FMOCKIDX":
			key, idx, _ := strings.Cut(v, "\t")
			if idx != "" {
				for s := range strings.SplitSeq(idx, ",") {
					i, err := strconv.Atoi(s)
					if err != nil {
						t.Fatal(err)
					}
					c.filtered = append(c.filtered, Value{Value: mock[key][i], Count: 1})
				}
			}
		case "RUN":
			cases = append(cases, c)
		default:
			t.Fatalf("unknown directive %q", k)
		}
	}
	return cases
}

func parseOracleExpected(t *testing.T) map[string][]string {
	out := map[string][]string{}
	var name string
	for _, line := range readLines(t, "testdata/oracle_expected.txt") {
		k, v, _ := strings.Cut(line, "\t")
		switch k {
		case "CASE":
			name = v
			out[name] = nil
		case "BIN", "ERR":
			out[name] = append(out[name], line)
		case "DONE":
		default:
			t.Fatalf("unknown expected line %q", line)
		}
	}
	return out
}

func oracleLines(bins []Bin) []string {
	var out []string
	for _, b := range bins {
		s := "null"
		if b.SpecialValue != nil {
			s = *b.SpecialValue
		}
		out = append(out, "BIN\t"+s+"\t"+fmtDec(b.Start)+"\t"+fmtDec(b.End)+"\t"+itoa(b.Count))
	}
	return out
}

func TestJavaOracle(t *testing.T) {
	mock := loadMockData(t)
	cases := parseOracleScenarios(t, mock)
	expected := parseOracleExpected(t)
	if len(cases) < 1000 {
		t.Fatalf("expected a large scenario corpus, got %d", len(cases))
	}
	for _, c := range cases {
		want, ok := expected[c.name]
		if !ok {
			t.Fatalf("no expected output for %s", c.name)
		}
		bins, err := Calculate(c.attr, c.unfiltered, c.filtered, c.opts)
		if len(want) == 1 && strings.HasPrefix(want[0], "ERR") {
			if err == nil {
				t.Errorf("%s: Java threw %s, Go returned %s", c.name, want[0], formatBins(bins))
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
			continue
		}
		got := oracleLines(bins)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s:\n got: %s\nwant: %s", c.name, strings.Join(got, " | "), strings.Join(want, " | "))
		}
	}
}

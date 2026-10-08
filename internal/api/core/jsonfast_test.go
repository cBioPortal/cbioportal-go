package core

import (
	"encoding/json"
	"math"
	"testing"
)

func TestAppendJSONMatchesEncodingJSON(t *testing.T) {
	for _, s := range []string{"", "P-0000004-T01-IM3", `a"b`, `a\b`, "<x>&y", "tab\there", "é", " ", "\xff"} {
		want, _ := json.Marshal(s)
		if got := AppendJSONString(nil, []byte(s)); string(got) != string(want) {
			t.Errorf("string %q: got %s want %s", s, got, want)
		}
	}
	for _, f := range []float64{0, -0.3244, 1, 1e-7, -2.5e-9, 1e21, 123456789.125, 1e20, 0.000001, math.SmallestNonzeroFloat64, math.MaxFloat64} {
		want, _ := json.Marshal(f)
		got, err := AppendJSONFloat(nil, f)
		if err != nil || string(got) != string(want) {
			t.Errorf("float %v: got %s want %s", f, got, want)
		}
	}
	if _, err := AppendJSONFloat(nil, math.NaN()); err == nil {
		t.Error("NaN accepted")
	}
}

package comparison

import (
	"math"
	"testing"
)

// Expected values come from cbioportal's FisherExactTestCalculator.calcqValue
// and BigDecimal.valueOf run on the same p-values.

func TestJavaQValues(t *testing.T) {
	cases := []struct{ p, q []float64 }{
		{[]float64{0.01, 0.02, 0.03, 0.5, 1.0}, []float64{0.05, 0.05, 0.05, 0.6, 1.0}},
		{[]float64{1, 1, 1}, []float64{1, 1, 1}},
		{[]float64{1e-5, 0.0012, 0.2, 0.2, 0.33}, []float64{5e-5, 0.003, 0.3, 0.3, 0.33}},
		{[]float64{0.10403371626055691, 0.11749575239143617, 0.2565679723337805, 0.9},
			[]float64{0.23499150478287234, 0.23499150478287234, 0.342090629778374, 0.9}},
		{[]float64{0}, []float64{0}},
		{nil, []float64{}},
	}
	for _, c := range cases {
		got := javaQValues(c.p)
		if len(got) != len(c.q) {
			t.Fatalf("javaQValues(%v) = %v, want %v", c.p, got, c.q)
		}
		for i := range got {
			if got[i] != c.q[i] {
				t.Errorf("javaQValues(%v) = %v, want %v", c.p, got, c.q)
				break
			}
		}
	}
}

func TestJavaBigDecimalOf(t *testing.T) {
	cases := []struct {
		d        float64
		unscaled string
		scale    int
	}{
		{1.0, "10", 1}, {0.5, "5", 1}, {1e-5, "10", 6}, {0.001, "1", 3}, {0.0012345, "12345", 7},
		{123.0, "1230", 1}, {0.09532479250348536, "9532479250348536", 17}, {1e-300, "10", 301}, {0, "0", 1},
	}
	for _, c := range cases {
		u, s := javaBigDecimalOf(c.d)
		if u.String() != c.unscaled || s != c.scale {
			t.Errorf("javaBigDecimalOf(%v) = %s scale %d, want %s scale %d", c.d, u, s, c.unscaled, c.scale)
		}
	}
}

func TestJavaParseBinaryValue(t *testing.T) {
	ok := map[string]float64{
		"true": 1, "yes": 1, "false": 0, "no": 0, "1": 1, "0": 0, "1.0": 1, " 0.5 ": 0.5, "1e2": 100,
		"2d": 2, "3F": 3, "-Infinity": math.Inf(-1), "+Infinity": math.Inf(1),
	}
	for in, want := range ok {
		if got, good := javaParseBinaryValue(in); !good || got != want {
			t.Errorf("javaParseBinaryValue(%q) = %v, %v; want %v", in, got, good, want)
		}
	}
	if got, good := javaParseBinaryValue("NaN"); !good || !math.IsNaN(got) {
		t.Errorf("javaParseBinaryValue(NaN) = %v, %v", got, good)
	}
	for _, in := range []string{"NA", "TRUE", "True", "inf", "nan", "", "1_0", "--1"} {
		if _, good := javaParseBinaryValue(in); good {
			t.Errorf("javaParseBinaryValue(%q) accepted; Double.parseDouble throws", in)
		}
	}
}

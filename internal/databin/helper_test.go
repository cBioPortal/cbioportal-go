package databin

// This file ports org.cbioportal.legacy.web.util.DataBinHelperTest.

import (
	"strings"
	"testing"
)

func decList(numbers ...int64) []Decimal {
	out := make([]Decimal, len(numbers))
	for i, n := range numbers {
		out[i] = decimalFromInt(n)
	}
	return out
}

func decimalsString(values []Decimal) string {
	s := make([]string, len(values))
	for i, v := range values {
		s[i] = v.String()
	}
	return "[" + strings.Join(s, ", ") + "]"
}

// assertDecimalsEqual is List<BigDecimal>.equals: same length and
// element-wise BigDecimal.equals.
func assertDecimalsEqual(t *testing.T, want, got []Decimal) {
	t.Helper()
	ok := len(want) == len(got)
	for i := 0; ok && i < len(want); i++ {
		ok = want[i].identical(got[i])
	}
	if !ok {
		t.Errorf("got %s, want %s", decimalsString(got), decimalsString(want))
	}
}

func assertPanicsWith(t *testing.T, prefix string, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		je, ok := r.(javaError)
		if !ok || !strings.HasPrefix(string(je), prefix) {
			t.Errorf("expected %s, got %v", prefix, r)
		}
	}()
	f()
}

func TestGenerateBins(t *testing.T) {
	cases := []struct {
		name           string
		values         []Decimal
		binSize        int64
		anchor         int64
		wantBoundaries []Decimal
	}{
		{"rangeSmallerThanAnchor", decList(-62, -38), 10, 0, decList(-60, -50, -40)},
		{"rangeLargerThanAnchor", decList(-62, -38), 10, -70, decList(-60, -50, -40)},
		{"rangeSpansAnchor", decList(-62, -38), 10, -20, decList(-60, -50, -40)},
		{"rangeSmallerThanAnchorPositiveRange", decList(38, 62), 10, 100, decList(40, 50, 60)},
		{"rangeLargerThanAnchorPositiveRange", decList(38, 62), 10, 0, decList(40, 50, 60)},
		{"rangeSpansAnchorPositiveRange", decList(38, 62), 10, 40, decList(40, 50, 60)},
		{"singleValueNoBoundaries", decList(38), 10, 40, decList()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			size, anchor := decimalFromInt(c.binSize), decimalFromInt(c.anchor)
			assertDecimalsEqual(t, c.wantBoundaries, generateBins(c.values, &size, &anchor))
		})
	}
}

func TestGenerateBinsEmptyNumericalValues(t *testing.T) {
	size, anchor := decimalFromInt(10), decimalFromInt(40)
	if got := generateBins([]Decimal{}, &size, &anchor); got != nil {
		t.Errorf("got %s, want nil", decimalsString(got))
	}
}

func TestGenerateBinsNullArguments(t *testing.T) {
	ten, forty := decimalFromInt(10), decimalFromInt(40)
	t.Run("nullCustomBinsArg", func(t *testing.T) {
		assertPanicsWith(t, "IllegalArgumentException", func() { generateBins(nil, &ten, &forty) })
	})
	t.Run("nullBinSizeArg", func(t *testing.T) {
		assertPanicsWith(t, "IllegalArgumentException", func() { generateBins([]Decimal{}, nil, &forty) })
	})
	t.Run("nullAnchorPointArg", func(t *testing.T) {
		assertPanicsWith(t, "IllegalArgumentException", func() { generateBins([]Decimal{}, &ten, nil) })
	})
}

func TestQuantiles(t *testing.T) {
	cases := []struct {
		name string
		f    func([]Decimal) *Decimal
		in   []Decimal
		want string // "" means null
	}{
		{"testMedianEvenSeries", calcMedian, decList(1, 2, 3, 4, 5, 6), "3.5"},
		{"testMedianUnevenSeries", calcMedian, decList(1, 2, 3, 4, 5, 6, 7), "4"},
		{"testMedianNullSeries", calcMedian, nil, ""},
		{"testMedianEmptySeries", calcMedian, []Decimal{}, ""},
		{"testQ1EvenSeries", calcQ1, decList(1, 2, 3, 4, 5, 6), "2"},
		{"testQ1UnevenSeries", calcQ1, decList(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11), "3"},
		{"testQ1NullSeries", calcQ1, nil, ""},
		{"testQ1EmptySeries", calcQ1, []Decimal{}, ""},
		{"testQ3EvenSeries", calcQ3, decList(1, 2, 3, 4, 5, 6), "5"},
		{"testQ3UnevenSeries", calcQ3, decList(1, 2, 3, 4, 5, 6, 7, 8, 9), "7.5"},
		{"testQ3NullSeries", calcQ3, nil, ""},
		{"testQ3EmptySeries", calcQ3, []Decimal{}, ""},
		{"testValueCloseToQ1EvenSeries", valueCloseToQ1, decList(1, 2, 3, 4, 5, 6), "2"},
		{"testValueCloseToQ1UnevenSeries", valueCloseToQ1, decList(1, 2, 3, 4, 5, 6, 7, 8, 9), "3"},
		{"testValueCloseToQ1NullSeries", valueCloseToQ1, nil, ""},
		{"testValueCloseToQ1EmptySeries", valueCloseToQ1, []Decimal{}, ""},
		{"testValueCloseToQ3EvenSeries", valueCloseToQ3, decList(1, 2, 3, 4, 5, 6), "5"},
		{"testValueCloseToQ3UnevenSeries", valueCloseToQ3, decList(1, 2, 3, 4, 5, 6, 7, 8, 9), "7"},
		{"testValueCloseToQ3NullSeries", valueCloseToQ3, nil, ""},
		{"testValueCloseToQ3EmptySeries", valueCloseToQ3, []Decimal{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.f(c.in)
			if c.want == "" {
				if got != nil {
					t.Errorf("got %s, want null", got)
				}
				return
			}
			if got == nil || !MustParseDecimal(c.want).identical(*got) {
				t.Errorf("got %v, want %s", fmtDec(got), c.want)
			}
		})
	}
}

func TestCalcQuartileBoundaries(t *testing.T) {
	cases := []struct {
		name string
		in   []Decimal
		want []string
	}{
		{"testCalcQuartileBoundariesUniqueSeries", decList(1, 2, 3, 4, 5, 6, 7, 8, 9), []string{"2.5", "5", "7.5"}},
		{"testCalcQuartileBoundariesIdenticalSeries", decList(1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1), []string{"1"}},
		{"testCalcQuartileBoundariesIdenitcalUpperValues", decList(1, 1, 1, 10, 10, 10, 10, 10, 10), []string{"1", "10"}},
		{"testCalcQuartileBoundariesIdenticalLowerValues", decList(1, 1, 1, 1, 1, 1, 10, 10, 10), []string{"1", "10"}},
		{"testCalcQuartileBoundariesFractions", decList(1, 2, 3, 4, 5, 6, 7, 8), []string{"2.5", "4.5", "6.5"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := calcQuartileBoundaries(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("got %s, want %v", decimalsString(got), c.want)
			}
			for i, w := range c.want {
				if got[i].Cmp(MustParseDecimal(w)) != 0 {
					t.Errorf("boundary %d = %s, want %s", i, got[i], w)
				}
			}
		})
	}
}

func TestCalcInterquartileRangeApproximation(t *testing.T) {
	cases := []struct {
		name     string
		in       []Decimal
		lo, high string
	}{
		{"testCalcInterquartileRangeApproximation", decList(1, 2, 3, 4, 5, 6, 7, 8), "3", "7"},
		{"testCalsIqrUnevenSeries", decList(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11), "3", "9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := calcInterquartileRangeApproximation(c.in)
			if !r.lowerEndpoint().identical(MustParseDecimal(c.lo)) || !r.upperEndpoint().identical(MustParseDecimal(c.high)) {
				t.Errorf("got [%s, %s), want [%s, %s)", r.lowerEndpoint(), r.upperEndpoint(), c.lo, c.high)
			}
		})
	}
}

package api

import (
	"encoding/json"
	"testing"
)

func rangeValue(start, end string) dataFilterValue {
	var v dataFilterValue
	if start != "" {
		n := json.Number(start)
		v.Start = &n
	}
	if end != "" {
		n := json.Number(end)
		v.End = &n
	}
	return v
}

func TestMergeDataFilters(t *testing.T) {
	values := func(f *clinicalDataFilter) *[]dataFilterValue { return &f.Values }
	consecutive := []clinicalDataFilter{{AttributeID: "AGE", Values: []dataFilterValue{
		rangeValue("", "10"), rangeValue("10", "20"), rangeValue("20", ""),
	}}}
	got := mergeDataFilters(consecutive, values)
	if len(got[0].Values) != 1 || got[0].Values[0].Start != nil || got[0].Values[0].End != nil {
		t.Errorf("consecutive bins: got %+v, want one open range", got[0].Values)
	}

	// A single-value bin (start == end) is not a valid bin sequence, so
	// DataFilterUtil leaves every filter of the request unmerged: 14 stays
	// selected by [14, 14] next to (14, ∞).
	exact := []clinicalDataFilter{
		{AttributeID: "AGE", Values: []dataFilterValue{rangeValue("", "10"), rangeValue("10", "20")}},
		{AttributeID: "MUTATION_COUNT", Values: []dataFilterValue{rangeValue("13", "13"), rangeValue("14", "14"), rangeValue("14", "")}},
	}
	got = mergeDataFilters(exact, values)
	if len(got[0].Values) != 2 || len(got[1].Values) != 3 {
		t.Errorf("invalid sequence: got %d and %d values, want 2 and 3 (unmerged)", len(got[0].Values), len(got[1].Values))
	}
}

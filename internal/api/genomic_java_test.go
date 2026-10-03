package api

import "testing"

func TestMutationKeyword(t *testing.T) {
	cases := []struct{ hugo, pc, mt, want string }{
		{"MUC4", "A2542T", "Missense_Mutation", "MUC4 A2542 missense"},
		{"tp53", "A138Cfs*27", "Frame_Shift_Del", "TP53 truncating"},
		{"TP53", "X125_splice", "Splice_Site", "TP53 truncating"},
		{"BRAF", "M1V", "Missense_Mutation", "BRAF truncating"},
		{"EGFR", "746_750ELREA>A", "Missense_Mutation", "EGFR 746-750 missense"},
		{"EGFR", "E746_A750del", "In_Frame_Del", ""},
		{"EGFR", "ELREA746del", "In_Frame_Del", "EGFR 746-750 deletion"},
		{"EGFR", "746_750ELREA>A", "In_Frame_Del", "EGFR 746-750 deletion"},
		{"ERBB2", "A775_G776insYVMA", "In_Frame_Ins", ""},
		{"ERBB2", "775_776AG>AYVMAG", "In_Frame_Ins", "ERBB2 775 insertion"},
		{"KRAS", "G12G", "Silent", "KRAS 12silent"},
		{"EGFR", "vIII", "vIII deletion", "EGFR vIII"},
		{"KRAS", "G12D", "NA", "KRAS G12 missense"},
		{"KRAS", "G12G", "NA", "KRAS G12 silent"},
		{"KRAS", "G12=", "NA", "KRAS G12 silent"},
		{"KRAS", "R100*", "NA", "KRAS truncating"},
		{"KRAS", "K23_E24del", "NA", "KRAS 23-24 deletion"},
		{"KRAS", "K23delins", "NA", ""},
		{"KRAS", "K23_L25>MN", "NA", "KRAS 23-25 deletion"},
		{"KRAS", "G12D", "Splice_Region", ""},
		{"KRAS", "G12D", "5'Flank", ""},
	}
	for _, c := range cases {
		if got := mutationKeyword(c.hugo, c.pc, c.mt); got != c.want {
			t.Errorf("mutationKeyword(%q, %q, %q) = %q, want %q", c.hugo, c.pc, c.mt, got, c.want)
		}
	}
}

func TestJavaDecimal(t *testing.T) {
	cases := map[string]string{
		"0": "0", "-0": "0", "2": "2", "-2": "-2", "1.2346": "1.2346", "0.50": "0.50",
		"1e25": "1E+25", "-1.234e-5": "-0.00001234", "1.5e-7": "1.5E-7", "+3": "3",
		".5": "0.5", "12345678.1235": "12345678.1235", "1.0e2": "1.0E+2",
	}
	for in, want := range cases {
		got, ok := javaDecimal(in)
		if !ok || string(got) != want {
			t.Errorf("javaDecimal(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"NA", "nan", "inf", "", " 1.5", "0x10", "-", "."} {
		if got, ok := javaDecimal(in); ok {
			t.Errorf("javaDecimal(%q) = %q, want non-numeric", in, got)
		}
	}
}

func TestJavaProteinPositions(t *testing.T) {
	cases := []struct {
		pos, pc    string
		start, end int64
	}{
		{"600/766", "V600E", 600, 600},
		{"600-601/766", "V600_K601delinsE", 600, 601},
		{"", "V600E", 600, 600},
		{"", "K23_E24del", 23, 24},
		{"", "A138Cfs*27", 138, 138},
		{"", "MUTATED", -1, -1},
		{"600/766", "MUTATED", 600, -1},
		{"", "ELREA746del", 746, 746},
		{"", "KR23", 23, 24},
		// Positions as transform --from-prod writes production's columns.
		{"125-125", "T125=", 125, 125},
		{"125-?", "T125=", 125, -1},
		{"", "Promoter", -1, -1},
	}
	for _, c := range cases {
		s, e := javaProteinPositions(c.pos, c.pc)
		if s != c.start || e != c.end {
			t.Errorf("javaProteinPositions(%q, %q) = %d, %d; want %d, %d", c.pos, c.pc, s, e, c.start, c.end)
		}
	}
}

func TestJavaPartIntAndNamespaces(t *testing.T) {
	for in, want := range map[string]int64{"": -1, "NA": -1, "12": 12, "6.7": 6, "-3": -3, "1e3": 1000} {
		if got := javaPartInt(in); got != want {
			t.Errorf("javaPartInt(%q) = %d, want %d", in, got, want)
		}
	}
	for _, in := range []string{"", "{}", "null", "not json"} {
		if namespaceColumnsJSON(in) != nil {
			t.Errorf("namespaceColumnsJSON(%q) should be absent", in)
		}
	}
	if string(namespaceColumnsJSON(`{"a":{"b":1}}`)) != `{"a":{"b":1}}` {
		t.Error("namespace object not kept")
	}
	d := newDriverAnnotation("", "ann", "NA", "", naString)
	if d.Filter != nil || d.TiersFilter != nil {
		t.Error("driver fields set without a driver filter value")
	}
	d = newDriverAnnotation("Putative_Driver", "", "", "", naString)
	if d.Filter == nil || *d.Filter != "Putative_Driver" || *d.FilterAnn != "NA" {
		t.Errorf("driver annotation: %+v", d)
	}
}

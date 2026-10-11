package core

import "testing"

func TestILikeMatcher(t *testing.T) {
	for _, c := range []struct {
		pattern, s string
		want       bool
	}{
		{"%tbx15%", "TBX15", true},
		{"%TBX15%", "cg00173504", false},
		{"%AZD_5%", "AZD0530", true},
		{"%AZD_5%", "AZD6244", false},
		{`%a\_b%`, "xa_by", true},
		{`%a\_b%`, "xacby", false},
		{"%50\\%%", "50%", true},
		{"%ö%", "SCHÖN", true},
		{"%.*%", "anything", false},
		{"%%", "", true},
		{"%x%", "", false},
	} {
		if got := ILikeMatcher(c.pattern)(c.s); got != c.want {
			t.Errorf("%q ILIKE %q = %v, want %v", c.s, c.pattern, got, c.want)
		}
	}
}

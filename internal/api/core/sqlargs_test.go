package core

import (
	"errors"
	"testing"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

func TestSQLArgs(t *testing.T) {
	a := NewArgs()
	s1, s2 := a.Str("x"), a.Str("x")
	ids := a.Strs([]string{"a"})
	if s1 != "{a0:String}" || s2 != s1 || ids != "{a1:Array(String)}" {
		t.Fatalf("placeholders %s %s %s", s1, s2, ids)
	}
	if p := a.Params("WHERE id IN " + ids); len(p) != 1 || p["a1"] == nil {
		t.Errorf("params for one placeholder: %v", p)
	}
}

func TestCacheKey(t *testing.T) {
	sql := "SELECT 1 WHERE x = {x:String}"
	plain, _ := cacheKey(sql, nil)
	a, _ := cacheKey(sql, ch.Params{"x": "a"})
	b, _ := cacheKey(sql, ch.Params{"x": "b"})
	arr, _ := cacheKey(sql, ch.Params{"x": []string{"a"}})
	if plain != sql || a == b || a == plain || a == arr {
		t.Errorf("keys not distinct: %q %q %q %q", plain, a, b, arr)
	}
	two1, _ := cacheKey(sql, ch.Params{"x": "a", "y": "b"})
	two2, _ := cacheKey(sql, ch.Params{"y": "b", "x": "a"})
	if two1 != two2 {
		t.Error("key depends on map order")
	}
}

// A statement whose fragment could not be built fails without running.
func TestSQLArgsFail(t *testing.T) {
	a := NewArgs()
	first := errors.New("lookup failed")
	a.Fail(first)
	a.Fail(errors.New("later"))
	type row struct{}
	if _, err := QueryA[row](nil, "SELECT 1", a); err != first {
		t.Fatalf("QueryA error %v, want %v", err, first)
	}
	if _, err := CachedQueryA[row](nil, "SELECT 1", a); err != first {
		t.Fatalf("CachedQueryA error %v, want %v", err, first)
	}
}

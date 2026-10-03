package api

import (
	"testing"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

func TestSQLArgs(t *testing.T) {
	a := newArgs()
	s1, s2 := a.str("x"), a.str("x")
	ids := a.strs([]string{"a"})
	if s1 != "{a0:String}" || s2 != s1 || ids != "{a1:Array(String)}" {
		t.Fatalf("placeholders %s %s %s", s1, s2, ids)
	}
	if p := a.params("WHERE id IN " + ids); len(p) != 1 || p["a1"] == nil {
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

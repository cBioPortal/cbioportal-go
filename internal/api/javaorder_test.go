package api

import (
	"reflect"
	"testing"
)

func TestJavaHashMapOrder(t *testing.T) {
	if javaStringHash("GB") != 2267 || javaStringHash("hello") != 99162322 {
		t.Errorf("hash mismatch: %d %d", javaStringHash("GB"), javaStringHash("hello"))
	}
	// Bucket indexes (16 buckets): AASTR 3, ODG 3, NA 3, OAST 7, GB 11, ASTR 14.
	got := javaHashMapOrder([]string{"AASTR", "ASTR", "GB", "OAST", "ODG", "NA"})
	want := []string{"AASTR", "ODG", "NA", "OAST", "GB", "ASTR"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

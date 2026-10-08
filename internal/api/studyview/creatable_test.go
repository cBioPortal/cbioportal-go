package studyview

import (
	"encoding/csv"
	"os"
	"regexp"
	"testing"
)

// TestDecimalCreatable checks decimalCreatable against Java's
// NumberUtils.isCreatable (internal/databin/testdata/numbers.tsv) on the
// strings it covers: neither hexadecimal nor with a type suffix.
func TestDecimalCreatable(t *testing.T) {
	f, err := os.Open("../../databin/testdata/numbers.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma = '\t'
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(decimalCreatable)
	outside := regexp.MustCompile(`[xXlLfFdD]`)
	n := 0
	for _, row := range rows {
		if len(row) < 2 || outside.MatchString(row[0]) {
			continue
		}
		n++
		if got, want := re.MatchString(row[0]), row[1] == "1"; got != want {
			t.Errorf("%q: decimalCreatable %v, isCreatable %v", row[0], got, want)
		}
	}
	if n < 1000 {
		t.Fatalf("only %d strings checked", n)
	}
}

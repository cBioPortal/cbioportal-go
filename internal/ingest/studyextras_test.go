package ingest

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMutSigRows(t *testing.T) {
	cases := []struct {
		name, body string
		want       []string
	}{
		{
			"MutSig with rank, N before n",
			"rank\tgene\tdescription\tN\tn\tnpat\tp\tq\n" +
				"1\tTP53\tp53\t1000\t30\t28\t<1e-15\t<1e-11\n" +
				"2\tPIK3CA\tpi3k\t2000\t20\t19\t1.2e-10\t0.00003\n",
			[]string{"1\tTP53\t1000\t30\t1e-15\t1e-11", "2\tPIK3CA\t2000\t20\t1.2e-10\t0.00003"},
		},
		{
			"MutSigCV without rank, Nnon/nnon, case-insensitive gene/p/q",
			"Gene\tnnon\tNnon\tP\tQ\nKRAS\t3\t500\t0.02\t0.05\n\nBRAF\t4\t600\t0.01\t0.04\n",
			[]string{"1\tKRAS\t500\t3\t0.02\t0.05", "2\tBRAF\t600\t4\t0.01\t0.04"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "data_mutsig.txt")
			if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := mutSigRows(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestMutSigRowsMissingColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data_mutsig.txt")
	if err := os.WriteFile(path, []byte("rank\tgene\tN\tp\tq\n1\tTP53\t10\t0.1\t0.01\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mutSigRows(path); err == nil {
		t.Error("expected an error for a file without an n column")
	}
}

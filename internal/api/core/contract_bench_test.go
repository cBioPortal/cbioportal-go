package core

import (
	"bytes"
	"fmt"
	"testing"
)

// samplesBody is a /samples/fetch DETAILED-sized response: n full sample
// objects as the handler writes them.
func samplesBody(n int) []byte {
	var b bytes.Buffer
	b.WriteByte('[')
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"uniqueSampleKey":"UC0wMDAwMDA0LVQwMS1JTTM6bXNrX2ltcGFjdF8yMDE3","uniquePatientKey":"UC0wMDAwMDA0Om1za19pbXBhY3RfMjAxNw",`+
			`"sampleType":"Primary Solid Tumor","sampleId":"P-%07d-T01-IM3","patientId":"P-%07d","studyId":"msk_impact_2017","sequenced":true,"copyNumberSegmentPresent":true}`, i, i)
	}
	b.WriteString("]\n")
	return b.Bytes()
}

func BenchmarkReshapeSamples(b *testing.B) {
	s := &State{}
	body := samplesBody(10945)
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		if _, ok := s.reshape(body, sampleShape["SUMMARY"], true, "", ""); !ok {
			b.Fatal("not reshaped")
		}
	}
}

func TestEmptyJSONArrayBodies(t *testing.T) {
	for body, want := range map[string]string{
		`[]`: MsgListSize("x"), " [ \n] ": MsgListSize("x"), `[1]`: "", `null`: "", `{}`: "", `[] x`: "", ``: "", `[`: "",
	} {
		if got := listBody("x")([]byte(body)); got != want {
			t.Errorf("listBody(%q) = %q, want %q", body, got, want)
		}
	}
	for body, want := range map[string]string{
		`{"sampleIdentifiers":[]}`:                         MsgListSize("sampleIdentifiers"),
		`{"sampleIdentifiers":[ ]}`:                        MsgListSize("sampleIdentifiers"),
		`{"sampleIdentifiers":[{"a":1}]}`:                  "",
		`{"sampleListIds":"x"}`:                            "",
		`{"sampleIdentifiers":null}`:                       "onlyOneTypeOfFilterPresent must be true",
		`{"sampleListIds":["a"],"uniqueSampleKeys":["b"]}`: "onlyOneTypeOfFilterPresent must be true",
	} {
		if got := sampleFilterBody([]byte(body)); got != want {
			t.Errorf("sampleFilterBody(%q) = %q, want %q", body, got, want)
		}
	}
}

package studies

import (
	"testing"
)

func TestStudyReadable(t *testing.T) {
	for _, c := range []struct {
		availability bool
		status       int
		want         bool
	}{
		{false, 0, true}, {false, 1, true}, {true, 1, true}, {true, 0, false},
	} {
		if got := studyReadable(c.availability, c.status); got != c.want {
			t.Errorf("studyReadable(%v, %d) = %v, want %v", c.availability, c.status, got, c.want)
		}
	}
}

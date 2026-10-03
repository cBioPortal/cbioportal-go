package api

import "testing"

func TestRoundHalfUp(t *testing.T) {
	for _, c := range []struct{ x, want float64 }{
		{27.155, 27.16}, {27.154, 27.15}, {27.16, 27.16}, {100, 100}, {-1.005, -1.01}, {14.197530864197532, 14.2},
	} {
		if got := roundHalfUp(c.x, 2); got != c.want {
			t.Errorf("roundHalfUp(%v) = %v, want %v", c.x, got, c.want)
		}
	}
}

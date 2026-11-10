package parity

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func TestStaleStudies(t *testing.T) {
	a := map[string]string{"s1": "2026-11-04 14:00:00", "s2": "2026-11-01 10:00:00", "gone": "2026-01-01 00:00:00"}
	b := map[string]string{"s1": "2026-11-04 15:16:52", "s2": "2026-11-01 10:00:00", "new": "2026-11-04 15:00:00"}
	if got := staleStudies(a, b); !slices.Equal(got, []string{"new", "s1"}) {
		t.Errorf("staleStudies: %v", got)
	}
}

// TestWaitForMirror checks that a run waits until A lists B's import
// dates, and gives up at mirrorWait.
func TestWaitForMirror(t *testing.T) {
	prevWait, prevPoll := mirrorWait, mirrorPoll
	defer func() { mirrorWait, mirrorPoll = prevWait, prevPoll }()
	mirrorPoll = 10 * time.Millisecond

	var polls atomic.Int32
	catchUpAfter := int32(3)
	mux := http.NewServeMux()
	mux.HandleFunc("/b/api/studies", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"studyId":"s1","importDate":"2026-11-04 15:16:52"}]`)
	})
	mux.HandleFunc("/a/api/studies", func(w http.ResponseWriter, r *http.Request) {
		date := "2026-07-30 22:17:30"
		if polls.Add(1) >= catchUpAfter {
			date = "2026-11-04 15:16:52"
		}
		fmt.Fprintf(w, `[{"studyId":"s1","importDate":%q}]`, date)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mirrorWait = time.Minute
	if stale := waitForMirror(srv.URL+"/a", srv.URL+"/b", "test", basicAuth{}, io.Discard); stale != nil || polls.Load() != catchUpAfter {
		t.Errorf("caught up: stale %v after %d polls, want none after %d", stale, polls.Load(), catchUpAfter)
	}

	polls.Store(0)
	catchUpAfter = 1 << 30
	mirrorWait = 35 * time.Millisecond
	if stale := waitForMirror(srv.URL+"/a", srv.URL+"/b", "test", basicAuth{}, io.Discard); !slices.Equal(stale, []string{"s1"}) {
		t.Errorf("never caught up: stale %v, want [s1]", stale)
	}

	if stale := waitForMirror(srv.URL+"/missing", srv.URL+"/b", "test", basicAuth{}, io.Discard); stale != nil {
		t.Errorf("unreadable A: stale %v, want none", stale)
	}
}

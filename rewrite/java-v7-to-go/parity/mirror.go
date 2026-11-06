package parity

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"
)

// mirrorWait bounds how long a run waits for A's data to catch up with B's,
// and mirrorPoll is how often it looks.
var (
	mirrorWait = 2 * time.Hour
	mirrorPoll = 5 * time.Minute
)

// importDates returns each study's importDate as origin lists it.
func importDates(origin, ua string) (map[string]string, error) {
	req, err := http.NewRequest(http.MethodGet, origin+"/api/studies", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s/api/studies: status %d", origin, resp.StatusCode)
	}
	var studies []struct {
		StudyID    string `json:"studyId"`
		ImportDate string `json:"importDate"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&studies); err != nil {
		return nil, err
	}
	dates := make(map[string]string, len(studies))
	for _, s := range studies {
		dates[s.StudyID] = s.ImportDate
	}
	return dates, nil
}

// staleStudies are the studies B lists that A lists with another import
// date or not at all, sorted.
func staleStudies(a, b map[string]string) []string {
	var stale []string
	for id, date := range b {
		if a[id] != date {
			stale = append(stale, id)
		}
	}
	slices.Sort(stale)
	return stale
}

// waitForMirror waits, up to mirrorWait, until A serves every study with
// the import date B serves it with: production imports studies during the
// day and the mirror copies them within the hour. It returns the studies
// still stale, nil when A caught up or the dates could not be read.
func waitForMirror(a, b, ua string, log io.Writer) []string {
	deadline := time.Now().Add(mirrorWait)
	for {
		datesB, errB := importDates(b, ua)
		datesA, errA := importDates(a, ua)
		if errA != nil || errB != nil {
			fmt.Fprintf(log, "mirror check skipped: a: %v, b: %v\n", errA, errB)
			return nil
		}
		stale := staleStudies(datesA, datesB)
		if len(stale) == 0 {
			return nil
		}
		if time.Now().Add(mirrorPoll).After(deadline) {
			return stale
		}
		fmt.Fprintf(log, "waiting for the mirror: %d studies differ in import date (%s)\n", len(stale), cutList(stale, 5))
		time.Sleep(mirrorPoll)
	}
}

// cutList is the first n ids, with a count of the rest.
func cutList(ids []string, n int) string {
	if len(ids) <= n {
		return fmt.Sprint(ids)
	}
	return fmt.Sprintf("%v and %d more", ids[:n], len(ids)-n)
}

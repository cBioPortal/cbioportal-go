package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/ingest"
	"github.com/cbioportal/cbioportal-go/internal/parser"
	"github.com/cbioportal/cbioportal-go/internal/transform"
)

// timelineHeaderStudy has timeline headers that are not upper case, one of
// them repeated, and a second file writing a shared column's header in
// another case: ImportTimelineData keys event data by each file's header
// as written.
var timelineHeaderStudy = map[string]string{
	"meta_study.txt": "type_of_cancer: mixed\ncancer_study_identifier: tl_headers\nname: Timeline headers\n" +
		"description: Timeline headers as written\nreference_genome: hg19\n",
	"meta_clinical_sample.txt": "cancer_study_identifier: tl_headers\ngenetic_alteration_type: CLINICAL\n" +
		"datatype: SAMPLE_ATTRIBUTES\ndata_filename: data_clinical_sample.txt\n",
	"data_clinical_sample.txt": "#Patient Identifier\tSample Identifier\n#Patient Identifier\tSample Identifier\n" +
		"#STRING\tSTRING\n#1\t1\nPATIENT_ID\tSAMPLE_ID\nP1\tS1\nP2\tS2\n",
	"meta_timeline_treatment.txt": "cancer_study_identifier: tl_headers\ngenetic_alteration_type: CLINICAL\n" +
		"datatype: TIMELINE\ndata_filename: data_timeline_treatment.txt\n",
	"data_timeline_treatment.txt": "PATIENT_ID\tSTART_DATE\tSTOP_DATE\tEVENT_TYPE\tTreatment_TYPE\tAGENT\tLine of Therapy\tLine of Therapy\n" +
		"P1\t10\t40\tTreatment\tMedical Therapy\tTamoxifen\t1\tfirst-line\n" +
		"P1\t50\t\tTreatment\tRadiation Therapy\t\tNA\t\n" +
		"P2\t0\t100\tTreatment\tMedical Therapy\tTrastuzumab\t1\t\n",
	"meta_timeline_diagnosis.txt": "cancer_study_identifier: tl_headers\ngenetic_alteration_type: CLINICAL\n" +
		"datatype: TIMELINE\ndata_filename: data_timeline_diagnosis.txt\n",
	"data_timeline_diagnosis.txt": "PATIENT_ID\tSTART_DATE\tEVENT_TYPE\tICD DESCRIPTION\ttreatment_type\n" +
		"P1\t0\tDiagnosis\tBreast cancer\tnone\n",
}

// liveTimelineServer is the ClickHouse server and reference database of
// the live timeline tests.
func liveTimelineServer(t *testing.T) (base, refDB string) {
	t.Helper()
	base = os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	refDB = os.Getenv("CLICKHOUSE_TEST_REF_DB")
	if refDB == "" {
		refDB = "cbioportal_ref"
	}
	return base, refDB
}

// liveTimelineDatabases creates a portal and a raw database, dropped after
// the test.
func liveTimelineDatabases(t *testing.T, base, suffix string) (db, rawDB string) {
	t.Helper()
	admin := ch.New(base, "default")
	db = fmt.Sprintf("cbioportal_go_test_tl_%d_%s", os.Getpid(), suffix)
	rawDB = db + "_raw"
	for _, d := range []string{db, rawDB} {
		if _, err := admin.Execute("CREATE DATABASE " + d); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = admin.Execute("DROP DATABASE IF EXISTS " + d) })
	}
	return db, rawDB
}

// importTimelineStudy ingests and transforms the study in dir and returns
// an export-enabled State over it.
func importTimelineStudy(t *testing.T, base, refDB, suffix, dir string) *State {
	t.Helper()
	db, rawDB := liveTimelineDatabases(t, base, suffix)
	study, err := parser.Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.IngestStudy(ch.New(base, rawDB), study); err != nil {
		t.Fatal(err)
	}
	if _, err := transform.Apply(ch.New(base, rawDB), rawDB, db, refDB); err != nil {
		t.Fatal(err)
	}
	c := ch.New(base, db)
	c.SetDatabaseAliases(rawDB, refDB)
	return newState(&core.State{CH: c, Props: map[string]string{"feature.study.export": "true"}})
}

func serveTimeline(t *testing.T, s *State, path string) []byte {
	t.Helper()
	mux := http.NewServeMux()
	s.routeSurvival(mux)
	s.routeExport(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, w.Code, w.Body)
	}
	return w.Body.Bytes()
}

// eventDataLines renders events as sorted "patient start stop type key=value"
// lines, one per attribute (an absent stop date as 0).
func eventDataLines(t *testing.T, body []byte) []string {
	t.Helper()
	var events []ClinicalEvent
	if err := json.Unmarshal(body, &events); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	var out []string
	for _, ev := range events {
		stop := int64(0)
		if ev.EndNumberOfDaysSinceDiagnosis != nil {
			stop = *ev.EndNumberOfDaysSinceDiagnosis
		}
		for _, a := range ev.Attributes {
			out = append(out, fmt.Sprintf("%s %d %d %s %s=%s", ev.PatientID, *ev.StartNumberOfDaysSinceDiagnosis,
				stop, ev.EventType, a.Key, a.Value))
		}
	}
	slices.Sort(out)
	return out
}

// derivedEventDataLines renders a study's clinical_event_data_derived rows
// as eventDataLines does.
func derivedEventDataLines(t *testing.T, s *State, studyID string) []string {
	t.Helper()
	type row struct {
		Line string `json:"line"`
	}
	rows, err := ch.QueryP[row](s.CH, "SELECT concat(substring(patient_unique_id, length({study:String}) + 2), ' ', "+
		"toString(start_date), ' ', toString(stop_date), ' ', event_type, ' ', key, '=', value) AS line "+
		"FROM clinical_event_data_derived WHERE cancer_study_identifier = {study:String}", ch.Params{"study": studyID})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Line
	}
	slices.Sort(out)
	return out
}

func unzipFiles(t *testing.T, body []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("export zip: %v", err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
	}
	return files
}

// lines splits a file into its lines without the trailing newline.
func lines(text string) []string {
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

func wantLines(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

// TestLiveTimelineHeaders imports timelineHeaderStudy when
// CLICKHOUSE_TEST_URL is set (genes from CLICKHOUSE_TEST_REF_DB, default
// cbioportal_ref): clinical-events and the study export key event data by
// the headers as written, like clinical_event_data_derived, and the
// exported timeline files import back to the same event data.
func TestLiveTimelineHeaders(t *testing.T) {
	base, refDB := liveTimelineServer(t)
	dir := t.TempDir()
	for name, content := range timelineHeaderStudy {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := importTimelineStudy(t, base, refDB, "staged", dir)

	want := []string{
		"P1 0 0 Diagnosis ICD DESCRIPTION=Breast cancer",
		"P1 0 0 Diagnosis treatment_type=none",
		"P1 10 40 Treatment AGENT=Tamoxifen",
		"P1 10 40 Treatment Line of Therapy=first-line",
		"P1 10 40 Treatment Treatment_TYPE=Medical Therapy",
		"P1 50 0 Treatment Line of Therapy=NA",
		"P1 50 0 Treatment Treatment_TYPE=Radiation Therapy",
		"P2 0 100 Treatment AGENT=Trastuzumab",
		"P2 0 100 Treatment Line of Therapy=1",
		"P2 0 100 Treatment Treatment_TYPE=Medical Therapy",
	}
	wantLines(t, "clinical_event_data_derived", derivedEventDataLines(t, s, "tl_headers"), want)
	wantLines(t, "study clinical-events", eventDataLines(t, serveTimeline(t, s, "/api/studies/tl_headers/clinical-events")), want)
	wantLines(t, "patient clinical-events", eventDataLines(t, serveTimeline(t, s, "/api/studies/tl_headers/patients/P2/clinical-events")),
		want[len(want)-3:])

	files := unzipFiles(t, serveTimeline(t, s, "/export/study/tl_headers.zip"))
	wantLines(t, "exported treatment timeline", lines(files["data_clinical_timeline_Treatment.txt"]), []string{
		"PATIENT_ID\tSTART_DATE\tSTOP_DATE\tEVENT_TYPE\tAGENT\tLine of Therapy\tTreatment_TYPE",
		"P1\t10\t40\tTreatment\tTamoxifen\tfirst-line\tMedical Therapy",
		"P1\t50\t\tTreatment\t\tNA\tRadiation Therapy",
		"P2\t0\t100\tTreatment\tTrastuzumab\t1\tMedical Therapy",
	})
	wantLines(t, "exported diagnosis timeline", lines(files["data_clinical_timeline_Diagnosis.txt"]), []string{
		"PATIENT_ID\tSTART_DATE\tSTOP_DATE\tEVENT_TYPE\tICD DESCRIPTION\ttreatment_type",
		"P1\t0\t\tDiagnosis\tBreast cancer\tnone",
	})

	// The export imports back to the same event data.
	again := t.TempDir()
	for name, content := range files {
		if strings.HasPrefix(name, "data_clinical_timeline_") || strings.HasPrefix(name, "meta_clinical_timeline_") ||
			name == "meta_study.txt" || strings.Contains(name, "clinical_sample") {
			if err := os.WriteFile(filepath.Join(again, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	s2 := importTimelineStudy(t, base, refDB, "roundtrip", again)
	wantLines(t, "re-imported clinical_event_data_derived", derivedEventDataLines(t, s2, "tl_headers"), want)
}

// TestLiveTimelineFromProd: a timeline cloned from production carries each
// event's stored (key, value) pairs, which clinical-events and the export
// serve unchanged.
func TestLiveTimelineFromProd(t *testing.T) {
	base, _ := liveTimelineServer(t)
	db, rawDB := liveTimelineDatabases(t, base, "fromprod")
	raw := ch.New(base, rawDB)
	for _, q := range []string{
		"CREATE TABLE timeline (study_id String, patient_id String, event_type String, start_date String, stop_date String, " +
			"event_attributes Array(Tuple(String, String)), clinical_event_id UInt64) ENGINE = MergeTree ORDER BY (study_id, patient_id)",
		"INSERT INTO timeline VALUES " +
			"('tl_prod', 'P1', 'Treatment', '10', '40', [('Treatment_TYPE', 'Medical Therapy'), ('Line of Therapy', 'first-line')], 2), " +
			"('tl_prod', 'P1', 'Diagnosis', '0', '', [('ICD DESCRIPTION', 'Breast cancer')], 1), " +
			"('tl_prod', 'P9', 'Treatment', '5', '', [('AGENT', 'X')], 3)",
	} {
		if _, err := raw.Execute(q); err != nil {
			t.Fatal(err)
		}
	}
	c := ch.New(base, db)
	c.SetDatabaseAliases(rawDB, "")
	s := newState(&core.State{CH: c})

	var events []ClinicalEvent
	body := serveTimeline(t, s, "/api/studies/tl_prod/patients/P1/clinical-events")
	if err := json.Unmarshal(body, &events); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	got, _ := json.Marshal(events)
	if want := `"attributes":[{"key":"ICD DESCRIPTION","value":"Breast cancer"}]`; !strings.Contains(string(got), want) {
		t.Errorf("clinical-events %s: missing %s", got, want)
	}
	if want := `"attributes":[{"key":"Treatment_TYPE","value":"Medical Therapy"},{"key":"Line of Therapy","value":"first-line"}]`; !strings.Contains(string(got), want) {
		t.Errorf("clinical-events %s: missing %s", got, want)
	}

	var buf bytes.Buffer
	e := &studyExport{s: s, studyID: "tl_prod", zw: zip.NewWriter(&buf), now: time.Now(), loaded: true, patients: []string{"P1"}}
	if err := e.clinicalTimeline(); err != nil {
		t.Fatal(err)
	}
	if err := e.zw.Close(); err != nil {
		t.Fatal(err)
	}
	files := unzipFiles(t, buf.Bytes())
	wantLines(t, "exported treatment timeline", lines(files["data_clinical_timeline_Treatment.txt"]), []string{
		"PATIENT_ID\tSTART_DATE\tSTOP_DATE\tEVENT_TYPE\tLine of Therapy\tTreatment_TYPE",
		"P1\t10\t40\tTreatment\tfirst-line\tMedical Therapy",
	})
	wantLines(t, "exported diagnosis timeline", lines(files["data_clinical_timeline_Diagnosis.txt"]), []string{
		"PATIENT_ID\tSTART_DATE\tSTOP_DATE\tEVENT_TYPE\tICD DESCRIPTION",
		"P1\t0\t\tDiagnosis\tBreast cancer",
	})
}

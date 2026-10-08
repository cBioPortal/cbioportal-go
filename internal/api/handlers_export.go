package api

import (
	"archive/zip"
	"bufio"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/databin"
	"github.com/cbioportal/cbioportal-go/internal/transform"
)

// Study export: GET /export/study/{studyId}.zip, cBioPortal's study file
// format written from the database (org.cbioportal.application.file.export).
// The endpoint exists only with feature.study.export=true. Exporters run in
// ExportConfig's order; each writes its meta and data files into the zip, and
// a failing exporter aborts the download with "ERROR: <message>" after the
// bytes already sent, as ZipOutputStreamWriterService.fail does.
//
// Exported: meta_study, cancer type, clinical patient / sample attributes,
// timeline, MAF (mutation extended / uncalled), structural variants, gene x
// sample matrices (mRNA expression, discrete / continuous / log2 copy number,
// methylation), copy-number segments, gene panel matrix, case lists, README.
// Not exported: protein level (RPPA) and generic assay profiles — their
// genetic entities (phosphoprotein genes, generic assay entities and their
// meta properties) are not modeled here — and virtual studies.

func (s *State) routeExport(mux *http.ServeMux) {
	mux.HandleFunc("GET /export/study/{file}", s.exportStudy)
}

// exportStudy serves the zip (ExportController.downloadStudyData): 404 with
// no body for a study that does not exist.
func (s *State) exportStudy(w http.ResponseWriter, r *http.Request) {
	studyID, ok := strings.CutSuffix(r.PathValue("file"), ".zip")
	if s.Props["feature.study.export"] != "true" || !ok || studyID == "" {
		s.catchAll(w, r)
		return
	}
	meta, err := s.exportStudyMetadata(studyID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if meta == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+studyID+`.zip"`)
	w.WriteHeader(http.StatusOK)
	bw := bufio.NewWriter(w)
	e := &studyExport{s: s, studyID: studyID, study: meta, zw: zip.NewWriter(bw), now: time.Now()}
	for _, ex := range studyExporters {
		if err := ex(e); err != nil {
			// The zip is corrupted on purpose, as Java's
			// ZipOutputStreamWriterService.fail does; the error itself goes
			// to the server log only.
			logServerError(r, "study export failed", err)
			bw.WriteString("ERROR: study export failed")
			bw.Flush()
			return
		}
	}
	e.zw.Close()
	bw.Flush()
}

// studyExporters is ExportConfig.exporters in order.
var studyExporters = []func(*studyExport) error{
	(*studyExport).cancerStudyMetadata,
	(*studyExport).cancerType,
	(*studyExport).clinicalPatientAttributes,
	(*studyExport).clinicalSampleAttributes,
	(*studyExport).clinicalTimeline,
	func(e *studyExport) error { return e.maf("MUTATION_EXTENDED", "mutation_extended") },
	func(e *studyExport) error { return e.maf("MUTATION_UNCALLED", "mutation_uncalled") },
	(*studyExport).structuralVariants,
	func(e *studyExport) error { return e.geneMatrix("MRNA_EXPRESSION", "CONTINUOUS") },
	func(e *studyExport) error { return e.geneMatrix("MRNA_EXPRESSION", "Z-SCORE") },
	func(e *studyExport) error { return e.geneMatrix("MRNA_EXPRESSION", "DISCRETE") },
	func(e *studyExport) error { return e.geneMatrix("COPY_NUMBER_ALTERATION", "DISCRETE") },
	func(e *studyExport) error { return e.geneMatrix("COPY_NUMBER_ALTERATION", "CONTINUOUS") },
	func(e *studyExport) error { return e.geneMatrix("COPY_NUMBER_ALTERATION", "LOG2-VALUE") },
	(*studyExport).copyNumberSegments,
	func(e *studyExport) error { return e.geneMatrix("METHYLATION", "CONTINUOUS") },
	(*studyExport).genePanelMatrix,
	(*studyExport).caseLists,
	(*studyExport).readme,
}

type studyExport struct {
	s       *State
	studyID string
	study   *exportStudyMeta
	zw      *zip.Writer
	now     time.Time

	// samples are the study's samples in database order (the clinical sample
	// file's order, in which the importer creates them); patients likewise
	// (first seen in the sample file, then the patient file).
	samples      []string
	samplePatent map[string]string
	patients     []string
	loaded       bool
}

// ---------- writers ----------

// create starts a zip entry, deflated and stamped with the export time as
// ZipOutputStream does.
func (e *studyExport) create(name string) (io.Writer, error) {
	return e.zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: e.now})
}

// metaEntry is one key of a meta file; a nil value is not written.
type metaEntry struct {
	key   string
	value *string
}

func strp(s string) *string { return &s }

// writeMeta writes a key-value meta file (KeyValueMetadataWriter): newlines
// in values are escaped.
func (e *studyExport) writeMeta(name string, entries []metaEntry) error {
	f, err := e.create(name)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, m := range entries {
		if m.value == nil {
			continue
		}
		b.WriteString(m.key + ": " + strings.ReplaceAll(*m.value, "\n", `\n`) + "\n")
	}
	_, err = io.WriteString(f, b.String())
	return err
}

// tsvFile writes a data file (TsvDataWriter): optional '#' comment rows and a
// header row, then the rows; tabs inside values are escaped.
type tsvFile struct {
	w   io.Writer
	buf *bufio.Writer
}

func (e *studyExport) createTSV(name string) (*tsvFile, error) {
	f, err := e.create(name)
	if err != nil {
		return nil, err
	}
	return &tsvFile{w: f, buf: bufio.NewWriter(f)}, nil
}

func (t *tsvFile) row(prefix string, cells []string) {
	t.buf.WriteString(prefix)
	for i, c := range cells {
		if i > 0 {
			t.buf.WriteByte('\t')
		}
		t.buf.WriteString(strings.ReplaceAll(c, "\t", `\t`))
	}
	t.buf.WriteByte('\n')
}

func (t *tsvFile) comment(cells []string) { t.row("#", cells) }
func (t *tsvFile) line(cells []string)    { t.row("", cells) }
func (t *tsvFile) close() error           { return t.buf.Flush() }

// profileMetaEntries is GeneticProfileDatatypeMetadata.toMetadataKeyValues
// followed by data_filename.
func profileMetaEntries(studyID, alterationType, datatype string, p *exportProfile, dataFile string) []metaEntry {
	out := []metaEntry{
		{"cancer_study_identifier", strp(studyID)},
		{"genetic_alteration_type", strp(alterationType)},
		{"datatype", strp(datatype)},
	}
	if p != nil {
		show := "false"
		if p.Show != 0 {
			show = "true"
		}
		out = append(out,
			metaEntry{"stable_id", strp(p.stableSuffix(studyID))},
			metaEntry{"show_profile_in_analysis_tab", strp(show)},
			metaEntry{"profile_name", strp(p.Name)},
			metaEntry{"profile_description", strp(p.Description)},
			metaEntry{"patient_level", strp("false")},
		)
	}
	return append(out, metaEntry{"data_filename", strp(dataFile)})
}

// ---------- study and case order ----------

type exportStudyMeta struct {
	StudyID         string `json:"study_id"`
	TypeOfCancer    string `json:"cancer_type_id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Citation        string `json:"citation"`
	Pmid            string `json:"pmid"`
	Groups          string `json:"groups"`
	ReferenceGenome string `json:"reference_genome"`
}

func (s *State) exportStudyMetadata(studyID string) (*exportStudyMeta, error) {
	rows, err := ch.QueryP[exportStudyMeta](s.CH,
		"SELECT study_id, cancer_type_id, name, description, citation, pmid, groups, reference_genome "+
			"FROM cancer_study WHERE study_id = {study:String} LIMIT 1", ch.Params{"study": studyID})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// hasRaw reports whether the raw staging database holds table.
func (e *studyExport) hasRaw(table string) bool { return len(e.s.rawColumns(table)) > 0 }

func (e *studyExport) loadCases() error {
	if e.loaded {
		return nil
	}
	e.loaded = true
	e.samplePatent = map[string]string{}
	type sampleRow struct {
		SampleID  string `json:"sample_id"`
		PatientID string `json:"patient_id"`
	}
	type patientRow struct {
		PatientID string `json:"patient_id"`
	}
	p := ch.Params{"study": e.studyID}
	var samples []sampleRow
	var patients []patientRow
	var err error
	if e.hasRaw("clinical_sample") && e.hasRaw("clinical_patient") {
		samples, err = ch.QueryP[sampleRow](e.s.CH,
			"SELECT sample_id, patient_id FROM cbioportal_raw.clinical_sample WHERE study_id = {study:String} AND sample_id != '' ORDER BY "+
				e.s.rawFileOrder("clinical_sample", ""), p)
		if err == nil {
			patients, err = ch.QueryP[patientRow](e.s.CH,
				"SELECT patient_id FROM cbioportal_raw.clinical_patient WHERE study_id = {study:String} AND patient_id != '' ORDER BY "+
					e.s.rawFileOrder("clinical_patient", ""), p)
		}
	} else {
		sc := byStudy(e.studyID)
		samples, err = ch.QueryP[sampleRow](e.s.CH,
			"SELECT sample_id, patient_id FROM "+sc.from(paramsBinder(p), tSample)+" ORDER BY sample_id", p)
		if err == nil {
			patients, err = ch.QueryP[patientRow](e.s.CH,
				"SELECT patient_id FROM "+sc.from(paramsBinder(p), tPatient)+" ORDER BY patient_id", p)
		}
	}
	if err != nil {
		return err
	}
	seenPatient := map[string]bool{}
	for _, r := range samples {
		if _, dup := e.samplePatent[r.SampleID]; dup {
			continue
		}
		e.samples = append(e.samples, r.SampleID)
		e.samplePatent[r.SampleID] = r.PatientID
		if !seenPatient[r.PatientID] {
			seenPatient[r.PatientID] = true
			e.patients = append(e.patients, r.PatientID)
		}
	}
	for _, r := range patients {
		if !seenPatient[r.PatientID] {
			seenPatient[r.PatientID] = true
			e.patients = append(e.patients, r.PatientID)
		}
	}
	return nil
}

// ---------- study metadata, cancer type, README ----------

// cancerStudyMetadata writes meta_study.txt (CancerStudyMetadataExporter).
// Absent citation / pmid are not written; groups are stored upper-cased as a
// HashSet joined by ';'; the reference genome defaults to hg19.
func (e *studyExport) cancerStudyMetadata() error {
	m := e.study
	opt := func(v string) *string {
		if v == "" {
			return nil
		}
		return &v
	}
	genome := m.ReferenceGenome
	if genome == "" {
		genome = "hg19"
	}
	return e.writeMeta("meta_study.txt", []metaEntry{
		{"cancer_study_identifier", strp(m.StudyID)},
		{"type_of_cancer", strp(strings.ToLower(m.TypeOfCancer))},
		{"name", strp(m.Name)},
		{"description", strp(m.Description)},
		{"citation", opt(m.Citation)},
		{"pmid", opt(m.Pmid)},
		{"groups", strp(javaGroups(m.Groups))},
		{"reference_genome", strp(genome)},
	})
}

// javaGroups is how cbioportal-core stores a study's groups: upper-cased,
// split on ';' into a HashSet, joined back with ';' in the set's order.
func javaGroups(groups string) string {
	if groups == "" {
		return ""
	}
	parts := javaSplit(strings.ToUpper(groups), ";")
	capacity := 16
	if need := int(float32(len(parts))/0.75) + 1; need > capacity {
		for capacity < need {
			capacity *= 2
		}
	}
	buckets := make([][]string, capacity)
	seen := map[string]bool{}
	n := 0
	for _, g := range parts {
		if seen[g] {
			continue
		}
		seen[g] = true
		i := javaSpread(javaStringHash(g)) & int32(capacity-1)
		buckets[i] = append(buckets[i], g)
		n++
	}
	out := make([]string, 0, n)
	for _, b := range buckets {
		out = append(out, b...)
	}
	return strings.Join(out, ";")
}

// cancerType writes the study's cancer type and its ancestors
// (CancerTypeDataTypeExporter): no header row.
func (e *studyExport) cancerType() error {
	types, err := e.s.queryCancerTypes("", nil)
	if err != nil {
		return err
	}
	byID := make(map[string]CancerType, len(types))
	for _, t := range types {
		byID[t.CancerTypeID] = t
	}
	dataFile := "data_cancer_type.txt"
	if err := e.writeMeta("meta_cancer_type.txt", []metaEntry{
		{"cancer_study_identifier", strp(e.studyID)},
		{"genetic_alteration_type", strp("CANCER_TYPE")},
		{"datatype", strp("CANCER_TYPE")},
		{"data_filename", strp(dataFile)},
	}); err != nil {
		return err
	}
	t, err := e.createTSV(dataFile)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for id := strings.ToLower(e.study.TypeOfCancer); !seen[id]; {
		ct, ok := byID[id]
		if !ok {
			break
		}
		seen[id] = true
		t.line([]string{ct.CancerTypeID, ct.Name, ct.DedicatedColor, ct.Parent})
		id = ct.Parent
	}
	return t.close()
}

func (e *studyExport) readme() error {
	f, err := e.create("README.txt")
	if err != nil {
		return err
	}
	_, err = io.WriteString(f, "This is a README file for the study "+e.studyID+".\n"+
		"This study export may not include all data types available in the study, as export is implemented only for certain data types.\n"+
		"Refer to the documentation for details on the data files included in this export.\n")
	return err
}

// ---------- clinical data ----------

type exportAttr struct {
	ID          string `json:"clinical_attribute_id"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Datatype    string `json:"datatype"`
	Priority    string `json:"priority"`
}

var (
	patientIDAttr = exportAttr{"PATIENT_ID", "Patient Identifier", "Patient Identifier", "STRING", "1"}
	sampleIDAttr  = exportAttr{"SAMPLE_ID", "Sample Identifier", "Sample Identifier", "STRING", "1"}
)

// excludedClinicalAttributes are the computed attributes the export leaves
// out (ClinicalAttributeDataMapper).
var excludedClinicalAttributes = map[bool]string{
	false: "'MUTATION_COUNT', 'FRACTION_GENOME_ALTERED'",
	true:  "'SAMPLE_COUNT'",
}

func (e *studyExport) clinicalAttrs(patient bool) ([]exportAttr, error) {
	flag := 0
	if patient {
		flag = 1
	}
	return ch.QueryP[exportAttr](e.s.CH, fmt.Sprintf(
		"SELECT clinical_attribute_id, display_name, description, datatype, priority FROM clinical_attribute_meta "+
			"WHERE study_id = {study:String} AND patient_attribute = %d AND clinical_attribute_id NOT IN (%s) ORDER BY clinical_attribute_id",
		flag, excludedClinicalAttributes[patient]), ch.Params{"study": e.studyID})
}

type clinicalValue struct {
	ID    string `json:"id"`
	Attr  string `json:"attr_id"`
	Value string `json:"attr_value"`
}

func (e *studyExport) clinicalValues(patient bool) (map[string]map[string]string, error) {
	table, idCol := "clinical_sample", "sample_id"
	if patient {
		table, idCol = "clinical_patient", "patient_id"
	}
	rows, err := ch.QueryP[clinicalValue](e.s.CH, fmt.Sprintf(
		"SELECT %s AS id, attr_id, attr_value FROM %s WHERE study_id = {study:String} AND attr_id NOT IN (%s)",
		idCol, table, excludedClinicalAttributes[patient]), ch.Params{"study": e.studyID})
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, r := range rows {
		if out[r.ID] == nil {
			out[r.ID] = map[string]string{}
		}
		out[r.ID][r.Attr] = r.Value
	}
	return out, nil
}

// writeClinicalTable writes a clinical attributes file
// (ClinicalAttributesTable): four comment rows (display names, descriptions,
// datatypes, priorities), the attribute ids, then one row per case.
func (e *studyExport) writeClinicalTable(datatype string, attrs []exportAttr, rows [][]string) error {
	dataFile := "data_clinical_" + strings.ToLower(datatype) + ".txt"
	if err := e.writeMeta("meta_clinical_"+strings.ToLower(datatype)+".txt", []metaEntry{
		{"cancer_study_identifier", strp(e.studyID)},
		{"genetic_alteration_type", strp("CLINICAL")},
		{"datatype", strp(datatype)},
		{"data_filename", strp(dataFile)},
	}); err != nil {
		return err
	}
	t, err := e.createTSV(dataFile)
	if err != nil {
		return err
	}
	col := func(f func(exportAttr) string) []string {
		out := make([]string, len(attrs))
		for i, a := range attrs {
			out[i] = f(a)
		}
		return out
	}
	t.comment(col(func(a exportAttr) string { return a.DisplayName }))
	t.comment(col(func(a exportAttr) string { return a.Description }))
	t.comment(col(func(a exportAttr) string { return a.Datatype }))
	t.comment(col(func(a exportAttr) string { return a.Priority }))
	t.line(col(func(a exportAttr) string { return a.ID }))
	for _, r := range rows {
		t.line(r)
	}
	return t.close()
}

// clinicalPatientAttributes writes the patient attributes of every patient
// when the study has any patient attribute value.
func (e *studyExport) clinicalPatientAttributes() error {
	values, err := e.clinicalValues(true)
	if err != nil || len(values) == 0 {
		return err
	}
	if err := e.loadCases(); err != nil {
		return err
	}
	attrs, err := e.clinicalAttrs(true)
	if err != nil {
		return err
	}
	attrs = append([]exportAttr{patientIDAttr}, attrs...)
	rows := make([][]string, 0, len(e.patients))
	for _, p := range e.patients {
		row := []string{p}
		for _, a := range attrs[1:] {
			row = append(row, values[p][a.ID])
		}
		rows = append(rows, row)
	}
	return e.writeClinicalTable("PATIENT_ATTRIBUTES", attrs, rows)
}

// clinicalSampleAttributes writes the sample attributes of every sample;
// written for any study with samples, even without sample attributes.
func (e *studyExport) clinicalSampleAttributes() error {
	if err := e.loadCases(); err != nil || len(e.samples) == 0 {
		return err
	}
	values, err := e.clinicalValues(false)
	if err != nil {
		return err
	}
	attrs, err := e.clinicalAttrs(false)
	if err != nil {
		return err
	}
	attrs = append([]exportAttr{patientIDAttr, sampleIDAttr}, attrs...)
	rows := make([][]string, 0, len(e.samples))
	for _, smp := range e.samples {
		row := []string{e.samplePatent[smp], smp}
		for _, a := range attrs[2:] {
			row = append(row, values[smp][a.ID])
		}
		rows = append(rows, row)
	}
	return e.writeClinicalTable("SAMPLE_ATTRIBUTES", attrs, rows)
}

// timelineDefaults are the values written for an event without them
// (ClinicalTimelineDataTypeExporter.DEFAULT_VALUES).
var timelineDefaults = map[string]string{"STYLE_COLOR": "#1f77b4", "STYLE_SHAPE": "circle"}

var timelineFixedColumns = map[string]bool{
	"study_id": true, "source_file": true, "alteration_type": true, "datatype": true, "profile_stable_id": true,
	"patient_id": true, "start_date": true, "stop_date": true, "event_type": true,
}

var fileNameUnsafe = regexp.MustCompile(`[ \\/:*?"<>|]`)

// clinicalTimeline writes one timeline file per event type
// (ClinicalTimelineDataTypeExporter): events in import order (files in
// name order, then line order) of the study's known patients; the extra
// columns are the event type's data keys in sorted order, each event's own
// non-empty values (staged column names, upper-cased).
func (e *studyExport) clinicalTimeline() error {
	cols := e.s.rawColumns("timeline")
	if len(cols) == 0 {
		return nil
	}
	if err := e.loadCases(); err != nil {
		return err
	}
	var keys []string
	sel := []string{"patient_id", "start_date", "event_type"}
	for _, c := range cols {
		if c == "stop_date" {
			sel = append(sel, c)
		}
		if !timelineFixedColumns[c] && !transform.OrderColumns[c] {
			keys = append(keys, c)
			sel = append(sel, "`"+c+"`")
		}
	}
	rows, err := ch.QueryP[map[string]string](e.s.CH, fmt.Sprintf(
		"SELECT %s FROM cbioportal_raw.timeline WHERE study_id = {study:String} ORDER BY source_file, %s",
		strings.Join(sel, ", "), e.s.rawFileOrder("timeline", "")), ch.Params{"study": e.studyID})
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, p := range e.patients {
		known[p] = true
	}
	type event struct {
		patient, start, stop, eventType string
		data                            map[string]string
	}
	var types []string
	byType := map[string][]event{}
	for _, r := range rows {
		if !known[r["patient_id"]] {
			continue
		}
		start, err := strconv.ParseInt(strings.TrimSpace(r["start_date"]), 10, 64)
		if err != nil {
			continue
		}
		ev := event{patient: r["patient_id"], start: strconv.FormatInt(start, 10), eventType: r["event_type"], data: map[string]string{}}
		if v := strings.TrimSpace(r["stop_date"]); v != "" {
			if stop, err := strconv.ParseInt(v, 10, 64); err == nil {
				ev.stop = strconv.FormatInt(stop, 10)
			}
		}
		for _, k := range keys {
			if v := r[k]; v != "" {
				ev.data[strings.ToUpper(k)] = v
			}
		}
		if _, ok := byType[ev.eventType]; !ok {
			types = append(types, ev.eventType)
		}
		byType[ev.eventType] = append(byType[ev.eventType], ev)
	}
	for _, et := range types {
		events := byType[et]
		keySet := map[string]bool{}
		for _, ev := range events {
			for k := range ev.data {
				keySet[k] = true
			}
		}
		eventKeys := make([]string, 0, len(keySet))
		for k := range keySet {
			eventKeys = append(eventKeys, k)
		}
		sort.Strings(eventKeys)
		part := "clinical_timeline_" + fileNameUnsafe.ReplaceAllString(et, "_") + ".txt"
		dataFile := "data_" + part
		if err := e.writeMeta("meta_"+part, []metaEntry{
			{"cancer_study_identifier", strp(e.studyID)},
			{"genetic_alteration_type", strp("CLINICAL")},
			{"datatype", strp("TIMELINE")},
			{"data_filename", strp(dataFile)},
		}); err != nil {
			return err
		}
		t, err := e.createTSV(dataFile)
		if err != nil {
			return err
		}
		t.line(append([]string{"PATIENT_ID", "START_DATE", "STOP_DATE", "EVENT_TYPE"}, eventKeys...))
		for _, ev := range events {
			row := []string{ev.patient, ev.start, ev.stop, ev.eventType}
			for _, k := range eventKeys {
				v, ok := ev.data[k]
				if !ok {
					v = timelineDefaults[k]
				}
				row = append(row, v)
			}
			t.line(row)
		}
		if err := t.close(); err != nil {
			return err
		}
	}
	return nil
}

// ---------- molecular profiles ----------

type exportProfile struct {
	MolecularProfileID string `json:"molecular_profile_id"`
	StableID           string `json:"profile_stable_id"`
	Name               string `json:"name"`
	Description        string `json:"description"`
	Show               uint8  `json:"show_profile_in_analysis_tab"`
	Datatype           string `json:"datatype"`
}

// stableSuffix is GeneticProfileDatatypeMetadata.getGeneticDatatypeStableId.
func (p *exportProfile) stableSuffix(studyID string) string {
	return strings.ReplaceAll(p.MolecularProfileID, studyID+"_", "")
}

// exportProfiles lists the study's profiles of a type. Discrete copy number
// includes the discrete-long profiles, which the importer stores as DISCRETE.
func (e *studyExport) exportProfiles(alterationType, datatype string) ([]exportProfile, error) {
	types := []string{datatype}
	if alterationType == "COPY_NUMBER_ALTERATION" && datatype == "DISCRETE" {
		types = append(types, "DISCRETE_LONG")
	}
	return ch.QueryP[exportProfile](e.s.CH,
		"SELECT molecular_profile_id, profile_stable_id, name, description, show_profile_in_analysis_tab, datatype "+
			"FROM molecular_profile WHERE study_id = {study:String} AND molecular_alteration_type = {type:String} "+
			"AND datatype IN {datatypes:Array(String)} ORDER BY molecular_profile_id",
		ch.Params{"study": e.studyID, "type": alterationType, "datatypes": types})
}

func profileFileNames(alterationType, datatype, suffix string) (meta, data string) {
	part := strings.ToLower(alterationType) + "_" + strings.ToLower(datatype) + "_" + strings.ToLower(suffix) + ".txt"
	return "meta_" + part, "data_" + part
}

// mafHeader is MafRecord's column order.
var mafHeader = []string{
	"Hugo_Symbol", "Entrez_Gene_Id", "Center", "NCBI_Build", "Chromosome", "Start_Position", "End_Position",
	"Strand", "Variant_Classification", "Variant_Type", "Reference_Allele", "Tumor_Seq_Allele1",
	"Tumor_Seq_Allele2", "dbSNP_RS", "dbSNP_Val_Status", "Tumor_Sample_Barcode", "Matched_Norm_Sample_Barcode",
	"Match_Norm_Seq_Allele1", "Match_Norm_Seq_Allele2", "Tumor_Validation_Allele1", "Tumor_Validation_Allele2",
	"Match_Norm_Validation_Allele1", "Match_Norm_Validation_Allele2", "Verification_Status", "Validation_Status",
	"Mutation_Status", "Sequencing_Phase", "Sequence_Source", "Validation_Method", "Score", "BAM_File",
	"Sequencer", "HGVSp_Short", "t_alt_count", "t_ref_count", "n_alt_count", "n_ref_count",
}

// maf writes each MAF profile of a mutation type (MafDataTypeExporter): the
// stored records of the study's samples, in file order, with string columns
// as the importer stores them (empty as NA), positions and read counts as
// numbers (empty when absent) and HGVSp_Short from the stored protein change.
func (e *studyExport) maf(alterationType, table string) error {
	profiles, err := e.exportProfiles(alterationType, "MAF")
	if err != nil || len(profiles) == 0 {
		return err
	}
	cols := e.s.rawColumns(table)
	has := make(map[string]bool, len(cols))
	for _, c := range cols {
		has[c] = true
	}
	if err := e.loadCases(); err != nil {
		return err
	}
	known := map[string]bool{}
	for _, smp := range e.samples {
		known[smp] = true
	}
	for i := range profiles {
		p := &profiles[i]
		metaFile, dataFile := profileFileNames(alterationType, "MAF", p.stableSuffix(e.studyID))
		if err := e.writeMeta(metaFile, profileMetaEntries(e.studyID, alterationType, "MAF", p, dataFile)); err != nil {
			return err
		}
		var rows []map[string]string
		if len(cols) > 0 {
			rows, err = ch.QueryP[map[string]string](e.s.CH, "SELECT * EXCEPT (entrez, ord), toString(entrez) AS entrez FROM ("+
				transform.MafRecordsSQL("cbioportal_raw."+table, table, e.s.CH.Database(), has)+
				") ORDER BY ord", ch.Params{"study": e.studyID, "profile": p.StableID})
			if err != nil {
				return err
			}
		}
		t, err := e.createTSV(dataFile)
		if err != nil {
			return err
		}
		t.line(mafHeader)
		for _, r := range rows {
			if !known[r["tumor_sample_barcode"]] {
				continue
			}
			na := func(k string) string { return naString(r[k]) }
			count := func(k string) string {
				if v := javaCount(r[k]); v != nil {
					return strconv.FormatInt(*v, 10)
				}
				return ""
			}
			pos := func(k string) string {
				if v, err := strconv.ParseInt(strings.TrimSpace(r[k]), 10, 64); err == nil {
					return strconv.FormatInt(v, 10)
				}
				return ""
			}
			hgvsp := ""
			if pc := strings.TrimSpace(r["protein_change"]); pc != "" && pc != "MUTATED" {
				hgvsp = "p." + r["protein_change"]
			}
			t.line([]string{
				r["hugo"], r["entrez"], na("center"), na("ncbi_build"), na("chromosome"), pos("start_position"),
				pos("end_position"), na("strand"), na("variant_classification"), na("variant_type"),
				na("reference_allele"), na("tumor_seq_allele1"), na("tumor_seq_allele2"), na("dbsnp_rs"),
				na("dbsnp_val_status"), r["tumor_sample_barcode"], na("matched_norm_sample_barcode"),
				na("match_norm_seq_allele1"), na("match_norm_seq_allele2"), na("tumor_validation_allele1"),
				na("tumor_validation_allele2"), na("match_norm_validation_allele1"), na("match_norm_validation_allele2"),
				na("verification_status"), na("validation_status"), na("mutation_status"), na("sequencing_phase"),
				na("sequence_source"), na("validation_method"), na("score"), na("bam_file"), na("sequencer"),
				hgvsp, count("t_alt_count"), count("t_ref_count"), count("n_alt_count"), count("n_ref_count"),
			})
		}
		if err := t.close(); err != nil {
			return err
		}
	}
	return nil
}

// svHeader is the export StructuralVariant's column order (Site1_Contig is
// not among them).
var svHeader = []string{
	"Sample_Id", "Site1_Entrez_Gene_Id", "Site1_Hugo_Symbol", "Site1_Ensembl_Transcript_Id", "Site1_Region_Number",
	"Site1_Chromosome", "Site1_Position", "Site1_Region", "Site1_Description", "Site2_Entrez_Gene_Id",
	"Site2_Hugo_Symbol", "Site2_Ensembl_Transcript_Id", "Site2_Region_Number", "Site2_Chromosome",
	"Site2_Position", "Site2_Contig", "Site2_Region", "Site2_Description", "Site2_Effect_On_Frame", "NCBI_Build",
	"DNA_Support", "RNA_Support", "Normal_Read_Count", "Tumor_Read_Count", "Normal_Variant_Count",
	"Tumor_Variant_Count", "Normal_Paired_End_Read_Count", "Tumor_Paired_End_Read_Count",
	"Normal_Split_Read_Count", "Tumor_Split_Read_Count", "Annotation", "Breakpoint_Type", "Connection_Type",
	"Event_Info", "Class", "SV_Length", "Comments", "SV_Status",
}

// structuralVariants writes each SV profile (StructuralVariantDataTypeExporter)
// with the values the structural-variant endpoint reports.
func (e *studyExport) structuralVariants() error {
	profiles, err := e.exportProfiles("STRUCTURAL_VARIANT", "SV")
	if err != nil || len(profiles) == 0 {
		return err
	}
	cols := svSelectColumns(e.s.svViewColumns())
	for i := range profiles {
		p := &profiles[i]
		metaFile, dataFile := profileFileNames("STRUCTURAL_VARIANT", "SV", p.stableSuffix(e.studyID))
		if err := e.writeMeta(metaFile, profileMetaEntries(e.studyID, "STRUCTURAL_VARIANT", "SV", p, dataFile)); err != nil {
			return err
		}
		params := ch.Params{"study": e.studyID, "profile": p.StableID}
		rows, err := ch.QueryP[map[string]any](e.s.CH, fmt.Sprintf(
			"SELECT %s FROM (%s) sv "+
				"INNER JOIN %s sm ON sv.study_id = sm.study_id AND sv.sample_id = sm.sample_id "+
				"LEFT JOIN cbioportal_ref.gene g1 ON g1.entrez_gene_id = sv.e1 "+
				"LEFT JOIN cbioportal_ref.gene g2 ON g2.entrez_gene_id = sv.e2 "+
				"WHERE sv.study_id = {study:String} AND sv.profile_stable_id = {profile:String} ORDER BY sampleId, site1HugoSymbol, site2HugoSymbol",
			strings.Join(cols, ", "), svSiteEntrezSQL, byStudy(e.studyID).samples(paramsBinder(params))), params)
		if err != nil {
			return err
		}
		t, err := e.createTSV(dataFile)
		if err != nil {
			return err
		}
		t.line(svHeader)
		entrez := func(v *int64) string {
			if v == nil || *v < 0 {
				return ""
			}
			return strconv.FormatInt(*v, 10)
		}
		num := func(v int64) string { return strconv.FormatInt(v, 10) }
		for _, r := range rows {
			v := toStructuralVariant(r)
			t.line([]string{
				v.SampleID, entrez(v.Site1EntrezGeneID), v.Site1HugoSymbol, v.Site1EnsemblTranscriptID,
				num(v.Site1RegionNumber), v.Site1Chromosome, num(v.Site1Position), v.Site1Region, v.Site1Description,
				entrez(v.Site2EntrezGeneID), v.Site2HugoSymbol, v.Site2EnsemblTranscriptID, num(v.Site2RegionNumber),
				v.Site2Chromosome, num(v.Site2Position), v.Site2Contig, v.Site2Region, v.Site2Description,
				v.Site2EffectOnFrame, v.NcbiBuild, v.DnaSupport, v.RnaSupport, num(v.NormalReadCount),
				num(v.TumorReadCount), num(v.NormalVariantCount), num(v.TumorVariantCount),
				num(v.NormalPairedEndReadCount), num(v.TumorPairedEndReadCount), num(v.NormalSplitReadCount),
				num(v.TumorSplitReadCount), v.Annotation, v.BreakpointType, v.ConnectionType, v.EventInfo,
				v.VariantClass, num(v.Length), v.Comments, v.SvStatus,
			})
		}
		if err := t.close(); err != nil {
			return err
		}
	}
	return nil
}

// ---------- gene x sample matrices ----------

var tcgaSampleBarcodeRE = regexp.MustCompile(`^(TCGA-\w\w-\w\w\w\w-\d\d)`)

// stableSampleID is StableIdUtil.getSampleId: a TCGA barcode cut to its
// sample part, other ids unchanged.
func stableSampleID(barcode string) string {
	if !strings.HasPrefix(barcode, "TCGA") {
		return barcode
	}
	clean := barcode
	if strings.Contains(clean, "Tumor") {
		clean = strings.ReplaceAll(clean, "Tumor", "01")
	} else if strings.Contains(clean, "Normal") {
		clean = strings.ReplaceAll(clean, "Normal", "11")
	}
	parts := javaSplit(clean, "-")
	if len(parts) < 4 {
		return barcode + "-01"
	}
	id := parts[0] + "-" + parts[1] + "-" + parts[2] + "-" + parts[3]
	if m := tcgaSampleBarcodeRE.FindStringSubmatch(id); m != nil {
		return m[1]
	}
	return id
}

type matrixCell struct {
	Entrez   int64  `json:"entrez"`
	EntityID int64  `json:"entity_id"`
	Hugo     string `json:"hugo"`
	SampleID string `json:"sample_id"`
	Value    string `json:"value"`
	Key      string `json:"gene_key"`
}

// geneMatrix writes each gene x sample profile of a type
// (GeneSampleWideTableDatatypeExporter): Hugo_Symbol, Entrez_Gene_Id and one
// column per profiled sample, genes in genetic-entity order, each gene's
// values as stored by the importer.
func (e *studyExport) geneMatrix(alterationType, datatype string) error {
	profiles, err := e.exportProfiles(alterationType, datatype)
	if err != nil || len(profiles) == 0 {
		return err
	}
	if err := e.loadCases(); err != nil {
		return err
	}
	known := map[string]bool{}
	for _, smp := range e.samples {
		known[smp] = true
	}
	for i := range profiles {
		p := &profiles[i]
		long := p.Datatype == "DISCRETE_LONG"
		table := alterationTypeTable(alterationType)
		if long {
			table = "cna_discrete_long"
		}
		if !e.hasRaw(table) {
			continue
		}
		metaFile, dataFile := profileFileNames(alterationType, datatype, p.stableSuffix(e.studyID))
		if err := e.writeMeta(metaFile, profileMetaEntries(e.studyID, alterationType, datatype, p, dataFile)); err != nil {
			return err
		}
		cells, err := ch.QueryP[matrixCell](e.s.CH, fmt.Sprintf(
			"SELECT gk.resolved_entrez_gene_id AS entrez, ifNull(rg.genetic_entity_id, 0) AS entity_id, "+
				"if(rg.hugo_gene_symbol != '', rg.hugo_gene_symbol, gk.resolved_hugo_gene_symbol) AS hugo, "+
				"t.sample_id AS sample_id, trimBoth(t.value) AS value, concat(t.hugo_symbol, '\\t', t.entrez_gene_id) AS gene_key "+
				"FROM (SELECT *%s, %s AS ord FROM cbioportal_raw.%s WHERE study_id = {study:String} AND profile_stable_id = {profile:String}) t "+
				"INNER JOIN (SELECT * FROM gene_key_map WHERE source = '%s' AND study_id = {study:String} AND profile_stable_id = {profile:String}) gk "+
				"ON gk.hugo_symbol = t.hugo_symbol AND gk.entrez_gene_id = t.entrez_gene_id "+
				"LEFT JOIN cbioportal_ref.gene rg ON rg.entrez_gene_id = gk.resolved_entrez_gene_id "+
				"ORDER BY t.ord",
			missingGeneKeyColumns(e.s.rawColumns(table)), e.s.rawFileOrder(table, ""), table, table),
			ch.Params{"study": e.studyID, "profile": p.StableID})
		if err != nil {
			return err
		}
		var header []string
		if long {
			header = longMatrixSamples(cells, known)
		} else if header, err = e.matrixHeader(p.StableID, known); err != nil {
			return err
		}
		col := make(map[string]int, len(header))
		for j, smp := range header {
			col[smp] = j
		}
		type geneRow struct {
			entityID int64
			hugo     string
			entrez   int64
			key      string
			values   []string
			set      []bool
		}
		genes := map[int64]*geneRow{}
		var order []*geneRow
		for _, c := range cells {
			j, ok := col[stableSampleID(c.SampleID)]
			if !ok {
				continue
			}
			g := genes[c.Entrez]
			if g == nil {
				g = &geneRow{entityID: c.EntityID, hugo: c.Hugo, entrez: c.Entrez, key: c.Key,
					values: make([]string, len(header)), set: make([]bool, len(header))}
				genes[c.Entrez] = g
				order = append(order, g)
			}
			if (!long && c.Key != g.key) || g.set[j] {
				continue
			}
			v := c.Value
			if long {
				if v == "-1.5" {
					v = "-2"
				}
				n, err := strconv.Atoi(v)
				if err != nil {
					continue
				}
				v = strconv.Itoa(int(int16(n)))
			}
			g.values[j], g.set[j] = v, true
		}
		sort.SliceStable(order, func(a, b int) bool { return order[a].entityID < order[b].entityID })
		t, err := e.createTSV(dataFile)
		if err != nil {
			return err
		}
		t.line(append([]string{"Hugo_Symbol", "Entrez_Gene_Id"}, header...))
		for _, g := range order {
			// The stored comma-joined values are split with String.split,
			// which drops trailing empty values.
			n := len(g.values)
			for n > 0 && g.values[n-1] == "" {
				n--
			}
			if n != len(header) {
				t.close()
				return fmt.Errorf("Error while writing data to file %s for data type Table.", dataFile)
			}
			entrez := ""
			if g.entrez >= 0 {
				entrez = strconv.FormatInt(g.entrez, 10)
			}
			t.line(append([]string{g.hugo, entrez}, g.values...))
		}
		if err := t.close(); err != nil {
			return err
		}
	}
	return nil
}

// missingGeneKeyColumns selects, as empty strings, the gene key columns a
// staged matrix lacks (have are its columns): a file may identify genes by
// Hugo_Symbol or Entrez_Gene_Id alone, and gene_key_map keys such rows with
// an empty string for the absent one. The result extends a SELECT * list.
func missingGeneKeyColumns(have []string) string {
	var b strings.Builder
	for _, c := range []string{"hugo_symbol", "entrez_gene_id"} {
		if !slices.Contains(have, c) {
			b.WriteString(", '' AS " + c)
		}
	}
	return b.String()
}

// matrixHeader is a wide matrix's sample columns: the file header's samples
// that are in the study, as stable sample ids.
func (e *studyExport) matrixHeader(stableID string, known map[string]bool) ([]string, error) {
	type row struct {
		SampleID string `json:"sample_id"`
	}
	rows, err := ch.QueryP[row](e.s.CH,
		"SELECT sample_id FROM cbioportal_raw.profile_sample WHERE study_id = {study:String} AND profile_stable_id = {profile:String} "+
			"AND kind = 'header' ORDER BY "+e.s.rawFileOrder("profile_sample", ""), ch.Params{"study": e.studyID, "profile": stableID})
	if err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for _, r := range rows {
		id := stableSampleID(r.SampleID)
		if known[id] && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// longMatrixSamples is a discrete-long profile's sample list as
// ImportCnaDiscreteLongData builds it: the column keys of a gene x sample
// table filled in file order — genes in first-seen order, each gene's samples
// in first-seen order.
func longMatrixSamples(cells []matrixCell, known map[string]bool) []string {
	var genes []int64
	bySample := map[int64][]string{}
	for _, c := range cells {
		id := stableSampleID(c.SampleID)
		if !known[id] {
			continue
		}
		if _, ok := bySample[c.Entrez]; !ok {
			genes = append(genes, c.Entrez)
		}
		bySample[c.Entrez] = append(bySample[c.Entrez], id)
	}
	var out []string
	seen := map[string]bool{}
	for _, g := range genes {
		for _, id := range bySample[g] {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// ---------- segments, gene panel matrix, case lists ----------

// copyNumberSegments writes the study's segments (CnaSegDatatypeExporter) as
// ImportCopyNumberSegmentData stores them: positions as integers, segments
// with start >= end and unknown samples left out, the mean as a Java double.
func (e *studyExport) copyNumberSegments() error {
	if !e.hasRaw("cna_seg") {
		return nil
	}
	type segRow struct {
		ID       string `json:"id"`
		Chrom    string `json:"chrom"`
		LocStart string `json:"loc_start"`
		LocEnd   string `json:"loc_end"`
		NumMark  string `json:"num_mark"`
		SegMean  string `json:"seg_mean"`
	}
	rows, err := ch.QueryP[segRow](e.s.CH,
		"SELECT id, chrom, loc_start, loc_end, num_mark, seg_mean FROM cbioportal_raw.cna_seg WHERE study_id = {study:String} ORDER BY "+
			e.s.rawFileOrder("cna_seg", ""), ch.Params{"study": e.studyID})
	if err != nil {
		return err
	}
	if err := e.loadCases(); err != nil {
		return err
	}
	known := map[string]bool{}
	for _, smp := range e.samples {
		known[smp] = true
	}
	var out [][]string
	for _, r := range rows {
		id := stableSampleID(r.ID)
		start, err1 := strconv.ParseFloat(strings.TrimSpace(r.LocStart), 64)
		end, err2 := strconv.ParseFloat(strings.TrimSpace(r.LocEnd), 64)
		probes, ok := new(big.Float).SetString(strings.TrimSpace(r.NumMark))
		mean, err3 := strconv.ParseFloat(strings.TrimSpace(r.SegMean), 64)
		if !known[id] || err1 != nil || err2 != nil || err3 != nil || !ok || int64(start) >= int64(end) {
			continue
		}
		n, _ := probes.Int64()
		out = append(out, []string{id, strings.TrimSpace(r.Chrom), strconv.FormatInt(int64(start), 10),
			strconv.FormatInt(int64(end), 10), strconv.FormatInt(n, 10),
			databin.JavaDoubleString(mean)})
	}
	if len(out) == 0 {
		return nil
	}
	genome := e.study.ReferenceGenome
	if genome == "" {
		genome = "hg19"
	}
	dataFile := "data_copy_number_alteration_seg.txt"
	if err := e.writeMeta("meta_copy_number_alteration_seg.txt", []metaEntry{
		{"cancer_study_identifier", strp(e.studyID)},
		{"genetic_alteration_type", strp("COPY_NUMBER_ALTERATION")},
		{"datatype", strp("SEG")},
		{"description", strp("Copy number alteration segments")},
		{"reference_genome_id", strp(genome)},
		{"data_filename", strp(dataFile)},
	}); err != nil {
		return err
	}
	t, err := e.createTSV(dataFile)
	if err != nil {
		return err
	}
	t.line([]string{"ID", "chrom", "loc.start", "loc.end", "num.mark", "seg.mean"})
	for _, r := range out {
		t.line(r)
	}
	return t.close()
}

// genePanelMatrix writes the panels samples were profiled with
// (GenePanelMatrixDatatypeExporter): one column per profile that has a panel
// for any sample, NA where a sample has none.
func (e *studyExport) genePanelMatrix() error {
	type row struct {
		SampleID  string `json:"sample_id"`
		ProfileID string `json:"molecular_profile_id"`
		Panel     string `json:"gene_panel_id"`
	}
	rows, err := ch.QueryP[row](e.s.CH,
		"SELECT sample_id, molecular_profile_id, gene_panel_id FROM sample_profile "+
			"WHERE study_id = {study:String} AND gene_panel_id IS NOT NULL", ch.Params{"study": e.studyID})
	if err != nil || len(rows) == 0 {
		return err
	}
	if err := e.loadCases(); err != nil {
		return err
	}
	panels := map[string]map[string]string{}
	profileSet := map[string]bool{}
	for _, r := range rows {
		if panels[r.SampleID] == nil {
			panels[r.SampleID] = map[string]string{}
		}
		panels[r.SampleID][r.ProfileID] = r.Panel
		profileSet[r.ProfileID] = true
	}
	profiles := make([]string, 0, len(profileSet))
	for p := range profileSet {
		profiles = append(profiles, p)
	}
	sort.Strings(profiles)
	dataFile := "data_gene_panel_matrix_gene_panel_matrix.txt"
	if err := e.writeMeta("meta_gene_panel_matrix_gene_panel_matrix.txt",
		profileMetaEntries(e.studyID, "GENE_PANEL_MATRIX", "GENE_PANEL_MATRIX", nil, dataFile)); err != nil {
		return err
	}
	t, err := e.createTSV(dataFile)
	if err != nil {
		return err
	}
	header := []string{"SAMPLE_ID"}
	for _, p := range profiles {
		header = append(header, strings.ReplaceAll(p, e.studyID+"_", ""))
	}
	t.line(header)
	for _, smp := range e.samples {
		byProfile, ok := panels[smp]
		if !ok {
			continue
		}
		line := []string{smp}
		for _, p := range profiles {
			v, ok := byProfile[p]
			if !ok {
				v = "NA"
			}
			line = append(line, v)
		}
		t.line(line)
	}
	return t.close()
}

// caseLists writes case_lists/cases_<suffix>.txt for every case list with
// samples (CaseListsExporter), sample ids sorted.
func (e *studyExport) caseLists() error {
	type row struct {
		ListID      string `json:"list_id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		SampleID    string `json:"sample_id"`
	}
	p := ch.Params{}
	sc := byStudy(e.studyID)
	rows, err := ch.QueryP[row](e.s.CH,
		"SELECT DISTINCT sl.list_id AS list_id, sl.name AS name, sl.description AS description, sl.sample_id AS sample_id "+
			"FROM "+sc.rows(paramsBinder(p), tSampleList)+" sl INNER JOIN "+sc.samples(paramsBinder(p))+
			" s ON s.study_id = sl.study_id AND s.sample_id = sl.sample_id ORDER BY list_id, sample_id", p)
	if err != nil {
		return err
	}
	for i := 0; i < len(rows); {
		j := i
		var ids []string
		for j < len(rows) && rows[j].ListID == rows[i].ListID {
			ids = append(ids, rows[j].SampleID)
			j++
		}
		sort.Strings(ids)
		l := rows[i]
		suffix := strings.ReplaceAll(l.ListID, e.studyID+"_", "")
		if err := e.writeMeta("case_lists/cases_"+suffix+".txt", []metaEntry{
			{"cancer_study_identifier", strp(e.studyID)},
			{"stable_id", strp(l.ListID)},
			{"case_list_name", strp(l.Name)},
			{"case_list_description", strp(l.Description)},
			{"case_list_ids", strp(strings.Join(ids, "\t"))},
		}); err != nil {
			return err
		}
		i = j
	}
	return nil
}

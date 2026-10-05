package transform

import "fmt"

// cbioportal-core numbers studies, patients, samples and segments as it
// stores them, and the Java backend returns many lists in that (internal id)
// order. The transform reproduces the numbering from the staged files:
// studies in import order, each study's rows in file order.

// OrderColumns are the staging columns the ingest writes to position a row
// in its study's files: file_num, the file among the study's data files, and
// row_num, the row among the file's rows. They are bookkeeping, not data.
var OrderColumns = map[string]bool{"file_num": true, "row_num": true}

// StagingOrderSQL is the expression ordering a staging table's rows of one
// study as its files list them: (file_num, row_num), then the row's storage
// position (insert block of its part, offset in the part). The storage
// position is the fallback for rows staged before file_num and row_num were
// written, which read 0 for both (or, when the table lacks the columns,
// hasOrderColumns is false). prefix qualifies the columns (e.g. "m.").
func StagingOrderSQL(prefix string, hasOrderColumns bool) string {
	storage := fmt.Sprintf("toUInt64OrZero(splitByChar('_', %[1]s_part)[2]), %[1]s_part_offset", prefix)
	if !hasOrderColumns {
		return "(toUInt32(0), toUInt64(0), " + storage + ")"
	}
	return fmt.Sprintf("(%[1]sfile_num, %[1]srow_num, %[2]s)", prefix, storage)
}

// fileOrderSQL is StagingOrderSQL for the positionedSources, whose stand-ins
// supply the order columns where the table lacks them.
var fileOrderSQL = StagingOrderSQL("", true)

// studyRankSQL numbers the staged studies in import order (study_id,
// study_rank): by the time the study was ingested (a re-ingested study comes
// last, as a re-imported one does), else by the position of its row. Only
// studies staged before import_time was recorded tie on it; cancer_study has
// no order columns, so its row position is the storage position.
func studyRankSQL(rawDB string) string {
	return fmt.Sprintf(
		"SELECT study_id, toUInt64(row_number() OVER (ORDER BY t, pos, study_id)) AS study_rank FROM ("+
			"SELECT study_id, max(import_time) AS t, min(%s) AS pos FROM `%s`.cancer_study GROUP BY study_id)",
		fileOrderSQL, rawDB)
}

// sampleSQL builds the samples with ImportClinicalData's internal ids: in the
// order of the sample file, study by study.
func sampleSQL(rawDB string) string {
	return fmt.Sprintf(
		"SELECT s.study_id AS study_id, s.sample_id AS sample_id, s.patient_id AS patient_id, "+
			"toUInt64(row_number() OVER (ORDER BY r.study_rank, s.pos, s.study_id, s.sample_id, s.patient_id)) AS internal_id "+
			"FROM (SELECT study_id, sample_id, patient_id, min(%[1]s) AS pos FROM `%[2]s`.clinical_sample "+
			"WHERE sample_id != '' GROUP BY study_id, sample_id, patient_id) s "+
			"LEFT JOIN (%[3]s) r ON r.study_id = s.study_id",
		fileOrderSQL, rawDB, studyRankSQL(rawDB))
}

// patientSQL builds the patients with ImportClinicalData's internal ids: the
// sample file adds each patient at its first sample, the patient file then
// adds the patients without samples, study by study.
func patientSQL(rawDB string) string {
	return fmt.Sprintf(
		"SELECT p.study_id AS study_id, p.patient_id AS patient_id, "+
			"toUInt64(row_number() OVER (ORDER BY r.study_rank, p.pos, p.study_id, p.patient_id)) AS internal_id "+
			"FROM (SELECT study_id, patient_id, min(pos) AS pos FROM ("+
			"SELECT study_id, patient_id, (0, %[1]s) AS pos FROM `%[2]s`.clinical_sample WHERE sample_id != '' AND patient_id != '' "+
			"UNION ALL SELECT study_id, patient_id, (1, %[1]s) AS pos FROM `%[2]s`.clinical_patient WHERE patient_id != ''"+
			") GROUP BY study_id, patient_id) p "+
			"LEFT JOIN (%[3]s) r ON r.study_id = p.study_id",
		fileOrderSQL, rawDB, studyRankSQL(rawDB))
}

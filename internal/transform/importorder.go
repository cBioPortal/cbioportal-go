package transform

import "fmt"

// cbioportal-core numbers studies, patients, samples and segments as it
// stores them, and the Java backend returns many lists in that (internal id)
// order. The transform reproduces the numbering from the staged files:
// studies in import order, each study's rows in file order.

// fileOrderSQL is a staging row's position in the files of its table: the
// insert block of its part, then its offset in the part. Rows of one file keep
// their order through merges (parts merge in block order and the sort key,
// study and file, ties within a file).
const fileOrderSQL = "(toUInt64OrZero(splitByChar('_', _part)[2]), _part_offset)"

// studyRankSQL numbers the staged studies in import order (study_id,
// study_rank): by the time the study was ingested (a re-ingested study comes
// last, as a re-imported one does), else by the position of its row.
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

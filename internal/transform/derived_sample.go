package transform

import "fmt"

// sampleDerivedSQL reproduces prod's `sample_derived` column-store table as a
// SELECT over our transform views. Prod sources sample/patient from OLTP-mirror
// tables; we source them from `cbioportal.sample` (study_id, sample_id,
// patient_id). sample_type comes from the SAMPLE_TYPE clinical attribute (long
// view `cbioportal.clinical_sample`), sequenced from membership in the
// `<study>_sequenced` sample list, and copy_number_segment_present from the
// presence of the sample id in the raw cna_seg staging table.
//
// internal_id / patient_internal_id are synthesized deterministically from the
// unique ids so joins with the other derived tables line up.
func sampleDerivedSQL(rawDB, refDB string) string {
	return fmt.Sprintf(`SELECT
  concat(s.study_id, '_', s.sample_id) AS sample_unique_id,
  base64Encode(s.sample_id) AS sample_unique_id_base64,
  s.sample_id AS sample_stable_id,
  concat(s.study_id, '_', s.patient_id) AS patient_unique_id,
  base64Encode(s.patient_id) AS patient_unique_id_base64,
  s.patient_id AS patient_stable_id,
  s.study_id AS cancer_study_identifier,
  toInt32(cityHash64(concat(s.study_id, '_', s.sample_id)) %% 2147483647) AS internal_id,
  toInt32(cityHash64(concat(s.study_id, '_', s.patient_id)) %% 2147483647) AS patient_internal_id,
  ifNull(st.attr_value, '') AS sample_type,
  if(concat(s.study_id, '_', s.sample_id) IN (SELECT concat(study_id, '_', sample_id) FROM cbioportal.sample_list WHERE list_id = concat(study_id, '_sequenced')), 1, 0) AS sequenced,
  if(concat(s.study_id, '_', s.sample_id) IN (SELECT concat(study_id, '_', id) FROM `+"`%s`"+`.cna_seg), 1, 0) AS copy_number_segment_present
FROM cbioportal.sample s
LEFT JOIN (
  SELECT study_id, sample_id, any(attr_value) AS attr_value
  FROM cbioportal.clinical_sample
  WHERE attr_id = 'SAMPLE_TYPE'
  GROUP BY study_id, sample_id
) st ON s.study_id = st.study_id AND s.sample_id = st.sample_id`, rawDB)
}

// clinicalDataDerivedSQL reproduces prod's `clinical_data_derived` table. Prod
// cross-joins every study attribute against every sample/patient and LEFT JOINs
// the value; we instead UNION ALL our long-format clinical views, which already
// carry only the (id, attr_id, attr_value) rows that have a value. Sample-level
// rows set sample_unique_id and resolve patient_unique_id via `cbioportal.sample`;
// patient-level rows leave sample_unique_id empty, matching prod's INSERT.
func clinicalDataDerivedSQL(rawDB, refDB string) string {
	return `SELECT
  toInt32(cityHash64(concat(cse.study_id, '_', cse.sample_id)) % 2147483647) AS internal_id,
  concat(cse.study_id, '_', cse.sample_id) AS sample_unique_id,
  concat(cse.study_id, '_', s.patient_id) AS patient_unique_id,
  cse.attr_id AS attribute_name,
  cse.attr_value AS attribute_value,
  cse.study_id AS cancer_study_identifier,
  'sample' AS type
FROM cbioportal.clinical_sample cse
LEFT JOIN cbioportal.sample s ON cse.study_id = s.study_id AND cse.sample_id = s.sample_id
UNION ALL
SELECT
  toInt32(cityHash64(concat(cpa.study_id, '_', cpa.patient_id)) % 2147483647) AS internal_id,
  '' AS sample_unique_id,
  concat(cpa.study_id, '_', cpa.patient_id) AS patient_unique_id,
  cpa.attr_id AS attribute_name,
  cpa.attr_value AS attribute_value,
  cpa.study_id AS cancer_study_identifier,
  'patient' AS type
FROM cbioportal.clinical_patient cpa`
}

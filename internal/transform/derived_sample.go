package transform

import (
	"fmt"
	"strings"
)

// sampleDerivedSQL reproduces prod's `sample_derived` column-store table as a
// SELECT over our transform views. Prod sources sample/patient from OLTP-mirror
// tables; we source them from the target-DB `sample` view (study_id, sample_id,
// patient_id). sample_type follows the importer: TCGA barcode code, else a valid
// SAMPLE_TYPE clinical value, else Primary Solid Tumor (long
// target-DB view `clinical_sample`), sequenced from membership in the
// `<study>_sequenced` sample list, and copy_number_segment_present from the
// presence of the sample id in the raw cna_seg staging table.
//
// internal_id / patient_internal_id are the sample's and patient's internal
// ids (the `sample` and `patient` tables).
func sampleDerivedSQL(rawDB, refDB, targetDB string) string {
	return inTarget(targetDB, fmt.Sprintf(`SELECT
  concat(s.study_id, '_', s.sample_id) AS sample_unique_id,
  base64Encode(s.sample_id) AS sample_unique_id_base64,
  s.sample_id AS sample_stable_id,
  concat(s.study_id, '_', s.patient_id) AS patient_unique_id,
  base64Encode(s.patient_id) AS patient_unique_id_base64,
  s.patient_id AS patient_stable_id,
  s.study_id AS cancer_study_identifier,
  toInt32(s.internal_id) AS internal_id,
  toInt32(pt.internal_id) AS patient_internal_id,
  multiIf(
    match(s.sample_id, '^TCGA-\\w\\w-\\w\\w\\w\\w-\\d\\d'),
    transform(extract(s.sample_id, '^TCGA-\\w\\w-\\w\\w\\w\\w-(\\d\\d)'),
      ['01', '02', '03', '04', '06', '10', '11'],
      ['Primary Solid Tumor', 'Recurrent Solid Tumor', 'Primary Blood Tumor', 'Recurrent Blood Tumor', 'Metastatic', 'Blood Derived Normal', 'Solid Tissues Normal'],
      'Primary Solid Tumor'),
    upper(replaceAll(trimBoth(ifNull(st.attr_value, '')), ' ', '_')) IN ('PRIMARY_SOLID_TUMOR', 'RECURRENT_SOLID_TUMOR', 'PRIMARY_BLOOD_TUMOR', 'RECURRENT_BLOOD_TUMOR', 'METASTATIC', 'BLOOD_DERIVED_NORMAL', 'SOLID_TISSUES_NORMAL'),
    transform(upper(replaceAll(trimBoth(ifNull(st.attr_value, '')), ' ', '_')),
      ['PRIMARY_SOLID_TUMOR', 'RECURRENT_SOLID_TUMOR', 'PRIMARY_BLOOD_TUMOR', 'RECURRENT_BLOOD_TUMOR', 'METASTATIC', 'BLOOD_DERIVED_NORMAL', 'SOLID_TISSUES_NORMAL'],
      ['Primary Solid Tumor', 'Recurrent Solid Tumor', 'Primary Blood Tumor', 'Recurrent Blood Tumor', 'Metastatic', 'Blood Derived Normal', 'Solid Tissues Normal'],
      'Primary Solid Tumor'),
    'Primary Solid Tumor') AS sample_type,
  if(concat(s.study_id, '_', s.sample_id) IN (SELECT concat(study_id, '_', sample_id) FROM {{target}}.sample_list WHERE list_id = concat(study_id, '_sequenced')), 1, 0) AS sequenced,
  if(concat(s.study_id, '_', s.sample_id) IN (SELECT concat(study_id, '_', id) FROM `+"`%s`"+`.cna_seg), 1, 0) AS copy_number_segment_present
FROM {{target}}.sample s
LEFT JOIN {{target}}.patient pt ON pt.study_id = s.study_id AND pt.patient_id = s.patient_id
LEFT JOIN (
  SELECT study_id, sample_id, any(attr_value) AS attr_value
  FROM {{target}}.clinical_sample
  WHERE attr_id = 'SAMPLE_TYPE'
  GROUP BY study_id, sample_id
) st ON s.study_id = st.study_id AND s.sample_id = st.sample_id`, rawDB))
}

// clinicalDataDerivedSQL reproduces prod's `clinical_data_derived` table. Prod
// cross-joins every study attribute against every sample/patient and LEFT JOINs
// the value; we instead UNION ALL our long-format clinical views, which already
// carry only the (id, attr_id, attr_value) rows that have a value. Sample-level
// rows set sample_unique_id and resolve patient_unique_id via the target-DB `sample` view;
// patient-level rows leave sample_unique_id empty, matching prod's INSERT.
func clinicalDataDerivedSQL(rawDB, refDB, targetDB string) string {
	return inTarget(targetDB, `SELECT
  toInt32(s.internal_id) AS internal_id,
  concat(cse.study_id, '_', cse.sample_id) AS sample_unique_id,
  concat(cse.study_id, '_', s.patient_id) AS patient_unique_id,
  cse.attr_id AS attribute_name,
  cse.attr_value AS attribute_value,
  cse.study_id AS cancer_study_identifier,
  'sample' AS type
FROM {{target}}.clinical_sample cse
LEFT JOIN {{target}}.sample s ON cse.study_id = s.study_id AND cse.sample_id = s.sample_id
UNION ALL
SELECT
  toInt32(p.internal_id) AS internal_id,
  '' AS sample_unique_id,
  concat(cpa.study_id, '_', cpa.patient_id) AS patient_unique_id,
  cpa.attr_id AS attribute_name,
  cpa.attr_value AS attribute_value,
  cpa.study_id AS cancer_study_identifier,
  'patient' AS type
FROM {{target}}.clinical_patient cpa
LEFT JOIN {{target}}.patient p ON cpa.study_id = p.study_id AND cpa.patient_id = p.patient_id`)
}

// inTarget substitutes the transform target database for the {{target}}
// placeholder that derived-view SQL uses to reference sibling views.
func inTarget(targetDB, sql string) string {
	return strings.ReplaceAll(sql, "{{target}}", "`"+targetDB+"`")
}

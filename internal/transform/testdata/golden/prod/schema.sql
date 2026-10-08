-- The Java importer's tables the from-prod transform reads, from java_e2e.
CREATE TABLE {db}.cancer_study
(
    `cancer_study_id` Int64,
    `cancer_study_identifier` Nullable(String),
    `type_of_cancer_id` String,
    `name` String,
    `description` String,
    `public` Int32,
    `pmid` Nullable(String),
    `citation` Nullable(String),
    `groups` Nullable(String),
    `status` Nullable(Int64),
    `import_date` Nullable(DateTime64(6)),
    `reference_genome_id` Nullable(Int64)
)
ENGINE = MergeTree
ORDER BY cancer_study_id
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.cancer_study_tags
(
    `cancer_study_id` Int64,
    `tags` String
)
ENGINE = MergeTree
ORDER BY cancer_study_id
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.clinical_attribute_meta
(
    `attr_id` String,
    `display_name` String,
    `description` String,
    `datatype` String,
    `patient_attribute` Int32,
    `priority` String,
    `cancer_study_id` Int64
)
ENGINE = MergeTree
ORDER BY (attr_id, cancer_study_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.reference_genome
(
    `reference_genome_id` Int64,
    `species` String,
    `name` String,
    `build_name` String,
    `genome_size` Nullable(Int64),
    `url` String,
    `release_date` Nullable(DateTime64(6))
)
ENGINE = MergeTree
ORDER BY reference_genome_id
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.patient
(
    `internal_id` Int64,
    `stable_id` String,
    `cancer_study_id` Int64
)
ENGINE = MergeTree
ORDER BY internal_id
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.sample
(
    `internal_id` Int64,
    `stable_id` String,
    `sample_type` String,
    `patient_id` Int64
)
ENGINE = MergeTree
ORDER BY internal_id
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.genetic_profile
(
    `genetic_profile_id` Int64,
    `stable_id` String,
    `cancer_study_id` Int64,
    `genetic_alteration_type` String,
    `generic_assay_type` Nullable(String),
    `datatype` String,
    `name` String,
    `description` Nullable(String),
    `show_profile_in_analysis_tab` Int32,
    `pivot_threshold` Nullable(Float64),
    `sort_order` Nullable(String),
    `patient_level` Nullable(Int32)
)
ENGINE = MergeTree
ORDER BY genetic_profile_id
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.genetic_profile_samples
(
    `genetic_profile_id` Int64,
    `ordered_sample_list` String
)
ENGINE = ReplacingMergeTree
ORDER BY genetic_profile_id
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.sample_list
(
    `list_id` Int64,
    `stable_id` String,
    `category` String,
    `cancer_study_id` Int64,
    `name` String,
    `description` Nullable(String)
)
ENGINE = MergeTree
ORDER BY list_id
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.sample_list_list
(
    `list_id` Int64,
    `sample_id` Int64
)
ENGINE = MergeTree
ORDER BY (list_id, sample_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.gene_panel
(
    `internal_id` Int64,
    `stable_id` String,
    `description` Nullable(String)
)
ENGINE = MergeTree
ORDER BY internal_id
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.gene_panel_list
(
    `internal_id` Int64,
    `gene_id` Int64
)
ENGINE = MergeTree
ORDER BY (internal_id, gene_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.gene
(
    `entrez_gene_id` Int64,
    `hugo_gene_symbol` String,
    `genetic_entity_id` Int64,
    `type` Nullable(String)
)
ENGINE = MergeTree
ORDER BY entrez_gene_id
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.clinical_sample
(
    `internal_id` Int64,
    `attr_id` String,
    `attr_value` String
)
ENGINE = ReplacingMergeTree
ORDER BY (internal_id, attr_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.clinical_patient
(
    `internal_id` Int64,
    `attr_id` String,
    `attr_value` String
)
ENGINE = ReplacingMergeTree
ORDER BY (internal_id, attr_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.sample_profile
(
    `sample_id` Int64,
    `genetic_profile_id` Int64,
    `panel_id` Nullable(Int64)
)
ENGINE = ReplacingMergeTree
ORDER BY (sample_id, genetic_profile_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.mutation_derived
(
    `molecularProfileId` String COMMENT 'Stable ID of the genetic profile',
    `sampleId` String COMMENT 'Stable ID of the sample',
    `sampleInternalId` Int64,
    `patientId` String COMMENT 'Stable ID of the patient',
    `entrezGeneId` Int64 COMMENT 'Entrez Gene ID from mutation table (NOT NULL)',
    `studyId` String COMMENT 'Cancer study identifier',
    `center` Nullable(String) COMMENT 'Sequencing center',
    `mutationStatus` Nullable(String) COMMENT 'Mutation status (e.g., Somatic, Germline)',
    `validationStatus` Nullable(String) COMMENT 'Validation status',
    `tumorAltCount` Nullable(Int64) COMMENT 'Tumor alternate allele count',
    `tumorRefCount` Nullable(Int64) COMMENT 'Tumor reference allele count',
    `normalAltCount` Nullable(Int64) COMMENT 'Normal alternate allele count',
    `normalRefCount` Nullable(Int64) COMMENT 'Normal reference allele count',
    `aminoAcidChange` Nullable(String) COMMENT 'Amino acid change',
    `chr` Nullable(String) COMMENT 'Chromosome',
    `startPosition` Nullable(Int64) COMMENT 'Start position',
    `endPosition` Nullable(Int64) COMMENT 'End position',
    `referenceAllele` Nullable(String) COMMENT 'Reference allele',
    `tumorSeqAllele` Nullable(String) COMMENT 'Tumor sequence allele',
    `proteinChange` Nullable(String) COMMENT 'Protein change',
    `mutationType` Nullable(String) COMMENT 'Type of mutation',
    `ncbiBuild` Nullable(String) COMMENT 'NCBI build version',
    `variantType` Nullable(String) COMMENT 'Variant type',
    `refseqMrnaId` Nullable(String) COMMENT 'RefSeq mRNA ID',
    `proteinPosStart` Nullable(Int64) COMMENT 'Protein position start',
    `proteinPosEnd` Nullable(Int64) COMMENT 'Protein position end',
    `keyword` Nullable(String) COMMENT 'Keyword',
    `annotationJSON` Nullable(String) COMMENT 'Annotation JSON',
    `driverFilter` Nullable(String) COMMENT 'Driver filter',
    `driverFilterAnnotation` Nullable(String) COMMENT 'Driver filter annotation',
    `driverTiersFilter` Nullable(String) COMMENT 'Driver tiers filter',
    `driverTiersFilterAnnotation` Nullable(String) COMMENT 'Driver tiers filter annotation',
    `GENE.entrezGeneId` Nullable(Int64) COMMENT 'Gene entrez ID',
    `GENE.hugoGeneSymbol` Nullable(String) COMMENT 'HUGO gene symbol',
    `GENE.type` Nullable(String) COMMENT 'Gene type',
    `alleleSpecificCopyNumber.ascnIntegerCopyNumber` Nullable(Int64) COMMENT 'ASCN integer copy number',
    `alleleSpecificCopyNumber.ascnMethod` Nullable(String) COMMENT 'ASCN method',
    `alleleSpecificCopyNumber.ccfExpectedCopiesUpper` Nullable(Float64) COMMENT 'CCF expected copies upper bound',
    `alleleSpecificCopyNumber.ccfExpectedCopies` Nullable(Float64) COMMENT 'CCF expected copies',
    `alleleSpecificCopyNumber.clonal` Nullable(String) COMMENT 'Clonality annotation',
    `alleleSpecificCopyNumber.minorCopyNumber` Nullable(Int64) COMMENT 'Minor copy number',
    `alleleSpecificCopyNumber.expectedAltCopies` Nullable(Int64) COMMENT 'Expected alternate copies',
    `alleleSpecificCopyNumber.totalCopyNumber` Nullable(Int64) COMMENT 'Total copy number'
)
ENGINE = MergeTree
ORDER BY (molecularProfileId, sampleId, entrezGeneId)
SETTINGS index_granularity = 8192
COMMENT 'Mutation query results with detailed annotations including driver status and allele-specific copy numbers';
CREATE TABLE {db}.genetic_alteration_derived
(
    `sample_unique_id` String,
    `cancer_study_identifier` LowCardinality(String),
    `hugo_gene_symbol` String,
    `profile_type` LowCardinality(String),
    `alteration_value` Nullable(String)
)
ENGINE = MergeTree
ORDER BY (cancer_study_identifier, hugo_gene_symbol, profile_type, sample_unique_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.cna_event
(
    `cna_event_id` Int64,
    `entrez_gene_id` Int64,
    `alteration` Int32
)
ENGINE = MergeTree
ORDER BY cna_event_id
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.sample_cna_event
(
    `cna_event_id` Int64 COMMENT 'References cna_event.cna_event_id.',
    `sample_id` Int64 COMMENT 'References sample.internal_id.',
    `genetic_profile_id` Int64 COMMENT 'References genetic_profile.genetic_profile_id.',
    `annotation_json` Nullable(String) COMMENT 'JSON-formatted annotation details.'
)
ENGINE = MergeTree
PRIMARY KEY (genetic_profile_id, cna_event_id, sample_id)
ORDER BY (genetic_profile_id, cna_event_id, sample_id)
SETTINGS index_granularity = 8192
COMMENT 'Observed CNA events per sample and profile. References cna_event, sample, and genetic_profile.';
CREATE TABLE {db}.alteration_driver_annotation
(
    `alteration_event_id` Int64,
    `genetic_profile_id` Int64,
    `sample_id` Int64,
    `driver_filter` Nullable(String),
    `driver_filter_annotation` Nullable(String),
    `driver_tiers_filter` Nullable(String),
    `driver_tiers_filter_annotation` Nullable(String)
)
ENGINE = MergeTree
ORDER BY (alteration_event_id, genetic_profile_id, sample_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.structural_variant
(
    `internal_id` Int64,
    `genetic_profile_id` Int64,
    `sample_id` Int64,
    `site1_entrez_gene_id` Nullable(Int64),
    `site1_ensembl_transcript_id` Nullable(String),
    `site1_chromosome` Nullable(String),
    `site1_region` Nullable(String),
    `site1_region_number` Nullable(Int64),
    `site1_contig` Nullable(String),
    `site1_position` Nullable(Int64),
    `site1_description` Nullable(String),
    `site2_entrez_gene_id` Nullable(Int64),
    `site2_ensembl_transcript_id` Nullable(String),
    `site2_chromosome` Nullable(String),
    `site2_region` Nullable(String),
    `site2_region_number` Nullable(Int64),
    `site2_contig` Nullable(String),
    `site2_position` Nullable(Int64),
    `site2_description` Nullable(String),
    `site2_effect_on_frame` Nullable(String),
    `ncbi_build` Nullable(String),
    `dna_support` Nullable(String),
    `rna_support` Nullable(String),
    `normal_read_count` Nullable(Int64),
    `tumor_read_count` Nullable(Int64),
    `normal_variant_count` Nullable(Int64),
    `tumor_variant_count` Nullable(Int64),
    `normal_paired_end_read_count` Nullable(Int64),
    `tumor_paired_end_read_count` Nullable(Int64),
    `normal_split_read_count` Nullable(Int64),
    `tumor_split_read_count` Nullable(Int64),
    `annotation` Nullable(String),
    `breakpoint_type` Nullable(String),
    `connection_type` Nullable(String),
    `event_info` Nullable(String),
    `class` Nullable(String),
    `length` Nullable(Int64),
    `comments` Nullable(String),
    `sv_status` String,
    `annotation_json` Nullable(String)
)
ENGINE = MergeTree
ORDER BY (sample_id, genetic_profile_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.copy_number_seg
(
    `seg_id` Int64,
    `cancer_study_id` Int64,
    `sample_id` Int64,
    `chr` String,
    `start` Int64,
    `end` Int64,
    `num_probes` Int64,
    `segment_mean` Float64
)
ENGINE = MergeTree
ORDER BY seg_id
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.sample_derived
(
    `sample_unique_id` String,
    `sample_unique_id_base64` String,
    `sample_stable_id` String,
    `patient_unique_id` String,
    `patient_unique_id_base64` String,
    `patient_stable_id` String,
    `cancer_study_identifier` LowCardinality(String),
    `internal_id` Int32,
    `patient_internal_id` Int32,
    `sample_type` String,
    `sequenced` Int32,
    `copy_number_segment_present` Int32
)
ENGINE = MergeTree
ORDER BY (cancer_study_identifier, sample_unique_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.clinical_data_derived
(
    `internal_id` Int32,
    `sample_unique_id` String,
    `patient_unique_id` String,
    `attribute_name` LowCardinality(String),
    `attribute_value` String,
    `cancer_study_identifier` LowCardinality(String),
    `type` LowCardinality(String)
)
ENGINE = MergeTree
ORDER BY (cancer_study_identifier, type, attribute_name, sample_unique_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.genomic_event_derived
(
    `sample_unique_id` String,
    `hugo_gene_symbol` String,
    `entrez_gene_id` Int32,
    `gene_panel_stable_id` LowCardinality(String),
    `cancer_study_identifier` LowCardinality(String),
    `genetic_profile_stable_id` LowCardinality(String),
    `variant_type` LowCardinality(String),
    `mutation_variant` String,
    `mutation_type` LowCardinality(String),
    `mutation_status` LowCardinality(String),
    `driver_filter` LowCardinality(String),
    `driver_filter_annotation` String,
    `driver_tiers_filter` LowCardinality(String),
    `driver_tiers_filter_annotation` String,
    `cna_alteration` Nullable(Int8),
    `cna_cytoband` String,
    `sv_event_info` String,
    `patient_unique_id` String,
    `off_panel` Bool DEFAULT false
)
ENGINE = MergeTree
ORDER BY (genetic_profile_stable_id, cancer_study_identifier, variant_type, entrez_gene_id, hugo_gene_symbol, sample_unique_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.generic_assay_data_derived
(
    `sample_unique_id` String,
    `patient_unique_id` String,
    `genetic_entity_id` String,
    `value` String,
    `generic_assay_type` String,
    `profile_stable_id` String,
    `entity_stable_id` String,
    `datatype` String,
    `patient_level` Decimal(10, 0),
    `profile_type` String
)
ENGINE = MergeTree
ORDER BY (profile_type, entity_stable_id, patient_unique_id, sample_unique_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.generic_assay_profile_entity_derived
(
    `profile_stable_id` LowCardinality(String),
    `entity_stable_id` String
)
ENGINE = MergeTree
ORDER BY (profile_stable_id, entity_stable_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.generic_assay_meta_derived
(
    `entity_stable_id` String,
    `entity_type` LowCardinality(String),
    `properties` Map(String, String)
)
ENGINE = MergeTree
ORDER BY entity_stable_id
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.sample_to_gene_panel_derived
(
    `sample_unique_id` String,
    `alteration_type` LowCardinality(String),
    `gene_panel_id` LowCardinality(String),
    `cancer_study_identifier` LowCardinality(String),
    `genetic_profile_id` LowCardinality(String)
)
ENGINE = MergeTree
ORDER BY (gene_panel_id, alteration_type, genetic_profile_id, sample_unique_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.gene_panel_to_gene_derived
(
    `gene_panel_id` LowCardinality(String),
    `gene` String
)
ENGINE = MergeTree
ORDER BY gene_panel_id
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.clinical_event
(
    `clinical_event_id` Int64,
    `patient_id` Int64,
    `start_date` Int64,
    `stop_date` Nullable(Int64),
    `event_type` String
)
ENGINE = MergeTree
ORDER BY clinical_event_id
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.clinical_event_data
(
    `clinical_event_id` Int64,
    `key` String,
    `value` String
)
ENGINE = MergeTree
ORDER BY clinical_event_id
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.clinical_event_derived
(
    `clinical_event_id` Int64,
    `patient_id` Nullable(Int64),
    `patient_stable_id` String,
    `start_date` Nullable(Int64),
    `stop_date` Nullable(Int64),
    `event_type` LowCardinality(String),
    `cancer_study_identifier` LowCardinality(String)
)
ENGINE = MergeTree
PRIMARY KEY (cancer_study_identifier, event_type, clinical_event_id)
ORDER BY (cancer_study_identifier, event_type, clinical_event_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.clinical_event_data_derived
(
    `patient_unique_id` String,
    `key` String,
    `value` String,
    `start_date` Int32,
    `stop_date` Int32 DEFAULT 0,
    `event_type` LowCardinality(String),
    `cancer_study_identifier` LowCardinality(String)
)
ENGINE = MergeTree
ORDER BY (cancer_study_identifier, event_type, patient_unique_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.resource_definition
(
    `resource_id` String,
    `display_name` String,
    `description` Nullable(String),
    `resource_type` String,
    `open_by_default` Nullable(Int32),
    `priority` Int64,
    `cancer_study_id` Int64,
    `custom_metadata` Nullable(String)
)
ENGINE = MergeTree
ORDER BY (resource_id, cancer_study_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.resource_study
(
    `internal_id` Int64,
    `resource_id` String,
    `url` String
)
ENGINE = MergeTree
ORDER BY (internal_id, resource_id, url)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.resource_patient
(
    `internal_id` Int64,
    `resource_id` String,
    `url` String
)
ENGINE = MergeTree
ORDER BY (internal_id, resource_id, url)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.resource_sample
(
    `internal_id` Int64,
    `resource_id` String,
    `url` String
)
ENGINE = MergeTree
ORDER BY (internal_id, resource_id, url)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.gistic
(
    `gistic_roi_id` Int64,
    `cancer_study_id` Int64,
    `chromosome` Int64,
    `cytoband` String,
    `wide_peak_start` Int64,
    `wide_peak_end` Int64,
    `q_value` Float64,
    `amp` Int32
)
ENGINE = MergeTree
ORDER BY gistic_roi_id
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.gistic_to_gene
(
    `gistic_roi_id` Int64,
    `entrez_gene_id` Int64
)
ENGINE = MergeTree
ORDER BY (gistic_roi_id, entrez_gene_id)
SETTINGS index_granularity = 8192;
CREATE TABLE {db}.mut_sig
(
    `cancer_study_id` Int64,
    `entrez_gene_id` Int64,
    `rank` Int64,
    `NumBasesCovered` Int64,
    `NumMutations` Int64,
    `p_value` Float64,
    `q_value` Float64
)
ENGINE = MergeTree
ORDER BY (cancer_study_id, entrez_gene_id)
SETTINGS index_granularity = 8192;

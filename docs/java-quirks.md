# Java quirks

One register of the Java backend's behaviour that looks like a bug or an
oddity, and what go does with it. go matches Java exactly, so most of it is
**copied** on purpose; some is left out where copying would hurt (**deviates**);
and Java's nondeterminism go **can't match**. Once go is the reference, this
list is the plan of things to fix in both.

**Summary.** 66 entries: 39 copied, 17 deviate, 9 can't match (Java's
nondeterminism) and one that does not apply. Every copied entry has a
pinning test except the shared `mutation_event` NCBI build (a property of
production's data); the export's failure marker, partly copied, has none
either (it needs a failing export).

- *Java source*: the class that causes it, or the endpoint with the production
  evidence.
- *Pinned*: a contract test in `test/` (`TestContract*`, `TestOracle*` hold
  for the Java backend too), a unit test in `internal/`, or a parity
  annotation: `known.go` is `test/parity/loggedgen/known.go`, `corpus` an
  entry of `test/parity/corpus.txt`, `fixtures` the Java oracle run
  ([Testing](testing.md#java-oracle)).

## Contents

- [Request handling and errors](#request-handling-and-errors)
- [Studies](#studies)
- [Genes](#genes)
- [Samples, patients and clinical data](#samples-patients-and-clinical-data)
- [Molecular data and projections](#molecular-data-and-projections)
- [Study view and group comparison](#study-view-and-group-comparison)
- [Sessions and export](#sessions-and-export)
- [Importer](#importer)
- [Nondeterminism](#nondeterminism)

## Request handling and errors

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| An uncaught exception is Spring Boot's generic 500 body, no message | BasicErrorController | copied | TestFailHidesDatabaseErrors, TestOracleSpringErrorTimestamp |
| A repeated parameter binds its first value (`entrezGeneId=7157&entrezGeneId=5925` reads TP53 only); a non-number first value is reported with all values comma-joined | Spring parameter binding | copied | TestContractRepeatedEntrezGeneID |
| `/api/x/` matches `/api/x` for every method | WebAppConfig.configurePathMatch (`setUseTrailingSlashMatch`) | copied | TestContractTrailingSlashGet |
| A slashed POST to a legacy multi-study endpoint fails: malformed JSON 400, `generic_assay_data` an empty 400, any other body 500 | InvolvedCancerStudyExtractorInterceptor matches exact paths | copied | TestContractTrailingSlashPost |
| An unreadable multi-study filter body is an empty 200 | InvolvedCancerStudyExtractorInterceptor | copied | TestContractFilterValidation |
| A blank String parameter is present: `sampleListId=` (or a number Jackson coerces to a list name) selects no sample | Spring / Jackson | copied | TestContractRequiredParams |
| `;`, `//`, `.`/`..` segments and encoded `.` `%` in a path are a 400 before any controller | StrictHttpFirewall, Tomcat | copied | TestContractFirewall, TestJavaFirewall |
| The request logger records 200 for a request that ends in an uncaught 500 (the status is read before the error page renders) | RequestLoggingFilter | n/a: go's access log has the real status; logged-traffic corpora (status 200) can hold Java 500s | paritydiff `status` outcome |

## Studies

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| `GET /api/studies` numbers pages from 1; `pageNumber=0` is the first page too | ColumnStoreStudyController | copied | TestContractPagingSlicesGetLists |
| `GET /api/studies?projection=ID` is an empty list | ColumnStoreStudyController | copied | TestContractStudyProjections |
| `GET /api/studies?projection=META` answers `[]` without a newline, plus the total-count | ColumnStoreStudyController | copied | TestOracleProjectionBehaviour, TestContractStudiesMeta |
| `readPermission` ignores the study's public flag and PUBLIC group | ColumnStoreStudyController.getAllStudies | copied | TestStudyReadable |
| A study being imported (status ≠ 1) is listed with `readPermission: false` whatever `study_availability.enabled` says (master; production v7.1.2 never) | ColumnStoreStudyController (master) | deviates: only with the setting on | TestContractStudyAvailability |
| `POST /api/studies/fetch` reports `allSampleCount: 1` | www (legacy StudyController) | deviates: the `_all` list's size | known.go, corpus `studies-fetch-*` |
| The study list and `resourceCounts` include resource definitions of other studies sharing a resource id | CancerStudyMapper joins `resource_definition` on `resource_id` only | deviates | known.go, corpus `studies-summary` |

## Genes

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| A keyword page gains alias matches, drops symbols with several Entrez ids and is re-sorted by symbol: `sortBy`/`direction` are ignored (DESC comes back ascending) | GeneServiceImpl.getAllGenes | copied | TestOracleGeneKeywordSearch |
| Every other page is re-sorted by symbol (and drops symbols with several Entrez ids) too, so `sortBy` only picks the page's genes | GeneServiceImpl.filterGenesWithMultipleEntrezIds | deviates: the page in `sortBy` (default Entrez) order | — |
| Keyword searches (genes, patients, legacy studies) and `projection=ID` ignore `sortBy` | GeneMapper, PatientMapper, StudyMapper, the legacy ID selects | copied | TestSortIgnored |
| `sortBy=cytoband` or `length` is a 500 SQL exception (GeneSortBy lists them; the gene table has no such column) | GeneMapper; www and fixtures | deviates: 200 in the default order | — |

## Samples, patients and clinical data

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| A patient keyword search lists a patient once per matching sample | PatientMapper joins samples | copied | TestContractPatientsByKeyword |
| The patient keyword `LIKE` is case-sensitive and `_` is a wildcard | PatientMapper on ClickHouse | copied | TestContractPatientKeywordLike |
| Clinical attribute counts count rows: a patient attribute once per selected sample | ClinicalAttributeMapper.getClinicalAttributeCountsBySampleIds | copied | TestContractClinicalAttributeCountsPerSample |
| The clinical table's search is case-sensitive | ClinicalDataMapper.searchedAttributeData | copied | TestContractClinicalDataTablePages |
| The clinical table answers 500 for a `sortBy` that is not an attribute | StudyViewController clinical table; fixtures | deviates | — |
| `clinical-events-meta` and `survival-data` answer 500: a MyBatis expression calls the missing `SqlUtils.combineStudyAndPatientIds` | ClinicalEventMapper; www and fixtures | deviates: answers the data | corpus `clinical-events-meta`, `survival-data*`; TestContractClinicalEventsMetaRequests, TestContractSurvivalFromEvents |
| `treatments/patient` and `/sample` with a bare `studyIds` body list the whole portal's treatments for a study without treatment data (1,445 rows for acbc_mskcc_2015) | TreatmentServiceImpl filters every sample out; TreatmentMapper builds no WHERE for empty lists | deviates: `[]` | known.go |

## Molecular data and projections

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| Generic assay data answers 500 for `projection=ID` | GenericAssayDataController; fixtures | deviates: the data | — |
| META is ignored (a body, no total-count) by `sample-lists/fetch`, `gene-panels/fetch`, `resource-definitions/fetch`, the generic assay meta endpoints and `mutation-data-counts/fetch` | their controllers; fixtures | deviates: an empty body with total-count (`treatments/sample-counts/fetch` answers its body, as Java) | — |
| META on the resource-data and resource-definitions GETs is 501 | controllers throw UnsupportedOperationException | copied | TestOracleResourceMetaNotImplemented |
| `mutation-data-counts`: only SUMMARY counts mutated / not mutated; ID counts by mutation type as DETAILED does | ColumnarStoreStudyViewController | copied | TestOracleProjectionBehaviour |
| A sample's molecular data come in genetic entity id order, not Entrez order | MolecularDataServiceImpl (`genetic_alteration` key order) | copied | TestContractMolecularDataGeneticEntityOrder |
| Variant counts: a missing keyword is stored as the text `"null"`, so `keyword IS NULL` never matches; a gene and keyword without mutations has no row | VariantCountMapper, `mutation_count_by_keyword` | copied | TestContractVariantCountsRows, TestOracleVariantCountsOnlyExistingKeywords |
| Generic assay entities are ordered by the number an id starts with, then by bytes (17-AAG last) | GenericAssayMapper | copied | TestContractGenericAssayMeta |
| The gene set hierarchy of a non-GSVA profile is a 500 | GenesetHierarchyServiceImpl (IllegalArgumentException) | copied | TestContractGenesetHierarchyBody |
| A mutation reports the NCBI build of the study that first imported its event | the importer shares `mutation_event` across studies | copied on a production mirror; go's own import keeps each study's | — |
| Undefined correlations are written `"NaN"`; an empty violin plot's axis runs from `Infinity` to `-Infinity` | Jackson, ViolinPlotServiceImpl | copied | TestOracleNonFiniteDoubles |
| Orders of Java `HashMap`s: enrichment groups, generic assay meta properties, treatment rows, mutation spectrum samples, CNA namespaces | the services' HashMaps (go's `javaHashMapOrder`) | copied | TestOracleGenericAssayMetaProperties, TestContractTreatmentsSample, TestContractMutationSpectrums, TestOracleProjectionBehaviour |

## Study view and group comparison

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| Bodies without `studyViewFilter` (or their bin filters, gene or attribute list, or no body) are a 500 NullPointerException; clinical counts and bins of a filter naming no studies a "SQL exception." | ColumnarStoreStudyViewController and its binners | copied | TestContractStudyViewBodyWithoutFilter |
| `negateFilters` is accepted and ignored | ColumnarStoreStudyViewController.fetchFilteredSamples | copied | TestContractFilteredSamplesNegateFiltersIgnored |
| `genomic-data-counts` reports a single NA count, attached to one gene: the filtered samples minus that gene's counts | ClickhouseGenomicDataMapper.getCNACounts | copied (the first gene; Java's choice is arbitrary, see below) | TestContractGenomicDataCountsNA |
| `generic-assay-data-counts` with entity filters appends an NA row even when it is 0 | getGenericAssayDataCounts | copied | TestContractGenericAssayDataCounts |
| `mutation-data-counts` with sample ids: "Not Mutated" and "Not Profiled" are items of their own, present even when empty | StudyViewColumnarServiceUtil | copied | TestContractMutationDataCountsWithSampleIDs |
| Clinical enrichments take a value as numeric per `NumberUtils.isCreatable`: an integer with a leading zero only as an octal literal (`07` yes, `08` no) | ClinicalDataEnrichmentUtil | copied | TestDecimalCreatable |
| `genomic-data-bin-counts` with two profiles bins every gene in the first filter's profile type and adds one NA bin | GetGenomicDataBinCountsUseCase; www | deviates | corpus `genomic-data-bin-counts-two-profiles` |
| SV pair counts append gene2's panels to gene1's shared panel list, so later pairs report extra panels | ProfiledCasesCounter | deviates | corpus `sv-counts-msk` |
| A numeric filter on a patient-level generic assay profile is ignored | column-store study-view filter; fixtures | deviates: filters per patient | — |
| Categorical enrichments of a patient-level profile are a 500 | GenericAssayEnrichmentController; fixtures | deviates: the enrichment | TestContractGenericAssayCategoricalEnrichmentPatientLevelHLA |
| Namespace attributes come from one arbitrary record per table and study (`LIMIT 1`, no ORDER BY) | NamespaceMapper | deviates: reads every record | TestContractNamespaceAttributes |

## Sessions and export

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| Adding or removing a user on a missing session is a 500 (the null session is dereferenced) | SessionServiceController | copied | TestVirtualStudyAuthorized |
| A failed export corrupts the zip on purpose by appending `ERROR: <exception message>` | ZipOutputStreamWriterService.fail | copies the corruption; writes `ERROR: study export failed`, the message goes to the server log | — |
| `meta_study.txt` groups are upper-cased in a HashSet's order | cbioportal-core | copied | TestJavaGroups |

## Importer

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| `clinical_data_derived` cross-joins every attribute with every sample or patient and keeps the empty values | the derived-table script (`clickhouse.sql`) | deviates: stored values only (the mirror's view drops `''`); the API answers the same | — |
| The meta file pattern matches `_meta_` anywhere in a name (ascn's `*_meta_cna_hg19_seg.txt` is imported) | cbioportal-core | copied | TestOracleMetaFileNamePattern |
| `MUTATION_COUNT` skips samples profiled only through the `_sequenced` case list | cbioportal-core (`sample_profile`) | copied | TestOracleMutationCountSkipsCaseListOnlySamples |
| A MAF chromosome X is stored (and served) as 23 | DaoGeneOptimized.normalizeChr | copied | TestOracleMutationChromosomeNormalized |
| SV text fields left empty are `"NA"`, integer fields -1 | StructuralVariantUtil | copied | TestContractStructuralVariantImportDefaults |
| Timeline event data are keyed by the header exactly as written (`Treatment_TYPE`, `ICD DESCRIPTION`); of a repeated header the last non-empty cell wins | ImportTimelineData (`HashMap` per event) | copied (files staged before go recorded headers fall back to the upper-cased column name) | golden `staged/clinical_event_data_derived` |

## Nondeterminism

Java leaves these to ClickHouse or to chance; paritydiff normalizes or
annotates them (`unordered`, `ties=`, `ignore=`, `known=`), and the nightly
job's self-check reports the rest as *Java nondeterministic*.

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| Sample and patient pages without `sortBy` or `keyword` have no ORDER BY (production answers different pages to identical requests) | ClickhouseSampleMapper, PatientMapper | can't match: import order | known.go |
| Clinical table pages without `sortBy` have no ORDER BY | getVisibleSampleInternalIdsForClinicalTable | can't match | known.go |
| Ties at a page boundary: clinical table sort values, a study's clinical data by internal id, a profile's mutations by profile, sample and gene, a sample id in several studies, resource data by resource id | ClinicalDataMapper, MutationMapper, ClickhouseSampleMapper, ResourceDataMapper | can't match | known.go; corpus `clinical-data-*`, `resource-data-study-all` |
| Profile-type and case-list-type labels are the name the unordered `GROUP BY` yields first | mergeCaseListCounts, profile sample counts | can't match | known.go (`ignore=label`) |
| Row order of a `GROUP BY` or hash join (study-view counts, gene panel data) and ties of an unstable sort | ClickHouse | can't match | corpus `unordered` / `ties=`; fixtures (~180) |
| The single NA count or bin of a multi-gene or multi-entity request goes to an arbitrary gene or entity | ClickhouseGenomicDataMapper, generic assay counts and bins | can't match | corpus `genomic-data-counts-two-genes`, `generic-assay-*-two` |
| The clinical table's `byUniqueSampleKey` map is a HashMap filled in ClickHouse's order | StudyViewController | can't match | TestOracleFieldOrder (left out) |
| Of several simultaneous Bean Validation violations, one at random is reported | Hibernate Validator | can't match: a fixed one | TestContractFilterValidation (single violations) |
| A study's `groups` come in a HashSet's order (the oracle's import) | cbioportal-core | can't match on go's own import; the mirror copies production's | fixtures |

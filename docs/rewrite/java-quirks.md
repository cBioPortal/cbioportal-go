# Java quirks

The Java backend of cBioPortal does some things that look like bugs or odd
choices. This page lists each of them and says what go (this project) does
about it. Read it when go gives an answer that surprises you, or before you
"fix" something that only looks broken.

go answers every request the way Java does, so for most quirks go does
the same odd thing on purpose. Clients, including the cBioPortal
frontend, may depend on them. But where Java is plainly wrong, go fixes it:

- Java answers a valid request with an error (often a 500). go answers with
  the data.
- Java answers with wrong data, such as another study's rows or a wrong
  count. go answers with the right data.
- Java accepts a paging or sorting parameter (`pageSize`, `pageNumber`,
  `sortBy`, `direction`) and then ignores it. go follows it.

Every fix is listed here, most with a test that keeps it fixed.

**Summary.** The page has 70 entries:

| What go does | Entries | Meaning |
|---|---|---|
| Copied | 40 | go does the same thing as Java, on purpose. |
| Fixed in go | 20 | Java gives an error or wrong data, or ignores a paging or sorting parameter, and go gives the right answer. |
| Can't match | 9 | Java gives a random or changing answer, so go cannot give the same one. |
| Does not apply | 1 | The quirk has no counterpart in go. |

A test checks 38 of the 40 copied entries. The two without a test are the
mutation's genome build, which comes from production's data, and the
marker a failed study export writes, which would need an export that fails.

**How to read the tables.** Each table has four columns:

- **Behaviour** says what Java does.
- **Java source** names the Java class that causes it. Sometimes it names
  the place the behaviour was seen instead: "www" means production
  (www.cbioportal.org), and "fixtures" means the Java backend run on the
  test studies.
- **go** says whether go copies it, fixes it, or can't match it.
- **Pinned** names the test that checks go's behaviour, so it cannot
  change by accident. A dash means there is no test. The test can be one
  of these:
  - A contract test in `test/`, such as `TestContractTrailingSlashGet`. Tests
    named `TestContract…` or `TestOracle…` also pass against the Java
    backend.
  - A unit test in `internal/`.
  - A line in a parity list. Parity checks send the same requests to go
    and to Java and compare the answers. "known.go" is
    `test/parity/loggedgen/known.go`, the list of differences we expect
    when replaying requests that production logged. "corpus" names a
    request in `test/parity/corpus.txt`, a hand-written list of requests
    the frontend makes. "fixtures" is the run against the Java backend
    itself (see [Parity with Java](parity.md#java-oracle)).

A few more terms used below:

- **Projection** is a request parameter that picks how much of each record
  to return: `ID` (just the ids), `SUMMARY`, `DETAILED`, or `META` (no
  records, only a `total-count` header).
- **`sortBy`** is a request parameter that names the field to sort a list by.
- A **500** is the HTTP status for "server error". A **400** means "bad
  request", and **200** means "OK".

## Contents

- [What go fixes](#what-go-fixes)
- [Request handling and errors](#request-handling-and-errors)
- [Studies](#studies)
- [Genes](#genes)
- [Samples, patients and clinical data](#samples-patients-and-clinical-data)
- [Molecular data and projections](#molecular-data-and-projections)
- [Study view and group comparison](#study-view-and-group-comparison)
- [Sessions and export](#sessions-and-export)
- [Importer](#importer)
- [Nondeterminism](#nondeterminism)

## What go fixes

This table collects all 20 fixes in one place. It shows what a client gets
from go that it would not get from Java. Most fixes are in the API. The
last three are in the importer: go keeps data that Java throws away, or
stores less where Java stores empty rows.

| Area | Java | go |
|---|---|---|
| Study availability | Java marks a study that is being imported as not readable (`readPermission: false`), whatever the setting says. | go does this only when `study_availability.enabled` is on, as the setting's name says. |
| `studies/fetch` | Java reports `allSampleCount: 1`. | go reports the number of samples in the study's `_all` sample list. |
| Study list, `resourceCounts` | Java also counts resources of other studies that share a resource id. | go counts only this study's resources. |
| Genes pages | Java sorts every page by gene symbol and drops symbols that have more than one Entrez id. | go returns the page in the order `sortBy` asks for. |
| Genes `sortBy=cytoband` or `length` | Java fails with a 500. | go sorts the genes by their cytoband or length on the portal's reference genome. |
| Keyword searches with `sortBy` | Java ignores `sortBy` and `direction` when searching genes, patients or `/api/legacy/studies` by keyword. | go sorts the matches as asked. |
| `projection=ID` with `sortBy` | About 30 list endpoints ignore `sortBy` and `direction` when only ids are asked for. | go sorts the ids as it sorts the full records. |
| Clinical table, unknown `sortBy` | Java fails with a 500. | go answers with the table. |
| `clinical-events-meta`, `survival-data` | Java fails with a 500, because a method it calls does not exist. | go answers with the data. |
| `treatments/*` for a study without treatment data | Java returns the treatments of every study in the portal. | go returns an empty list. |
| Generic assay data, `projection=ID` | Java fails with a 500. | go answers with the data. |
| META on five fetch endpoints | Java ignores META and returns the records without a total count. | go returns an empty body with the total count. |
| `genomic-data-bin-counts` with two profiles | Java puts every gene in the first profile's type and adds an extra NA bin. | go puts each gene in its own profile. |
| Structural variant pair counts | Java adds earlier pairs' gene panels to later pairs. | go reports each pair's own panels. |
| Numeric filter on a patient-level generic assay | Java ignores the filter. | go filters by patient. |
| Categorical enrichment, patient-level | Java fails with a 500. | go answers with the enrichment. |
| Namespace attributes | Java reads them from one record, picked at random. | go reads them from every record. |
| Import: `clinical_data_derived` | Java stores a row for every attribute and case, even empty ones. | go stores only real values. The API answers the same. |
| Import: copy-number segments | Java skips segments whose start is not below their end. | go keeps them. There are 4 on msk_chord_2024 and 10 on msk_impact_50k_2026. |
| Import: timeline rows | Java drops rows for patients not in the study, and rows with more fields than the header. | go keeps them. |

## Request handling and errors

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| When Java hits an error it did not plan for, it returns Spring Boot's generic 500 body with no message. | BasicErrorController | Copied. | TestFailHidesDatabaseErrors, TestOracleSpringErrorTimestamp |
| When a parameter is given twice, Java uses only the first value. For example, `entrezGeneId=7157&entrezGeneId=5925` reads only TP53. If the first value is not a number, the error message lists all values joined by commas. | Spring parameter binding | Copied. | TestContractRepeatedEntrezGeneID |
| A path with a trailing slash, such as `/api/x/`, works the same as `/api/x`, for every method. | WebAppConfig.configurePathMatch (`setUseTrailingSlashMatch`) | Copied. | TestContractTrailingSlashGet |
| A POST with a trailing slash to an older multi-study endpoint fails. Bad JSON gives a 400. `generic_assay_data` gives an empty 400. Any other body gives a 500. | InvolvedCancerStudyExtractorInterceptor matches exact paths | Copied. | TestContractTrailingSlashPost |
| A multi-study filter body that Java cannot read gives an empty 200. | InvolvedCancerStudyExtractorInterceptor | Copied. | TestContractFilterValidation |
| An empty text parameter still counts as given. So `sampleListId=` selects no samples. The same happens when a number is turned into a sample list name. | Spring / Jackson | Copied. | TestContractRequiredParams |
| Paths with `;`, `//`, `.` or `..` segments, or with an encoded `.` or `%`, get a 400 before any endpoint sees them. | StrictHttpFirewall, Tomcat | Copied. | TestContractFirewall, TestJavaFirewall |
| Java's request log records status 200 for a request that ends in a 500. It reads the status before the error page is written. | RequestLoggingFilter | Does not apply. go's access log records the real status. Lists of requests built from production's log may contain Java 500s marked as 200. | paritydiff `status` outcome |

## Studies

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| `GET /api/studies` numbers its pages from 1. `pageNumber=0` also gives the first page. | ColumnStoreStudyController | Copied. | TestContractPagingSlicesGetLists |
| `GET /api/legacy/studies` numbers its pages from 0, like every other list. | legacy StudyController | Copied. | TestLegacyStudiesPaging, TestPageListLegacyStudies |
| `GET /api/studies?projection=ID` returns an empty list. | ColumnStoreStudyController | Copied. | TestContractStudyProjections |
| `GET /api/studies?projection=META` returns `[]` with no newline, plus the total count. | ColumnStoreStudyController | Copied. | TestOracleProjectionBehaviour, TestContractStudiesMeta |
| `readPermission` ignores whether the study is public and ignores the PUBLIC group. | ColumnStoreStudyController.getAllStudies | Copied. | TestStudyReadable |
| A study that is being imported (its status is not 1) is listed with `readPermission: false`, whatever `study_availability.enabled` says. This is Java's main branch. Production (v7.1.2) never does this. | ColumnStoreStudyController (master) | Fixed in go. go does this only when the setting is on, as the setting's name says. | TestContractStudyAvailability |
| `POST /api/studies/fetch` reports `allSampleCount: 1`. | www (legacy StudyController) | Fixed in go. go reports the size of the study's `_all` sample list. | known.go, corpus `studies-fetch-*` |
| The study list and `resourceCounts` include resources of other studies that share a resource id. | CancerStudyMapper joins `resource_definition` on `resource_id` only | Fixed in go. go counts only the study's own resources. | known.go, corpus `studies-summary` |

## Genes

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| A gene search by keyword also matches gene aliases. It drops symbols that have more than one Entrez id, and it sorts the page by symbol. | GeneServiceImpl.getAllGenes | Copied. With `sortBy`, go sorts the page by it instead (see the keyword row below). | TestOracleGeneKeywordSearch |
| Every other gene page is also sorted by symbol and also drops symbols with more than one Entrez id. So `sortBy` only decides which genes land on the page, not their order. | GeneServiceImpl.filterGenesWithMultipleEntrezIds | Fixed in go. go returns the page in `sortBy` order, which is Entrez id by default. | — |
| Keyword searches ignore `sortBy` and `direction`. This happens for genes, patients and `/api/legacy/studies`. | GeneMapper, PatientMapper, StudyMapper | Fixed in go. go sorts the matches by `sortBy`. Without it, they keep the keyword order. | TestGeneKeywordSortBy, TestPatientKeywordSortBy, TestLegacyStudiesPaging |
| `projection=ID` ignores `sortBy` and `direction` on about 30 list endpoints. These are every legacy list endpoint and the three sample endpoints. | the legacy mappers, ClickhouseSampleMapper | Fixed in go. The ids come in the order the full records would. | TestSortedIDProjection, TestSortIgnored |
| `sortBy=cytoband` or `sortBy=length` fails with a 500. Java allows these values, but the gene table has no such columns. | GeneMapper; www and fixtures | Fixed in go. go sorts by the gene's cytoband, or by its length (end minus start), on the portal's reference genome. That is the `ucsc.build` property, which is hg19 unless set. Genes without a location there come last. | TestGeneSortByLocation |

## Samples, patients and clinical data

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| A patient keyword search lists a patient once for each of its matching samples. | PatientMapper joins samples | Copied. | TestContractPatientsByKeyword |
| The patient keyword search is case-sensitive, and `_` matches any single character. | PatientMapper on ClickHouse | Copied. | TestContractPatientKeywordLike |
| Clinical attribute counts count rows. A patient attribute is counted once for each selected sample of that patient. | ClinicalAttributeMapper.getClinicalAttributeCountsBySampleIds | Copied. | TestContractClinicalAttributeCountsPerSample |
| The clinical table's search is case-sensitive. | ClinicalDataMapper.searchedAttributeData | Copied. | TestContractClinicalDataTablePages |
| The clinical table fails with a 500 when `sortBy` is not an attribute. | StudyViewController clinical table; fixtures | Fixed in go. go answers with the table. | — |
| `clinical-events-meta` and `survival-data` fail with a 500. A database query calls the method `SqlUtils.combineStudyAndPatientIds`, which does not exist. | ClinicalEventMapper; www and fixtures | Fixed in go. go answers with the data. | corpus `clinical-events-meta`, `survival-data*`; TestContractClinicalEventsMetaRequests, TestContractSurvivalFromEvents |
| `treatments/patient` and `treatments/sample`, asked only for `studyIds` of a study without treatment data, return the treatments of every study in the portal. For acbc_mskcc_2015 that is 1,445 rows. | TreatmentServiceImpl filters every sample out; TreatmentMapper builds no WHERE for empty lists | Fixed in go. go returns an empty list. | known.go |

## Molecular data and projections

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| Generic assay data fails with a 500 for `projection=ID`. | GenericAssayDataController; fixtures | Fixed in go. go answers with the data. | — |
| Some endpoints ignore META and return the records without a total count. They are `sample-lists/fetch`, `gene-panels/fetch`, `resource-definitions/fetch`, the generic assay meta endpoints and `mutation-data-counts/fetch`. | their controllers; fixtures | Fixed in go. go returns an empty body with the total count. `treatments/sample-counts/fetch` returns its records, as Java does. | — |
| META on the resource-data and resource-definitions GET endpoints gives a 501 ("not implemented"). | controllers throw UnsupportedOperationException | Copied. | TestOracleResourceMetaNotImplemented |
| In `mutation-data-counts`, only SUMMARY counts samples as mutated or not mutated. ID counts by mutation type, the same as DETAILED. | ColumnarStoreStudyViewController | Copied. | TestOracleProjectionBehaviour |
| A sample's molecular data come in the order of Java's internal entity ids, not in Entrez id order. | MolecularDataServiceImpl (`genetic_alteration` key order) | Copied. | TestContractMolecularDataGeneticEntityOrder |
| In variant counts, a missing keyword is stored as the text `"null"`, so a search for an empty keyword never matches. A gene and keyword with no mutations get no row. | VariantCountMapper, `mutation_count_by_keyword` | Copied. | TestContractVariantCountsRows, TestOracleVariantCountsOnlyExistingKeywords |
| Generic assay entities are sorted by the number their id starts with, then by text. For example, 17-AAG comes last. | GenericAssayMapper | Copied. | TestContractGenericAssayMeta |
| Asking for the gene set hierarchy of a profile that is not a GSVA profile gives a 500. | GenesetHierarchyServiceImpl (IllegalArgumentException) | Copied. | TestContractGenesetHierarchyBody |
| A mutation reports the genome build (NCBI build) of whichever study imported that mutation first. Java's importer shares one mutation record across studies. | the importer shares `mutation_event` across studies | Copied on a copy of production's database. go's own importer keeps each study's own build. | — |
| A correlation that cannot be computed is written as `"NaN"`. An empty violin plot's axis runs from `Infinity` to `-Infinity`. | Jackson, ViolinPlotServiceImpl | Copied. | TestOracleNonFiniteDoubles |
| Some lists come in the order of a Java `HashMap`, which looks random but is fixed. These are enrichment groups, generic assay meta properties, treatment rows, mutation spectrum samples and copy-number namespaces. | the services' HashMaps | Copied. go rebuilds the same order (`javaHashMapOrder`). | TestOracleGenericAssayMetaProperties, TestContractTreatmentsSample, TestContractMutationSpectrums, TestOracleProjectionBehaviour |

## Study view and group comparison

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| A request body without `studyViewFilter` gives a 500 (a null pointer error). So does a body missing its bin filters or its gene or attribute list, or no body at all. Clinical counts and bins for a filter that names no studies give the error "SQL exception." | ColumnarStoreStudyViewController and its binners | Copied. | TestContractStudyViewBodyWithoutFilter |
| `negateFilters` is accepted and then ignored. | ColumnarStoreStudyViewController.fetchFilteredSamples | Copied. | TestContractFilteredSamplesNegateFiltersIgnored |
| `genomic-data-counts` reports one NA ("not available") count, attached to one gene. It equals the filtered samples minus that gene's counts. | ClickhouseGenomicDataMapper.getCNACounts | Copied. go attaches it to the first gene. Java picks a gene at random (see [Nondeterminism](#nondeterminism)). | TestContractGenomicDataCountsNA |
| `generic-assay-data-counts` with entity filters adds an NA row, even when its count is 0. | getGenericAssayDataCounts | Copied. | TestContractGenericAssayDataCounts |
| `mutation-data-counts` with sample ids lists "Not Mutated" and "Not Profiled" as their own items, even when they are empty. | StudyViewColumnarServiceUtil | Copied. | TestContractMutationDataCountsWithSampleIDs |
| Clinical enrichments decide whether a value is a number with Java's `NumberUtils.isCreatable`. A whole number with a leading zero counts only if it is a valid octal number: `07` counts, `08` does not. | ClinicalDataEnrichmentUtil | Copied. | TestDecimalCreatable |
| `genomic-data-bin-counts` with two profiles puts every gene in the first filter's profile type and adds one NA bin. | GetGenomicDataBinCountsUseCase; www | Fixed in go. go puts each gene in its own profile. | corpus `genomic-data-bin-counts-two-profiles` |
| Structural variant pair counts add gene2's panels to a panel list shared by all pairs. So later pairs report extra panels. | ProfiledCasesCounter | Fixed in go. go reports each pair's own panels. | corpus `sv-counts-msk` |
| A numeric filter on a patient-level generic assay profile is ignored. | column-store study-view filter; fixtures | Fixed in go. go filters by patient. | — |
| Categorical enrichments of a patient-level profile give a 500. | GenericAssayEnrichmentController; fixtures | Fixed in go. go answers with the enrichment. | TestContractGenericAssayCategoricalEnrichmentPatientLevelHLA |
| Namespace attributes are read from one record per table and study, picked at random. | NamespaceMapper | Fixed in go. go reads every record. | TestContractNamespaceAttributes |

## Sessions and export

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| Adding or removing a user on a session that does not exist gives a 500. | SessionServiceController | Copied. | TestVirtualStudyAuthorized |
| When a study export fails, Java breaks the zip file on purpose by adding `ERROR: <error message>` to the end. | ZipOutputStreamWriterService.fail | Copied in part. go also breaks the zip, but writes `ERROR: study export failed` and puts the message in the server log. | — |
| The groups in `meta_study.txt` are upper-cased and come in a `HashSet`'s order. | cbioportal-core | Copied. | TestJavaGroups |

## Importer

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| `clinical_data_derived` gets a row for every attribute and every sample or patient, and keeps the empty values. | the derived-table script (`clickhouse.sql`) | Fixed in go. go stores only real values. A copy of production's database drops the empty values too. The API answers the same. | — |
| A file counts as a meta file when `_meta_` appears anywhere in its name. So ascn's `*_meta_cna_hg19_seg.txt` is imported. | cbioportal-core | Copied. | TestOracleMetaFileNamePattern |
| `MUTATION_COUNT` skips samples that are profiled only through the `_sequenced` case list. | cbioportal-core (`sample_profile`) | Copied. | TestOracleMutationCountSkipsCaseListOnlySamples |
| Chromosome X in a mutation file is stored and served as 23. | DaoGeneOptimized.normalizeChr | Copied. | TestOracleMutationChromosomeNormalized |
| Empty text fields of a structural variant become `"NA"`, and empty number fields become -1. | StructuralVariantUtil | Copied. | TestContractStructuralVariantImportDefaults |
| Timeline event data are keyed by the column header exactly as written, such as `Treatment_TYPE` or `ICD DESCRIPTION`. When a header appears twice, the last non-empty cell wins. | ImportTimelineData (`HashMap` per event) | Copied. The clinical-events endpoints and the study export use the same keys, so an exported timeline file has the original headers. Studies staged by an older go importer did not record headers, so go uses the upper-cased column name for them. | golden `staged/clinical_event_data_derived`, TestLiveTimelineHeaders |
| A copy-number segment whose start is not below its end (a single-base segment) is skipped with a warning. | ImportCopyNumberSegmentData | Fixed in go. go keeps it. | import benchmark row counts |
| Timeline rows for a patient not in the study, or with more fields than the header, are dropped. | ImportTimelineData | Fixed in go. go keeps them. | — |

## Nondeterminism

"Nondeterministic" means Java can give different answers to the same
request. Usually it lets the database return rows in any order. go cannot
copy an answer that changes, so these entries are "can't match". This is
not a bug in either server. When a request does not ask for a sort order,
any order is a correct answer.

The parity tool (paritydiff) handles them so they do not show up as
failures. It can compare lists without caring about order (`unordered`),
sort rows that tie (`ties=`), skip a field (`ignore=`), or mark a known
difference (`known=`). The nightly parity job sends each request to Java
twice. When Java's two answers differ, the job reports the request as
"Java nondeterministic" instead of as a go failure.

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| Sample and patient pages without `sortBy` or `keyword` have no sort order. Production can return different pages for the same request. | ClickhouseSampleMapper, PatientMapper | Can't match. go uses import order. | known.go |
| Clinical table pages without `sortBy` have no sort order. | getVisibleSampleInternalIdsForClinicalTable | Can't match. | known.go |
| Rows that tie at a page boundary can fall on either page. This happens with clinical table sort values, a study's clinical data by internal id, a profile's mutations by profile, sample and gene, a sample id used in several studies, and resource data by resource id. | ClinicalDataMapper, MutationMapper, ClickhouseSampleMapper, ResourceDataMapper | Can't match. | known.go; corpus `clinical-data-*`, `resource-data-study-all` |
| Profile-type and case-list-type labels use whichever name the database returns first. | mergeCaseListCounts, profile sample counts | Can't match. | known.go (`ignore=label`) |
| Rows from database grouping or joining come in any order (study-view counts, gene panel data). Rows that tie in a sort can swap. | ClickHouse | Can't match. | corpus `unordered` / `ties=`; fixtures (about 180 requests) |
| The single NA count or bin of a request for several genes or entities goes to a gene or entity picked at random. | ClickhouseGenomicDataMapper, generic assay counts and bins | Can't match. | corpus `genomic-data-counts-two-genes`, `generic-assay-*-two` |
| The clinical table's `byUniqueSampleKey` map is filled in the order the database returns rows. | StudyViewController | Can't match. | TestOracleFieldOrder (skips this field) |
| When a request breaks several validation rules at once, Java reports one of them at random. | Hibernate Validator | Can't match. go always reports the same one. | TestContractFilterValidation (one broken rule only) |
| A study's `groups` come in a `HashSet`'s order, which depends on the import. | cbioportal-core | Can't match on go's own import. A copy of production's database keeps production's order. | fixtures |

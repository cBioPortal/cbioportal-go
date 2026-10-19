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

go also adds a few parameters that Java's endpoints do not have. Without
them, go answers as Java does.

Every fix and every added parameter is listed here, most with a test that
keeps it working.

**Summary.** The page has 95 entries:

| What go does | Entries | Meaning |
|---|---|---|
| Copied | 57 | go does the same thing as Java, on purpose. |
| Fixed in go | 32 | Java gives an error or wrong data, or ignores a paging or sorting parameter, and go gives the right answer. |
| Added in go | 1 | go accepts a parameter that Java's endpoint does not have, so Java ignores it. Without the parameter, go answers as Java does. |
| Can't match | 9 | Java gives a random or changing answer, so go cannot give the same one. |
| Does not apply | 1 | The quirk has no counterpart in go. |

A test checks 55 of the 57 copied entries. The two without a test are the
mutation's genome build, which comes from production's data, and the
marker a failed study export writes, which would need an export that fails.

**How to read the tables.** Each table has four columns:

- **Behaviour** says what Java does.
- **Java source** names the Java class that causes it. Sometimes it names
  the place the behaviour was seen instead: "www" means production
  (www.cbioportal.org), and "fixtures" means the Java backend run on the
  test studies.
- **go** says whether go copies it, fixes it, adds a parameter for it, or
  can't match it.
- **Pinned** names the test that checks go's behaviour, so it cannot
  change by accident. A dash means there is no test. The test can be one
  of these:
  - A contract test in `test/`, such as `TestContractTrailingSlashGet`. Tests
    named `TestContract…` or `TestOracle…` also pass against the Java
    backend.
  - A unit test in `internal/`.
  - A line in a parity list. Parity checks send the same requests to go
    and to Java and compare the answers. "known.go" is
    `rewrite/java-v7-to-go/loggedgen/known.go`, the list of differences we expect
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
- [What go adds](#what-go-adds)
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

This table collects all 32 fixes in one place. It shows what a client gets
from go that it would not get from Java. Most fixes are in the API. The
last three are in the importer: go keeps data that Java throws away, or
stores less where Java stores empty rows.

| Area | Java | go |
|---|---|---|
| POST with a trailing slash to an older multi-study endpoint (`clinical-data/fetch/`, `survival-data/fetch/`, …) | Java fails: a 400 for bad JSON, an empty 400 for `generic_assay_data`, a 500 for any other body. | go answers as it does without the slash. |
| A number with a fraction where the body wants a whole number (`"entrezGeneIds": [100.7]`) | Java drops the fraction and reads gene 100. | go refuses the request as malformed, as Java does for any other bad value. A whole number written as a decimal (`100.0`, `1e2`) is read as that number by both. |
| A field the filter does not have in the body of an older fetch endpoint (`molecular-profiles/fetch`, `patients/fetch`, the multi-study filters, …) | Java answers 200 with an empty body. | go skips the field and answers as without it. |
| Study availability | Java marks a study that is being imported as not readable (`readPermission: false`), whatever the setting says. | go does this only when `study_availability.enabled` is on, as the setting's name says. |
| `studies/fetch`, `/api/legacy/studies` | Java reports `allSampleCount: 1`. | go reports the number of samples in the study's `_all` sample list. |
| `studies/fetch`, `/api/legacy/studies`, `treatmentCount` | Java counts only timeline events of type `Treatment`, so a study whose events are typed `TREATMENT` reports 0 treated patients. | go counts both spellings, as `GET /api/studies` does in Java too. |
| Study list, `resourceCounts` | Java also counts resources of other studies that share a resource id. | go counts only this study's resources. |
| Genes pages | Java sorts every page by gene symbol and drops symbols that have more than one Entrez id. | go returns the page in the order `sortBy` asks for. |
| Genes `sortBy=cytoband` or `length` | Java fails with a 500. | go sorts the genes by their cytoband or length on the portal's reference genome. |
| Keyword searches with `sortBy` | Java ignores `sortBy` and `direction` when searching genes, patients or `/api/legacy/studies` by keyword. | go sorts the matches as asked. |
| `/api/legacy/studies` keyword search, later pages | Java fills every page that has too few matches with the studies of the matching primary site, starting from the first one each time, so pages repeat studies. | go makes one list, the matches and then those studies, and cuts the pages from it. |
| `projection=ID` with `sortBy` | About 30 list endpoints ignore `sortBy` and `direction` when only ids are asked for. | go sorts the ids as it sorts the full records. |
| Clinical table, unknown `sortBy` | Java fails with a 500. | go answers with the table. |
| `clinical-events-meta`, `survival-data` | Java fails with a 500 for more than one patient, because a method it calls does not exist. | go answers with the data. |
| `clinical-data/fetch` without `identifiers` | Java fails with a 500 (a null pointer error). | go answers with an empty list. |
| `treatments/*` for a study without treatment data | Java returns the treatments of every study in the portal. | go returns an empty list. |
| `treatments/patient` for studies that share patient ids | Java lists the other study's samples under a treated patient, and counts the patient once. | go keeps each study's patients and samples apart. |
| Alteration enrichments across gene panels | When no sample of a group has a gene altered, Java counts every sample of the group as profiled for it, even samples whose panel lacks the gene. This changes the p-value. | go counts only the samples profiled for the gene. |
| Clinical table without `pageSize` | Java returns no samples, but the full total count. | go returns every sample. |
| A clinical data filter on an attribute none of the studies has, on the `/api/legacy/` study view endpoints | Java drops the filter and answers for every sample. | go applies it, so no sample matches, as Java's newer endpoints do. |
| Generic assay data, `projection=ID` | Java fails with a 500. | go answers with the data. |
| META on six fetch endpoints | Java ignores META and returns the records without a total count. | go returns an empty body with the total count. |
| `mutations/fetch` with `sampleMolecularIdentifiers` | Java returns each sample's mutations from every mutation profile of its study, such as the `_uncalled` profile next to `_mutations`. | go returns the mutations of the profiles named. |
| `genomic-data-bin-counts` with two profiles | Java puts every gene in the first profile's type and adds an extra NA bin. | go puts each gene in its own profile. |
| `genomic-data-bin-counts` and `generic-assay-data-bin-counts` with several genes or entities | Java adds one NA bin for the whole request, to one of them, so the others have none. | go gives each gene or entity its own NA bin. |
| Structural variant pair counts | Java adds earlier pairs' gene panels to later pairs. | go reports each pair's own panels. |
| Numeric filter on a patient-level generic assay | Java ignores the filter. | go filters by patient. |
| Categorical enrichment, patient-level | Java fails with a 500. | go answers with the enrichment. |
| Namespace attributes | Java reads them from one record, picked at random. | go reads them from every record. |
| Import: `clinical_data_derived` | Java stores a row for every attribute and case, even empty ones. | go stores only real values. The API answers the same. |
| Import: copy-number segments | Java skips segments whose start is not below their end. | go keeps them. There are 4 on msk_chord_2024 and 10 on msk_impact_50k_2026. |
| Import: timeline rows | Java drops rows for patients not in the study, and rows with more fields than the header. | go keeps them. |

## What go adds

Java gives 33 endpoints that can return very long lists no way to ask for
one page at a time. Examples are the molecular data, clinical data and
structural variant fetches, and the enrichments. go adds Java's paging
and sorting parameters (`pageSize`, `pageNumber`, `sortBy`, `direction`)
to them. They are optional: without them, the answer is exactly what Java
would send. They are checked as Java checks them on its own list
endpoints, so a bad value gets the same kind of 400. The list of
endpoints, their sort fields and the endpoints left out are in
[API coverage](api-coverage.md#paging-that-go-adds). The tests
`TestAddedPaging` and `TestAddedPagingParams` pin them.

go also adds a gene parameter, `entrezGeneId`, to the copy-number GET
(`GET /api/molecular-profiles/{id}/discrete-copy-number`). Java's GET has
none, ignores one in the address, and answers with every gene's calls.
With it, go answers with that gene's calls. It is listed under
[Molecular data and projections](#molecular-data-and-projections) as
"Added in go", and the served API descriptions list it too.

go also limits what one query of a bulk endpoint may cost ClickHouse:
memory, time, and the memory a sort may use before it moves to disk.
Java has no such limits. A request that hits one before any data is sent
gets a 400 with a `message` that suggests naming genes, fewer samples, or
paging. One that hits it later is broken off, never cut short silently.
Normal requests stay far below the limits. See
[Limits on bulk requests](../configuration.md#limits-on-bulk-requests).
The tests `TestLiveBulkLimits` and `TestLiveBulkLimitTrips` pin them.

go also adds the portal property `robots.disallow_all`, for preview sites
that should stay out of search engines. When it is `true`, `/robots.txt`
asks every crawler to stay away and the sitemaps answer 404, whatever
`sitemaps` says. Java has no such setting. `TestRobotsDisallowAll` pins it.

## Request handling and errors

| Behaviour | Java source | go | Pinned |
|---|---|---|---|
| When Java hits an error it did not plan for, it returns Spring Boot's generic 500 body with no message. | BasicErrorController | Copied. | TestFailHidesDatabaseErrors, TestOracleSpringErrorTimestamp |
| When a parameter is given twice, Java uses only the first value. For example, `entrezGeneId=7157&entrezGeneId=5925` reads only TP53. If the first value is not a number, the error message lists all values joined by commas. A text parameter, such as `attributeId`, `keyword` or `sampleListId`, instead gets all values joined by commas: `attributeId=A&attributeId=B` asks for the attribute `A,B`, which does not exist. | Spring parameter binding (ArrayToStringConverter for text) | Copied. | TestContractRepeatedEntrezGeneID, TestContractRepeatedStringParam |
| A path with a trailing slash, such as `/api/x/`, works the same as `/api/x`, for every method. | WebAppConfig.configurePathMatch (`setUseTrailingSlashMatch`) | Copied. | TestContractTrailingSlashGet |
| A POST with a trailing slash to an older multi-study endpoint, such as `clinical-data/fetch/`, fails. Bad JSON gives a 400. `generic_assay_data` gives an empty 400. Any other body gives a 500. | InvolvedCancerStudyExtractorInterceptor matches exact paths | Fixed in go. go answers as it does without the slash. | TestContractTrailingSlashPost, TestTrailingSlashMatch, known.go |
| An older fetch endpoint answers 200 with an empty body when Java cannot read its request body, for example a list sent as a single value or an unknown enum value. This covers the multi-study filters and `patients/fetch`, `molecular-profiles/fetch`, `copy-number-segments/fetch`, `survival-data/fetch` and `clinical-events-meta/fetch`. | InvolvedCancerStudyExtractorInterceptor reads these bodies before the controller | Copied. | TestContractFilterValidation, TestReadInterceptedFilters |
| The same endpoints also answer 200 with an empty body when the request body has a field the filter does not have. For example, `{"studyIds":["lihc_tcga"],"projection":"DETAILED"}` to `molecular-profiles/fetch` gets an empty answer, because `projection` belongs in the URL. | InvolvedCancerStudyExtractorInterceptor (a plain `ObjectMapper`, which fails on unknown fields) | Fixed in go. go skips the unknown field and answers as without it. | TestContractInterceptedFilterUnknownProperties, TestReadInterceptedFiltersIgnoreUnknown, known.go |
| A number with a fraction where the body wants a whole number, such as `"entrezGeneIds": [100.7]`, is read without its fraction, as gene 100. A whole number written as a decimal (`100.0`, `1e2`) is read as that number. Python scripts often send gene ids this way. | Jackson's default `ACCEPT_FLOAT_AS_INT` | Fixed in go. go reads `100.0` and `1e2` as Java does, and refuses `100.7` as a malformed body. | TestReadBodyFraction, TestReadBodyCoercesScalars |
| A structural variant fetch with an empty `sampleMolecularIdentifiers` list returns nothing, even when `molecularProfileIds` is given. Java uses the sample list whenever it is present. | StructuralVariantController | Copied. | TestContractStructuralVariantQueries |
| An empty text parameter still counts as given. So `sampleListId=` selects no samples. The same happens when a number is turned into a sample list name. | Spring / Jackson | Copied. | TestContractRequiredParams |
| Paths with `;`, `//`, `.` or `..` segments, or with an encoded `.` or `%`, get a 400 before any endpoint sees them. | StrictHttpFirewall, Tomcat | Copied. | TestContractFirewall, TestJavaFirewall |
| Only a fixed list of about 25 paths serves the portal page. Other paths outside `/api/` get a 404, even ones the frontend has a page for, such as `/about` and `/faq`. Those pages still work when you click your way to them. Paths are matched exactly as Spring does: `/patient/x` serves the page, but `/patient/x/` and `/restore/` do not. | WebAppConfig.addViewControllers, IndexPageController | Copied. | TestPortalRoutes, TestContractPortalRoutes |
| `/installations`, `/tutorials`, `/oql` and `/donate` redirect to other websites, although the frontend has its own pages for them. | WebAppConfig.addViewControllers | Copied. | TestPortalRoutes, TestContractPortalRoutes |
| The API descriptions answer 406 ("not acceptable") when the request's `Accept` header rules out their format. For example, `Accept: application/json` on `/api/v3/api-docs.yaml` gets a 406. With no `Accept` header, or with `*/*`, they answer normally. | Spring content negotiation (springdoc's endpoints declare what they produce) | Copied. | TestOpenAPIDocsAccept |
| Every answer carries Spring Security's default headers, including `Cache-Control: no-store`, so browsers cache nothing, not even images or the frontend's code. | Spring Security defaults | Copied, except that go lets browsers cache the frontend's code files, which are several MB. | TestSecurityHeaders, TestContractSecurityHeaders |
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
| `POST /api/studies/fetch` and the `/api/legacy/studies` endpoints report `allSampleCount: 1`. | www (legacy StudyMapper) | Fixed in go. go reports the size of the study's `_all` sample list. | known.go, corpus `studies-fetch-*` |
| `POST /api/studies/fetch` and the `/api/legacy/studies` endpoints count only timeline events of type `Treatment` in `treatmentCount`. A study whose events are typed `TREATMENT`, such as gist_msk_2022, reports 0 treated patients. `GET /api/studies` counts both spellings. | legacy StudyMapper (`event_type = 'Treatment'`) | Fixed in go. Every study endpoint counts both spellings. | TestTreatmentCountBothSpellings, known.go |
| `/api/legacy/studies` and `/api/legacy/studies/{studyId}` write the legacy model, not the column-store one: the nested cancer type is keyed `cancerTypeId` and comes before `allSampleCount`. The list's ID projection lists the studies by id, and META answers only the total count. A keyword is split at spaces, and each part must occur, case-sensitively, in the study's name or id or its cancer type's name or id. Matches come with names that start with the keyword first, then names containing `tcga`, then by name. Studies without sample lists are left out. | legacy StudyController, StudyMapper on ClickHouse | Copied. | TestContractLegacyStudies |
| After the matches, a `/api/legacy/studies` keyword search adds the studies whose cancer type belongs to a matching primary site. A primary site is a top cancer type, right below `tissue`, such as Breast. It matches when its id or name contains the whole keyword, ignoring case. So `keyword=breast` also lists every breast cancer study, although no study name contains "breast" in lower case. The added studies come without resource counts and in the SUMMARY projection, also when the request asks for ID or DETAILED. META counts them. | StudyServiceImpl.findPrimarySiteMatchingStudies, CancerTypeServiceImpl.getPrimarySiteMap | Copied. Java lists the added studies in no set order; go lists them in import order. | TestContractLegacyStudiesPrimarySite, TestPrimarySite |
| With paging, Java adds the primary-site studies of a keyword search to every page that has fewer matches than `pageSize`, and starts again from the first added study each time. Later pages repeat the same studies. | StudyServiceImpl.getAllStudies | Fixed in go. go cuts every page from one list: the matches, then the added studies. | TestContractLegacyStudiesPrimarySite |
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
| `POST /api/clinical-data/fetch` without `identifiers` fails with a 500: the controller reads the identifiers of a filter that has none. | ClinicalDataController.fetchClinicalData | Fixed in go. go answers with an empty list. | TestClinicalDataFetchWithoutIdentifiers, known.go |
| The clinical table fails with a 500 when `sortBy` is not an attribute. | StudyViewController clinical table; fixtures | Fixed in go. go answers with the table. | — |
| `clinical-events-meta` and `survival-data` fail with a 500 when they name more than one patient. A database query calls the method `SqlUtils.combineStudyAndPatientIds`, which does not exist. | ClinicalEventMapper; www and fixtures | Fixed in go. go answers with the data. | corpus `clinical-events-meta`, `survival-data*`; TestContractClinicalEventsMetaRequests, TestContractSurvivalFromEvents |
| `treatments/patient` and `treatments/sample`, asked for studies or samples without treatment data, return the treatments of every study in the portal. For acbc_mskcc_2015 that is 1,445 rows. | TreatmentServiceImpl filters every sample out; TreatmentMapper builds no WHERE for empty lists | Fixed in go. go returns an empty list. | known.go |
| `treatments/patient` groups the samples of the treated patients by patient id alone. When two of the asked studies share a patient id, such as lung_msk_pdx and lung_pdx_msk_2021, a treatment of the patient in one study also lists the patient's samples in the other, and the patient is counted once. | TreatmentServiceImpl.getAllPatientTreatmentRows (`samplesByPatient`) | Fixed in go. go matches patients and samples by study and patient id. | TestLiveTreatmentsPatientSharedPatientID, known.go |
| Alteration enrichments give a group in which no sample has a gene altered the profiled count of the whole group. Samples whose gene panel lacks the gene count as profiled, so the p-value compares against samples that were never tested for it. For example, MSK-IMPACT samples count as profiled for genes off the panel when compared with whole-exome samples. | AlterationEnrichmentUtil.createAlterationEnrichments | Fixed in go. go counts, for every group and gene, the samples profiled for the gene. | TestContractAlterationEnrichmentsProfiledPerGene, known.go |
| The clinical table without `pageSize` returns no samples, though the `total-count` header has the full count. The default page size 0 is meant to mean no paging, but the list is cut at offset plus page size, which is 0. | ClinicalDataServiceImpl, PaginationCalculator.lastIndex | Fixed in go. go returns every sample. | TestContractClinicalTableWithoutPageSize, known.go |
| The `/api/legacy/` study view endpoints drop a clinical data filter on an attribute that none of the asked studies has, and answer for every sample. For example, a filter `CANCER_STUDY = glioma_mskcc_2019` on glioma_mskcc_2019 and lgg_tcga_pan_can_atlas_2018 counts the samples of both studies. The newer endpoints apply the filter, so no sample matches. | legacy StudyViewFilterApplier (filters split by the attributes found) | Fixed in go. go applies the filter on both, so no sample matches. An error would break saved study view links when a study loses an attribute. | TestContractClinicalFilterOnMissingAttribute, known.go |

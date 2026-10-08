# API coverage

This page lists every endpoint of cBioPortal's Java backend and says whether
go (this project) serves it. Use it to check whether a request you need
works on go, and which ones are left out on purpose.

**Summary.** The Java backend has 190 endpoints. go serves 176 of them and
leaves out 14. Tests cover 160. An endpoint here is one method and path,
such as `GET /api/studies`. The list comes from the request mappings of the
Java controllers on the `master` branch of
[cBioPortal/cbioportal](https://github.com/cBioPortal/cbioportal) (the
packages `org.cbioportal.legacy.web` and `org.cbioportal.application`).

## Contents

- [How to read the tables](#how-to-read-the-tables)
- [Studies, cancer types, server info](#studies-cancer-types-server-info)
- [Patients, samples, sample lists](#patients-samples-sample-lists)
- [Clinical data, attributes, events](#clinical-data-attributes-events)
- [Molecular profiles and data](#molecular-profiles-and-data)
- [Generic assays](#generic-assays)
- [Genes, gene sets, reference genome](#genes-gene-sets-reference-genome)
- [Study view](#study-view)
- [Group comparison](#group-comparison)
- [Resources, MutSig / GISTIC](#resources-mutsig--gistic)
- [Sessions, virtual studies](#sessions-virtual-studies)
- [Authentication, data-access tokens](#authentication-data-access-tokens)
- [Portal, proxies, operations](#portal-proxies-operations)
- [Not ported, and why](#not-ported-and-why)

This table counts the endpoints in each area. The last two columns count
the endpoints that take a `projection` parameter and the endpoints that
take paging or sorting parameters.

| Area | Endpoints | Implemented | Missing | Tested | Take `projection` | Take paging / sort |
|---|---|---|---|---|---|---|
| [Studies, cancer types, server info](#studies-cancer-types-server-info) | 9 | 9 | — | 9 | 3 | 3 |
| [Patients, samples, sample lists](#patients-samples-sample-lists) | 14 | 14 | — | 12 | 10 | 7 |
| [Clinical data, attributes, events](#clinical-data-attributes-events) | 13 | 13 | — | 11 | 10 | 7 |
| [Molecular profiles and data](#molecular-profiles-and-data) | 27 | 27 | — | 26 | 16 | 8 |
| [Generic assays](#generic-assays) | 6 | 6 | — | 6 | 6 | 1 |
| [Genes, gene sets, reference genome](#genes-gene-sets-reference-genome) | 15 | 15 | — | 13 | 3 | 2 |
| [Study view](#study-view) | 31 | 31 | — | 31 | 2 | 1 |
| [Group comparison](#group-comparison) | 7 | 7 | — | 7 | — | — |
| [Resources, MutSig / GISTIC](#resources-mutsig--gistic) | 9 | 9 | — | 9 | 8 | 7 |
| [Sessions, virtual studies](#sessions-virtual-studies) | 14 | 14 | — | 14 | — | — |
| [Authentication, data-access tokens](#authentication-data-access-tokens) | 10 | 9 | 1 | 7 | — | — |
| [Portal, proxies, operations](#portal-proxies-operations) | 35 | 22 | 13 | 15 | — | — |
| **Total** | **190** | **176** | **14** | **160** | **58** | **36** |

## How to read the tables

Java also has 33 extra paths that start with `/api/legacy/` or
`/api/not-ready-yet/`. Each one is a second name for an `/api/…` endpoint of
the same name. go answers them with the same code, and they are not counted
above. Two of them page differently, as in Java. `/api/legacy/studies`
numbers its pages from 0, while `/api/studies` numbers them from 1.
`/api/legacy/generic_assay_meta/fetch` takes no paging parameters.

Each table below has these columns:

| Column | Meaning |
|---|---|
| Method | The HTTP method, such as GET or POST. ANY means every method. |
| Path | The URL path. Parts in braces, such as `{studyId}`, are filled in by the caller. |
| Body | The type of the JSON body that a POST sends, as Java names it. |
| Params | The query parameters Java accepts. "projection" picks how much of each record to return (`ID`, `SUMMARY`, `DETAILED` or `META`). "paging" means `pageSize` and `pageNumber`. "sort" means `sortBy` and `direction`. |
| Status | "implemented" means go serves the endpoint with the same fields, order and errors as Java. "missing" means go does not serve it. go answers 404, or for a path outside `/api/`, the frontend page. |
| Tested | "yes" means a contract test in `test/` checks the endpoint. |
| Notes | Anything that differs from a plain copy of Java. |

Where go fixes a Java bug or copies an odd Java behaviour, the details are
in [Java quirks](java-quirks.md).

## Studies, cancer types, server info

| Method | Path | Body | Params | Status | Tested | Notes |
|---|---|---|---|---|---|---|
| GET | `/api/cancer-types` |  | projection, paging, sort | implemented | yes |  |
| GET | `/api/cancer-types/{cancerTypeId}` |  |  | implemented | yes |  |
| GET | `/api/studies` |  | projection, paging, sort, keyword | implemented | yes |  |
| GET | `/api/studies/{studyId}` |  |  | implemented | yes |  |
| GET | `/api/studies/meta` |  | sort, keyword | implemented | yes | Returns only the total count, in a header. |
| GET | `/api/info` |  |  | implemented | yes |  |
| POST | `/api/studies/fetch` | List&lt;String&gt; | projection | implemented | yes |  |
| GET | `/api/studies/{studyId}/tags` |  |  | implemented | yes |  |
| POST | `/api/studies/tags/fetch` | List&lt;String&gt; |  | implemented | yes |  |

## Patients, samples, sample lists

| Method | Path | Body | Params | Status | Tested | Notes |
|---|---|---|---|---|---|---|
| GET | `/api/samples` |  | projection, paging, sort, keyword | implemented | yes |  |
| POST | `/api/samples/fetch` | SampleFilter | projection | implemented | yes |  |
| GET | `/api/studies/{studyId}/samples` |  | projection, paging, sort | implemented | yes |  |
| GET | `/api/studies/{studyId}/samples/{sampleId}` |  |  | implemented | yes |  |
| GET | `/api/studies/{studyId}/patients/{patientId}/samples` |  | projection, paging, sort | implemented |  |  |
| GET | `/api/patients` |  | projection, paging, sort, keyword | implemented | yes |  |
| GET | `/api/studies/{studyId}/patients` |  | projection, paging, sort | implemented | yes |  |
| GET | `/api/studies/{studyId}/patients/{patientId}` |  |  | implemented | yes |  |
| POST | `/api/patients/fetch` | PatientFilter | projection | implemented | yes |  |
| GET | `/api/sample-lists` |  | projection, paging, sort | implemented |  |  |
| GET | `/api/sample-lists/{sampleListId}` |  |  | implemented | yes |  |
| GET | `/api/studies/{studyId}/sample-lists` |  | projection, paging, sort | implemented | yes |  |
| GET | `/api/sample-lists/{sampleListId}/sample-ids` |  |  | implemented | yes |  |
| POST | `/api/sample-lists/fetch` | List&lt;String&gt; | projection | implemented | yes |  |

## Clinical data, attributes, events

| Method | Path | Body | Params | Status | Tested | Notes |
|---|---|---|---|---|---|---|
| GET | `/api/clinical-attributes` |  | projection, paging, sort | implemented | yes |  |
| GET | `/api/studies/{studyId}/clinical-attributes` |  | projection, paging, sort | implemented | yes |  |
| GET | `/api/studies/{studyId}/clinical-attributes/{clinicalAttributeId}` |  |  | implemented | yes |  |
| POST | `/api/clinical-attributes/fetch` | List&lt;String&gt; | projection | implemented | yes |  |
| POST | `/api/clinical-attributes/counts/fetch` | ClinicalAttributeCountFilter |  | implemented | yes |  |
| GET | `/api/studies/{studyId}/samples/{sampleId}/clinical-data` |  | projection, paging, sort, attributeId | implemented | yes |  |
| GET | `/api/studies/{studyId}/patients/{patientId}/clinical-data` |  | projection, paging, sort, attributeId | implemented | yes |  |
| GET | `/api/studies/{studyId}/clinical-data` |  | projection, paging, sort, attributeId, clinicalDataType | implemented | yes |  |
| POST | `/api/studies/{studyId}/clinical-data/fetch` | ClinicalDataSingleStudyFilter | projection, clinicalDataType | implemented |  |  |
| POST | `/api/clinical-data/fetch` | ClinicalDataMultiStudyFilter | projection, clinicalDataType | implemented | yes |  |
| GET | `/api/studies/{studyId}/patients/{patientId}/clinical-events` |  | projection, paging, sort | implemented | yes |  |
| GET | `/api/studies/{studyId}/clinical-events` |  | projection, paging, sort | implemented |  |  |
| POST | `/api/clinical-events-meta/fetch` | ClinicalEventAttributeRequest |  | implemented | yes |  |

## Molecular profiles and data

| Method | Path | Body | Params | Status | Tested | Notes |
|---|---|---|---|---|---|---|
| POST | `/api/custom-driver-annotation-report/fetch` | List&lt;String&gt; |  | implemented | yes |  |
| GET | `/api/studies/{studyId}/samples/{sampleId}/copy-number-segments` |  | projection, paging, sort, chromosome | implemented | yes |  |
| POST | `/api/copy-number-segments/fetch` | List&lt;SampleIdentifier&gt; | projection, chromosome | implemented | yes |  |
| GET | `/api/molecular-profiles/{molecularProfileId}/discrete-copy-number` |  | projection, discreteCopyNumberEventType, sampleListId | implemented | yes |  |
| POST | `/api/molecular-profiles/{molecularProfileId}/discrete-copy-number/fetch` | DiscreteCopyNumberFilter | projection, discreteCopyNumberEventType | implemented | yes |  |
| POST | `/api/molecular-profiles/{molecularProfileId}/discrete-copy-number-counts/fetch` | List&lt;CopyNumberCountIdentifier&gt; |  | implemented | yes |  |
| GET | `/api/gene-panels/` |  | projection, paging, sort | implemented |  |  |
| GET | `/api/gene-panels` |  | projection, paging, sort | implemented | yes |  |
| GET | `/api/gene-panels/{genePanelId}` |  |  | implemented | yes |  |
| POST | `/api/gene-panels/fetch` | List&lt;String&gt; | projection | implemented | yes |  |
| POST | `/api/molecular-profiles/{molecularProfileId}/gene-panel-data/fetch` | GenePanelDataFilter |  | implemented | yes |  |
| POST | `/api/gene-panel-data/fetch` | GenePanelDataMultipleStudyFilter |  | implemented | yes |  |
| GET | `/api/molecular-profiles/{molecularProfileId}/molecular-data` |  | projection, entrezGeneId, sampleListId | implemented | yes |  |
| POST | `/api/molecular-profiles/{molecularProfileId}/molecular-data/fetch` | MolecularDataFilter | projection | implemented | yes |  |
| POST | `/api/molecular-data/fetch` | MolecularDataMultipleStudyFilter | projection | implemented | yes |  |
| GET | `/api/molecular-profiles` |  | projection, paging, sort | implemented | yes |  |
| GET | `/api/molecular-profiles/{molecularProfileId}` |  |  | implemented | yes |  |
| GET | `/api/studies/{studyId}/molecular-profiles` |  | projection, paging, sort | implemented | yes |  |
| POST | `/api/molecular-profiles/fetch` | MolecularProfileFilter | projection | implemented | yes |  |
| POST | `/api/molecular-profiles/{molecularProfileId}/mrna-percentile/fetch` | List&lt;Integer&gt; | sampleId | implemented | yes |  |
| GET | `/api/molecular-profiles/{molecularProfileId}/mutations` |  | projection, paging, sort, entrezGeneId, sampleListId | implemented | yes |  |
| POST | `/api/molecular-profiles/{molecularProfileId}/mutations/fetch` | MutationFilter | projection, paging, sort | implemented | yes |  |
| POST | `/api/mutations/fetch` | MutationMultipleStudyFilter | projection, paging, sort | implemented | yes |  |
| POST | `/api/mutation-counts-by-position/fetch` | List&lt;MutationPositionIdentifier&gt; |  | implemented | yes |  |
| POST | `/api/molecular-profiles/{molecularProfileId}/mutation-spectrums/fetch` | MutationSpectrumFilter |  | implemented | yes |  |
| POST | `/api/structural-variant/fetch` | StructuralVariantFilter |  | implemented | yes |  |
| POST | `/api/molecular-profiles/{molecularProfileId}/variant-counts/fetch` | List&lt;VariantCountIdentifier&gt; |  | implemented | yes |  |

## Generic assays

| Method | Path | Body | Params | Status | Tested | Notes |
|---|---|---|---|---|---|---|
| POST | `/api/generic-assay-meta/fetch` | GenericAssayMetaFilter | projection, paging, searchTerm | implemented | yes | `pageSize` and `pageNumber` must come together. A page has a `total-count` header. go does not use `searchTerm` yet. |
| GET | `/api/generic-assay-meta/{molecularProfileId}` |  | projection | implemented | yes |  |
| GET | `/api/generic-assay-meta/generic-assay/{genericAssayStableId}` |  | projection | implemented | yes |  |
| GET | `/api/generic-assay-data/{molecularProfileId}/generic-assay/{genericAssayStableId}` |  | projection | implemented | yes |  |
| POST | `/api/generic_assay_data/{molecularProfileId}/fetch` | GenericAssayFilter | projection | implemented | yes |  |
| POST | `/api/generic_assay_data/fetch` | GenericAssayDataMultipleStudyFilter | projection | implemented | yes |  |

## Genes, gene sets, reference genome

| Method | Path | Body | Params | Status | Tested | Notes |
|---|---|---|---|---|---|---|
| GET | `/api/genes` |  | projection, paging, sort, alias, keyword | implemented | yes | `sortBy=cytoband` and `length` use the portal's reference genome. |
| GET | `/api/genes/{geneId}` |  |  | implemented | yes |  |
| GET | `/api/genes/{geneId}/aliases` |  |  | implemented |  |  |
| POST | `/api/genes/fetch` | List&lt;String&gt; | projection, geneIdType | implemented | yes |  |
| GET | `/api/genesets` |  | projection, paging | implemented | yes | Needs gene sets loaded with `load-genesets`. Without them, go builds the list from the GSVA profiles. |
| GET | `/api/genesets/{genesetId}` |  |  | implemented | yes |  |
| POST | `/api/genesets/fetch` | List&lt;String&gt; |  | implemented | yes |  |
| GET | `/api/genesets/version` |  |  | implemented | yes |  |
| POST | `/api/genesets/{genesetId}/expression-correlation/fetch` | List&lt;String&gt; | correlationThreshold, geneticProfileId, sampleListId | implemented | yes |  |
| POST | `/api/genetic-profiles/{geneticProfileId}/geneset-genetic-data/fetch` | GenesetDataFilterCriteria |  | implemented | yes |  |
| POST | `/api/geneset-hierarchy/fetch` | List&lt;String&gt; | geneticProfileId, percentile, pvalueThreshold, sampleListId, scoreThreshold | implemented | yes | Needs gene sets loaded with `load-genesets`. |
| GET | `/api/reference-genome-genes/{genomeName}` |  |  | implemented |  |  |
| GET | `/api/reference-genome-genes/{genomeName}/{geneId}` |  |  | implemented | yes |  |
| POST | `/api/reference-genome-genes/{genomeName}/fetch` | List&lt;String&gt; |  | implemented | yes |  |
| GET | `/api/timestamps` |  |  | implemented | yes |  |

## Study view

| Method | Path | Body | Params | Status | Tested | Notes |
|---|---|---|---|---|---|---|
| POST | `/api/filtered-samples/fetch` | StudyViewFilter | negateFilters | implemented | yes |  |
| POST | `/api/mutated-genes/fetch` | StudyViewFilter |  | implemented | yes |  |
| POST | `/api/molecular-profile-sample-counts/fetch` | StudyViewFilter |  | implemented | yes |  |
| POST | `/api/cna-genes/fetch` | StudyViewFilter |  | implemented | yes |  |
| POST | `/api/structuralvariant-genes/fetch` | StudyViewFilter |  | implemented | yes |  |
| POST | `/api/clinical-data-counts/fetch` | ClinicalDataCountFilter |  | implemented | yes |  |
| POST | `/api/sample-lists-counts/fetch` | StudyViewFilter |  | implemented | yes |  |
| POST | `/api/clinical-data-bin-counts/fetch` | ClinicalDataBinCountFilter | dataBinMethod | implemented | yes |  |
| POST | `/api/clinical-data-density-plot/fetch` | StudyViewFilter | xAxisAttributeId, xAxisBinCount, xAxisEnd, xAxisLogScale, xAxisStart, yAxisAttributeId, yAxisBinCount, yAxisEnd, yAxisLogScale, yAxisStart | implemented | yes |  |
| POST | `/api/clinical-data-violin-plots/fetch` | StudyViewFilter | axisEnd, axisStart, categoricalAttributeId, logScale, numCurvePoints, numericalAttributeId, sigmaMultiplier | implemented | yes |  |
| POST | `/api/genomic-data-counts/fetch` | GenomicDataCountFilter |  | implemented | yes |  |
| POST | `/api/generic-assay-data-counts/fetch` | GenericAssayDataCountFilter |  | implemented | yes |  |
| POST | `/api/mutation-data-counts/fetch` | GenomicDataCountFilter | projection, includeSampleIds | implemented | yes |  |
| POST | `/api/clinical-event-type-counts/fetch` | StudyViewFilter |  | implemented | yes |  |
| POST | `/api/treatments/patient-counts/fetch` | StudyViewFilter | tier | implemented | yes |  |
| POST | `/api/treatments/sample-counts/fetch` | StudyViewFilter | projection, tier | implemented | yes |  |
| POST | `/api/genomic-data-bin-counts/fetch` | GenomicDataBinCountFilter | dataBinMethod | implemented | yes |  |
| POST | `/api/generic-assay-data-bin-counts/fetch` | GenericAssayDataBinCountFilter | dataBinMethod | implemented | yes |  |
| POST | `/api/custom-data-counts/fetch` | ClinicalDataCountFilter |  | implemented | yes |  |
| POST | `/api/namespace-attributes/fetch` | List&lt;String&gt; |  | implemented | yes | go reads the keys from every record. Java reads them from one record picked at random. |
| POST | `/api/namespace-attributes/counts/fetch` | NamespaceAttributeCountFilter |  | implemented | yes |  |
| POST | `/api/namespace-data/fetch` | NamespaceComparisonFilter |  | implemented | yes |  |
| POST | `/api/custom-data-bin-counts/fetch` | ClinicalDataBinCountFilter | dataBinMethod | implemented | yes |  |
| POST | `/api/structuralvariant-counts/fetch` | StudyViewFilter |  | implemented | yes |  |
| POST | `/api/clinical-data-table/fetch` | StudyViewFilter | paging, sort, searchTerm | implemented | yes |  |
| POST | `/api/namespace-data-counts/fetch` | NamespaceDataCountFilter |  | implemented | yes | Applies `namespaceDataFilters` the same way Java's older filter code does. |
| POST | `/api/survival-data/fetch` | SurvivalRequest |  | implemented | yes |  |
| POST | `/api/treatments/patient` | StudyViewFilter | tier | implemented | yes |  |
| POST | `/api/treatments/sample` | StudyViewFilter | tier | implemented | yes |  |
| POST | `/api/treatments/display-patient` | Set&lt;String&gt; | tier | implemented | yes |  |
| POST | `/api/treatments/display-sample` | Set&lt;String&gt; | tier | implemented | yes |  |

## Group comparison

| Method | Path | Body | Params | Status | Tested | Notes |
|---|---|---|---|---|---|---|
| POST | `/api/clinical-data-enrichments/fetch` | GroupFilter |  | implemented | yes |  |
| POST | `/api/molecular-profiles/co-expressions/fetch` | CoExpressionFilter | molecularProfileIdA, molecularProfileIdB, threshold | implemented | yes | Works like Java's newer ClickHouse code, so a request for a gene set instead of a gene returns an empty list. |
| POST | `/api/alteration-enrichments/fetch` | MolecularProfileCasesGroupAndAlterationTypeFilter | enrichmentType | implemented | yes |  |
| POST | `/api/expression-enrichments/fetch` | List&lt;MolecularProfileCasesGroupFilter&gt; | enrichmentType | implemented | yes |  |
| POST | `/api/generic-assay-enrichments/fetch` | List&lt;MolecularProfileCasesGroupFilter&gt; | enrichmentType | implemented | yes |  |
| POST | `/api/generic-assay-categorical-enrichments/fetch` | List&lt;MolecularProfileCasesGroupFilter&gt; | enrichmentType | implemented | yes |  |
| POST | `/api/generic-assay-binary-enrichments/fetch` | List&lt;MolecularProfileCasesGroupFilter&gt; | enrichmentType | implemented | yes |  |

## Resources, MutSig / GISTIC

| Method | Path | Body | Params | Status | Tested | Notes |
|---|---|---|---|---|---|---|
| GET | `/api/studies/{studyId}/samples/{sampleId}/resource-data` |  | projection, paging, sort, resourceId | implemented | yes |  |
| GET | `/api/studies/{studyId}/patients/{patientId}/resource-data` |  | projection, paging, sort, resourceId | implemented | yes |  |
| GET | `/api/studies/{studyId}/resource-data` |  | projection, paging, sort, resourceId | implemented | yes |  |
| GET | `/api/studies/{studyId}/resource-data-all` |  | projection, paging, sort, resourceId | implemented | yes |  |
| GET | `/api/studies/{studyId}/resource-definitions` |  | projection, paging, sort | implemented | yes |  |
| GET | `/api/studies/{studyId}/resource-definitions/{resourceId}` |  |  | implemented | yes |  |
| POST | `/api/resource-definitions/fetch` | List&lt;String&gt; | projection | implemented | yes |  |
| GET | `/api/studies/{studyId}/significant-copy-number-regions` |  | projection, paging, sort | implemented | yes |  |
| GET | `/api/studies/{studyId}/significantly-mutated-genes` |  | projection, paging, sort | implemented | yes |  |

## Sessions, virtual studies

| Method | Path | Body | Params | Status | Tested | Notes |
|---|---|---|---|---|---|---|
| GET | `/api/public_virtual_studies` |  |  | implemented | yes |  |
| POST | `/api/public_virtual_studies/{id}` | VirtualStudyData | pmid, typeOfCancerId | implemented | yes |  |
| DELETE | `/api/public_virtual_studies/{id}` |  | softDelete | implemented | yes |  |
| GET | `/api/session/{type}/{id}` |  |  | implemented | yes |  |
| GET | `/api/session/virtual_study` |  |  | implemented | yes | A user who is not logged in gets an empty list. Java answers 500. |
| POST | `/api/session/{type}` | JSONObject |  | implemented | yes |  |
| POST | `/api/session/virtual_study/save` | JSONObject |  | implemented | yes |  |
| GET | `/api/session/{type:virtual_study\|group\|custom_data\|custom_gene_list}/{operation}/{id}` |  |  | implemented | yes | Works with the built-in store and with the session service. |
| POST | `/api/session/groups/fetch` | List&lt;String&gt; |  | implemented | yes |  |
| POST | `/api/session/settings` | PageSettingsData |  | implemented | yes |  |
| POST | `/api/session/settings/fetch` | PageSettingsIdentifier |  | implemented | yes |  |
| POST | `/api/session/custom_data/fetch` | List&lt;String&gt; |  | implemented | yes |  |
| POST | `/api/session/custom_gene_list/save` | JSONObject |  | implemented | yes |  |
| GET | `/api/session/custom_gene_list` |  |  | implemented | yes |  |

## Authentication, data-access tokens

| Method | Path | Body | Params | Status | Tested | Notes |
|---|---|---|---|---|---|---|
| GET | `/api/data-access-token` |  |  | implemented | yes | With `dat.method=oauth2`, it sends the user to the login provider to get a token (see [Data access tokens](../configuration.md#data-access-tokens)). Otherwise it returns a placeholder. |
| GET | `/api/data-access-token/oauth2` |  | code | implemented | yes | With `dat.method=oauth2`, it downloads the token as a file. |
| POST | `/api/data-access-tokens` |  |  | implemented | yes | With `dat.method=oauth2`, it answers 501, as Java does. Otherwise it returns `{}`. |
| GET | `/api/data-access-tokens` |  |  | implemented | yes | With `dat.method=oauth2`, it answers 501, as Java does. Otherwise it returns `[]`. |
| GET | `/api/data-access-tokens/{token}` |  |  | implemented | yes | With `dat.method=oauth2`, it answers 501, as Java does. Otherwise it returns `{}`. |
| DELETE | `/api/data-access-tokens` |  |  | implemented | yes | With `dat.method=oauth2`, it answers 501, as Java does. Otherwise it does nothing. |
| DELETE | `/api/data-access-tokens/{token}` |  |  | implemented | yes | With `dat.method=oauth2`, it answers 501, as Java does. Otherwise it does nothing. |
| POST | `/login` |  |  | implemented |  | With `authenticate=oauth2`, it sends the user to the login provider (see [Login](../configuration.md#login-and-study-authorization)). |
| GET | `/login` |  |  | implemented |  | With `authenticate=oauth2`, it sends the user to the login provider. With `?logout_failure`, it explains why a login was refused. |
| GET | `/restful_login` |  |  | missing |  | The SAML and password login page. go does not support SAML. |

## Portal, proxies, operations

| Method | Path | Body | Params | Status | Tested | Notes |
|---|---|---|---|---|---|---|
| DELETE | `/api/cache` |  | boolean | implemented | yes | Clears go's metadata cache. Needs `cache.endpoint.enabled` and the `cache.endpoint.api-key` key. |
| DELETE | `/api/cache/{studyId}` |  | boolean | implemented | yes | Clears go's metadata cache. Needs `cache.endpoint.enabled` and the `cache.endpoint.api-key` key. |
| GET | `/api/{cache}/keysInCache` |  |  | implemented | yes | Answers 404, as Java does when no cache is set up. |
| GET | `/api/{cache}/keyCountsPerClass` |  |  | implemented |  | Answers 404, as Java does when no cache is set up. |
| GET | `/api/cacheStatistics` |  |  | implemented | yes | Answers 404, as Java does when no cache is set up. |
| GET | `/export/study/{studyId}.zip` |  |  | implemented | yes | Needs `feature.study.export`. Protein-level and generic assay profiles and virtual studies are not exported. |
| GET | `/api/getexternalpage.json` |  | sourceURL | missing |  | Lets anyone make the server fetch any URL, which is a security hole. Left out on purpose. |
| ANY | `/` |  |  | implemented | yes | Serves the frontend (see `FRONTEND_DIR` and `FRONTEND_URL` in [Configuration](../configuration.md#cbioportal-api)). |
| ANY | `/index` |  |  | implemented |  | Serves the frontend. |
| ANY | `/index.html` |  |  | implemented |  | Serves the frontend. |
| ANY | `/study/summary` |  |  | implemented |  | Serves the frontend, also for POST. |
| ANY | `/results` |  |  | implemented |  | Serves the frontend, also for POST. |
| GET | `/config_service` |  |  | implemented | yes | Built from the portal properties. |
| GET | `/api/v2/api-docs` |  |  | implemented | yes | The Java portal's Swagger 2.0 API description, with the host set to the one the request used. |
| GET | `/api/v3/api-docs[/{public,internal}]`, `/api/v3/api-docs.yaml[/{public,internal}]` |  |  | implemented | yes | The OpenAPI 3.1 API descriptions, copied from api.cbioportal.org with `scripts/refresh-api-docs.sh`. The server URL is set to the one the request used. |
| GET | `/api/v3/api-docs/swagger-config`, `/api/swagger-ui/**` |  |  | implemented | yes | The Swagger UI page for browsing the API, as Java serves it. |
| ANY | `/api/proxy/{path}` | String |  | missing |  | An old proxy for Bitly and 3D Hotspots. The frontend no longer uses it. |
| GET | `/api/proxy/bitly` |  |  | missing |  | An old Bitly proxy. The frontend no longer uses it. |
| ANY | `/api/matchminer/api/**` | JSONObject |  | missing |  | A proxy to MatchMiner (`matchminer.url`). Only MSK's own portal uses it. |
| GET | `/api-legacy/epic/sample/{sampleID}` |  |  | missing |  | Translates sample ids for MSK's internal systems (`msk_entity_translation_enabled`). Only MSK uses it. |
| GET | `/api-legacy/cis/{sampleID}` |  |  | missing |  | Translates sample ids for MSK's internal systems (`msk_entity_translation_enabled`). Only MSK uses it. |
| GET | `/api-legacy/crdb/{sampleID}` |  |  | missing |  | Translates sample ids for MSK's internal systems (`msk_entity_translation_enabled`). Only MSK uses it. |
| GET | `/api-legacy/darwin/{sampleID}` |  |  | missing |  | Translates sample ids for MSK's internal systems (`msk_entity_translation_enabled`). Only MSK uses it. |
| GET | `/api-legacy/cis/{sampleID}/exists` |  |  | missing |  | Translates sample ids for MSK's internal systems (`msk_entity_translation_enabled`). Only MSK uses it. |
| GET | `/api-legacy/crdb/{sampleID}/exists` |  |  | missing |  | Translates sample ids for MSK's internal systems (`msk_entity_translation_enabled`). Only MSK uses it. |
| GET | `/api-legacy/darwin/{sampleID}/exists` |  |  | missing |  | Translates sample ids for MSK's internal systems (`msk_entity_translation_enabled`). Only MSK uses it. |
| GET | `/api-legacy/epic/{sampleID}/exists` |  |  | missing |  | Translates sample ids for MSK's internal systems (`msk_entity_translation_enabled`). Only MSK uses it. |
| ANY | `/proxy/dev/oncokb/**` | String |  | implemented |  | Passes requests on to OncoKB. |
| ANY | `/proxy/A8F74CD7851BDEE8DCD2E86AB4E2A711/**` | String |  | implemented |  | Passes requests on to OncoKB. |
| GET | `/robots.txt` |  |  | implemented | yes | Needs the `sitemaps` property. |
| GET | `/api/health` |  |  | implemented | yes |  |
| GET | `/sitemap_index.xml` |  |  | implemented | yes | Needs the `sitemaps` property. |
| GET | `/sitemap_study.xml` |  | page, studyId | implemented | yes | Needs the `sitemaps` property. |
| GET | `/api/hello` |  |  | implemented | yes |  |
| GET | `/api/url-shortener` |  | url | missing |  | Shortens URLs through Bitly. The frontend calls Bitly itself. |

## Not ported, and why

These parts of the Java backend are left out of go:

| What | Why |
|---|---|
| Data access tokens other than `dat.method=oauth2` (the `uuid` and `jwt` methods) | They need a place to store tokens. Without `dat.method=oauth2`, the token endpoints return placeholders. |
| SAML login pages, MSK sample id translation, MatchMiner | They depend on Spring Security or on services that only some deployments have. |
| `/api/getexternalpage.json` | It fetches any URL for anyone who asks, which is a security hole. It is left out on purpose. |
| The URL shortener and the old `/api/proxy/*` endpoints | They pass requests on to Bitly and 3D Hotspots. The frontend no longer calls them. |

Java has one more controller, `DocRedirectController` (`/api-docs`,
`/swagger-ui.html`). Java does not turn it on, so it is not listed.

When the Java backend gains endpoints, update the tables from its
controllers. Do not list endpoints that go serves and Java does not. Java
would answer 404 for them.

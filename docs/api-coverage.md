# API coverage

Endpoints of the Java backend ([cBioPortal/cbioportal](https://github.com/cBioPortal/cbioportal) `master`, controllers under `org.cbioportal.legacy.web` and `org.cbioportal.application`) and their status here. Generated from the controllers' request mappings; `params` lists the projection, paging and other request parameters Java accepts.

**188 endpoints** (method + path, aliases below not counted): 167 implemented, 6 stubs, 15 missing; 154 covered by tests (`Tested`).

The 33 `/api/legacy/…` and `/api/not-ready-yet/…` aliases (legacy MyBatis and unfinished column-store controllers) are served by the `/api/…` handler of the same name.

Status: **implemented** — served with Java's fields, ordering and errors (contract tests in `test/`); **stub** — answers with a fixed placeholder; **missing** — not served (404 / the frontend).


## Studies, cancer types, server info

| Method | Path | Body | Params | Status | Tested | Notes |
|---|---|---|---|---|---|---|
| GET | `/api/cancer-types` |  | projection, paging, sort | implemented | yes |  |
| GET | `/api/cancer-types/{cancerTypeId}` |  |  | implemented | yes |  |
| GET | `/api/studies` |  | projection, paging, sort, keyword | implemented | yes |  |
| GET | `/api/studies/{studyId}` |  |  | implemented | yes |  |
| GET | `/api/studies/meta` |  | sort, keyword | implemented | yes | headers only |
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
| POST | `/api/generic-assay-meta/fetch` | GenericAssayMetaFilter | projection, paging, searchTerm | implemented | yes |  |
| GET | `/api/generic-assay-meta/{molecularProfileId}` |  | projection | implemented | yes |  |
| GET | `/api/generic-assay-meta/generic-assay/{genericAssayStableId}` |  | projection | implemented | yes |  |
| GET | `/api/generic-assay-data/{molecularProfileId}/generic-assay/{genericAssayStableId}` |  | projection | implemented | yes |  |
| POST | `/api/generic_assay_data/{molecularProfileId}/fetch` | GenericAssayFilter | projection | implemented | yes |  |
| POST | `/api/generic_assay_data/fetch` | GenericAssayDataMultipleStudyFilter | projection | implemented | yes |  |

## Genes, gene sets, reference genome

| Method | Path | Body | Params | Status | Tested | Notes |
|---|---|---|---|---|---|---|
| GET | `/api/genes` |  | projection, paging, sort, alias, keyword | implemented | yes |  |
| GET | `/api/genes/{geneId}` |  |  | implemented | yes |  |
| GET | `/api/genes/{geneId}/aliases` |  |  | implemented |  |  |
| POST | `/api/genes/fetch` | List&lt;String&gt; | projection, geneIdType | implemented | yes |  |
| GET | `/api/genesets` |  | projection, paging | implemented | yes | needs `load-genesets` (else synthesized from GSVA profiles) |
| GET | `/api/genesets/{genesetId}` |  |  | implemented | yes |  |
| POST | `/api/genesets/fetch` | List&lt;String&gt; |  | implemented | yes |  |
| GET | `/api/genesets/version` |  |  | implemented | yes |  |
| POST | `/api/genesets/{genesetId}/expression-correlation/fetch` | List&lt;String&gt; | correlationThreshold, geneticProfileId, sampleListId | implemented | yes |  |
| POST | `/api/genetic-profiles/{geneticProfileId}/geneset-genetic-data/fetch` | GenesetDataFilterCriteria |  | implemented | yes |  |
| POST | `/api/geneset-hierarchy/fetch` | List&lt;String&gt; | geneticProfileId, percentile, pvalueThreshold, sampleListId, scoreThreshold | implemented | yes | needs `load-genesets` |
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
| POST | `/api/namespace-attributes/fetch` | List&lt;String&gt; |  | implemented | yes | keys from every record, not Java's arbitrary LIMIT 1 record |
| POST | `/api/namespace-attributes/counts/fetch` | NamespaceAttributeCountFilter |  | implemented | yes |  |
| POST | `/api/namespace-data/fetch` | NamespaceComparisonFilter |  | implemented | yes |  |
| POST | `/api/custom-data-bin-counts/fetch` | ClinicalDataBinCountFilter | dataBinMethod | implemented | yes |  |
| POST | `/api/structuralvariant-counts/fetch` | StudyViewFilter |  | implemented | yes |  |
| POST | `/api/clinical-data-table/fetch` | StudyViewFilter | paging, sort, searchTerm | implemented | yes |  |
| POST | `/api/namespace-data-counts/fetch` | NamespaceDataCountFilter |  | implemented | yes | applies namespaceDataFilters like the legacy filter applier |
| POST | `/api/survival-data/fetch` | SurvivalRequest |  | implemented | yes |  |
| POST | `/api/treatments/patient` | StudyViewFilter | tier | implemented | yes |  |
| POST | `/api/treatments/sample` | StudyViewFilter | tier | implemented | yes |  |
| POST | `/api/treatments/display-patient` | Set&lt;String&gt; | tier | implemented | yes |  |
| POST | `/api/treatments/display-sample` | Set&lt;String&gt; | tier | implemented | yes |  |

## Group comparison

| Method | Path | Body | Params | Status | Tested | Notes |
|---|---|---|---|---|---|---|
| POST | `/api/clinical-data-enrichments/fetch` | GroupFilter |  | implemented | yes |  |
| POST | `/api/molecular-profiles/co-expressions/fetch` | CoExpressionFilter | molecularProfileIdA, molecularProfileIdB, threshold | implemented | yes | column-store use case: gene sets give [] |
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
| GET | `/api/session/virtual_study` |  |  | implemented | yes | anonymous: [] (Java: 500) |
| POST | `/api/session/{type}` | JSONObject |  | implemented | yes |  |
| POST | `/api/session/virtual_study/save` | JSONObject |  | implemented | yes |  |
| GET | `/api/session/{type:virtual_study\|group\|custom_data\|custom_gene_list}/{operation}/{id}` |  |  | implemented | yes | session store (SESSION_STORE_FILE) |
| POST | `/api/session/groups/fetch` | List&lt;String&gt; |  | implemented | yes |  |
| POST | `/api/session/settings` | PageSettingsData |  | implemented | yes |  |
| POST | `/api/session/settings/fetch` | PageSettingsIdentifier |  | implemented | yes |  |
| POST | `/api/session/custom_data/fetch` | List&lt;String&gt; |  | implemented | yes |  |
| POST | `/api/session/custom_gene_list/save` | JSONObject |  | implemented | yes |  |
| GET | `/api/session/custom_gene_list` |  |  | implemented | yes |  |

## Authentication, data-access tokens

| Method | Path | Body | Params | Status | Tested | Notes |
|---|---|---|---|---|---|---|
| GET | `/api/data-access-token` |  |  | stub |  | token download needs user auth + token store |
| GET | `/api/data-access-token/oauth2` |  |  | missing |  | OAuth2 token exchange (Keycloak offline tokens) |
| POST | `/api/data-access-tokens` |  |  | stub | yes | returns {} |
| GET | `/api/data-access-tokens` |  |  | stub | yes | returns [] |
| GET | `/api/data-access-tokens/{token}` |  |  | stub |  | returns {} |
| DELETE | `/api/data-access-tokens` |  |  | stub | yes | no-op |
| DELETE | `/api/data-access-tokens/{token}` |  |  | stub |  | no-op |
| POST | `/login` |  |  | implemented |  | with `authenticate=oauth2`: redirects to the provider (see docs/configuration.md) |
| GET | `/login` |  |  | implemented |  | with `authenticate=oauth2`: redirects to the provider, or explains a refused login (`?logout_failure`) |
| GET | `/restful_login` |  |  | missing |  | SAML/basic login page (SAML not supported) |

## Portal, proxies, operations

| Method | Path | Body | Params | Status | Tested | Notes |
|---|---|---|---|---|---|---|
| DELETE | `/api/cache` |  | boolean | implemented | yes | cache.endpoint.enabled / api-key; drops the metadata cache |
| DELETE | `/api/cache/{studyId}` |  | boolean | implemented | yes | cache.endpoint.enabled / api-key; drops the metadata cache |
| GET | `/api/{cache}/keysInCache` |  |  | implemented | yes | 404, as Java without a configured cache |
| GET | `/api/{cache}/keyCountsPerClass` |  |  | implemented |  | 404, as Java without a configured cache |
| GET | `/api/cacheStatistics` |  |  | implemented | yes | 404, as Java without a configured cache |
| GET | `/export/study/{studyId}.zip` |  |  | implemented | yes | feature.study.export; protein-level and generic-assay profiles and virtual studies not exported |
| GET | `/api/getexternalpage.json` |  | sourceURL | missing |  | unauthenticated fetch of any URL (SSRF); not ported on purpose |
| ANY | `/` |  |  | implemented | yes | the frontend build (FRONTEND_DIR / FRONTEND_URL) |
| ANY | `/index` |  |  | implemented |  | frontend |
| ANY | `/index.html` |  |  | implemented |  | frontend |
| ANY | `/study/summary` |  |  | implemented |  | frontend (POST too) |
| ANY | `/results` |  |  | implemented |  | frontend (POST too) |
| GET | `/config_service` |  |  | implemented | yes | from portal properties |
| GET | `/api/v2/api-docs` |  |  | implemented | yes | the Java portal's legacy-api.json (Swagger 2.0), host set to the request's |
| ANY | `/api/proxy/{path}` | String |  | missing |  | legacy generic proxy (bitly, 3dHotspots); unused by the frontend |
| GET | `/api/proxy/bitly` |  |  | missing |  | legacy Bitly proxy; unused by the frontend |
| ANY | `/api/matchminer/api/**` | JSONObject |  | missing |  | MatchMiner proxy (matchminer.url); MSK deployment only |
| GET | `/api-legacy/epic/sample/{sampleID}` |  |  | missing |  | MSK entity translation (msk_entity_translation_enabled); MSK-internal systems |
| GET | `/api-legacy/cis/{sampleID}` |  |  | missing |  | MSK entity translation (msk_entity_translation_enabled); MSK-internal systems |
| GET | `/api-legacy/crdb/{sampleID}` |  |  | missing |  | MSK entity translation (msk_entity_translation_enabled); MSK-internal systems |
| GET | `/api-legacy/darwin/{sampleID}` |  |  | missing |  | MSK entity translation (msk_entity_translation_enabled); MSK-internal systems |
| GET | `/api-legacy/cis/{sampleID}/exists` |  |  | missing |  | MSK entity translation (msk_entity_translation_enabled); MSK-internal systems |
| GET | `/api-legacy/crdb/{sampleID}/exists` |  |  | missing |  | MSK entity translation (msk_entity_translation_enabled); MSK-internal systems |
| GET | `/api-legacy/darwin/{sampleID}/exists` |  |  | missing |  | MSK entity translation (msk_entity_translation_enabled); MSK-internal systems |
| GET | `/api-legacy/epic/{sampleID}/exists` |  |  | missing |  | MSK entity translation (msk_entity_translation_enabled); MSK-internal systems |
| ANY | `/proxy/dev/oncokb/**` | String |  | implemented |  | OncoKB reverse proxy |
| ANY | `/proxy/A8F74CD7851BDEE8DCD2E86AB4E2A711/**` | String |  | implemented |  | OncoKB reverse proxy |
| GET | `/robots.txt` |  |  | implemented | yes | behind the sitemaps flag |
| GET | `/api/health` |  |  | implemented | yes |  |
| GET | `/sitemap_index.xml` |  |  | implemented | yes | behind the sitemaps flag |
| GET | `/sitemap_study.xml` |  | page, studyId | implemented | yes | behind the sitemaps flag |
| GET | `/api/hello` |  |  | implemented | yes |  |
| GET | `/api/url-shortener` |  | url | missing |  | Bitly API client; the frontend calls Bitly directly |

## Not ported, and why

- **Data-access tokens** (`/api/data-access-token*`): need user authentication
  and a token store; the endpoints answer placeholders.
- **Login pages, OAuth2 token download, MSK entity translation, MatchMiner**:
  depend on Spring Security / deployment-specific services.
- **`/api/getexternalpage.json`**: fetches any URL for any caller; left out on
  purpose.
- **URL shortener and the legacy `/api/proxy/*`**: Bitly / 3D Hotspots
  proxies the frontend no longer calls.

`DocRedirectController` (`/api-docs`, `/swagger-ui.html`) is not registered
in Java either and is not listed.

Regenerate this file's table from the controllers when the Java backend gains
endpoints; endpoints Go serves that Java does not are removed (they would be a
404 there).

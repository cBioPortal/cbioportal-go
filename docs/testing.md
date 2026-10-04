# Testing


Three tiers, fastest first:

1. **Unit tests** — `go test ./internal/...` (no services needed). Includes the
   `internal/databin` port of Java's DataBinner, checked against ~1,800
   Java-recorded scenarios in `internal/databin/testdata`. With
   `CLICKHOUSE_TEST_URL=http://localhost:18123`, `internal/clickhouse` also
   round-trips query parameters (escaping, URL / form / external-table
   transport) through a live server.
2. **API contract tests** — `make contract` (`go test ./test/`, ~5s) against a
   running API (`API=http://host:port`, default `http://localhost:18090`;
   skipped when unreachable). They pin the exact endpoint behaviour the
   frontend and e2e suite rely on — study-view filters, chart counts, bins,
   sessions, gene tables, enrichments, Java's projections and error contract.
   Add a case whenever a contract is fixed. They expect the API to serve the
   test databases built from cBioPortal's e2e fixture studies and gene sets
   (`setup_test_db.sh` also runs `load-genesets` on `data/genesets`; on an
   older reference database `cbioportal-import load-genesets` adds them):

   ```bash
   git clone https://github.com/cBioPortal/cbioportal-test
   test/setup_test_db.sh cbioportal-test seed-cbioportal_hg19_hg38_v2.14.5.sql.gz
   KEYCLOAK=false test/e2e_api.sh     # the API on :18090, anonymous
   make contract
   ```
3. **Frontend e2e** — `test/run_e2e.sh [WORKERS] [spec...]` drives the
   cBioPortal Playwright suite (`frontend/end-to-end-test-playwright`)
   through the API origin, which serves the portal UI as a cBioPortal
   deployment does. `frontend/` is the cbioportal-frontend repo as a git
   submodule, so the backend and the frontend/e2e suite it is tested against
   are pinned together. Build it once (and after bumping the submodule) as
   upstream CI does:

   ```bash
   git submodule update --init
   (cd frontend && pnpm install --frozen-lockfile && pnpm run buildAll)
   (cd frontend/end-to-end-test-playwright && pnpm install --frozen-lockfile --ignore-workspace)
   test/e2e_api.sh                    # Keycloak + the API on :18090
   test/run_e2e.sh 3 tests/local/annotation-filter-menu.spec.ts
   ```

   The production build loads faster than the dev server, has no dev error
   overlay, and leaves memory for more Playwright workers.
   (`FRONTEND_URL=http://localhost:3000` proxies a dev server instead.)

   Run single specs while iterating; the full suite only at milestones.

   The runner mirrors the reference CI job: `CI=1` (one retry per test;
   tests that pass on retry are reported as flaky), `LOCALDEV=1`, and the
   config's 120s test timeout. 3 workers matches CI; on a 4-core box shared
   with ClickHouse, expect occasional load-induced flakes above that.
   `go run ./cmd/e2ereport --fails` summarizes the last run.

   As in upstream CI, the suite logs in through Keycloak:
   `test/e2e_api.sh` starts it (`test/keycloak/start.sh`: Keycloak 16.1.1
   on :8084, realm `cbio`, user `testuser` / `P@ssword1` with one role per
   fixture study) and runs the API with `authenticate=oauth2`. Docker must
   be available; `KEYCLOAK=false` runs the API anonymously instead.

   Data access tokens (`dat.method=oauth2`) have their own end-to-end test
   against that Keycloak, skipped unless `DAT_KEYCLOAK` is set. It logs in
   as `datuser` (granted one study), downloads a token and calls the API
   with it:

   ```bash
   DAT=true PORT=18096 test/e2e_api.sh
   DAT_KEYCLOAK=1 API=http://localhost:18096 go test -run DATKeycloak ./test/
   ```

CI (`.github/workflows/ci.yml`) runs gofmt, vet and the unit tests, then the
contract tests against a ClickHouse service loaded with the fixture studies,
on every push and pull request. The full frontend e2e suite
(`.github/workflows/e2e.yml`) runs weekly and on demand.

## API parity with www.cbioportal.org

`cmd/paritydiff` sends a corpus of API requests to two origins and compares
the responses: JSON key order is ignored, numbers match to a relative 1e-9,
and per-request options sort arrays whose order Java leaves unspecified
(`unordered`, `unordered=key`, `ties=field`), drop keys (`ignore=`) or mark an
accepted difference with its reason (`known="..."`). Responses of the
reference origin are cached in `.parity/cache` (gitignored) and fetched at
most twice a second with a browser User-Agent, so reruns do not hit www.

`test/parity/corpus.txt` holds ~275 requests the frontend makes — studies,
samples, clinical data, profiles, mutations, CNA, SVs, molecular and generic
assay data, study view, results view, group comparison, patient view — on
acc_tcga_pan_can_atlas_2018, msk_impact_2017, acbc_mskcc_2015 and
lgg_ucsf_2014. Run it against an API serving the go.cbioportal.org mirror
databases:

```bash
go run ./cmd/paritydiff -a http://localhost:18091 -b https://www.cbioportal.org \
  -corpus test/parity/corpus.txt -q -dump .parity/dump
```

`-q` lists only non-identical requests, `-run REGEXP` selects requests,
`-dump` writes both bodies of each difference, `-refresh` refetches www. The
exit status is non-zero when a request differs without a `known` reason.

On 2026-10-03: 257 of 275 requests identical, 18 known differences, none
unexplained (216 identical and 38 differing before the fixes it prompted).
The known differences are listed in
[Deployment](deployment.md#mirroring-production); three of them disappear
once the mirror is rebuilt with the current `transform --from-prod`.

### Logged production requests

`test/parity/loggedgen` samples a corpus from the requests production logged
(`cbioportal_qc.logged_requests`), GET and POST in proportion to each
endpoint's volume, and marks the requests whose difference is expected
(`test/parity/loggedgen/known.go`): www's session store (sessions and
custom-data ids exist only there), deployment-specific fields (`/api/info`
versions, the API documentation's origin), www bugs (resource definitions of
other studies in the study list, `allSampleCount` 1 in `studies/fetch`) and
pages whose boundary falls in ties Java leaves unordered (samples of the same
id in several studies, clinical-table sort values). `-annotate` marks them in
an existing corpus. Compare it against api.cbioportal.org with an API serving a mirror
built by `transform --from-prod`:

```bash
go run ./test/parity/loggedgen -n 500 > .parity/logged.txt      # CLICKHOUSE_URL/USER/PASSWORD
go run ./cmd/paritydiff -corpus .parity/logged.txt -b https://api.cbioportal.org \
  -ua cbioportal-go-parity -a http://localhost:18091 -cache .parity/cache-api -q
```

On 2026-10-03: 620 of 636 requests identical, 16 known differences, none
unexplained. One more is a www failure rather than a difference: a study
without clinical events can time out there (HTTP 502), as
`getDataOfClinicalEvents` without event ids reads all of
`clinical_event_data`.

## Java oracle

The Java backend itself, run on the same fixture studies, is the reference
for the edge cases production does not exercise (gene panels, generic and
patient-level assays, hg38, ASCN, namespaces, timelines, SVs).
`test/java_oracle.sh` builds the ClickHouse database `java_e2e` with the
`cbioportal/cbioportal:master` image's own tools (its schema and seed,
cbioportal-core's importer for the gene panels, gene sets and the five
studies, then its derived-table script; ~2 min) and runs the backend
(`clickhouse_mode=true`, `authenticate=false`, 2 GB heap) on :18095:

```bash
test/java_oracle.sh db cbioportal-test      # needs ClickHouse on :18123 (container ch-dge)
test/java_oracle.sh start
go run ./cmd/paritydiff -a http://localhost:18090 -b http://localhost:18095 \
  -corpus test/parity/fixtures.txt -cache '' -rate 0 -q -dump .parity/oracle
API=http://localhost:18095 go test ./test/ -run Oracle   # the oracle cases hold for Java too
test/java_oracle.sh stop
```

`test/parity/fixtures.txt` (~1,450 requests) is generated from the Java
backend's answers — each study's profiles, sample lists, attributes and the
chart values its filters use — by `go run ./test/parity/fixturegen >
test/parity/fixtures.txt`; regenerate it rather than editing it. The API
under test must serve databases built by `test/setup_test_db.sh` from the
same cbioportal-test checkout. `TestOracle*` contract tests pin what the
comparison fixed; running the whole contract suite against the Java backend
also shows where a test asserts something else than Java does (the session
and configuration tests fail there by design: no session service).

On 2026-10-04: 1,123 of 1,447 requests identical (798 before the fixes it
prompted, 1,090 before the API followed the importer's internal id order).
Most of the rest are known classes: row order (~180) that comes from
ClickHouse rather than from the data — the output order of a `GROUP BY` or
of a hash join's matches (study-view counts, gene panel data), ties of an
unstable sort (mutations of a gene in a sample, attributes of a sample's
clinical data) — or from a Java `HashMap`; the study `status` (the Java
importer stores 1 = AVAILABLE, production and we report 0) and `groups`
order (a Java HashSet's), and Java
bugs on these fixtures — a numeric filter on a patient-level generic assay
profile is ignored, `/clinical-events-meta/fetch` and `/survival-data/fetch`
answer 500 (a MyBatis expression calls a missing
`SqlUtils.combineStudyAndPatientIds`), categorical enrichments of a
patient-level profile answer 500, and `/clinical-data-table/fetch` answers
500 for a `sortBy` that is not an attribute.

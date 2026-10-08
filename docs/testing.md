# Testing


Three tiers, fastest first:

1. **Unit tests** — `go test ./internal/...` (no services needed). Includes the
   `internal/databin` port of Java's DataBinner, checked against ~1,800
   Java-recorded scenarios in `internal/databin/testdata`. With
   `CLICKHOUSE_TEST_URL=http://localhost:18123`, `internal/clickhouse` also
   round-trips query parameters (escaping, URL / form / external-table
   transport) through a live server and checks that the driver path decodes
   every supported column type exactly as the JSONEachRow path does, and
   `internal/api` ingests, transforms and exports a small study whose
   matrices have a single gene key column (reading genes from
   `cbioportal_ref`, or `CLICKHOUSE_TEST_REF_DB`).
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
   config's 120s test timeout. Locally 3 workers is the practical ceiling on
   a 4-core box shared with ClickHouse. The GitHub workflow uses 2: the
   private repo's runner has 2 vCPUs, and 3 workers there make specs miss
   their 5s waits (the IGV-heavy study-view download-controls test is still
   timing-sensitive on that runner).
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

   Optional sign-in (`authenticate=optional_oauth2`) has one too, skipped
   unless `OPTIONAL_KEYCLOAK` is set: anonymous browsing, sign-in as
   `testuser` through `/login`, the same studies either way, a saved virtual
   study, logout. With `REDIS` the API keeps its login sessions in Redis
   (the store's live test is `REDIS_TEST_ADDR=… go test -run
   RedisLoginStoreLive ./internal/api`):

   ```bash
   docker run -d --name cbioportal-go-redis -p 127.0.0.1:16379:6379 redis:7-alpine
   OPTIONAL=true REDIS=localhost:16379 PORT=18096 test/e2e_api.sh
   OPTIONAL_KEYCLOAK=1 API=http://localhost:18096 go test -run OptionalOAuth2Keycloak ./test/
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

### Nightly parity job

`test/parity/nightly.sh` compares go.cbioportal.org with api.cbioportal.org
on production traffic every day. It replays a corpus of ~5,000 logged
requests (`test/parity/logged_corpus.sh`) drawn from a capture window of
`cbioportal_qc.logged_requests` (`CORPUS_SINCE`, default 2026-10-07
22:30 UTC, and `CORPUS_UNTIL`; read with `FINAL`, so each distinct request
counts once): only www.cbioportal.org and api.cbioportal.org (not master.*
or internal hosts), read-only endpoints, status 200, untruncated bodies;
each endpoint's share grows with the square root of its volume, at least
3, and a quarter of the corpus (`CORPUS_API_SHARE`) comes from
api.cbioportal.org, the go/no-go target, as its own stratum (entries named
`api-…`). It is seeded by ISO week and written to the gitignored
`.parity/nightly/corpus.txt`, mode 600, as it is real user traffic; its
header records the window, each host's requests and the endpoint coverage.
Each run marks it again with `loggedgen -annotate`, so expectations added
to `known.go` apply before the weekly regeneration.

Every request goes to B twice (paritydiff `-self-check`), at most 4
requests/s to B in all, without a response cache (a night's responses take
several GB). Where Java's two answers differ the request is *Java
nondeterministic*: reported apart, never unexplained. Requests that failed
in transport or with 502/503/504 are sent once more after 2 minutes, so a
pod rollout is not reported as a regression. `runs/<date>/report.md`
records both deployments' `/api/info` versions, the window, the hosts'
traffic and endpoint coverage, the outcome counts (identical, known, Java
nondeterministic, unexplained difference, status mismatch, error) overall
and per host, the clean nights in a row (`nights.txt` in `PARITY_STATE`;
7 are the go/no-go for api.cbioportal.org), and details the unexplained
requests (request, body cut to 200 bytes, first diff lines), marking those
new since the previous run, and the Java nondeterministic ones;
`runs/<date>/dump` keeps both bodies of each unexplained request, cut to
1 MB (paritydiff `-dump -dump-unexplained -dump-max`). Runs are kept 14
days. The exit status is 1 when anything is unexplained.

While anything is unexplained it keeps one issue open in this repository,
"Nightly parity: unexplained differences" (label `parity`): opened with the
report, commented with each later failing report, commented and closed on
the first clean night. `--dry-run` prints the `gh` actions instead and
records no night, `--regen` regenerates the corpus first (as does a
corpus without a window).

On the parity host it runs from cron at 15:17 UTC, after the 14:00 mirror
rebuild: `~/.local/share/cbioportal-go-parity/run.sh` checks out
`origin/main` (or the ref in `state/ref`) into `src/`, keeps the night
record in `state/` (`PARITY_STATE`), regenerates the corpus on Sundays,
and logs to `logs/<date>.log`; a lock keeps runs from
overlapping.

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

paritydiff ignores key order; the field order of each projection (Jackson's:
a model's fields in declaration order, superclass first, properties renamed
by a mixin or `@JsonProperty` last) is pinned by the `TestOracleProjection*`
contract tests. [Java quirks](java-quirks.md) lists every Java oddity go
copies, leaves out or cannot match. Left as they are: generic assay data
answers 500 for `projection=ID`; `/sample-lists/fetch`, `/gene-panels/fetch`,
`/resource-definitions/fetch`, the generic assay meta endpoints,
`/mutation-data-counts/fetch` and `/treatments/sample-counts/fetch` ignore
META (they answer a body, no total-count); `/genes` answers 500 for
`sortBy=cytoband` or `length` (GeneSortBy lists them, the gene table has no
such column) and re-sorts every page by symbol, so `sortBy` only picks the
page's genes; and mutations share the importer's `mutation_event` across
studies, so a record reports the NCBI build of the study that first imported
its event.

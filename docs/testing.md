# Testing


Three tiers, fastest first:

1. **Unit tests** — `go test ./internal/...` (no services needed). Includes the
   `internal/databin` port of Java's DataBinner, checked against ~1,800
   Java-recorded scenarios in `internal/databin/testdata`.
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

CI (`.github/workflows/ci.yml`) runs gofmt, vet and the unit tests, then the
contract tests against a ClickHouse service loaded with the fixture studies,
on every push and pull request. The full frontend e2e suite
(`.github/workflows/e2e.yml`) runs weekly and on demand.

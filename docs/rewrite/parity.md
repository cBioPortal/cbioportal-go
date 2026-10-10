# Parity with Java

"Parity" means go gives the same answer as the Java backend for the same
request. This page explains the tools that check it: comparing go with
production (www.cbioportal.org) on real requests, the nightly job that does
this every day, and the "Java oracle", a local Java server run on the same
test data as go.

## Contents

- [API parity with www.cbioportal.org](#api-parity-with-wwwcbioportalorg)
- [Logged production requests](#logged-production-requests)
- [Nightly parity job](#nightly-parity-job)
- [Java oracle](#java-oracle)

## API parity with www.cbioportal.org

"Parity" means go gives the same answer as Java. `rewrite/java-v7-to-go/paritydiff` sends a
list of API requests to two servers and compares the responses.

- The order of keys in JSON does not matter.
- Numbers match if they differ by less than one part in a billion (1e-9).
- Options on each request can sort arrays whose order Java does not define
  (`unordered`, `unordered=key`, `ties=field`), drop keys (`ignore=`), or
  mark a difference as expected with its reason (`known="..."`), as
  expected only when go answers an empty list (`known-if-empty="..."`), or
  as expected only when go succeeds and Java fails with a 4xx or 5xx
  (`known-if-b-fails="..."`).

Responses from the reference server are cached in `.parity/cache`, which git
ignores. They are fetched at most twice a second with a browser User-Agent,
so running the check again does not load www.

`test/parity/corpus.txt` holds about 275 requests the frontend makes. They
cover studies, samples, clinical data, profiles, mutations, copy number,
structural variants, molecular and generic assay data, study view, results
view, group comparison and patient view, on four studies
(acc_tcga_pan_can_atlas_2018, msk_impact_2017, acbc_mskcc_2015 and
lgg_ucsf_2014). Run it against an API serving a copy of production's data
(see [Deployment](../deployment.md#mirroring-production)):

```bash
go run ./rewrite/java-v7-to-go/paritydiff -a http://localhost:18091 -b https://www.cbioportal.org \
  -corpus test/parity/corpus.txt -q -dump .parity/dump
```

| Flag | Effect |
|---|---|
| `-q` | list only requests whose answers differ |
| `-run REGEXP` | run only matching requests |
| `-dump DIR` | save both responses of each difference |
| `-refresh` | fetch www's responses again instead of using the cache |

The command fails (non-zero exit status) when a request differs without a
`known` reason.

On 2026-10-03, 257 of 275 requests were identical and the other 18 were known
differences. These are listed in
[Deployment](../deployment.md#mirroring-production).

### Logged production requests

Production logs the API requests it receives in the ClickHouse table
`cbioportal_qc.logged_requests`. `rewrite/java-v7-to-go/loggedgen` picks a sample of
them, GET and POST, in proportion to how often each endpoint is called.

It also marks requests whose difference is expected. The list is in
`rewrite/java-v7-to-go/loggedgen/known.go`:

- **Saved sessions:** sessions and custom data exist only in www's session
  store.
- **Server-specific fields:** version numbers in `/api/info` and the address
  in the API documentation.
- **Bugs in www:** the study list shows resource definitions of other
  studies, `studies/fetch` and the legacy studies report
  `allSampleCount: 1`, and other Java bugs that go fixes
  ([Java quirks](java-quirks.md)), such as 500s and the treatments of the
  whole portal (marked only when go answers an empty list).
- **Paging go adds:** a page of an endpoint whose Java controller has no
  paging parameters, and the API documentation that lists them.
- **Tied rows:** a page boundary falls among rows that Java leaves in no
  fixed order, such as samples with the same id in several studies.

Requests for a profile's whole copy-number matrix
(`discreteCopyNumberEventType=ALL` without genes) are left out: Java
answers them with gigabytes, too costly to replay every night.

`-annotate` marks an existing list again with the current expectations,
replacing earlier marks, and leaves out the requests now skipped. Compare against
api.cbioportal.org, with go serving a copy of production built by
`transform --from-prod`:

```bash
go run ./rewrite/java-v7-to-go/loggedgen -n 500 > .parity/logged.txt      # needs CLICKHOUSE_URL/USER/PASSWORD
go run ./rewrite/java-v7-to-go/paritydiff -corpus .parity/logged.txt -b https://api.cbioportal.org \
  -ua cbioportal-go-parity -a http://localhost:18091 -cache .parity/cache-api -q
```

On 2026-10-03, 620 of 636 requests were identical and 16 were known
differences. One more request failed on www rather than differing: for a
study without clinical events, www can time out (HTTP 502), because Java then
reads the whole `clinical_event_data` table.

### Nightly parity job

`cbioportal-rewrite-test java-v7-to-go parity run` compares go.cbioportal.org
with api.cbioportal.org on real production traffic every day. For each request it reports whether the
answers are identical, a known difference, or unexplained.

```bash
go run ./rewrite/cmd/cbioportal-rewrite-test java-v7-to-go parity run [--regen] [--dry-run]
```

`test/parity/nightly.sh` runs the same command. `parity -h` lists the
settings (environment variables such as `PARITY_DIR`, `A`, `B`, `RATE`).

**The request list.** `parity corpus` (or `test/parity/logged_corpus.sh`)
builds a list of about 5,000 logged requests:

- They come from a capture window of `cbioportal_qc.logged_requests` set by
  `CORPUS_SINCE` (default 2026-10-07 22:30 UTC) and `CORPUS_UNTIL`. Each
  distinct request counts once.
- Only requests to www.cbioportal.org and api.cbioportal.org, only read-only
  endpoints, only those that returned 200 with a complete body.
- Busy endpoints get more requests, growing with the square root of their
  volume, with at least 3 each.
- A quarter (`CORPUS_API_SHARE`) comes from api.cbioportal.org, since that is
  the site go aims to replace first. Those entries are named `api-…`.
- The list changes once a week. It is saved to `.parity/nightly/corpus.txt`,
  which git ignores and only the owner can read, because it is real user
  traffic. Its header records the window, the requests per host and which
  endpoints are covered.
- Each run marks the list again with `loggedgen -annotate`, so new and
  changed entries in `known.go` apply right away.

**How requests are sent.** Every request goes to api.cbioportal.org twice
(`-self-check`), at most 4 requests a second, with no cache. If Java's two
answers differ, the request is reported as *Java nondeterministic*, never as
unexplained. Requests that fail to connect or get 502, 503 or 504 are sent
once more after 2 minutes, so a server restart is not reported as a bug.
The same goes for a request where go answers and Java returns 500: Java
sometimes fails once under load and answers fine a minute later. A 500
from go is never sent again, so a go failure always shows.

**When Java fails and go answers.** If Java still answers with a 5xx after
the second try while go answers, the request is counted as *Java failed,
go answered*. It does not count against a clean night: the request was
valid, and answering it is what go should do. The report lists these
requests on their own, because there is no Java answer to check go's
answer against.

**Memory.** paritydiff holds one request at a time, keeps bodies over 1 MB
in files, and compares large arrays element by element with an external
sort, so its memory does not grow with response size. It still runs under a
hard cap, `PARITY_MEMORY_MAX` (2G): a systemd user scope, found from cron too
via `/run/user/UID`, else `prlimit --as`. With neither, the run stops rather
than run uncapped.

**The report.** Each run writes `runs/<date>/report.md` with:

- both servers' versions from `/api/info`, the window, traffic per host and
  endpoint coverage;
- counts of identical, known, Java nondeterministic, unexplained, status
  mismatch and error, overall and per host;
- the number of clean nights in a row (stored in `nights.txt` in
  `PARITY_STATE`). Seven clean nights are the bar for go to take over
  api.cbioportal.org;
- each unexplained request with its body (first 200 bytes) and the first
  lines of the difference, marking those that are new since the last run.

`runs/<date>/dump` keeps both responses of each unexplained request, up to
1 MB each. Runs are kept for 14 days. The command exits with status 1 when
anything is unexplained, 2 when the check itself fails.

**The GitHub issue.** One issue with the label `parity` tracks the goal of 7
clean nights in a row. Its description says what the goal is and shows where
it stands: the clean nights so far and a table of recent nights, each linked
to its report. Every night, clean or not, the job posts that night's report as
a comment and rewrites the description. When the goal is reached, it says so
and closes the issue. If no issue is open, the next run opens one.

| Option | Effect |
|---|---|
| `--dry-run` | print the GitHub actions instead of doing them, and do not count the night |
| `--regen` | rebuild the request list first (also done when the list has no window) |

**Where it runs.** On the parity host, cron starts it at 15:17 UTC, after the
mirror is rebuilt at 14:00:

```bash
go build -o ~/.local/bin/cbioportal-rewrite-test ./rewrite/cmd/cbioportal-rewrite-test
# crontab -e
17 15 * * * $HOME/.local/bin/cbioportal-rewrite-test java-v7-to-go parity nightly
```

`parity nightly` works in `~/.local/share/cbioportal-go-parity` (`-home`). It
checks out `origin/main` (or the ref in `state/ref`, e.g. `local/<branch>`
from the local clone) into `src/`, runs `parity run` from there, keeps the
record of nights in `state/` (`PARITY_STATE`), rebuilds the request list on
Sundays, and logs to `logs/<date>.log` (kept 60 days). A lock stops two runs
from overlapping.

## Java oracle

An oracle is a trusted source of correct answers. Here it is the Java backend
itself, run locally on the same test studies as go. It covers cases that
production traffic rarely reaches, such as gene panels, generic and
patient-level assays, hg38, allele-specific copy number, namespaces,
timelines and structural variants.

`test/java_oracle.sh db` builds the ClickHouse database `java_e2e` with the
tools inside the `cbioportal/cbioportal:master` image: its schema and seed
data, the Java importer for the gene panels, gene sets and five test studies,
and its derived-table script. This takes about 2 minutes.
`test/java_oracle.sh start` runs the Java backend on port 18095
(`clickhouse_mode=true`, `authenticate=false`, 2 GB memory).

```bash
test/java_oracle.sh db cbioportal-test      # needs ClickHouse on :18123 (container ch-dge)
test/java_oracle.sh start
go run ./rewrite/java-v7-to-go/paritydiff -a http://localhost:18090 -b http://localhost:18095 \
  -corpus test/parity/fixtures.txt -cache '' -rate 0 -q -dump .parity/oracle
API=http://localhost:18095 go test ./test/ -run Oracle   # the oracle cases hold for Java too
test/java_oracle.sh stop
```

`test/parity/fixtures.txt` holds about 1,450 requests built from the Java
backend's own answers: each study's profiles, sample lists, attributes and
the chart values its filters use. Rebuild it with
`go run ./test/parity/fixturegen > test/parity/fixtures.txt` rather than
editing it. The go API must serve databases built by `test/setup_test_db.sh`
from the same cbioportal-test checkout.

The `TestOracle*` contract tests lock in what this comparison fixed. You can
also run the whole contract suite against Java to see where a test expects
something different from Java. The session and configuration tests fail
there on purpose, because the local Java has no session service.

On 2026-10-04, 1,123 of 1,447 requests were identical. Most of the rest fall
into known groups:

| Difference | Why |
|---|---|
| Row order (about 180 requests) | The order comes from ClickHouse, not the data: the output of a `GROUP BY` or a join (study-view counts, gene panel data), ties in a sort (mutations of a gene in one sample, a sample's clinical attributes), or a Java `HashMap`. |
| Study `status` | Java's importer stores 1 (available). Production and go report 0. |
| Study `groups` order | It comes from a Java `HashSet`. |
| Numeric filter on a patient-level generic assay profile | Java ignores it (bug). |
| `/clinical-events-meta/fetch`, `/survival-data/fetch` | Java answers 500: a database query calls a function that does not exist (bug). |
| Categorical enrichments of a patient-level profile | Java answers 500 (bug). |
| `/clinical-data-table/fetch` with a `sortBy` that is not an attribute | Java answers 500 (bug). |

paritydiff ignores the order of keys in JSON. The order Java writes fields in
is checked separately by the `TestOracleProjection*` contract tests. Java
writes a model's fields in the order they are declared, parent class first,
and renamed fields last.

[Java quirks](java-quirks.md) lists every Java oddity that go copies, leaves
out or cannot match. These Java behaviours are left as they are:

- Generic assay data answers 500 for `projection=ID`.
- Some endpoints ignore `projection=META` and return a body instead of only a
  total count: `/sample-lists/fetch`, `/gene-panels/fetch`,
  `/resource-definitions/fetch`, the generic assay meta endpoints,
  `/mutation-data-counts/fetch` and `/treatments/sample-counts/fetch`.
- `/genes` answers 500 for `sortBy=cytoband` or `sortBy=length`, because the
  gene table has no such columns. It also re-sorts every page by symbol, so
  `sortBy` only decides which genes land on the page.
- Studies share mutation records in the importer's `mutation_event` table. A
  mutation therefore reports the genome build of the study that first
  imported it.

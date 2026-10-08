# The rewrite

cbioportal-go replaces cBioPortal's Java backend (the REST API and the
importer). These pages explain how the rewrite was done, how closely go
matches Java, where go fixes Java's bugs, and how the two compare in speed
and cost. Once go has replaced Java, these pages are a record of how the
switch was made.

| Page | What it covers |
|---|---|
| [Method](method.md) | How the rewrite was built and checked against Java. |
| [Parity with Java](parity.md) | The tools that compare go's answers with Java's, including the nightly job. |
| [Java quirks](java-quirks.md) | Odd or buggy Java behaviour: what go copies, what it fixes, and what it cannot match. |
| [API coverage](api-coverage.md) | Every Java endpoint, whether go serves it, and which tests cover it. |
| [Benchmark](benchmark.md) | Speed, memory and cost of go compared with Java, for both the API and the importer. |

## Before switching api.cbioportal.org to go

- **Nightly parity.** The nightly job must report no unexplained difference
  for seven nights in a row. Its results are in the GitHub issue labelled
  `parity`.
- **Paging and sorting.** go will honour every paging and sorting parameter
  an endpoint accepts, including where Java ignores it. This is in progress.

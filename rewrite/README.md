# Rewrite tools

This folder holds the tools used to move cBioPortal from its Java backend to
go. They are only needed during the move. Each move has its own folder, so
the tools for one move can be deleted in one step once it is finished.

The documentation for the move, including how these tools are used and what
they found, is in [docs/rewrite](../docs/rewrite/).

| Folder | What it holds |
|---|---|
| [cmd/cbioportal-rewrite-test](cmd/cbioportal-rewrite-test/) | The command that runs the tools: `cbioportal-rewrite-test <move> <command>`. |
| [java-v7-to-go](java-v7-to-go/) | Tools for the move from Java cBioPortal v7 (www.cbioportal.org) to go. |

The `java-v7-to-go` tools:

| Tool | What it does | More |
|---|---|---|
| `mirror` | Copies production's database into go's own database within an hour of each production import, so go.cbioportal.org serves the same data as www. | [Deployment](../docs/deployment.md#mirroring-production) |
| `parity` | Sends the same API requests to go and to Java and reports any answer that differs. `parity nightly` is the daily job. | [Parity with Java](../docs/rewrite/parity.md) |
| `paritydiff` | The comparison engine `parity` uses: it sends requests to two servers and compares the JSON. | [Parity with Java](../docs/rewrite/parity.md#api-parity-with-wwwcbioportalorg) |
| `loggedgen` | Picks a sample of real production requests to compare, and notes the differences that are already known and explained. | [Logged production requests](../docs/rewrite/parity.md#logged-production-requests) |

To see the commands and their options:

```bash
go run ./rewrite/cmd/cbioportal-rewrite-test java-v7-to-go help
```

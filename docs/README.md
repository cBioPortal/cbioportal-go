# Documentation

These pages explain how cbioportal-go works and how to run it. Pages about
the move from the Java backend to go, such as comparisons, benchmarks and
the differences between the two, are in [rewrite/](rewrite/).

| Page | What it covers |
|---|---|
| [Architecture](architecture.md) | How the code is laid out and how data flows from study files to the website. |
| [Configuration](configuration.md) | Every setting and flag of the importer and the API server. |
| [Command-line conventions](cli.md) | The rules the programs share: JSON output, dry runs, exit codes and suggestions. |
| [Deployment](deployment.md) | Running go in production: the container image, Kubernetes, the production mirror, hub.cbioportal.org and pull request previews. |
| [Testing](testing.md) | The unit, contract and end-to-end tests, and how to run them. |
| [The rewrite](rewrite/) | How go compares with the Java backend and what is left before switching. |

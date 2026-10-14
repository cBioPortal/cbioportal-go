# Command-line conventions

go has three programs: `cbioportal-import` loads studies, `cbioportal-api`
serves the portal, and `cbioportal-ops` runs the cBioPortal team's own jobs
for cbioportal.org. This page lists the rules they share. The rules make
the programs easy to use from scripts and by AI agents, as well as by
people. Each program's commands and settings are in
[Configuration](configuration.md).

## Contents

- [Commands and flags](#commands-and-flags)
- [Settings from the environment](#settings-from-the-environment)
- [JSON output](#json-output)
- [Trying a change first](#trying-a-change-first)
- [Exit codes](#exit-codes)
- [Mistakes and suggestions](#mistakes-and-suggestions)
- [Checking a setup](#checking-a-setup)
- [Shell completion](#shell-completion)

## Commands and flags

- The command comes first, then its arguments and flags:
  `cbioportal-import ingest <study-dir> --db cbioportal_raw`.
  `cbioportal-api` serves when it is given no command.
- A flag can be written `-db x`, `--db x` or `--db=x`. Flags can come
  before or after the arguments.
- `-h`, `--help` or `help <command>` prints the help, with examples. The
  help lists each flag with its environment variable and its default.
- `version` (or `--version`) prints the program's version.
- The programs use [Cobra](https://github.com/spf13/cobra) for their
  commands. Each command is one constructor in the program's `main`
  package that declares its flags and the function that runs it.
- No program ever asks a question while it runs. Everything comes from
  the command line and the environment.

## Settings from the environment

Every `cbioportal-api` setting has an environment variable, such as
`CBIOPORTAL_DB` for `--db`. A flag wins over the environment variable,
and the environment variable wins over the default. Containers usually
use the variables.

Secrets, such as passwords and client secrets, come only from environment
variables. A flag would show them to every user of the machine, through
the list of running programs. Programs never print a secret: where one is
shown, it appears as `(redacted)`.

## JSON output

Commands that report a result take `--json`. They then print one JSON
document on standard output instead of text. Progress messages still go
to standard error, so a script can read standard output alone. The field
names stay the same from one version to the next.

| Program | Commands with `--json` |
|---|---|
| `cbioportal-import` | `plan`, `ingest`, `remove-study`, `ingest-case-lists`, `load-reference`, `load-genesets`, `load-gene-panels`, `import-study`, `fetch-datahub`, `transform`, `version` |
| `cbioportal-api` | `config`, `check`, `version` |
| `cbioportal-ops` | `version` |

For example, `cbioportal-import plan <study-dir> --json` lists each data
file with its type and the staging table it loads into.

## Trying a change first

Commands that delete or replace data take `--dry-run`. With it, the
command reports what it would do and changes nothing.

| Command | What `--dry-run` reports |
|---|---|
| `cbioportal-import remove-study` | The tables and the number of rows it would delete. |
| `cbioportal-import import-study` | Which studies are new, which would replace a study already imported, and whether the reference data and gene panels would be loaded. It takes no lock. |
| `cbioportal-ops preview teardown`, `preview gc` | The databases it would drop. |

## Exit codes

Every program exits with one of three codes:

| Code | Meaning |
|---|---|
| 0 | The command worked. |
| 1 | The command failed while it ran, or a check failed. |
| 2 | The command line or the settings are wrong, so nothing was done. |

## Mistakes and suggestions

A mistyped command or flag stops the program with code 2 and a
suggestion of the nearest name:

```
$ cbioportal-api --prot=8080
cbioportal-api: unknown flag --prot (did you mean --port?)
```

`cbioportal-api` passes any other `--key=value` argument on as a portal
property, as the Java portal does. A key that is a near miss of one of its
flags, and is not a property it knows, is treated as a typo instead.

When a setting is given both by its go name and by the Java portal's name
with different values, `cbioportal-api` stops and names both.

## Checking a setup

`cbioportal-api config` prints every setting the server would use, with
where its value came from. `cbioportal-api check` checks the settings and
reaches every service they name: ClickHouse and its databases, the sign-in
providers and the login session store. Both take the same flags as the
server, so you can run them with the server's own command line:

```
$ cbioportal-api check --db cbioportal_e2e --raw-db cbioportal_e2e_raw
ok    settings: valid
ok    clickhouse: http://localhost:18123, ClickHouse 25.3.14.14
ok    clickhouse --db: cbioportal_e2e: 34 tables
ok    clickhouse --raw-db: cbioportal_e2e_raw: 29 tables
ok    clickhouse --ref-db: cbioportal_ref: 10 tables
```

## Shell completion

`completion` prints a script that lets your shell complete the program's
commands and flags when you press Tab. For example, in bash:

```
source <(cbioportal-import completion bash)
```

`cbioportal-import completion -h` lists the shells it supports.

#!/usr/bin/env bash
# Nightly API parity check: replays the logged-traffic corpus
# (test/parity/logged_corpus.sh) against go.cbioportal.org (A, the Go API on
# the mirror) and api.cbioportal.org (B, the Java API of production), writes
# a short report, and keeps one GitHub issue open while unexplained
# differences, status mismatches or errors remain.
#
#   test/parity/nightly.sh [--regen] [--dry-run]
#
#   --regen    regenerate the corpus first (also done when there is none)
#   --dry-run  print the GitHub issue actions and body instead of running gh
#
# Read-only: the corpus holds no writing endpoints and B is sent at most
# RATE requests per second. Responses are not cached (a night's would take
# several GB); only the bodies of the unexplained requests are kept, cut to
# 1 MB each (runs/<date>/dump).
#
# Environment:
#   PARITY_DIR  corpus.txt, runs/<date>/ (results.json, output.txt,
#               report.md, issue.md, dump/; runs older than 14 days are
#               pruned) and
#               last-unexplained.txt; default .parity/nightly
#   A, B        origins (default https://go.cbioportal.org, https://api.cbioportal.org)
#   RATE        requests per second to B (default 4)
#   UA          User-Agent (api.cbioportal.org refuses browsers; default cbioportal-go-parity)
#   ISSUE_REPO  repository of the alert issue (default cBioPortal/cbioportal-go)
#   PARITY_ISSUE  the alert issue's number, instead of looking it up (for --dry-run tests)
#
# Exit status: 0 when every request is identical or a known difference, 1 on
# unexplained differences, status mismatches or errors, 2 when the check
# itself could not run.
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DIR="${PARITY_DIR:-$ROOT/.parity/nightly}"
A="${A:-https://go.cbioportal.org}"
B="${B:-https://api.cbioportal.org}"
RATE="${RATE:-4}"
UA="${UA:-cbioportal-go-parity}"
ISSUE_REPO="${ISSUE_REPO:-cBioPortal/cbioportal-go}"
TITLE="Nightly parity: unexplained differences"
LABEL="parity"
SHOWN=40 # unexplained requests detailed in the report

regen="" dry=""
for arg in "$@"; do
  case "$arg" in
    --regen) regen=1 ;;
    --dry-run) dry=1 ;;
    *) echo "usage: $0 [--regen] [--dry-run]" >&2; exit 2 ;;
  esac
done

die() { echo "nightly: $*" >&2; exit 2; }

DAY="$(date -u +%F)"
RUN="$DIR/runs/$DAY"
CORPUS="$DIR/corpus.txt"
mkdir -p "$DIR/runs" || die "cannot create $DIR"

if [ -n "$regen" ] || [ ! -s "$CORPUS" ]; then
  "$ROOT/test/parity/logged_corpus.sh" "$CORPUS" || die "corpus generation failed"
fi
find "$DIR/runs" -mindepth 1 -maxdepth 1 -type d -mtime +14 -exec rm -rf {} +
# No response cache is kept: remove any left in a run directory.
find "$DIR/runs" -mindepth 2 -maxdepth 2 -type d -name cache -exec rm -rf {} +
rm -rf "$RUN"
mkdir -p "$RUN"
(cd "$ROOT" && go build -o "$DIR/bin/paritydiff" ./cmd/paritydiff) || die "building paritydiff failed"

# version describes a deployment from its /api/info.
version() {
  curl -sS -m 60 -A "$UA" -H 'Accept: application/json' "$1/api/info" 2>/dev/null |
    jq -r '[.portalVersion,
            (.gitCommitId // "" | select(. != "" and . != "not set") | "commit " + .[0:12]),
            (.dbVersion // empty | "db " + .),
            (.derivedTableVersion // empty | "derived tables " + .)] | join(", ")' 2>/dev/null ||
    echo "unreachable"
}
VA="$(version "$A")"
VB="$(version "$B")"

start=$(date +%s)
"$DIR/bin/paritydiff" -corpus "$CORPUS" -a "$A" -b "$B" -ua "$UA" -rate "$RATE" \
  -cache '' -retry 2m -q -json "$RUN/results.json" \
  -dump "$RUN/dump" -dump-unexplained -dump-max 1000000 >"$RUN/output.txt" 2>&1
rc=$?
minutes=$(( ($(date +%s) - start + 59) / 60 ))
[ -s "$RUN/results.json" ] || die "paritydiff wrote no results (exit $rc), see $RUN/output.txt"

R="$RUN/results.json"
count() { jq --arg o "$1" '[.[] | select(.outcome == $o)] | length' "$R"; }
n_all=$(jq length "$R")
n_identical=$(count identical)
n_known=$(count known)
n_differs=$(count differs)
n_status=$(count status)
n_error=$(count error)
n_retried=$(jq '[.[] | select(.retried)] | length' "$R")

if [ "$n_differs" -gt 0 ]; then result="unexplained diff"
elif [ "$n_status" -gt 0 ]; then result="status mismatch"
elif [ "$n_error" -gt 0 ]; then result="error"
elif [ "$n_known" -gt 0 ]; then result="known"
else result="identical"
fi
bad=$((n_differs + n_status + n_error))

# Requests that differ without a known reason, and those new since the last run.
jq -r '.[] | select(.outcome == "differs" or .outcome == "status" or .outcome == "error") | .name' "$R" |
  sort >"$RUN/unexplained.txt"
touch "$DIR/last-unexplained.txt"
comm -13 "$DIR/last-unexplained.txt" "$RUN/unexplained.txt" >"$RUN/new.txt"
n_new=$(wc -l <"$RUN/new.txt")

{
  echo "# Nightly parity $DAY"
  echo
  echo "Result: **$result** ($bad unexplained of $n_all requests, $n_new new since the last run)"
  echo
  echo "- A: $A ($VA)"
  echo "- B: $B ($VB)"
  echo "- Corpus: $n_all requests sampled from production traffic, generated $(date -u -r "$CORPUS" +%F); $minutes min at $RATE/s"
  echo
  echo "| outcome | requests |"
  echo "|---|---:|"
  echo "| identical | $n_identical |"
  echo "| known difference | $n_known |"
  echo "| unexplained difference | $n_differs |"
  echo "| status mismatch | $n_status |"
  echo "| error | $n_error |"
  if [ "$n_retried" -gt 0 ]; then
    echo
    echo "$n_retried requests failed in transport or with 502/503/504 and were sent again after 2 min; the table counts the retry."
  fi
  if [ "$bad" -gt 0 ]; then
    echo
    echo "## Unexplained"
    if [ "$n_new" -gt 0 ]; then
      echo
      echo "New since the last run: $(head -n 60 "$RUN/new.txt" | sed 's/.*/`&`/' | paste -sd ' ' -)$([ "$n_new" -gt 60 ] && echo " and $((n_new - 60)) more")"
    fi
    jq -r --rawfile new "$RUN/new.txt" --argjson shown "$SHOWN" '
      ($new | split("\n")) as $new
      | [.[] | select(.outcome == "differs" or .outcome == "status" or .outcome == "error")]
      | sort_by(if (.name | IN($new[])) then 0 else 1 end)
      | .[:$shown][]
      | "\n### \(.name)\(if (.name | IN($new[])) then " (new)" else "" end): \(.outcome)" +
        (if .outcome == "status" then " a=\(.statusA) b=\(.statusB)" elif .outcome == "differs" then ", \(.differences) differences" else "" end) +
        "\n\n`\(.request | .[0:300])`" +
        (if (.body // "") != "" then "\n\nbody: `\(.body)`" else "" end) +
        "\n\n```\n" + ([.diffs[]? | .[0:300]] | .[:5] | join("\n")) + "\n```"' "$R"
    if [ "$bad" -gt "$SHOWN" ]; then
      echo
      echo "… and $((bad - SHOWN)) more, listed in $RUN/unexplained.txt on the parity host."
    fi
  fi
} >"$RUN/report.md"
cat "$RUN/report.md"

# gh_run runs gh, or prints the command in a dry run.
gh_run() {
  if [ -n "$dry" ]; then
    echo "[dry-run] gh $*"
  else
    gh "$@"
  fi
}

# The alert issue: one open issue titled $TITLE with label $LABEL.
issue="${PARITY_ISSUE:-}"
[ -n "$issue" ] || issue=$(gh issue list -R "$ISSUE_REPO" --state open --label "$LABEL" \
  --json number,title -q ".[] | select(.title == \"$TITLE\") | .number" 2>/dev/null | head -n 1)
body="$RUN/issue.md"
head -c 60000 "$RUN/report.md" >"$body"
if [ "$bad" -gt 0 ]; then
  if [ -n "$issue" ]; then
    echo "updating issue #$issue"
    gh_run issue comment "$issue" -R "$ISSUE_REPO" --body-file "$body"
  else
    echo "opening the issue"
    if ! gh label list -R "$ISSUE_REPO" --search "$LABEL" --json name -q '.[].name' 2>/dev/null | grep -qx "$LABEL"; then
      gh_run label create "$LABEL" -R "$ISSUE_REPO" --color FBCA04 \
        --description "API parity between go.cbioportal.org and production"
    fi
    gh_run issue create -R "$ISSUE_REPO" --title "$TITLE" --label "$LABEL" --body-file "$body"
  fi
elif [ -n "$issue" ]; then
  echo "closing issue #$issue"
  printf 'Clean on %s: %s identical, %s known differences of %s requests.\n' \
    "$DAY" "$n_identical" "$n_known" "$n_all" >"$RUN/close.md"
  gh_run issue comment "$issue" -R "$ISSUE_REPO" --body-file "$RUN/close.md"
  gh_run issue close "$issue" -R "$ISSUE_REPO"
fi

[ -n "$dry" ] || cp "$RUN/unexplained.txt" "$DIR/last-unexplained.txt"
[ "$bad" -eq 0 ]

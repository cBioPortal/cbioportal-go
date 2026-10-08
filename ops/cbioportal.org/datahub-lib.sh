# Shared by hub-import.sh and pr-preview-setup.sh (sourced, not run):
# ClickHouse over HTTP, the GitHub App, and sparse checkouts of
# cBioPortal/datahub with their LFS files.
#
# Callers set WORKDIR and REPO (the checkout directory). Environment:
#   CLICKHOUSE_URL / CLICKHOUSE_USER / CLICKHOUSE_PASSWORD
#   GH_CLIENT_ID, GH_INSTALLATION_ID, GH_APP_KEY (/etc/gh-app/key.pem)
#                        GitHub App for API calls; reads go anonymous without it
#   GITHUB_API           (https://api.github.com)
#   DATAHUB_GIT          (https://github.com/cBioPortal/datahub.git)
#   DATAHUB_LFS_URL      datahub's LFS store (its .lfsconfig)
#   CBIOPORTAL_IMPORT    the importer binary (cbioportal-import)

GITHUB_API=${GITHUB_API:-https://api.github.com}
DATAHUB=cBioPortal/datahub
DATAHUB_GIT=${DATAHUB_GIT:-https://github.com/$DATAHUB.git}
# datahub's LFS objects live behind this endpoint, also for PRs from forks.
DATAHUB_LFS_URL=${DATAHUB_LFS_URL:-https://nsssw8k94d.execute-api.us-east-1.amazonaws.com/}
GH_APP_KEY=${GH_APP_KEY:-/etc/gh-app/key.pem}
IMPORT=${CBIOPORTAL_IMPORT:-cbioportal-import}

log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }

# ---- ClickHouse ----
# Credentials travel as headers, so they stay out of URLs and process lists.
ch() {
  local auth=()
  if [ -n "${CLICKHOUSE_USER:-}" ]; then
    auth=(-H "X-ClickHouse-User: $CLICKHOUSE_USER" -H "X-ClickHouse-Key: ${CLICKHOUSE_PASSWORD:-}")
  fi
  curl -sS --fail-with-body "${auth[@]}" "${CLICKHOUSE_URL:-http://localhost:18123}/" --data-binary "$1"
}
sql_lit() { printf '%s' "$1" | sed "s/\\\\/\\\\\\\\/g; s/'/\\\\'/g"; }

# ---- GitHub ----
# An installation token of the App, cached for 50 minutes.
gh_token() {
  if [ ! -f "$GH_APP_KEY" ] || [ -z "${GH_CLIENT_ID:-}" ] || [ -z "${GH_INSTALLATION_ID:-}" ]; then return 1; fi
  if [ -s "$WORKDIR/.gh-token" ] && [ "$(($(date +%s) - $(stat -c %Y "$WORKDIR/.gh-token")))" -lt 3000 ]; then
    cat "$WORKDIR/.gh-token"; return 0
  fi
  local b64url='openssl base64 -A | tr -d = | tr /+ _-'
  local now h p sig tok
  now=$(date +%s)
  h=$(printf '%s' '{"alg":"RS256","typ":"JWT"}' | eval "$b64url")
  p=$(printf '{"iat":%d,"exp":%d,"iss":"%s"}' $((now - 60)) $((now + 540)) "$GH_CLIENT_ID" | eval "$b64url")
  sig=$(printf '%s.%s' "$h" "$p" | openssl dgst -sha256 -sign "$GH_APP_KEY" -binary | eval "$b64url")
  tok=$(curl -sf -X POST -H "Authorization: Bearer $h.$p.$sig" -H "Accept: application/vnd.github+json" \
    "$GITHUB_API/app/installations/$GH_INSTALLATION_ID/access_tokens" | jq -r '.token // empty')
  [ -n "$tok" ] || return 1
  (umask 077; printf '%s' "$tok" > "$WORKDIR/.gh-token")
  printf '%s' "$tok"
}
# gh_api METHOD PATH [curl args]: a write with the App's token.
gh_api() {
  local method=$1 path=$2 tok
  shift 2
  tok=$(gh_token) || return 1
  curl -sf -X "$method" -H "Authorization: Bearer $tok" -H "Accept: application/vnd.github+json" \
    -H "X-GitHub-Api-Version: 2022-11-28" -H "Content-Type: application/json" "$GITHUB_API$path" "$@"
}
# gh_get PATH: a read, with the App's token when there is one (higher rate
# limit, private repositories), else anonymous.
gh_get() {
  local path=$1 tok auth=()
  if tok=$(gh_token); then auth=(-H "Authorization: Bearer $tok"); fi
  curl -sf "${auth[@]}" -H "Accept: application/vnd.github+json" "$GITHUB_API$path"
}

# ---- datahub ----
# datahub_pr_files PR: the files a datahub pull request changes.
datahub_pr_files() {
  local page files
  for page in $(seq 1 30); do
    files=$(gh_get "/repos/$DATAHUB/pulls/$1/files?per_page=100&page=$page" | jq -r '.[].filename') || return 1
    [ -n "$files" ] || break
    printf '%s\n' "$files"
  done
}
# study_dirs: the study directories (public/<study>, crdc/gdc/<study>) of
# the file paths on stdin.
study_dirs() {
  local files
  files=$(cat)
  { grep -E '^public/[^/]+/' <<<"$files" | cut -d/ -f1-2
    grep -E '^crdc/gdc/[^/]+/' <<<"$files" | cut -d/ -f1-3
    true; } | sort -u
}
# body_list KEY: the "- <item>" lines following a "KEY:" line of the text on
# stdin.
body_list() {
  tr -d '\r' | awk -v key="$1:" '
    index($0, key) == 1 {found=1; next}
    found { if ($0 ~ /^-[[:space:]]/) { sub(/^-[[:space:]]+/, ""); sub(/[[:space:]]+$/, ""); if (length($0)) print } else exit }'
}

# datahub_fetch REF: a shallow, blobless, sparse fetch of REF (a branch or
# pull/<N>/head) into $REPO without LFS content, limited to the studies on
# stdin plus the shared gene panels and the seed dump. Sets FETCHED to the
# fetched commit; datahub_checkout then checks it out.
datahub_fetch() {
  rm -rf "$REPO"
  git init -q "$REPO" || return 1
  git -C "$REPO" config lfs.url "$DATAHUB_LFS_URL"
  # datahub's .lfsconfig excludes every path from LFS downloads by default.
  git -C "$REPO" config lfs.fetchexclude ""
  git -C "$REPO" remote add origin "$DATAHUB_GIT"
  # The studies, the shared gene panels and the seed dump (not its archives).
  { sed 's|.*|/&/|'; echo /reference_data/gene_panels/; echo '/seedDB/seed-cbioportal_hg19_hg38_*.sql.gz'; } \
    | grep -v '^//$' | git -C "$REPO" sparse-checkout set --no-cone --stdin || return 1
  GIT_LFS_SKIP_SMUDGE=1 git -C "$REPO" fetch -q --depth=1 --filter=blob:none origin "$1" || return 1
  FETCHED=$(git -C "$REPO" rev-parse FETCH_HEAD)
}
datahub_checkout() {
  GIT_LFS_SKIP_SMUDGE=1 git -C "$REPO" -c advice.ignoredHook=false checkout -q FETCH_HEAD
}

# lfs_pull DIR replaces the LFS pointer files under $REPO/DIR by their
# content. Each file is smudged on its own: `git lfs pull` lists the whole
# tree with sizes, which in a blobless clone downloads every blob. An object
# missing from datahub's LFS store is fetched from HEAD_LFS_URL (the GitHub
# LFS of a PR's head repository, where a contributor may have pushed it).
is_pointer() { head -c 100 "$1" | grep -q '^version https://git-lfs'; }
lfs_pull() {
  local f n=0 fallback=${HEAD_LFS_URL:-$DATAHUB_GIT/info/lfs}
  while IFS= read -r -d '' f; do
    is_pointer "$f" || continue
    if ! git -C "$REPO" -c lfs.transfer.maxretries=1 lfs smudge -- "${f#"$REPO"/}" < "$f" > "$f.lfs" 2>"$WORKDIR/lfs.err" || is_pointer "$f.lfs"; then
      log "  ${f#"$REPO"/}: not in datahub's LFS store, trying $fallback"
      if ! git -C "$REPO" -c lfs.url="$fallback" lfs smudge -- "${f#"$REPO"/}" < "$f" > "$f.lfs" || is_pointer "$f.lfs"; then
        log "  LFS download failed: ${f#"$REPO"/}"
        rm -f "$f.lfs"
        return 1
      fi
    fi
    mv "$f.lfs" "$f"
    n=$((n + 1))
  done < <(find "$REPO/$1" -type f -size -2k -print0)
  log "  $n LFS files downloaded"
}

# load_seed REF_DB: genes, aliases and cancer types from the checkout's
# newest seed dump.
load_seed() {
  local seed
  seed=$(ls "$REPO"/seedDB/seed-cbioportal_hg19_hg38_*.sql.gz 2>/dev/null | sort -V | tail -1)
  log "loading reference data from ${seed:-?}"
  [ -n "$seed" ] && "$IMPORT" load-reference "$seed" --db "$1"
}
# load_gene_panels RAW_DB: the shared gene panels of the checkout.
load_gene_panels() {
  lfs_pull reference_data/gene_panels && "$IMPORT" load-gene-panels "$REPO/reference_data/gene_panels" --db "$1"
}
# study_id DIR: the cancer_study_identifier of a checked-out study.
study_id() {
  sed -n 's/^cancer_study_identifier:[[:space:]]*//p' "$REPO/$1/meta_study.txt" | tr -d '\r' | sed 's/[[:space:]]*$//'
}

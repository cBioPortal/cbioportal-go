package hub

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/studyimport"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/preview"
)

const golden = "../../../../internal/transform/testdata/golden"

// liveDatabases creates the three databases of a portal named name on the
// test ClickHouse ($CLICKHOUSE_TEST_URL), dropped after the test.
func liveDatabases(t *testing.T, name string) (string, preview.Databases) {
	t.Helper()
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	d := preview.DatabasesFor(fmt.Sprintf("rwtest_%s_%d_", name, os.Getpid()), 1)
	admin := ch.New(base, "default")
	drop := func() {
		for _, db := range d.All() {
			admin.ExecuteServerwide("DROP DATABASE IF EXISTS `" + db + "` SYNC")
		}
	}
	drop()
	t.Cleanup(drop)
	for _, db := range d.All() {
		if _, err := admin.ExecuteServerwide("CREATE DATABASE `" + db + "`"); err != nil {
			t.Fatal(err)
		}
	}
	return base, d
}

// testRepo is a git repository standing in for datahub.
type testRepo struct {
	t   *testing.T
	dir string
}

func (r *testRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.org",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.org")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// copyDir copies the files of directory src to dst.
func (r *testRepo) copyDir(src, dst string) {
	r.t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(r.dir, dst, strings.TrimPrefix(p, src))
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		r.t.Fatal(err)
	}
}

func (r *testRepo) commit(msg string) string {
	r.git("add", "-A")
	r.git("commit", "-q", "-m", msg)
	return r.git("rev-parse", "HEAD")
}

func newTestRepo(t *testing.T) *testRepo {
	r := &testRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "master")
	r.git("config", "uploadpack.allowFilter", "true")
	r.git("config", "uploadpack.allowAnySHA1InWant", "true")
	sql, err := os.ReadFile(filepath.Join(golden, "reference", "seed.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(sql)
	w.Close()
	for path, body := range map[string][]byte{
		"seedDB/seed-cbioportal_hg19_hg38_v1.0.0.sql.gz": gz.Bytes(),
		"README.md": []byte("datahub\n"),
	} {
		p := filepath.Join(r.dir, path)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	panel, err := os.ReadFile(filepath.Join(golden, "studies", "golden_a", "data_gene_panel_one.txt"))
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(r.dir, "reference_data/gene_panels"), 0o755)
	if err := os.WriteFile(filepath.Join(r.dir, "reference_data/gene_panels/data_gene_panel_shared.txt"), panel, 0o644); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLiveSyncMaster(t *testing.T) {
	base, hub := liveDatabases(t, "hubsync")
	t.Setenv("CLICKHOUSE_URL", base)
	q := func(sql string) string {
		t.Helper()
		out, err := ch.New(base, "default").Execute(sql + " FORMAT TSV")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	studies := func() string {
		return q("SELECT arrayStringConcat(groupArray(s), ',') FROM (SELECT DISTINCT cancer_study_identifier AS s FROM `" + hub.DB + "`.sample_derived ORDER BY s)")
	}

	// The hub holds a study master does not have, from before the sync.
	res, err := studyimport.Run(studyimport.Options{URL: base, DB: hub.DB, RawDB: hub.Raw, RefDB: hub.Ref,
		Seed: filepath.Join(newTestRepo(t).dir, "seedDB/seed-cbioportal_hg19_hg38_v1.0.0.sql.gz"), Logf: t.Logf},
		[]string{filepath.Join(golden, "studies", "golden_c")})
	if err != nil || res.Count(studyimport.Imported) != 1 {
		t.Fatalf("seeding the hub: %v %+v", err, res)
	}

	// A sync table from before failed studies were recorded.
	if _, err := ch.New(base, "default").Execute("CREATE TABLE `" + hub.Raw + "`." + syncTable +
		" (sha String, conclusion String, imported UInt32, removed UInt32, failed UInt32, error String, finished DateTime)" +
		" ENGINE = MergeTree ORDER BY finished"); err != nil {
		t.Fatal(err)
	}

	repo := newTestRepo(t)
	repo.copyDir(filepath.Join(golden, "studies", "golden_a"), "public/golden_a")
	repo.copyDir(filepath.Join(golden, "studies", "golden_b"), "public/golden_b")
	first := repo.commit("two studies")
	st := &MasterSettings{Hub: hub, Branch: "master", DatahubGit: "file://" + repo.dir, Workdir: t.TempDir(),
		RetryAfter: time.Hour, LockWait: time.Minute, LockStale: time.Minute, LockPoll: time.Second}
	ctx := context.Background()

	if err := SyncMaster(ctx, st); err != nil {
		t.Fatal(err)
	}
	if got := studies(); got != "golden_a,golden_b" {
		t.Fatalf("after the first sync the hub serves %q", got)
	}
	state, err := readSyncState(ch.New(base, "default"), hub.Raw)
	if err != nil || state.Imported != first {
		t.Fatalf("state %+v, %v; want %s imported", state, err, first)
	}

	// Nothing changed: the sync stops after comparing the commits.
	if err := SyncMaster(ctx, st); err != nil {
		t.Fatal(err)
	}
	if n := q("SELECT count() FROM `" + hub.Raw + "`." + syncTable); n != "1" {
		t.Errorf("%s syncs recorded, want 1: an unchanged master is not imported again", n)
	}

	// Two merges since the last sync are one import at the head: golden_b
	// is removed and golden_c added.
	repo.git("rm", "-q", "-r", "public/golden_b")
	repo.commit("remove golden_b")
	repo.copyDir(filepath.Join(golden, "studies", "golden_c"), "public/golden_c")
	head := repo.commit("add golden_c")
	if err := SyncMaster(ctx, st); err != nil {
		t.Fatal(err)
	}
	if got := studies(); got != "golden_a,golden_c" {
		t.Fatalf("after the second sync the hub serves %q", got)
	}
	if got := q("SELECT arrayStringConcat(groupArray(sha), ',') FROM (SELECT sha FROM `" + hub.Raw + "`." + syncTable + " ORDER BY finished, sha)"); !strings.HasSuffix(got, head) || strings.Count(got, ",") != 1 {
		t.Errorf("recorded syncs %q, want %s then %s", got, first, head)
	}

	// A study that fails to import is recorded, and the next sync after
	// RetryAfter imports it again, without the studies that worked.
	repo.copyDir(filepath.Join(golden, "studies", "golden_b"), "public/golden_b")
	// A MutSig file without its required columns fails the import.
	for name, body := range map[string]string{
		"meta_mutsig.txt": "cancer_study_identifier: golden_b\ngenetic_alteration_type: MUTSIG\ndatatype: Q-VALUE\ndata_filename: data_mutsig.txt\n",
		"data_mutsig.txt": "rank\tgene\n1\tTP53\n",
	} {
		if err := os.WriteFile(filepath.Join(repo.dir, "public/golden_b", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	broken := repo.commit("golden_b with a broken meta file")
	if err := SyncMaster(ctx, st); err != nil {
		t.Fatal(err)
	}
	last := func() string {
		return q("SELECT concat(conclusion, ' ', toString(imported), ' ', arrayStringConcat(failed_studies, ',')) FROM `" + hub.Raw + "`." + syncTable + " ORDER BY finished DESC, imported DESC LIMIT 1")
	}
	if got := last(); got != "partial 0 public/golden_b" {
		t.Fatalf("sync with a broken study recorded %q", got)
	}
	if err := SyncMaster(ctx, st); err != nil {
		t.Fatal(err)
	}
	if n := q("SELECT count() FROM `" + hub.Raw + "`." + syncTable + " WHERE sha = '" + broken + "'"); n != "1" {
		t.Errorf("%s syncs of %s; the failed study waits RetryAfter", n, broken)
	}
	st.RetryAfter = 0
	repo.copyDir(filepath.Join(golden, "studies", "golden_b"), "public/golden_b")
	if err := SyncMaster(ctx, st); err != nil {
		t.Fatal(err)
	}
	if n := q("SELECT count() FROM `" + hub.Raw + "`." + syncTable + " WHERE sha = '" + broken + "'"); n != "2" {
		t.Errorf("%s syncs of %s, want the failed study tried again", n, broken)
	}
	st.RetryAfter = time.Hour
	repo.git("rm", "-q", "public/golden_b/meta_mutsig.txt", "public/golden_b/data_mutsig.txt")
	fixed := repo.commit("golden_b fixed")
	if err := SyncMaster(ctx, st); err != nil {
		t.Fatal(err)
	}
	if got := last(); got != "success 1" {
		t.Errorf("after golden_b was fixed the sync recorded %q", got)
	}
	if got := studies(); got != "golden_a,golden_b,golden_c" {
		t.Errorf("after golden_b was fixed (%s) the hub serves %q", short(fixed), got)
	}

	// A preview of a pull request in `all` mode starts from a copy of the
	// hub's staged studies.
	_, pr := liveDatabases(t, "hubcopy")
	if err := CopyDatabases(ch.New(base, "default"), hub, pr); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"cancer_study", "mutation_extended", "clinical_sample"} {
		src := q("SELECT count() FROM `" + hub.Raw + "`." + table)
		if got := q("SELECT count() FROM `" + pr.Raw + "`." + table); got != src || got == "0" {
			t.Errorf("copied %s has %s rows, the hub %s", table, got, src)
		}
	}
	if got, want := q("SELECT count() FROM `"+pr.Ref+"`.gene"), q("SELECT count() FROM `"+hub.Ref+"`.gene"); got != want {
		t.Errorf("copied genes %s, want %s", got, want)
	}
	if n := q("SELECT count() FROM system.tables WHERE database = '" + pr.Raw + "' AND startsWith(name, '_hub_')"); n != "0" {
		t.Errorf("%s of the hub's own tables copied", n)
	}
	views := q("SELECT count() FROM system.tables WHERE database = '" + pr.Raw + "' AND engine = 'View' AND position(create_table_query, '" + hub.Raw + ".') > 0")
	if views != "0" {
		t.Errorf("%s copied views still read the hub's databases", views)
	}
	if got, want := q("SELECT count() FROM `"+pr.DB+"`.sample_derived"), q("SELECT count() FROM `"+hub.DB+"`.sample_derived"); got != want || got == "0" {
		t.Errorf("copied portal has %s samples, the hub %s", got, want)
	}
	var log []string
	logf := func(format string, args ...any) {
		log = append(log, fmt.Sprintf(format, args...))
		t.Logf(format, args...)
	}
	res, err = studyimport.Run(studyimport.Options{URL: base, DB: pr.DB, RawDB: pr.Raw, RefDB: pr.Ref, Logf: logf},
		[]string{filepath.Join(golden, "studies", "golden_b")})
	if err != nil || res.TransformErr != nil || res.Count(studyimport.Imported) != 1 {
		t.Fatalf("importing on top of the copy: %v %+v", err, res)
	}
	if !strings.Contains(strings.Join(log, "\n"), "rebuilding 1 changed and 0 removed of 3 studies: golden_b") {
		t.Errorf("the import on top of the copy did not rebuild only golden_b:\n%s", strings.Join(log, "\n"))
	}
	if got := q("SELECT arrayStringConcat(groupArray(s), ',') FROM (SELECT DISTINCT cancer_study_identifier AS s FROM `" + pr.DB + "`.sample_derived ORDER BY s)"); got != "golden_a,golden_b,golden_c" {
		t.Errorf("the preview serves %q, want the hub's studies plus golden_b", got)
	}
}

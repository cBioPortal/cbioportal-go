package studyimport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

func TestParseLockState(t *testing.T) {
	st, err := parseLockState("pr-1-abc\t42\t3\n")
	if err != nil || st.holder != "pr-1-abc" || st.age != 42*time.Second || st.rows != 3 {
		t.Errorf("got %+v, %v", st, err)
	}
	for _, bad := range []string{"", "a\tb\tc", "a\t1"} {
		if _, err := parseLockState(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestLockStale(t *testing.T) {
	limit := 10 * time.Minute
	for _, tt := range []struct {
		age  time.Duration
		want bool
	}{{0, false}, {limit, false}, {limit + time.Second, true}} {
		if got := (lockState{age: tt.age}).stale(limit); got != tt.want {
			t.Errorf("age %s: stale=%v, want %v", tt.age, got, tt.want)
		}
	}
}

// fakeExec answers the lock's statements from a script of results.
type fakeExec struct {
	answers []fakeAnswer
	sql     []string
}

type fakeAnswer struct {
	out string
	err error
}

func (f *fakeExec) Execute(sql string) (string, error) {
	f.sql = append(f.sql, sql)
	if len(f.answers) == 0 {
		return "", fmt.Errorf("unexpected statement %q", sql)
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	return a.out, a.err
}

func TestAcquireTakesOverStaleLock(t *testing.T) {
	exists := errors.New("clickhouse HTTP 500: Code: 57. DB::Exception: Table x already exists. (TABLE_ALREADY_EXISTS)")
	f := &fakeExec{answers: []fakeAnswer{
		{err: exists},           // CREATE
		{out: "ghost\t3600\t2"}, // state: no heartbeat for an hour
		{},                      // DROP
		{},                      // CREATE
	}}
	l := &Lock{C: f, DB: "raw", Table: "_import_lock", Owner: "me", Stale: time.Minute, Heartbeat: time.Hour}
	if err := l.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer close(l.stop)
	if !strings.HasPrefix(f.sql[2], "DROP TABLE IF EXISTS `raw`.`_import_lock`") {
		t.Errorf("third statement %q, want the drop of the stale lock", f.sql[2])
	}
}

func TestAcquireGivesUp(t *testing.T) {
	exists := errors.New("TABLE_ALREADY_EXISTS")
	f := &fakeExec{answers: []fakeAnswer{{err: exists}, {out: "other\t5\t1"}}}
	l := &Lock{C: f, DB: "raw", Table: "_import_lock", Owner: "me"}
	err := l.Acquire(context.Background())
	if !errors.Is(err, ErrLockHeld) || !strings.Contains(err.Error(), "other") {
		t.Errorf("got %v, want ErrLockHeld naming the holder", err)
	}
}

func TestAcquireOtherError(t *testing.T) {
	f := &fakeExec{answers: []fakeAnswer{{err: errors.New("ACCESS_DENIED")}}}
	l := &Lock{C: f, DB: "raw", Table: "_import_lock", Owner: "me", Wait: time.Hour}
	if err := l.Acquire(context.Background()); err == nil || errors.Is(err, ErrLockHeld) {
		t.Errorf("got %v, want the create error", err)
	}
}

// liveDB returns a client for a scratch database rwtest_<name>_<pid>,
// dropped before and after the test.
func liveDB(t *testing.T, name string) (string, string) {
	t.Helper()
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	db := fmt.Sprintf("rwtest_%s_%d", name, os.Getpid())
	admin := ch.New(base, "default")
	drop := func() { admin.ExecuteServerwide("DROP DATABASE IF EXISTS `" + db + "` SYNC") }
	drop()
	t.Cleanup(drop)
	if _, err := admin.ExecuteServerwide("CREATE DATABASE `" + db + "`"); err != nil {
		t.Fatal(err)
	}
	return base, db
}

func TestLiveLock(t *testing.T) {
	base, db := liveDB(t, "lock")
	c := ch.New(base, db)
	newLock := func(owner string) *Lock {
		return &Lock{C: c, DB: db, Table: "_import_lock", Owner: owner,
			Stale: time.Minute, Poll: 100 * time.Millisecond, Heartbeat: 300 * time.Millisecond, Logf: t.Logf}
	}
	ctx := context.Background()

	a := newLock("a")
	if err := a.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if err := newLock("b").Acquire(ctx); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("second import: %v, want ErrLockHeld", err)
	}

	// The heartbeat adds rows while a holds the lock.
	time.Sleep(time.Second)
	rows, _ := c.Execute("SELECT count() FROM `" + db + "`._import_lock WHERE owner = 'a'")
	if n := strings.TrimSpace(rows); n == "1" || n == "0" {
		t.Errorf("%s heartbeat rows after 1s", n)
	}

	// b waits for a.
	b := newLock("b")
	b.Wait = 10 * time.Second
	go func() {
		time.Sleep(500 * time.Millisecond)
		if err := a.Release(); err != nil {
			t.Error(err)
		}
	}()
	start := time.Now()
	if err := b.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 400*time.Millisecond {
		t.Errorf("b acquired after %s, before a released", time.Since(start))
	}

	// Another import takes over b's lock (as if b's heartbeat had stopped):
	// b's heartbeat must not claim it back, nor b's release drop it.
	if _, err := c.Execute("INSERT INTO `" + db + "`._import_lock VALUES ('c', now() + 60)"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if h, _ := b.Holder(); h != "c" {
		t.Errorf("holder %q after the take-over, want c", h)
	}
	if err := b.Release(); err != nil {
		t.Fatal(err)
	}
	if h, _ := b.Holder(); h != "c" {
		t.Errorf("b's release dropped c's lock (holder %q)", h)
	}

	// A lock without a heartbeat for longer than Stale is taken over.
	if _, err := c.Execute("TRUNCATE TABLE `" + db + "`._import_lock"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Execute("INSERT INTO `" + db + "`._import_lock VALUES ('ghost', now() - 3600)"); err != nil {
		t.Fatal(err)
	}
	d := newLock("d")
	if err := d.Acquire(ctx); err != nil {
		t.Fatalf("taking over a stale lock: %v", err)
	}
	if h, _ := d.Holder(); h != "d" {
		t.Errorf("holder %q, want d", h)
	}
	if err := d.Release(); err != nil {
		t.Fatal(err)
	}
	if h, err := d.Holder(); h != "" || err != nil {
		t.Errorf("after release: holder %q, %v", h, err)
	}
}

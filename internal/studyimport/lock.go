// Package studyimport imports studies into a portal that is being served:
// one import at a time per database (Lock), each study staged on its own,
// and the portal tables published together by one transform (Run).
package studyimport

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// Execer runs one SQL statement and returns its text answer;
// *clickhouse.Client is one.
type Execer interface {
	Execute(sql string) (string, error)
}

// Lock is an import lock kept in ClickHouse, so it works across machines
// and Jobs: CREATE TABLE of the lock table succeeds for one holder only.
// The holder adds a heartbeat row every Heartbeat; a lock whose newest
// heartbeat is older than Stale (its holder was killed) is taken over.
type Lock struct {
	C         Execer
	DB, Table string // the lock table, e.g. <raw db>._import_lock
	Owner     string // who holds it, for the logs of whoever waits

	Wait      time.Duration // give up after waiting this long (0: try once)
	Stale     time.Duration // take over a lock without a heartbeat this long
	Poll      time.Duration // check a held lock this often
	Heartbeat time.Duration // refresh a held lock this often

	Logf func(format string, args ...any)

	mu   sync.Mutex
	stop chan struct{}
	done chan struct{}
}

// Default lock timings.
const (
	DefaultLockWait      = 2 * time.Hour
	DefaultLockStale     = 10 * time.Minute
	DefaultLockPoll      = 30 * time.Second
	DefaultLockHeartbeat = time.Minute
)

// ErrLockHeld is returned by Acquire when another holder kept the lock for
// the whole wait.
var ErrLockHeld = errors.New("import lock held by another import")

func (l *Lock) table() string { return fmt.Sprintf("`%s`.`%s`", l.DB, l.Table) }

func (l *Lock) logf(format string, args ...any) {
	if l.Logf != nil {
		l.Logf(format, args...)
	}
}

func durationOr(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// lockState is what a waiter sees of a held lock.
type lockState struct {
	holder string
	age    time.Duration // since the newest heartbeat, or the table's creation
	rows   int
}

// stale reports whether a lock in state s may be taken over.
func (s lockState) stale(limit time.Duration) bool { return s.age > limit }

// parseLockState reads "<holder>\t<age seconds>\t<rows>".
func parseLockState(text string) (lockState, error) {
	f := strings.Split(strings.TrimRight(text, "\n"), "\t")
	if len(f) != 3 {
		return lockState{}, fmt.Errorf("unexpected lock state %q", text)
	}
	age, err1 := strconv.ParseInt(f[1], 10, 64)
	rows, err2 := strconv.Atoi(f[2])
	if err := errors.Join(err1, err2); err != nil {
		return lockState{}, fmt.Errorf("unexpected lock state %q: %w", text, err)
	}
	return lockState{holder: f[0], age: time.Duration(age) * time.Second, rows: rows}, nil
}

func isAlreadyExists(err error) bool {
	s := err.Error()
	return strings.Contains(s, "TABLE_ALREADY_EXISTS") || strings.Contains(s, "already exists")
}

// Acquire takes the lock, waiting up to Wait for another holder, and starts
// the heartbeat. The lock's database must exist.
func (l *Lock) Acquire(ctx context.Context) error {
	wait := l.Wait
	stale := durationOr(l.Stale, DefaultLockStale)
	poll := durationOr(l.Poll, DefaultLockPoll)
	deadline := time.Now().Add(wait)
	me := ch.SQLLit(l.Owner)
	for {
		_, err := l.C.Execute(fmt.Sprintf(
			"CREATE TABLE %s ENGINE = MergeTree ORDER BY tuple() AS SELECT '%s' AS owner, now() AS heartbeat", l.table(), me))
		if err == nil {
			l.startHeartbeat()
			l.logf("lock: acquired as %s", l.Owner)
			return nil
		}
		if !isAlreadyExists(err) {
			return fmt.Errorf("lock: cannot create %s: %w", l.table(), err)
		}
		st, err := l.state()
		if err != nil {
			// The table may have been dropped between the two statements.
			l.logf("lock: reading %s: %v", l.table(), err)
		} else if st.stale(stale) {
			l.logf("lock: taking over from %s (no heartbeat for %s)", st.holder, st.age)
			if _, err := l.C.Execute("DROP TABLE IF EXISTS " + l.table()); err != nil {
				return fmt.Errorf("lock: dropping the stale lock: %w", err)
			}
			continue
		}
		if !time.Now().Before(deadline) {
			holder := "?"
			if err == nil {
				holder = st.holder
			}
			return fmt.Errorf("%w: still held by %s after %s", ErrLockHeld, holder, wait)
		}
		if err == nil {
			l.logf("lock: held by %s (heartbeat %s ago), waiting", st.holder, st.age)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(poll, time.Until(deadline)+time.Second)):
		}
	}
}

// state reads the holder and heartbeat age of the lock table. A table
// without rows is being created by CREATE ... AS SELECT; its age is that of
// the table.
func (l *Lock) state() (lockState, error) {
	text, err := l.C.Execute(fmt.Sprintf(
		"SELECT argMax(owner, heartbeat), dateDiff('second', max(heartbeat), now()), count() FROM %s FORMAT TSV", l.table()))
	if err != nil {
		return lockState{}, err
	}
	st, err := parseLockState(text)
	if err != nil || st.rows > 0 {
		return st, err
	}
	st.holder = "(starting)"
	age, err := l.C.Execute(fmt.Sprintf(
		"SELECT dateDiff('second', metadata_modification_time, now()) FROM system.tables WHERE database = '%s' AND name = '%s' FORMAT TSV",
		ch.SQLLit(l.DB), ch.SQLLit(l.Table)))
	if err != nil {
		return st, err
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(age), 10, 64)
	st.age = time.Duration(n) * time.Second
	return st, nil
}

// Holder returns the current holder of the lock ("" when free).
func (l *Lock) Holder() (string, error) {
	text, err := l.C.Execute(fmt.Sprintf(
		"SELECT argMax(owner, heartbeat) FROM %s FORMAT TSV", l.table()))
	if err != nil {
		if strings.Contains(err.Error(), "UNKNOWN_TABLE") || strings.Contains(err.Error(), "doesn't exist") {
			return "", nil
		}
		return "", err
	}
	return strings.TrimRight(text, "\n"), nil
}

// startHeartbeat refreshes the lock while this holder has it. A row is
// added only while the newest row is this holder's, so a lock taken over
// by another import is not claimed back.
func (l *Lock) startHeartbeat() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stop, l.done = make(chan struct{}), make(chan struct{})
	every := durationOr(l.Heartbeat, DefaultLockHeartbeat)
	me := ch.SQLLit(l.Owner)
	stop, done := l.stop, l.done
	go func() {
		defer close(done)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				_, err := l.C.Execute(fmt.Sprintf(
					"INSERT INTO %[1]s SELECT '%[2]s', now() FROM %[1]s HAVING argMax(owner, heartbeat) = '%[2]s'", l.table(), me))
				if err != nil {
					l.logf("lock: heartbeat failed: %v", err)
				}
			}
		}
	}()
}

// Held reports whether this Lock was acquired and not yet released.
func (l *Lock) Held() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stop != nil
}

// Release stops the heartbeat and drops the lock table if this holder
// still has it; a lock taken over meanwhile is left to its new holder.
func (l *Lock) Release() error {
	l.mu.Lock()
	stop, done := l.stop, l.done
	l.stop, l.done = nil, nil
	l.mu.Unlock()
	if stop == nil {
		return nil
	}
	close(stop)
	<-done
	holder, err := l.Holder()
	if err != nil {
		return fmt.Errorf("lock: reading the holder: %w", err)
	}
	if holder != l.Owner {
		l.logf("lock: held by %s now, left as is", orNobody(holder))
		return nil
	}
	if _, err := l.C.Execute("DROP TABLE IF EXISTS " + l.table()); err != nil {
		return fmt.Errorf("lock: releasing: %w", err)
	}
	l.logf("lock: released")
	return nil
}

func orNobody(s string) string {
	if s == "" {
		return "nobody"
	}
	return s
}

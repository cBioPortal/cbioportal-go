package transform

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// A full build can be resumed. Its tables are built under their staged
// names (stagePrefix) and survive a build that stops part of the way, from
// an error or a lost process. resumeTable records the build's inputs (its
// key) and each step it finished: a table, or one batch of studies of a
// table filled in batches. A later full build with the same key keeps
// those steps' tables and does only the rest; a batch that was being
// filled is emptied (its studies' partitions dropped) and filled again.
// Views and snapshotted views are cheap and are built again. Once
// publishing starts, the staged names may hold published tables, so a
// build that stopped while publishing is not resumed.
const resumeTable = stagePrefix + "_transform_resume"

// publishingStep marks that publishing started.
const publishingStep = "publishing"

type resumeState struct {
	targetDB, key string
	resumed       bool
	mu            sync.Mutex
	done          map[string]bool
}

// resumeKey identifies a full build's inputs: what the build fingerprint
// covers, every study with its import time, and the batch size.
func resumeKey(b *studyBuild) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%d\nbalanced\n", b.fingerprint, tuning.BatchStudies)
	for _, s := range b.order {
		fmt.Fprintf(h, "%s\t%s\n", s.StudyID, s.ImportTime)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// startStaging prepares the staged objects of a build: for a full build
// whose inputs match the previous unfinished one, it keeps that build's
// staged tables and finished steps; otherwise it drops every staged
// object, and a full build starts recording its steps.
func startStaging(c *client, targetDB string, b *studyBuild) error {
	c.resume = nil
	key := ""
	if !b.partial {
		key = resumeKey(b)
		type step struct {
			Key  string `json:"key"`
			Step string `json:"step"`
		}
		exists, err := stagedExists(c, targetDB, resumeTable)
		if err != nil {
			return err
		}
		if exists {
			rows, err := ch.QueryP[step](c.Client, fmt.Sprintf("SELECT key, step FROM `%s`.`%s`", targetDB, resumeTable), nil)
			if err != nil {
				return err
			}
			done := map[string]bool{}
			match := len(rows) > 0
			for _, r := range rows {
				match = match && r.Key == key
				done[r.Step] = true
			}
			if match && !done[publishingStep] {
				logf("transform: resuming the previous build, which finished %d steps", len(done))
				stopEarlierInserts(c, targetDB)
				c.resume = &resumeState{targetDB: targetDB, key: key, resumed: true, done: done}
				return nil
			}
		}
	}
	if err := dropStaged(c, targetDB); err != nil {
		return err
	}
	if b.partial {
		return nil
	}
	if _, err := c.Client.Execute(fmt.Sprintf(
		"CREATE TABLE `%s`.`%s` (key String, step String) ENGINE = MergeTree ORDER BY tuple()", targetDB, resumeTable)); err != nil {
		return err
	}
	c.resume = &resumeState{targetDB: targetDB, key: key, done: map[string]bool{}}
	return c.markDone("started")
}

// stopEarlierInserts stops the inserts into staged tables of targetDB that
// are still running: when a build's process is lost, its queries go on in
// ClickHouse, and an insert that ended after the resumed build emptied its
// batch would add its rows a second time. The build holds the import lock,
// so no other build fills these tables. A user may stop only its own
// queries, and only on the replica it is connected to, so it connects to
// each replica: as many as the service has, or up to 3 when a user may
// not count them.
func stopEarlierInserts(c *client, targetDB string) {
	kill := fmt.Sprintf("KILL QUERY WHERE startsWith(query, 'INSERT INTO `%s`.`%s') SYNC", ch.SQLLit(targetDB), stagePrefix)
	lanes := pinReplicas(c.Client, max(3, replicaCount(c.Client)))
	defer closeLanes(lanes)
	clients := []*ch.Client{c.Client}
	if len(lanes) > 0 {
		clients = nil
		for _, l := range lanes {
			clients = append(clients, l.cl)
		}
	}
	for _, cl := range clients {
		if _, err := cl.ExecuteServerwide(kill); err != nil {
			logf("transform: stopping the previous build's inserts: %v", err)
		}
	}
}

// stepDone reports whether a resumed build finished step before.
func (c *client) stepDone(step string) bool {
	if c.resume == nil {
		return false
	}
	c.resume.mu.Lock()
	defer c.resume.mu.Unlock()
	return c.resume.done[step]
}

// markDone records a finished step of a full build. A step recorded twice
// is the same as once, so a request that did not reach ClickHouse, such as
// a new connection to a busy service that timed out, is sent again.
func (c *client) markDone(step string) error {
	if c.resume == nil {
		return nil
	}
	insert := fmt.Sprintf("INSERT INTO `%s`.`%s` (key, step) VALUES ('%s', '%s')",
		c.resume.targetDB, resumeTable, c.resume.key, ch.SQLLit(step))
	for wait := markRetryWait; ; wait *= 2 {
		_, err := c.Client.Execute(insert)
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "clickhouse transport") || wait > 8*markRetryWait {
			return fmt.Errorf("recording the build's progress: %w", err)
		}
		time.Sleep(wait)
	}
	c.resume.mu.Lock()
	c.resume.done[step] = true
	c.resume.mu.Unlock()
	if stepHook != nil {
		return stepHook(step)
	}
	return nil
}

// markRetryWait is how long markDone waits before it sends a step again,
// twice as long each time, four times in all.
var markRetryWait = 2 * time.Second

// stepHook, when set (by tests), runs after each recorded step; an error
// stops the build there.
var stepHook func(step string) error

// stagedExists reports whether db has the object named name, or its
// staged name.
func stagedExists(c *client, db, name string) (bool, error) {
	if !strings.HasPrefix(name, stagePrefix) {
		name = stagePrefix + name
	}
	text, err := c.Client.Execute(fmt.Sprintf(
		"SELECT count() FROM system.tables WHERE database = '%s' AND name = '%s' FORMAT TabSeparated", ch.SQLLit(db), ch.SQLLit(name)))
	return strings.TrimSpace(text) == "1", err
}

// tableDone reports whether a resumed build already built table name.
func (c *client) tableDone(targetDB, name string) bool {
	if !c.stepDone("table:" + name) {
		return false
	}
	ok, err := stagedExists(c, targetDB, name)
	return err == nil && ok
}

package transform

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

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
	done          map[string]bool
}

// resumeKey identifies a full build's inputs: what the build fingerprint
// covers, every study with its import time, and the batch size.
func resumeKey(b *studyBuild) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%d\n", b.fingerprint, batchStudies)
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

// stepDone reports whether a resumed build finished step before.
func (c *client) stepDone(step string) bool {
	return c.resume != nil && c.resume.done[step]
}

// markDone records a finished step of a full build.
func (c *client) markDone(step string) error {
	if c.resume == nil {
		return nil
	}
	if _, err := c.Client.Execute(fmt.Sprintf("INSERT INTO `%s`.`%s` (key, step) VALUES ('%s', '%s')",
		c.resume.targetDB, resumeTable, c.resume.key, ch.SQLLit(step))); err != nil {
		return fmt.Errorf("recording the build's progress: %w", err)
	}
	c.resume.done[step] = true
	if stepHook != nil {
		return stepHook(step)
	}
	return nil
}

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

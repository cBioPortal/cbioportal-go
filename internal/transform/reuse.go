package transform

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// A full build keeps a published table instead of building it again when
// the table would come out the same: its definition, everything it reads
// in the portal database and the staged studies and reference data are
// those of the build that published it. A new release of the transform
// that changes only some tables then rebuilds only those and the tables
// that read them. The kept table gets the published one's parts, which
// copies no data.
//
// Each table and view of a build gets a hash of its definition, the hashes
// of the portal objects it reads, and the build's data key (the staged
// studies, their ranks and the reference data). A build records the hashes
// in hashTable; the next full build keeps a table whose hash is recorded
// there. An object that reads a portal object without a hash gets none, so
// it and the objects that read it are always built.
//
// A study build numbers the changed studies' rows after the highest id of
// the published table, where a full build numbers every study's from 1,
// in the same order. The ids' starting point is left out of the hash: a
// kept table holds the ids of the build that published it, and every table
// that reads them is either kept too or built from them.

// hashTable records the hash of each object of the published build.
const hashTable = "_table_hashes"

// reuseFormat is part of every hash: raising it builds every table again,
// as when how fill creates a table changes.
const reuseFormat = 1

// stateTables are portal tables written outside fill whose contents the
// data key covers.
var stateTables = map[string]bool{studyRanksTable: true, buildTable: true, hashTable: true}

// hashSalt, when set (by tests), adds to an object's definition, as a
// change to its SQL would.
var hashSalt func(name string) string

// idStart matches the starting point of the ids nextIDs numbers rows with.
var idStart = regexp.MustCompile(`toUInt64\(\d+\) \+ toUInt64\(row_number\(\) OVER `)

// objectHashes holds the hashes of a build's objects and those of the
// published build.
type objectHashes struct {
	mu       sync.Mutex
	dataKey  string
	current  map[string]string
	previous map[string]string
	kept     []string
}

// objectHash returns the hash of portal object name of targetDB defined by
// parts and records it; "" when an object it reads has no hash.
func (c *client) objectHash(targetDB, name string, parts ...string) string {
	h := c.hashes
	if h == nil || c.stage == nil {
		return ""
	}
	text := idStart.ReplaceAllString(strings.Join(parts, "\n"), "toUInt64(0) + toUInt64(row_number() OVER ")
	if hashSalt != nil {
		text += hashSalt(name)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var deps []string
	for _, m := range c.stage.ref.FindAllStringSubmatch(text, -1) {
		dep := strings.TrimPrefix(m[1]+m[2], stagePrefix)
		if dep == name || stateTables[dep] || slices.Contains(deps, dep) {
			continue
		}
		deps = append(deps, dep)
	}
	slices.Sort(deps)
	sum := sha256.New()
	fmt.Fprintf(sum, "%d\n%s\n%s\n%s\n", reuseFormat, h.dataKey, name, text)
	for _, dep := range deps {
		dh, ok := h.current[dep]
		if !ok || dh == "" {
			h.current[name] = ""
			return ""
		}
		fmt.Fprintf(sum, "%s %s\n", dep, dh)
	}
	out := hex.EncodeToString(sum.Sum(nil))
	h.current[name] = out
	return out
}

// keepTable copies the parts of the published table holding name's rows
// into name's staged table when the published build recorded hash for it. A snapshot
// (snapshotPrefix) is published under the name of the view it replaces.
func (c *client) keepTable(targetDB, name, hash string) (bool, error) {
	h := c.hashes
	if hash == "" || h == nil || c.part != nil || h.previous[name] != hash {
		return false, nil
	}
	published := strings.TrimPrefix(name, snapshotPrefix)
	engine, err := c.Client.Execute(fmt.Sprintf(
		"SELECT engine FROM system.tables WHERE database = '%s' AND name = '%s' FORMAT TabSeparated",
		ch.SQLLit(targetDB), ch.SQLLit(published)))
	if err != nil || !strings.HasSuffix(strings.TrimSpace(engine), "MergeTree") {
		return false, err
	}
	staged := c.stage.name(name)
	for _, q := range []string{
		fmt.Sprintf("DROP TABLE IF EXISTS `%s`.`%s`", targetDB, staged),
		fmt.Sprintf("CREATE TABLE `%[1]s`.`%[2]s` AS `%[1]s`.`%[3]s`", targetDB, staged, published),
		fmt.Sprintf("ALTER TABLE `%[1]s`.`%[2]s` ATTACH PARTITION ALL FROM `%[1]s`.`%[3]s`", targetDB, staged, published),
	} {
		if _, err := c.Client.Execute(q); err != nil {
			return false, fmt.Errorf("keeping table %s: %w", name, err)
		}
	}
	h.mu.Lock()
	h.kept = append(h.kept, published)
	h.mu.Unlock()
	return true, nil
}

// loadHashes reads the hashes the published build recorded.
func loadHashes(c *client, targetDB, dataKey string) error {
	c.hashes = &objectHashes{dataKey: dataKey, current: map[string]string{}, previous: map[string]string{}}
	ok, err := c.Client.Execute(fmt.Sprintf(
		"SELECT count() FROM system.tables WHERE database = '%s' AND name = '%s' FORMAT TabSeparated",
		ch.SQLLit(targetDB), hashTable))
	if err != nil || strings.TrimSpace(ok) == "0" {
		return err
	}
	type row struct {
		Name string `json:"name"`
		Hash string `json:"hash"`
	}
	rows, err := ch.QueryP[row](c.Client, fmt.Sprintf("SELECT name, hash FROM `%s`.`%s`", targetDB, hashTable), nil)
	if err != nil {
		return err
	}
	for _, r := range rows {
		c.hashes.previous[r.Name] = r.Hash
	}
	return nil
}

// writeHashes records the build's hashes for the next build.
func writeHashes(c *client, targetDB string) error {
	h := c.hashes
	if h == nil {
		return nil
	}
	h.mu.Lock()
	names := make([]string, 0, len(h.current))
	for name := range h.current {
		names = append(names, name)
	}
	slices.Sort(names)
	values := make([]string, 0, len(names))
	for _, name := range names {
		if hash := h.current[name]; hash != "" {
			values = append(values, fmt.Sprintf("('%s', '%s')", ch.SQLLit(name), hash))
		}
	}
	kept := slices.Sorted(slices.Values(h.kept))
	h.mu.Unlock()
	stmts := []string{fmt.Sprintf("CREATE OR REPLACE TABLE `%s`.`%s` (name String, hash String) ENGINE = MergeTree ORDER BY name",
		targetDB, hashTable)}
	if len(values) > 0 {
		stmts = append(stmts, fmt.Sprintf("INSERT INTO `%s`.`%s` VALUES %s", targetDB, hashTable, strings.Join(values, ", ")))
	}
	for _, q := range stmts {
		if _, err := c.Execute(q); err != nil {
			return fmt.Errorf("recording the table hashes: %w", err)
		}
	}
	if len(kept) > 0 {
		logf("transform: kept %d unchanged tables: %s", len(kept), strings.Join(kept, ", "))
	}
	return nil
}

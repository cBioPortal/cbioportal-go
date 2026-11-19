package transform

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"golang.org/x/sync/errgroup"
)

// stagePrefix names the objects a transform builds before it publishes
// them under their own names.
const stagePrefix = "__next_"

// staging redirects the transform's writes to the portal database into
// stagePrefix-named objects, so the objects the API reads stay untouched
// until every object of the run is built. Each view definition is kept as
// written (with the published names) to be recreated at publish time.
type staging struct {
	db        string
	ref       *regexp.Regexp
	createRef *regexp.Regexp
	views     []string          // published view names, in creation order
	viewSQL   map[string]string // the last definition of each view
	// kept are the published tables the build keeps as they are
	// (reuse.go): they have no staged object, and statements read them
	// under their own names.
	mu   sync.Mutex
	kept map[string]bool
}

func newStaging(db string) *staging {
	d := regexp.QuoteMeta(db)
	dbRef := "(?:`" + d + "`|\\b" + d + "\\b)\\."
	return &staging{
		db:        db,
		ref:       regexp.MustCompile(dbRef + "(?:`([^`]+)`|([A-Za-z_][A-Za-z0-9_]*))"),
		createRef: regexp.MustCompile("^\\s*CREATE\\s+(?:OR\\s+REPLACE\\s+)?VIEW\\s+" + dbRef + "(?:`([^`]+)`|([A-Za-z_][A-Za-z0-9_]*))"),
		viewSQL:   map[string]string{},
		kept:      map[string]bool{},
	}
}

// keep makes statements read the published table name from now on.
func (s *staging) keep(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kept[name] = true
}

// isKept reports whether the build keeps the published table name.
func (s *staging) isKept(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kept[name]
}

// rewrite points every reference to an object of the portal database at its
// staged name, but a kept table's at the published table.
func (s *staging) rewrite(sql string) string {
	return s.ref.ReplaceAllStringFunc(sql, func(m string) string {
		sub := s.ref.FindStringSubmatch(m)
		name := sub[1] + sub[2]
		if s.isKept(name) {
			return fmt.Sprintf("`%s`.`%s`", s.db, name)
		}
		return fmt.Sprintf("`%s`.`%s`", s.db, s.name(name))
	})
}

// name is the staged name of a portal object.
func (s *staging) name(n string) string {
	if strings.HasPrefix(n, stagePrefix) {
		return n
	}
	return stagePrefix + n
}

// record keeps the definition of a view the statement creates.
func (s *staging) record(sql string) {
	sub := s.createRef.FindStringSubmatch(sql)
	if sub == nil {
		return
	}
	name := sub[1] + sub[2]
	if _, ok := s.viewSQL[name]; ok {
		for i, v := range s.views {
			if v == name {
				s.views = append(s.views[:i], s.views[i+1:]...)
				break
			}
		}
	}
	s.views = append(s.views, name)
	s.viewSQL[name] = sql
}

type dbObject struct {
	name, engine string
}

// stagedObjects lists the objects of db that carry the staging prefix.
func stagedObjects(c *client, db string) ([]dbObject, error) {
	text, err := c.Client.Execute(fmt.Sprintf(
		"SELECT name, engine FROM system.tables WHERE database = '%s' AND startsWith(name, '%s') ORDER BY name FORMAT TabSeparated",
		db, stagePrefix))
	if err != nil {
		return nil, err
	}
	var out []dbObject
	for line := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
		name, engine, ok := strings.Cut(line, "\t")
		if ok {
			out = append(out, dbObject{name, engine})
		}
	}
	return out, nil
}

// dropStagedStatements is how many DROP statements share the staged
// objects. A replicated service drops the objects of one statement one
// after another, each waiting for every replica, so a few statements at
// once finish sooner than one.
const dropStagedStatements = 8

// dropStaged drops every staged object of db: those of a run that failed,
// and after publishing, the objects the run replaced.
func dropStaged(c *client, db string) error {
	objs, err := stagedObjects(c, db)
	if err != nil || len(objs) == 0 {
		return err
	}
	groups := make([][]string, min(dropStagedStatements, len(objs)))
	for i, o := range objs {
		groups[i%len(groups)] = append(groups[i%len(groups)], fmt.Sprintf("`%s`.`%s`", db, o.name))
	}
	var g errgroup.Group
	for _, names := range groups {
		g.Go(func() error {
			_, err := c.Client.Execute("DROP TABLE IF EXISTS " + strings.Join(names, ", "))
			return err
		})
	}
	return g.Wait()
}

// publish moves the staged objects under their own names. In a study
// build, the tables partitioned by study get the changed studies'
// partitions (publishStudies). Each other table is swapped in with
// EXCHANGE TABLES (the replaced table stays behind under the
// staged name until dropStaged), then each view is recreated with its
// definition over the published names. Every object the API reads exists
// throughout; only an object that turns from a view into a table or back is
// briefly absent.
func publish(c *client, s *staging) error {
	if c.part != nil {
		if err := publishStudies(c, c.part); err != nil {
			return err
		}
	}
	objs, err := stagedObjects(c, s.db)
	if err != nil {
		return err
	}
	engines, queries := map[string]string{}, map[string]string{}
	type object struct {
		Name     string `json:"name"`
		Engine   string `json:"engine"`
		AsSelect string `json:"as_select"`
	}
	rows, err := ch.QueryP[object](c.Client, "SELECT name, engine, as_select FROM system.tables WHERE database = {db:String}",
		ch.Params{"db": s.db})
	if err != nil {
		return err
	}
	for _, r := range rows {
		if strings.HasPrefix(r.Name, stagePrefix) {
			queries[r.Name] = r.AsSelect
		} else {
			engines[r.Name] = r.Engine
			queries[r.Name] = r.AsSelect
		}
	}
	exec := func(q string) error {
		if _, err := c.Client.Execute(q); err != nil {
			return fmt.Errorf("publishing: %w", err)
		}
		return nil
	}
	tables := map[string]bool{}
	s.mu.Lock()
	for name := range s.kept {
		tables[name] = true // stays as published
	}
	s.mu.Unlock()
	for _, o := range objs {
		if o.engine == "View" || o.name == resumeTable {
			continue
		}
		final := strings.TrimPrefix(o.name, stagePrefix)
		tables[final] = true
		if c.part != nil && studyColumns[final] != "" {
			continue // its changed studies' partitions are published
		}
		switch engines[final] {
		case "":
			err = exec(fmt.Sprintf("RENAME TABLE `%[1]s`.`%[2]s` TO `%[1]s`.`%[3]s`", s.db, o.name, final))
		case "View":
			if err = exec(fmt.Sprintf("DROP VIEW `%s`.`%s`", s.db, final)); err == nil {
				err = exec(fmt.Sprintf("RENAME TABLE `%[1]s`.`%[2]s` TO `%[1]s`.`%[3]s`", s.db, o.name, final))
			}
		default:
			err = exec(fmt.Sprintf("EXCHANGE TABLES `%[1]s`.`%[2]s` AND `%[1]s`.`%[3]s`", s.db, o.name, final))
		}
		if err != nil {
			return err
		}
	}
	for _, v := range s.views {
		if tables[v] {
			continue // snapshotted into a table after the view was created
		}
		// A published view with the staged one's query, over the published
		// names, is left as it is.
		if engines[v] == "View" && queries[v] != "" && strings.ReplaceAll(queries[stagePrefix+v], stagePrefix, "") == queries[v] {
			continue
		}
		if e := engines[v]; e != "" && e != "View" {
			if err := exec(fmt.Sprintf("DROP TABLE `%s`.`%s`", s.db, v)); err != nil {
				return err
			}
		}
		// Raw staging stand-ins are substituted again; names are not staged.
		if _, err := c.executeUnstaged(s.viewSQL[v]); err != nil {
			return fmt.Errorf("publishing view %s: %w", v, err)
		}
	}
	return nil
}

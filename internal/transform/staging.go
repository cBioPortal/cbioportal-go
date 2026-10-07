package transform

import (
	"fmt"
	"regexp"
	"strings"
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
}

func newStaging(db string) *staging {
	d := regexp.QuoteMeta(db)
	dbRef := "(?:`" + d + "`|\\b" + d + "\\b)\\."
	return &staging{
		db:        db,
		ref:       regexp.MustCompile(dbRef + "(?:`([^`]+)`|([A-Za-z_][A-Za-z0-9_]*))"),
		createRef: regexp.MustCompile("^\\s*CREATE\\s+(?:OR\\s+REPLACE\\s+)?VIEW\\s+" + dbRef + "(?:`([^`]+)`|([A-Za-z_][A-Za-z0-9_]*))"),
		viewSQL:   map[string]string{},
	}
}

// rewrite points every reference to an object of the portal database at its
// staged name.
func (s *staging) rewrite(sql string) string {
	return s.ref.ReplaceAllStringFunc(sql, func(m string) string {
		sub := s.ref.FindStringSubmatch(m)
		name := sub[1] + sub[2]
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

// dropStaged drops every staged object of db: those of a run that failed,
// and after publishing, the objects the run replaced. Views go first, as
// they may read the tables.
func dropStaged(c *client, db string) error {
	objs, err := stagedObjects(c, db)
	if err != nil {
		return err
	}
	for _, views := range []bool{true, false} {
		for _, o := range objs {
			if (o.engine == "View") != views {
				continue
			}
			kind := "TABLE"
			if views {
				kind = "VIEW"
			}
			if _, err := c.Client.Execute(fmt.Sprintf("DROP %s IF EXISTS `%s`.`%s`", kind, db, o.name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// publish moves the staged objects under their own names. Each table is
// swapped in with EXCHANGE TABLES (the replaced table stays behind under the
// staged name until dropStaged), then each view is recreated with its
// definition over the published names. Every object the API reads exists
// throughout; only an object that turns from a view into a table or back is
// briefly absent.
func publish(c *client, s *staging) error {
	objs, err := stagedObjects(c, s.db)
	if err != nil {
		return err
	}
	engines := map[string]string{}
	text, err := c.Client.Execute(fmt.Sprintf(
		"SELECT name, engine FROM system.tables WHERE database = '%s' AND NOT startsWith(name, '%s') FORMAT TabSeparated",
		s.db, stagePrefix))
	if err != nil {
		return err
	}
	for line := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
		if name, engine, ok := strings.Cut(line, "\t"); ok {
			engines[name] = engine
		}
	}
	exec := func(q string) error {
		if _, err := c.Client.Execute(q); err != nil {
			return fmt.Errorf("publishing: %w", err)
		}
		return nil
	}
	tables := map[string]bool{}
	for _, o := range objs {
		if o.engine == "View" {
			continue
		}
		final := strings.TrimPrefix(o.name, stagePrefix)
		tables[final] = true
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

package reference

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// GenesetStats counts what LoadGenesets loaded.
type GenesetStats struct {
	Genesets, Genes, SkippedGenes, Nodes, Leaves int
}

// LoadGenesets replaces the gene-set reference in c's database the way
// cbioportal-core's importGenesetData.pl --new-version and
// importGenesetHierarchy.pl do:
//
//   - gmt: one gene set per line, `<id>\t<ref link>\t<entrez id>...`; ids are
//     numbered from 1 in file order, name and description default to the id,
//     and Entrez ids absent from the gene table are skipped;
//   - supp (optional): `<id>\t<name>\t<description>` per line, updating gene
//     sets already loaded (unknown ids are skipped);
//   - version: the collection version stored in info.geneset_version;
//   - hierarchy (optional): the YAML tree of categories (see parseGenesetTree),
//     its nodes numbered from 1 depth-first in file order, top-level nodes
//     with parent 0. A gene set it names that is not loaded is an error.
func LoadGenesets(c *ch.Client, gmtPath, suppPath, version, hierarchyPath string) (GenesetStats, error) {
	var st GenesetStats
	type geneset struct {
		externalID, name, description, refLink string
		genes                                  []int64
	}
	var sets []*geneset
	byID := map[string]int{}
	err := eachLine(gmtPath, func(line string) error {
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			return fmt.Errorf("gmt line %q: want <id>\\t<ref link>\\t<genes>", line)
		}
		g := &geneset{externalID: parts[0], name: parts[0], description: parts[0], refLink: parts[1]}
		seen := map[int64]bool{}
		for _, p := range parts[2:] {
			id, err := strconv.ParseInt(p, 10, 64)
			if err != nil {
				return fmt.Errorf("gene set %s: Entrez id %q: %w", g.externalID, p, err)
			}
			if !seen[id] {
				seen[id] = true
				g.genes = append(g.genes, id)
			}
		}
		byID[g.externalID] = len(sets)
		sets = append(sets, g)
		return nil
	})
	if err != nil {
		return st, err
	}
	if suppPath != "" {
		err := eachLine(suppPath, func(line string) error {
			parts := strings.Split(line, "\t")
			i, ok := byID[parts[0]]
			if !ok || len(parts) < 3 {
				return nil
			}
			sets[i].name, sets[i].description = parts[1], parts[2]
			return nil
		})
		if err != nil {
			return st, err
		}
	}
	var tree *yamlNode
	if hierarchyPath != "" {
		raw, err := os.ReadFile(hierarchyPath)
		if err != nil {
			return st, err
		}
		if tree, err = parseGenesetTree(string(raw)); err != nil {
			return st, fmt.Errorf("%s: %w", hierarchyPath, err)
		}
	}

	ddls := []string{
		"CREATE TABLE IF NOT EXISTS `%s`.geneset (id Int64, external_id String, name String, description String, ref_link String) ENGINE = MergeTree ORDER BY id",
		"CREATE TABLE IF NOT EXISTS `%s`.geneset_gene (geneset_id Int64, entrez_gene_id Int64) ENGINE = MergeTree ORDER BY (geneset_id, entrez_gene_id)",
		"CREATE TABLE IF NOT EXISTS `%s`.geneset_hierarchy_node (node_id Int64, node_name String, parent_id Int64) ENGINE = MergeTree ORDER BY node_id",
		"CREATE TABLE IF NOT EXISTS `%s`.geneset_hierarchy_leaf (node_id Int64, geneset_id Int64) ENGINE = MergeTree ORDER BY (node_id, geneset_id)",
		"CREATE TABLE IF NOT EXISTS `%s`.geneset_info (geneset_version String) ENGINE = MergeTree ORDER BY tuple()",
	}
	for _, d := range ddls {
		if _, err := c.Execute(fmt.Sprintf(d, c.Database())); err != nil {
			return st, err
		}
	}
	for _, t := range []string{"geneset", "geneset_gene", "geneset_hierarchy_node", "geneset_hierarchy_leaf", "geneset_info"} {
		if _, err := c.Execute(fmt.Sprintf("TRUNCATE TABLE `%s`.%s", c.Database(), t)); err != nil {
			return st, err
		}
	}

	known, err := knownGenes(c)
	if err != nil {
		return st, err
	}
	var gs, genes strings.Builder
	for i, g := range sets {
		fmt.Fprintf(&gs, "(%d,%s,%s,%s,%s)", i+1, sqlString(g.externalID), sqlString(g.name), sqlString(g.description), sqlString(g.refLink))
		for _, e := range g.genes {
			if !known[e] {
				st.SkippedGenes++
				continue
			}
			fmt.Fprintf(&genes, "(%d,%d)", i+1, e)
			st.Genes++
		}
	}
	st.Genesets = len(sets)
	if err := insertTuples(c, "geneset", gs.String()); err != nil {
		return st, err
	}
	if err := insertTuples(c, "geneset_gene", genes.String()); err != nil {
		return st, err
	}
	if err := insertTuples(c, "geneset_info", "("+sqlString(version)+")"); err != nil {
		return st, err
	}

	if tree != nil {
		var nodes, leaves strings.Builder
		next := 1
		var walk func(n *yamlNode, parent int) error
		walk = func(n *yamlNode, parent int) error {
			for _, e := range n.entries {
				if e.key == "Gene sets" {
					for _, name := range e.list {
						i, ok := byID[name]
						if !ok {
							return fmt.Errorf("gene set %q of the hierarchy is not in the gene set file", name)
						}
						fmt.Fprintf(&leaves, "(%d,%d)", parent, i+1)
						st.Leaves++
					}
					continue
				}
				id := next
				next++
				fmt.Fprintf(&nodes, "(%d,%s,%d)", id, sqlString(e.key), parent)
				st.Nodes++
				if e.child != nil {
					if err := walk(e.child, id); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := walk(tree, 0); err != nil {
			return st, err
		}
		if err := insertTuples(c, "geneset_hierarchy_node", nodes.String()); err != nil {
			return st, err
		}
		if err := insertTuples(c, "geneset_hierarchy_leaf", leaves.String()); err != nil {
			return st, err
		}
	}
	return st, nil
}

func eachLine(path string, fn func(string) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	return sc.Err()
}

func knownGenes(c *ch.Client) (map[int64]bool, error) {
	out, err := c.Execute(fmt.Sprintf("SELECT entrez_gene_id FROM `%s`.gene FORMAT TabSeparated", c.Database()))
	if err != nil {
		return nil, err
	}
	known := map[int64]bool{}
	for _, l := range strings.Split(out, "\n") {
		if id, err := strconv.ParseInt(strings.TrimSpace(l), 10, 64); err == nil {
			known[id] = true
		}
	}
	return known, nil
}

func insertTuples(c *ch.Client, table, values string) error {
	if values == "" {
		return nil
	}
	return c.InsertStream(fmt.Sprintf("INSERT INTO `%s`.`%s` FORMAT Values", c.Database(), table), strings.NewReader(values))
}

func sqlString(s string) string { return "'" + ch.SQLLit(s) + "'" }

// yamlNode is a YAML mapping in key order; an entry holds a nested mapping
// or, for a key followed by `- item` lines, a list of scalars.
type yamlNode struct {
	entries []yamlEntry
}

type yamlEntry struct {
	key   string
	child *yamlNode
	list  []string
}

// parseGenesetTree parses the YAML subset of gene set hierarchy files:
// block mappings of `key:` lines nested by indentation, whose leaves are
// block sequences of plain or quoted scalars (`- GENESET_ID`). Comments
// (`#` at the start of a line or after a space) and blank lines are ignored;
// flow collections, anchors and multi-line scalars are not supported.
func parseGenesetTree(src string) (*yamlNode, error) {
	type line struct {
		indent int
		text   string
		no     int
	}
	var lines []line
	for i, raw := range strings.Split(src, "\n") {
		raw = strings.TrimRight(raw, "\r")
		if j := strings.Index(raw, " #"); j >= 0 {
			raw = raw[:j]
		}
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "#") || t == "---" {
			continue
		}
		if strings.Contains(raw[:len(raw)-len(strings.TrimLeft(raw, " \t"))], "\t") {
			return nil, fmt.Errorf("line %d: tabs are not allowed in indentation", i+1)
		}
		lines = append(lines, line{len(raw) - len(strings.TrimLeft(raw, " ")), t, i + 1})
	}
	pos := 0
	var parseMap func(indent int) (*yamlNode, error)
	parseMap = func(indent int) (*yamlNode, error) {
		n := &yamlNode{}
		for pos < len(lines) && lines[pos].indent == indent {
			l := lines[pos]
			if strings.HasPrefix(l.text, "- ") || l.text == "-" {
				return nil, fmt.Errorf("line %d: list item where a key was expected", l.no)
			}
			key, rest, ok := strings.Cut(l.text, ":")
			if !ok || strings.TrimSpace(rest) != "" {
				return nil, fmt.Errorf("line %d: want `key:`", l.no)
			}
			e := yamlEntry{key: unquote(strings.TrimSpace(key))}
			pos++
			if pos < len(lines) && lines[pos].indent >= indent && strings.HasPrefix(lines[pos].text, "-") {
				itemIndent := lines[pos].indent
				for pos < len(lines) && lines[pos].indent == itemIndent && strings.HasPrefix(lines[pos].text, "-") {
					e.list = append(e.list, unquote(strings.TrimSpace(strings.TrimPrefix(lines[pos].text, "-"))))
					pos++
				}
			} else if pos < len(lines) && lines[pos].indent > indent {
				child, err := parseMap(lines[pos].indent)
				if err != nil {
					return nil, err
				}
				e.child = child
			}
			n.entries = append(n.entries, e)
		}
		if pos < len(lines) && lines[pos].indent > indent {
			return nil, fmt.Errorf("line %d: unexpected indentation", lines[pos].no)
		}
		return n, nil
	}
	if len(lines) == 0 {
		return &yamlNode{}, nil
	}
	root, err := parseMap(lines[0].indent)
	if err != nil {
		return nil, err
	}
	if pos < len(lines) {
		return nil, fmt.Errorf("line %d: unexpected indentation", lines[pos].no)
	}
	return root, nil
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/cli"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/ingest"
	"github.com/cbioportal/cbioportal-go/internal/parser"
	"github.com/cbioportal/cbioportal-go/internal/reference"
	"github.com/cbioportal/cbioportal-go/internal/transform"
)

// command is one subcommand. setup registers its flags on fs and returns the
// function that runs it once fs is parsed.
type command struct {
	name     string
	args     string // positional arguments in the usage line
	minArgs  int
	maxArgs  int // -1: no limit
	short    string
	long     string
	examples []string
	setup    func(fs *flag.FlagSet) func(args []string, w io.Writer) error
}

var commands = []command{
	{
		name: "plan", args: "<study-dir>", minArgs: 1, maxArgs: 1,
		short: "list a study's data files and the staging tables they load into",
		long: `Plan reads the meta files of a study directory and lists each data file
with its alteration type, datatype, layout and staging table, without
connecting to ClickHouse.`,
		examples: []string{"plan datahub/public/acbc_mskcc_2015", "plan datahub/public/acbc_mskcc_2015 --json"},
		setup:    setupPlan,
	},
	{
		name: "ingest", args: "<study-dir>", minArgs: 1, maxArgs: 1,
		short: "stage a study's data files in the raw database",
		long: `Ingest streams every data file of a study directory, as is, into staging
tables of the raw database, replacing that study's earlier rows. Run
transform afterwards to make the study queryable.`,
		examples: []string{
			"ingest datahub/public/acbc_mskcc_2015 --db cbioportal_raw",
			"ingest --url https://<host>:8443 --db cbioportal_raw datahub/public/msk_impact_2017",
		},
		setup: setupIngest,
	},
	{
		name: "remove-study", args: "<study-id>...", minArgs: 1, maxArgs: -1,
		short: "delete studies' staged rows from the raw database",
		long: `Remove-study deletes every staged row of each study from the raw
database. Run transform afterwards to remove the studies from the portal
database. --dry-run lists the tables and rows it would delete.`,
		examples: []string{
			"remove-study acbc_mskcc_2015 --db cbioportal_raw --dry-run",
			"remove-study acbc_mskcc_2015 --db cbioportal_raw",
		},
		setup: setupRemoveStudy,
	},
	{
		name: "ingest-case-lists", args: "<study-dir>...", minArgs: 1, maxArgs: -1,
		short: "restage only the case lists of one or more studies",
		long: `Ingest-case-lists restages only the case_lists/ directory of each study,
e.g. for raw databases whose data files cannot be re-read.`,
		examples: []string{"ingest-case-lists datahub/public/study_a datahub/public/study_b --db cbioportal_raw"},
		setup:    setupIngestCaseLists,
	},
	{
		name: "load-reference", args: "<seed.sql.gz>", minArgs: 1, maxArgs: 1,
		short: "load genes, gene aliases and cancer types from a seed dump",
		long: `Load-reference loads the genes, gene aliases and cancer types of a
cBioPortal seed dump (datahub seedDB/seed-*.sql.gz) into the reference
database. It is needed once, before the first transform.`,
		examples: []string{"load-reference seed-cbioportal_hg19_hg38_v2.14.5.sql.gz --db cbioportal_ref"},
		setup:    setupLoadReference,
	},
	{
		name: "load-genesets", args: "<genesets.gmt>", minArgs: 1, maxArgs: 1,
		short: "load gene sets and their hierarchy into the reference database",
		long: `Load-genesets replaces the gene sets of the reference database, as
cbioportal-core's importGenesetData.pl --new-version followed by
importGenesetHierarchy.pl: the GMT file (<id> <ref link> <Entrez ids>... per
line), an optional supplementary file (<id> <name> <description> per line),
the collection version, and an optional hierarchy YAML file. GSVA profiles
reference gene sets by id, so load them before serving such studies.`,
		examples: []string{
			"load-genesets genesets.gmt --version msigdb_7.5.1 --supp supp-genesets.txt --hierarchy tree.yaml --db cbioportal_ref",
		},
		setup: setupLoadGenesets,
	},
	{
		name: "load-gene-panels", args: "<panels-dir>", minArgs: 1, maxArgs: 1,
		short: "load gene panel definitions into the raw database",
		long: `Load-gene-panels loads every gene panel file of a directory (datahub
reference_data/gene_panels) into the raw database.`,
		examples: []string{"load-gene-panels datahub/reference_data/gene_panels --db cbioportal_raw"},
		setup:    setupLoadGenePanels,
	},
	{
		name: "import-study", args: "<study-dir>...", minArgs: 1, maxArgs: -1,
		short: "import studies into a portal that is being served",
		long: `Import-study stages each study directory in the raw database and then
rebuilds the portal database once, like ingest followed by transform, but
safe to run against a portal that is being served:

  - One import at a time: the import holds a lock (a table in the raw
    database) and refreshes it every minute. Another import waits for it
    (--lock-wait) and takes over a lock whose holder stopped refreshing it
    (--lock-stale).
  - The transform builds the new tables beside the served ones (named
    __next_*) and swaps them in together at the end (EXCHANGE TABLES), so
    the portal serves either its previous state or the new one.
  - A study that fails to stage is removed from the portal. When the
    transform fails, this run's studies are removed and the portal is
    rebuilt without them.

The reference database is loaded from --seed when it has no genes yet, and
the shared gene panels from --gene-panels when the raw database has none
(or always, with --reload-panels). --raw-db and --ref-db default to the
portal database's name with _raw and _ref. The exit status is 1 when any
study failed.

--dry-run reports what the import would do: which studies are new, which
replace a study the portal already has, and whether the reference data
and gene panels would be loaded. It changes nothing and takes no lock.`,
		examples: []string{
			"import-study datahub/public/acbc_mskcc_2015 --db cbioportal --dry-run",
			"import-study datahub/public/acbc_mskcc_2015 --db cbioportal",
			"import-study datahub/public/a datahub/public/b --db portal --seed datahub/seedDB/seed-cbioportal_hg19_hg38_v2.14.5.sql.gz --gene-panels datahub/reference_data/gene_panels",
		},
		setup: setupImportStudy,
	},
	{
		name: "fetch-datahub", args: "<dir> [<study-path>...]", minArgs: 1, maxArgs: -1,
		short: "check out datahub studies with their LFS files",
		long: `Fetch-datahub checks out the given studies of cBioPortal/datahub
(public/<study>, crdc/gdc/<study>) into <dir>, together with the shared gene
panels (reference_data/gene_panels) and the seed dumps (seedDB). The
checkout is shallow and sparse, so only these paths are downloaded. Large
files come from datahub's own Git LFS store (--lfs-url, the store named in
datahub's .lfsconfig), never from GitHub's LFS. <dir> is replaced.`,
		examples: []string{
			"fetch-datahub datahub public/acbc_mskcc_2015 public/msk_impact_2017",
			"fetch-datahub datahub --ref pull/2372/head public/brca_tcga",
		},
		setup: setupFetchDatahub,
	},
	{
		name: "transform", minArgs: 0, maxArgs: 0,
		short: "build the portal database's tables from the raw and reference databases",
		long: `Transform rebuilds every derived table and view of the portal database
from the staged studies (--raw-db) and the reference data (--ref-db), and
prints the row count of each. The API serves the portal database (--db).

--from-prod is for the migration from Java cBioPortal only: it builds go's
tables from Java's. --ref-db is then a clone of a cBioPortal production
ClickHouse database (cbioportal-core's schema plus its *_derived tables, e.g.
cloned with CREATE TABLE ... CLONE AS) and serves as the reference database
too: the portal database is built from it, mostly as views over the clone,
and --raw-db receives the staging-shaped views the API reads (molecular
matrices, timeline, generic assay entities). Nothing is read from staged
studies. Serve the result with the API's -db, -raw-db and -ref-db set to the
same three databases.`,
		examples: []string{
			"transform --raw-db cbioportal_raw --db cbioportal --ref-db cbioportal_ref",
			"transform --from-prod --ref-db mirror_ref --raw-db mirror_raw --db mirror",
		},
		setup: setupTransform,
	},
}

// urlFlag registers the ClickHouse endpoint flag shared by every command
// that connects to ClickHouse.
func urlFlag(fs *flag.FlagSet) *string {
	return fs.String("url", clickhouseURL(), "ClickHouse HTTP `endpoint`; defaults to $CLICKHOUSE_URL if set")
}

// clickhouseURL is the default --url: CLICKHOUSE_URL, else a local server.
func clickhouseURL() string {
	if u := os.Getenv("CLICKHOUSE_URL"); u != "" {
		return u
	}
	return "http://localhost:18123"
}

// jsonFlag registers --json, which prints the command's result as JSON.
func jsonFlag(fs *flag.FlagSet) *bool {
	return fs.Bool("json", false, "print the result as JSON on stdout (progress stays on stderr)")
}

// dryRunFlag registers --dry-run on commands that delete or replace data.
func dryRunFlag(fs *flag.FlagSet) *bool {
	return fs.Bool("dry-run", false, "report what would change, and change nothing")
}

func setupPlan(fs *flag.FlagSet) func([]string, io.Writer) error {
	asJSON := jsonFlag(fs)
	return func(args []string, w io.Writer) error {
		s, err := parser.Scan(args[0])
		if err != nil {
			return err
		}
		if *asJSON {
			type file struct {
				MetaFile       string `json:"metaFile"`
				DataFile       string `json:"dataFile"`
				AlterationType string `json:"alterationType"`
				Datatype       string `json:"datatype"`
				Layout         string `json:"layout"`
				StageTable     string `json:"stageTable"`
			}
			files := []file{}
			for _, f := range s.Files {
				files = append(files, file{f.MetaPath, f.DataPath, f.AlterationType, f.Datatype, f.Layout.String(), f.StageSlug})
			}
			return cli.WriteJSON(w, map[string]any{"studyId": s.StudyID, "files": files})
		}
		fmt.Fprintf(w, "study: %s\n%d data files:\n", s.StudyID, len(s.Files))
		for _, f := range s.Files {
			fmt.Fprintf(w, "  %-24s %-18s %-11s -> %s\n", f.AlterationType, f.Datatype, f.Layout, f.StageSlug)
		}
		return nil
	}
}

func setupIngest(fs *flag.FlagSet) func([]string, io.Writer) error {
	url := urlFlag(fs)
	db := fs.String("db", "cbioportal_raw", "raw staging `database` to load into")
	asJSON := jsonFlag(fs)
	return func(args []string, w io.Writer) error {
		s, err := parser.Scan(args[0])
		if err != nil {
			return err
		}
		if !*asJSON {
			fmt.Fprintf(w, "ingesting study=%s files=%d\n", s.StudyID, len(s.Files))
		}
		start := time.Now()
		stats, err := ingest.IngestStudy(ch.New(*url, *db), s)
		if err != nil {
			return err
		}
		elapsed := time.Since(start).Seconds()
		if *asJSON {
			type file struct {
				File         string  `json:"file"`
				Table        string  `json:"table"`
				Rows         uint64  `json:"rows"`
				ExpectedRows uint64  `json:"expectedRows"`
				Bytes        int64   `json:"bytes"`
				Seconds      float64 `json:"seconds"`
				Skipped      string  `json:"skipped,omitempty"`
			}
			files := []file{}
			for _, st := range stats {
				files = append(files, file{st.SourceFile, st.Table, st.RowsLoaded, st.RowsExpected, st.Bytes, st.Secs, st.Skipped})
			}
			return cli.WriteJSON(w, map[string]any{"studyId": s.StudyID, "database": *db, "seconds": elapsed, "files": files})
		}

		rule := strings.Repeat("-", 72)
		fmt.Fprintf(w, "\n%-24s %10s %9s %8s %8s %9s\n", "file", "rows", "MB", "s", "MB/s", "krows/s")
		fmt.Fprintln(w, rule)
		var totalRows uint64
		var totalBytes int64
		var mismatch int
		for _, st := range stats {
			mark := ""
			if st.RowsLoaded != st.RowsExpected {
				mismatch++
				mark = " !"
			}
			fmt.Fprintf(w, "%-24s %10d %9.1f %8.2f %8.0f %9.1f%s\n",
				st.SourceFile, st.RowsLoaded, float64(st.Bytes)/1e6, st.Secs, st.MBPerS(), st.RowsPerS()/1000, mark)
			totalRows += st.RowsLoaded
			totalBytes += st.Bytes
		}
		fmt.Fprintln(w, rule)
		mb := float64(totalBytes) / 1e6
		mbps, krps := 0.0, 0.0
		if elapsed > 0 {
			mbps = mb / elapsed
			krps = float64(totalRows) / elapsed / 1000
		}
		fmt.Fprintf(w, "TOTAL: %d rows, %.1f MB in %.2fs  =>  %.0f MB/s, %.0f krows/s\n", totalRows, mb, elapsed, mbps, krps)
		if mismatch > 0 {
			fmt.Fprintf(w, "WARNING: %d file(s) had a row-count mismatch (marked !) — check TSV escaping.\n", mismatch)
		}
		return nil
	}
}

func setupRemoveStudy(fs *flag.FlagSet) func([]string, io.Writer) error {
	url := urlFlag(fs)
	db := fs.String("db", "cbioportal_raw", "raw staging `database` to delete from")
	dryRun := dryRunFlag(fs)
	asJSON := jsonFlag(fs)
	return func(args []string, w io.Writer) error {
		c := ch.New(*url, *db)
		type removal struct {
			StudyID string             `json:"studyId"`
			Tables  []ingest.TableRows `json:"tables"`
		}
		out := []removal{}
		for _, id := range args {
			tables, err := ingest.StudyTables(c, id)
			if err != nil {
				return err
			}
			if !*dryRun {
				if _, err := ingest.RemoveStudy(c, id); err != nil {
					return err
				}
			}
			out = append(out, removal{id, append([]ingest.TableRows{}, tables...)})
		}
		if *asJSON {
			return cli.WriteJSON(w, map[string]any{"database": *db, "dryRun": *dryRun, "studies": out})
		}
		verb := "removed"
		if *dryRun {
			verb = "would remove"
		}
		for _, r := range out {
			var rows uint64
			for _, t := range r.Tables {
				rows += t.Rows
			}
			fmt.Fprintf(w, "%s study=%s: %d rows from %d tables of `%s`\n", verb, r.StudyID, rows, len(r.Tables), *db)
			if *dryRun {
				for _, t := range r.Tables {
					fmt.Fprintf(w, "  %-32s %12d rows\n", t.Table, t.Rows)
				}
			}
		}
		return nil
	}
}

func setupIngestCaseLists(fs *flag.FlagSet) func([]string, io.Writer) error {
	url := urlFlag(fs)
	db := fs.String("db", "cbioportal_raw", "raw staging `database` to load into")
	asJSON := jsonFlag(fs)
	return func(args []string, w io.Writer) error {
		c := ch.New(*url, *db)
		studies := []string{}
		for _, dir := range args {
			s, err := parser.Scan(dir)
			if err != nil {
				return err
			}
			if err := ingest.IngestCaseLists(c, s); err != nil {
				return fmt.Errorf("study %s: %w", s.StudyID, err)
			}
			studies = append(studies, s.StudyID)
			if !*asJSON {
				fmt.Fprintf(w, "staged case lists of study=%s into `%s`\n", s.StudyID, *db)
			}
		}
		if *asJSON {
			return cli.WriteJSON(w, map[string]any{"database": *db, "studies": studies})
		}
		return nil
	}
}

func setupTransform(fs *flag.FlagSet) func([]string, io.Writer) error {
	url := urlFlag(fs)
	rawDB := fs.String("raw-db", "cbioportal_raw", "raw staging `database` to read")
	db := fs.String("db", "cbioportal", "portal `database` to build; the API serves it with -db")
	refDB := fs.String("ref-db", "cbioportal_ref", "gene reference `database` to read (with -from-prod: the production clone)")
	fromProd := fs.Bool("from-prod", false, "Java migration only: build from a clone of a Java cBioPortal production database (-ref-db) instead of staged studies")
	asJSON := jsonFlag(fs)
	return func(_ []string, w io.Writer) error {
		start := time.Now()
		var counts []transform.ViewCount
		var err error
		if *fromProd {
			counts, err = transform.ApplyFromProd(ch.New(*url, *refDB), *refDB, *rawDB, *db)
		} else {
			counts, err = transform.Apply(ch.New(*url, *rawDB), *rawDB, *db, *refDB)
		}
		if err != nil {
			return err
		}
		if *asJSON {
			type object struct {
				Name string `json:"name"`
				View bool   `json:"view"`
				Rows uint64 `json:"rows"`
			}
			objects := []object{}
			for _, vc := range counts {
				name, view := strings.CutSuffix(vc.Name, " (view)")
				objects = append(objects, object{name, view, vc.Rows})
			}
			return cli.WriteJSON(w, map[string]any{"database": *db, "rawDatabase": *rawDB, "refDatabase": *refDB,
				"fromProd": *fromProd, "seconds": time.Since(start).Seconds(), "objects": objects})
		}
		if *fromProd {
			fmt.Fprintf(w, "objects built from production clone `%s` in %.0fs:\n", *refDB, time.Since(start).Seconds())
			for _, vc := range counts {
				if name, ok := strings.CutSuffix(vc.Name, " (view)"); ok {
					fmt.Fprintf(w, "  %-64s view\n", name)
					continue
				}
				fmt.Fprintf(w, "  %-64s %12d rows\n", vc.Name, vc.Rows)
			}
			return nil
		}
		fmt.Fprintf(w, "transform views in `%s` (over `%s`):\n", *db, *rawDB)
		for _, vc := range counts {
			fmt.Fprintf(w, "  %-20s %12d rows\n", vc.Name, vc.Rows)
		}
		return nil
	}
}

func setupLoadReference(fs *flag.FlagSet) func([]string, io.Writer) error {
	url := urlFlag(fs)
	db := fs.String("db", "cbioportal_ref", "reference `database` to load into")
	asJSON := jsonFlag(fs)
	return func(args []string, w io.Writer) error {
		genes, aliases, err := reference.LoadReference(ch.New(*url, *db), args[0])
		if err != nil {
			return err
		}
		if *asJSON {
			return cli.WriteJSON(w, map[string]any{"database": *db, "genes": genes, "aliases": aliases})
		}
		fmt.Fprintf(w, "loaded %d genes and %d aliases into `%s`\n", genes, aliases, *db)
		return nil
	}
}

func setupLoadGenesets(fs *flag.FlagSet) func([]string, io.Writer) error {
	url := urlFlag(fs)
	db := fs.String("db", "cbioportal_ref", "reference `database` to load into")
	version := fs.String("version", "", "gene set collection `version` (required), e.g. msigdb_7.5.1")
	supp := fs.String("supp", "", "supplementary `file` with gene set names and descriptions")
	hierarchy := fs.String("hierarchy", "", "gene set hierarchy YAML `file`")
	asJSON := jsonFlag(fs)
	return func(args []string, w io.Writer) error {
		if *version == "" {
			return fmt.Errorf("--version is required")
		}
		st, err := reference.LoadGenesets(ch.New(*url, *db), args[0], *supp, *version, *hierarchy)
		if err != nil {
			return err
		}
		if *asJSON {
			return cli.WriteJSON(w, map[string]any{"database": *db, "genesets": st.Genesets, "genes": st.Genes,
				"skippedGenes": st.SkippedGenes, "hierarchyNodes": st.Nodes, "hierarchyLeaves": st.Leaves})
		}
		fmt.Fprintf(w, "loaded %d gene sets (%d genes, %d skipped as unknown) and %d hierarchy nodes (%d leaves) into `%s`\n",
			st.Genesets, st.Genes, st.SkippedGenes, st.Nodes, st.Leaves, *db)
		return nil
	}
}

func setupLoadGenePanels(fs *flag.FlagSet) func([]string, io.Writer) error {
	url := urlFlag(fs)
	db := fs.String("db", "cbioportal_raw", "raw staging `database` to load into")
	asJSON := jsonFlag(fs)
	return func(args []string, w io.Writer) error {
		panels, genes, err := ingest.LoadGenePanelsDir(ch.New(*url, *db), args[0])
		if err != nil {
			return err
		}
		if *asJSON {
			return cli.WriteJSON(w, map[string]any{"database": *db, "genePanels": panels, "genes": genes})
		}
		fmt.Fprintf(w, "loaded %d gene panels (%d genes) into `%s`\n", panels, genes, *db)
		return nil
	}
}

package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/cbioportal/cbioportal-go/internal/cli"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/ingest"
	"github.com/cbioportal/cbioportal-go/internal/parser"
	"github.com/cbioportal/cbioportal-go/internal/reference"
	"github.com/cbioportal/cbioportal-go/internal/transform"
)

// urlFlag registers the ClickHouse endpoint flag shared by every command
// that connects to ClickHouse, falling back to $CLICKHOUSE_URL, and adds
// the ClickHouse environment variables to the command's help.
func urlFlag(cmd *cobra.Command) *string {
	url := cmd.Flags().String("url", "http://localhost:18123", "ClickHouse HTTP `endpoint`")
	cli.Env(cmd.Flags(), "url", "CLICKHOUSE_URL")
	cmd.Long += "\n\n" + environmentHelp
	return url
}

// jsonFlag registers --json, which prints the command's result as JSON.
func jsonFlag(fs *pflag.FlagSet) *bool {
	return fs.Bool("json", false, "print the result as JSON on stdout (progress stays on stderr)")
}

// dryRunFlag registers --dry-run on commands that delete or replace data.
func dryRunFlag(fs *pflag.FlagSet) *bool {
	return fs.Bool("dry-run", false, "report what would change, and change nothing")
}

func newPlanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan <study-dir>",
		Short: "list a study's data files and the staging tables they load into",
		Long: `Plan reads the meta files of a study directory and lists each data file
with its alteration type, datatype, layout and staging table, without
connecting to ClickHouse.`,
		Example: `  cbioportal-import plan datahub/public/acbc_mskcc_2015
  cbioportal-import plan datahub/public/acbc_mskcc_2015 --json`,
		Args: cli.ArgsBetween(1, 1),
	}
	fs := cmd.Flags()
	asJSON := jsonFlag(fs)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		w := cmd.OutOrStdout()
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
	return cmd
}

func newIngestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ingest <study-dir>",
		Short: "stage a study's data files in the raw database",
		Long: `Ingest streams every data file of a study directory, as is, into staging
tables of the raw database, replacing that study's earlier rows. Run
transform afterwards to make the study queryable.`,
		Example: `  cbioportal-import ingest datahub/public/acbc_mskcc_2015 --db cbioportal_raw
  cbioportal-import ingest --url https://<host>:8443 --db cbioportal_raw datahub/public/msk_impact_2017`,
		Args: cli.ArgsBetween(1, 1),
	}
	fs := cmd.Flags()
	url := urlFlag(cmd)
	db := fs.String("db", "cbioportal_raw", "raw staging `database` to load into")
	asJSON := jsonFlag(fs)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		w := cmd.OutOrStdout()
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
	return cmd
}

func newRemoveStudyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove-study <study-id>...",
		Short: "delete studies' staged rows from the raw database",
		Long: `Remove-study deletes every staged row of each study from the raw
database. Run transform afterwards to remove the studies from the portal
database. --dry-run lists the tables and rows it would delete.`,
		Example: `  cbioportal-import remove-study acbc_mskcc_2015 --db cbioportal_raw --dry-run
  cbioportal-import remove-study acbc_mskcc_2015 --db cbioportal_raw`,
		Args: cli.ArgsBetween(1, -1),
	}
	fs := cmd.Flags()
	url := urlFlag(cmd)
	db := fs.String("db", "cbioportal_raw", "raw staging `database` to delete from")
	dryRun := dryRunFlag(fs)
	asJSON := jsonFlag(fs)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		w := cmd.OutOrStdout()
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
	return cmd
}

func newIngestCaseListsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ingest-case-lists <study-dir>...",
		Short: "restage only the case lists of one or more studies",
		Long: `Ingest-case-lists restages only the case_lists/ directory of each study,
e.g. for raw databases whose data files cannot be re-read.`,
		Example: `  cbioportal-import ingest-case-lists datahub/public/study_a datahub/public/study_b --db cbioportal_raw`,
		Args:    cli.ArgsBetween(1, -1),
	}
	fs := cmd.Flags()
	url := urlFlag(cmd)
	db := fs.String("db", "cbioportal_raw", "raw staging `database` to load into")
	asJSON := jsonFlag(fs)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		w := cmd.OutOrStdout()
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
	return cmd
}

func newTransformCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "transform",
		Short: "build the portal database's tables from the raw and reference databases",
		Long: `Transform rebuilds every derived table and view of the portal database
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
		Example: `  cbioportal-import transform --raw-db cbioportal_raw --db cbioportal --ref-db cbioportal_ref
  cbioportal-import transform --from-prod --ref-db mirror_ref --raw-db mirror_raw --db mirror`,
		Args: cli.ArgsBetween(0, 0),
	}
	fs := cmd.Flags()
	url := urlFlag(cmd)
	rawDB := fs.String("raw-db", "cbioportal_raw", "raw staging `database` to read")
	db := fs.String("db", "cbioportal", "portal `database` to build; the API serves it with -db")
	refDB := fs.String("ref-db", "cbioportal_ref", "gene reference `database` to read (with -from-prod: the production clone)")
	fromProd := fs.Bool("from-prod", false, "Java migration only: build from a clone of a Java cBioPortal production database (-ref-db) instead of staged studies")
	asJSON := jsonFlag(fs)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		w := cmd.OutOrStdout()
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
	return cmd
}

func newLoadReferenceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "load-reference <seed.sql.gz>",
		Short: "load genes, gene aliases and cancer types from a seed dump",
		Long: `Load-reference loads the genes, gene aliases and cancer types of a
cBioPortal seed dump (datahub seedDB/seed-*.sql.gz) into the reference
database. It is needed once, before the first transform.`,
		Example: `  cbioportal-import load-reference seed-cbioportal_hg19_hg38_v2.14.5.sql.gz --db cbioportal_ref`,
		Args:    cli.ArgsBetween(1, 1),
	}
	fs := cmd.Flags()
	url := urlFlag(cmd)
	db := fs.String("db", "cbioportal_ref", "reference `database` to load into")
	asJSON := jsonFlag(fs)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		w := cmd.OutOrStdout()
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
	return cmd
}

func newLoadGenesetsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "load-genesets <genesets.gmt>",
		Short: "load gene sets and their hierarchy into the reference database",
		Long: `Load-genesets replaces the gene sets of the reference database, as
cbioportal-core's importGenesetData.pl --new-version followed by
importGenesetHierarchy.pl: the GMT file (<id> <ref link> <Entrez ids>... per
line), an optional supplementary file (<id> <name> <description> per line),
the collection version, and an optional hierarchy YAML file. GSVA profiles
reference gene sets by id, so load them before serving such studies.`,
		Example: `  cbioportal-import load-genesets genesets.gmt --version msigdb_7.5.1 --supp supp-genesets.txt --hierarchy tree.yaml --db cbioportal_ref`,
		Args:    cli.ArgsBetween(1, 1),
	}
	fs := cmd.Flags()
	url := urlFlag(cmd)
	db := fs.String("db", "cbioportal_ref", "reference `database` to load into")
	version := fs.String("version", "", "gene set collection `version` (required), e.g. msigdb_7.5.1")
	supp := fs.String("supp", "", "supplementary `file` with gene set names and descriptions")
	hierarchy := fs.String("hierarchy", "", "gene set hierarchy YAML `file`")
	asJSON := jsonFlag(fs)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		w := cmd.OutOrStdout()
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
	return cmd
}

func newLoadGenePanelsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "load-gene-panels <panels-dir>",
		Short: "load gene panel definitions into the raw database",
		Long: `Load-gene-panels loads every gene panel file of a directory (datahub
reference_data/gene_panels) into the raw database.`,
		Example: `  cbioportal-import load-gene-panels datahub/reference_data/gene_panels --db cbioportal_raw`,
		Args:    cli.ArgsBetween(1, 1),
	}
	fs := cmd.Flags()
	url := urlFlag(cmd)
	db := fs.String("db", "cbioportal_raw", "raw staging `database` to load into")
	asJSON := jsonFlag(fs)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		w := cmd.OutOrStdout()
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
	return cmd
}

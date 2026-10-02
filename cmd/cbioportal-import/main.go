// cbioportal-import — fast ELT loader for cBioPortal datahub studies.
// Streams raw TSV data files of a study directory straight into ClickHouse
// staging tables (schema-on-read); transforms run afterwards as SQL views.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
	"github.com/inodb/cbioportal-go/internal/ingest"
	"github.com/inodb/cbioportal-go/internal/parser"
	"github.com/inodb/cbioportal-go/internal/reference"
	"github.com/inodb/cbioportal-go/internal/transform"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "plan":
		cmdPlan(os.Args[2:])
	case "ingest":
		cmdIngest(os.Args[2:])
	case "ingest-case-lists":
		cmdIngestCaseLists(os.Args[2:])
	case "transform":
		cmdTransform(os.Args[2:])
	case "load-reference":
		cmdLoadReference(os.Args[2:])
	case "load-gene-panels":
		cmdLoadGenePanels(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: cbioportal-import <plan|ingest|ingest-case-lists|transform|load-reference|load-gene-panels> [args]")
	os.Exit(2)
}

func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// reorder moves flag arguments (and their values) ahead of positional args, so
// `ingest <dir> --db X` works despite the stdlib flag package stopping at the
// first positional. All flags here take a value.
func reorder(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}

func cmdPlan(args []string) {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	fs.Parse(reorder(args))
	s, err := parser.Scan(fs.Arg(0))
	fatal(err)
	fmt.Printf("study: %s\n%d data files:\n", s.StudyID, len(s.Files))
	for _, f := range s.Files {
		fmt.Printf("  %-24s %-18s %-11s -> %s\n", f.AlterationType, f.Datatype, f.Layout, f.StageSlug)
	}
}

func cmdIngest(args []string) {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	url := fs.String("url", "http://localhost:18123", "ClickHouse HTTP endpoint")
	db := fs.String("db", "cbioportal_raw", "target database")
	fs.Parse(reorder(args))

	s, err := parser.Scan(fs.Arg(0))
	fatal(err)
	fmt.Printf("ingesting study=%s files=%d\n", s.StudyID, len(s.Files))
	c := ch.New(*url, *db)
	start := time.Now()
	stats, err := ingest.IngestStudy(c, s)
	fatal(err)
	elapsed := time.Since(start).Seconds()

	fmt.Printf("\n%-24s %10s %9s %8s %8s %9s\n", "file", "rows", "MB", "s", "MB/s", "krows/s")
	fmt.Println(dashes(72))
	var totalRows uint64
	var totalBytes int64
	var mismatch int
	for _, st := range stats {
		flag := ""
		if st.RowsLoaded != st.RowsExpected {
			mismatch++
			flag = " !"
		}
		fmt.Printf("%-24s %10d %9.1f %8.2f %8.0f %9.1f%s\n",
			st.SourceFile, st.RowsLoaded, float64(st.Bytes)/1e6, st.Secs, st.MBPerS(), st.RowsPerS()/1000, flag)
		totalRows += st.RowsLoaded
		totalBytes += st.Bytes
	}
	fmt.Println(dashes(72))
	mb := float64(totalBytes) / 1e6
	mbps, krps := 0.0, 0.0
	if elapsed > 0 {
		mbps = mb / elapsed
		krps = float64(totalRows) / elapsed / 1000
	}
	fmt.Printf("TOTAL: %d rows, %.1f MB in %.2fs  =>  %.0f MB/s, %.0f krows/s\n", totalRows, mb, elapsed, mbps, krps)
	if mismatch > 0 {
		fmt.Printf("WARNING: %d file(s) had a row-count mismatch (marked !) — check TSV escaping.\n", mismatch)
	}
}

// cmdIngestCaseLists re-stages only the case lists of one or more study
// directories, e.g. for raw databases whose data files cannot be re-read.
func cmdIngestCaseLists(args []string) {
	fs := flag.NewFlagSet("ingest-case-lists", flag.ExitOnError)
	url := fs.String("url", "http://localhost:18123", "ClickHouse HTTP endpoint")
	db := fs.String("db", "cbioportal_raw", "target database")
	fs.Parse(reorder(args))
	c := ch.New(*url, *db)
	for _, dir := range fs.Args() {
		s, err := parser.Scan(dir)
		fatal(err)
		fatal(ingest.IngestCaseLists(c, s))
		fmt.Printf("staged case lists of study=%s into `%s`\n", s.StudyID, *db)
	}
}

func cmdTransform(args []string) {
	fs := flag.NewFlagSet("transform", flag.ExitOnError)
	url := fs.String("url", "http://localhost:18123", "ClickHouse HTTP endpoint")
	rawDB := fs.String("raw-db", "cbioportal_raw", "raw staging database")
	db := fs.String("db", "cbioportal", "target database for views")
	refDB := fs.String("ref-db", "cbioportal_ref", "gene reference database")
	fs.Parse(reorder(args))

	c := ch.New(*url, *rawDB)
	counts, err := transform.Apply(c, *rawDB, *db, *refDB)
	fatal(err)
	fmt.Printf("transform views in `%s` (over `%s`):\n", *db, *rawDB)
	for _, vc := range counts {
		fmt.Printf("  %-20s %12d rows\n", vc.Name, vc.Rows)
	}
}

func cmdLoadReference(args []string) {
	fs := flag.NewFlagSet("load-reference", flag.ExitOnError)
	url := fs.String("url", "http://localhost:18123", "ClickHouse HTTP endpoint")
	db := fs.String("db", "cbioportal_ref", "reference database")
	fs.Parse(reorder(args))
	c := ch.New(*url, *db)
	genes, aliases, err := reference.LoadReference(c, fs.Arg(0))
	fatal(err)
	fmt.Printf("loaded %d genes and %d aliases into `%s`\n", genes, aliases, *db)
}

func cmdLoadGenePanels(args []string) {
	fs := flag.NewFlagSet("load-gene-panels", flag.ExitOnError)
	url := fs.String("url", "http://localhost:18123", "ClickHouse HTTP endpoint")
	db := fs.String("db", "cbioportal_raw", "raw staging database")
	fs.Parse(reorder(args))
	c := ch.New(*url, *db)
	panels, genes, err := ingest.LoadGenePanelsDir(c, fs.Arg(0))
	fatal(err)
	fmt.Printf("loaded %d gene panels (%d genes) into `%s`\n", panels, genes, *db)
}

func dashes(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '-'
	}
	return string(b)
}

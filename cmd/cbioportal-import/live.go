package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/cbioportal/cbioportal-go/internal/cli"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/datahub"
	"github.com/cbioportal/cbioportal-go/internal/studyimport"
)

// stderrLog prints progress lines with a UTC time stamp.
func stderrLog(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().UTC().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func newImportStudyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import-study <study-dir>...",
		Short: "import studies into a portal that is being served",
		Long: `Import-study stages each study directory in the raw database and then
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
		Example: `  cbioportal-import import-study datahub/public/acbc_mskcc_2015 --db cbioportal --dry-run
  cbioportal-import import-study datahub/public/acbc_mskcc_2015 --db cbioportal
  cbioportal-import import-study datahub/public/a datahub/public/b --db portal --seed datahub/seedDB/seed-cbioportal_hg19_hg38_v2.14.5.sql.gz --gene-panels datahub/reference_data/gene_panels`,
		Args: cli.ArgsBetween(1, -1),
	}
	fs := cmd.Flags()
	url := urlFlag(cmd)
	db := fs.String("db", "cbioportal", "portal `database` to update")
	rawDB := fs.String("raw-db", "", "raw staging `database` (default: <db>_raw)")
	refDB := fs.String("ref-db", "", "reference `database` (default: <db>_ref)")
	seed := fs.String("seed", "", "seed dump (`seed.sql.gz`) to load when the reference database has no genes")
	panels := fs.String("gene-panels", "", "`directory` of shared gene panels to load when the raw database has none")
	reloadPanels := fs.Bool("reload-panels", false, "load --gene-panels even when the raw database has shared gene panels")
	lockTable := fs.String("lock-table", "_import_lock", "`name` of the lock table in the raw database")
	lockWait := fs.Duration("lock-wait", studyimport.DefaultLockWait, "how long to wait for another import")
	lockStale := fs.Duration("lock-stale", studyimport.DefaultLockStale, "take over a lock without a heartbeat for this long")
	owner := fs.String("owner", "", "`name` this import holds the lock as (default: <host>-<pid>)")
	dryRun := dryRunFlag(fs)
	asJSON := jsonFlag(fs)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		w := cmd.OutOrStdout()
		o := studyimport.Options{
			URL: *url, DB: *db, RawDB: orDefault(*rawDB, *db+"_raw"), RefDB: orDefault(*refDB, *db+"_ref"),
			Seed: *seed, GenePanels: *panels, ReloadPanels: *reloadPanels, Logf: stderrLog,
		}
		if *dryRun {
			p, err := studyimport.Plan(o, args)
			if err != nil {
				return err
			}
			if *asJSON {
				return cli.WriteJSON(w, p)
			}
			printImportPlan(w, o, p)
			return nil
		}
		if *owner == "" {
			host, _ := os.Hostname()
			*owner = fmt.Sprintf("%s-%d", host, os.Getpid())
		}
		res, err := importLocked(o, args, &studyimport.Lock{
			DB: o.RawDB, Table: *lockTable, Owner: *owner, Wait: *lockWait, Stale: *lockStale, Logf: stderrLog,
		})
		if err != nil {
			return err
		}
		failed := 0
		type study struct {
			Dir     string  `json:"dir"`
			StudyID string  `json:"studyId,omitempty"`
			Status  string  `json:"status"`
			Error   string  `json:"error,omitempty"`
			Seconds float64 `json:"seconds"`
		}
		studies := []study{}
		for _, st := range res.Studies {
			line := fmt.Sprintf("%-9s %s", st.Status, st.Dir)
			if st.StudyID != "" {
				line += " (" + st.StudyID + ")"
			}
			s := study{Dir: st.Dir, StudyID: st.StudyID, Status: string(st.Status), Seconds: st.Duration.Seconds()}
			if st.Err != nil && st.Status != studyimport.Skipped {
				line += ": " + st.Err.Error()
				s.Error = st.Err.Error()
			}
			if !*asJSON {
				fmt.Fprintln(w, line)
			}
			studies = append(studies, s)
			if st.Status == studyimport.Failed || st.Status == studyimport.Removed {
				failed++
			}
		}
		if *asJSON {
			errText := func(err error) string {
				if err == nil {
					return ""
				}
				return err.Error()
			}
			cli.WriteJSON(w, map[string]any{"database": o.DB, "studies": studies, "panelsLoaded": res.PanelsLoaded,
				"panelsError": errText(res.PanelsErr), "transformError": errText(res.TransformErr)})
		}
		switch {
		case res.PanelsErr != nil:
			return fmt.Errorf("loading the gene panels: %w", res.PanelsErr)
		case res.TransformErr != nil:
			return fmt.Errorf("transform: %w", res.TransformErr)
		case failed > 0:
			return fmt.Errorf("%d of %d studies failed", failed, len(res.Studies))
		}
		return nil
	}
	return cmd
}

// printImportPlan prints what import-study would do.
func printImportPlan(w io.Writer, o studyimport.Options, p *studyimport.ImportPlan) {
	if p.LoadSeed {
		fmt.Fprintf(w, "would load the reference data into %s from %s\n", o.RefDB, o.Seed)
	}
	if p.LoadPanels {
		fmt.Fprintf(w, "would load the shared gene panels into %s from %s\n", o.RawDB, o.GenePanels)
	}
	for _, s := range p.Studies {
		switch s.Action {
		case "skip":
			fmt.Fprintf(w, "would skip    %s: %s\n", s.Dir, s.Reason)
		case "replace":
			fmt.Fprintf(w, "would replace %s (%s, staged in %d tables of %s)\n", s.Dir, s.StudyID, s.Tables, o.RawDB)
		default:
			fmt.Fprintf(w, "would import  %s (%s, new)\n", s.Dir, s.StudyID)
		}
	}
	if p.Transform {
		fmt.Fprintf(w, "would then rebuild %s and swap it in\n", o.DB)
	} else {
		fmt.Fprintf(w, "nothing to stage: %s would be left as is\n", o.DB)
	}
}

// importLocked runs studyimport.Run while holding lock, which is released
// also when the process is interrupted.
func importLocked(o studyimport.Options, dirs []string, lock *studyimport.Lock) (*studyimport.Result, error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	raw := ch.New(o.URL, o.RawDB)
	if err := raw.CreateDatabase(); err != nil {
		return nil, err
	}
	lock.C = raw
	if err := lock.Acquire(ctx); err != nil {
		return nil, err
	}
	released := make(chan struct{})
	defer close(released)
	go func() {
		select {
		case <-ctx.Done():
			// A killed import leaves its staged tables (__next_*) behind;
			// the next transform drops them. Releasing the lock lets the
			// next import start without waiting for it to go stale.
			if err := lock.Release(); err != nil {
				stderrLog("%v", err)
			}
			os.Exit(1)
		case <-released:
		}
	}()
	res, err := studyimport.Run(o, dirs)
	if rerr := lock.Release(); rerr != nil {
		err = errors.Join(err, rerr)
	}
	return res, err
}

func orDefault(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

func newFetchDatahubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fetch-datahub <dir> [<study-path>...]",
		Short: "check out datahub studies with their LFS files",
		Long: `Fetch-datahub checks out the given studies of cBioPortal/datahub
(public/<study>, crdc/gdc/<study>) into <dir>, together with the shared gene
panels (reference_data/gene_panels) and the seed dumps (seedDB). The
checkout is shallow and sparse, so only these paths are downloaded. Large
files come from datahub's own Git LFS store (--lfs-url, the store named in
datahub's .lfsconfig), never from GitHub's LFS. <dir> is replaced.`,
		Example: `  cbioportal-import fetch-datahub datahub public/acbc_mskcc_2015 public/msk_impact_2017
  cbioportal-import fetch-datahub datahub --ref pull/2372/head public/brca_tcga`,
		Args: cli.ArgsBetween(1, -1),
	}
	fs := cmd.Flags()
	ref := fs.String("ref", "master", "datahub `ref` to check out: a branch, a commit or pull/<N>/head")
	gitURL := fs.String("git", datahub.DefaultGit, "datahub git `URL`")
	cli.Env(fs, "git", "DATAHUB_GIT")
	lfsURL := fs.String("lfs-url", datahub.DefaultLFSURL, "datahub's LFS store `URL`")
	cli.Env(fs, "lfs-url", "DATAHUB_LFS_URL")
	asJSON := jsonFlag(fs)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		w := cmd.OutOrStdout()
		dir, err := filepath.Abs(args[0])
		if err != nil {
			return err
		}
		studies := args[1:]
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		co := &datahub.Checkout{Dir: dir, Git: *gitURL, LFSURL: *lfsURL, Logf: stderrLog}
		commit, err := co.Fetch(ctx, *ref, studies)
		if err != nil {
			return err
		}
		if err := co.CheckoutFetched(ctx); err != nil {
			return err
		}
		stderrLog("datahub %s at %s", *ref, commit)
		for _, sub := range append([]string{datahub.GenePanelsDir, datahub.SeedDir}, studies...) {
			stderrLog("%s", sub)
			if _, err := os.Stat(filepath.Join(dir, sub)); err != nil {
				return fmt.Errorf("%s is not in datahub %s", sub, *ref)
			}
			if _, err := co.PullLFS(ctx, sub); err != nil {
				return fmt.Errorf("%s: %w", sub, err)
			}
		}
		if *asJSON {
			return cli.WriteJSON(w, map[string]any{"ref": *ref, "commit": commit, "dir": dir, "studies": append([]string{}, studies...)})
		}
		fmt.Fprintf(w, "datahub %s (%s) in %s\n", *ref, commit, dir)
		return nil
	}
	return cmd
}

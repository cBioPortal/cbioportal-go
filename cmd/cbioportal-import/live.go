package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/cli"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/datahub"
	"github.com/cbioportal/cbioportal-go/internal/studyimport"
)

// stderrLog prints progress lines with a UTC time stamp.
func stderrLog(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().UTC().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func setupImportStudy(fs *flag.FlagSet) func([]string, io.Writer) error {
	url := urlFlag(fs)
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
	return func(args []string, w io.Writer) error {
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

func setupFetchDatahub(fs *flag.FlagSet) func([]string, io.Writer) error {
	ref := fs.String("ref", "master", "datahub `ref` to check out: a branch, a commit or pull/<N>/head")
	gitURL := fs.String("git", datahub.GitURL(), "datahub git `URL`; defaults to $DATAHUB_GIT if set")
	lfsURL := fs.String("lfs-url", datahub.LFSURL(), "datahub's LFS store `URL`; defaults to $DATAHUB_LFS_URL if set")
	asJSON := jsonFlag(fs)
	return func(args []string, w io.Writer) error {
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
}

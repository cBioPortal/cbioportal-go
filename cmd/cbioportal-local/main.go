// cbioportal-local runs a whole cBioPortal on one computer, from study
// folders, in one command: ClickHouse runs inside the process (chDB), the
// studies are imported, and the portal is served with its website. When a
// study's files change, that study is imported again.
//
//	cbioportal-local ~/datahub/public/acc_2019
//	cbioportal-local ~/my-studies --port 8080 --open
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/cbioportal/cbioportal-go/internal/api"
	"github.com/cbioportal/cbioportal-go/internal/chdb"
	"github.com/cbioportal/cbioportal-go/internal/cli"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/config"
	"github.com/cbioportal/cbioportal-go/internal/studyimport"
	"github.com/cbioportal/cbioportal-go/internal/transform"
)

const name = "cbioportal-local"

// The portal's databases inside the local engine.
const (
	portalDB = "cbioportal"
	rawDB    = "cbioportal_raw"
	refDB    = "cbioportal_ref"
)

func main() {
	os.Exit(cli.Execute(newRootCmd(), os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

type options struct {
	dataDir, cacheDir string
	chdbLib           string
	frontendDir       string
	seed, genePanels  string
	host              string
	port              int
	watch, open       bool
	watchInterval     time.Duration
	connections       int
}

func newRootCmd() *cobra.Command {
	o := &options{}
	cmd := &cobra.Command{
		Use:   name + " <study-or-folder>...",
		Short: "run cBioPortal on this computer from study folders",
		Long: `cbioportal-local runs a whole cBioPortal on this computer, from study
folders, in one command. Nothing is sent anywhere: the data stays in a
folder on this computer.

  1. It starts ClickHouse inside the program (chDB). The first run downloads
     the engine once, about 120-180 MB, into the cache folder.
  2. It loads the reference genes from a datahub seed file and the shared
     gene panels, found next to the studies in a datahub checkout, or given
     with --seed and --gene-panels. This happens once per data folder.
  3. It imports each study: a folder with a meta_study.txt, or every such
     folder inside the folders given.
  4. It serves the portal and its website on --port.
  5. It watches the study folders. When a study's files change, it imports
     that study again; only that study is rebuilt.

Sessions (virtual studies, groups, custom data) are kept in a file in the
data folder. There is no login.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return run(o, args) },
	}
	cache, _ := os.UserCacheDir()
	fs := cmd.Flags()
	fs.StringVar(&o.dataDir, "data", filepath.Join(cache, "cbioportal-local", "data"), "`folder` the portal's databases are kept in")
	fs.StringVar(&o.cacheDir, "cache", filepath.Join(cache, "cbioportal-local"), "`folder` for the downloaded engine and website")
	fs.StringVar(&o.chdbLib, "chdb-lib", "", "use this libchdb `file` instead of downloading one")
	fs.StringVar(&o.frontendDir, "frontend-dir", "", "serve this frontend build `folder` instead of the built-in one")
	fs.StringVar(&o.seed, "seed", "", "datahub seed `file` (seed-*.sql.gz) with the reference genes; found in a datahub checkout when unset")
	fs.StringVar(&o.genePanels, "gene-panels", "", "`folder` of shared gene panels; found in a datahub checkout when unset")
	fs.StringVar(&o.host, "host", "127.0.0.1", "`address` to serve on (0.0.0.0 for other computers on the network)")
	fs.IntVar(&o.port, "port", 8080, "`port` to serve on")
	fs.BoolVar(&o.watch, "watch", true, "import a study again when its files change")
	fs.DurationVar(&o.watchInterval, "watch-interval", 2*time.Second, "how often to look for changed files")
	fs.BoolVar(&o.open, "open", false, "open the portal in the web browser")
	fs.IntVar(&o.connections, "connections", 16, "ClickHouse connections the importer and the API share")
	for _, f := range []struct{ flag, env string }{
		{"data", "CBIOPORTAL_LOCAL_DATA"}, {"cache", "CBIOPORTAL_LOCAL_CACHE"}, {"chdb-lib", "CHDB_LIB"},
		{"frontend-dir", "FRONTEND_DIR"}, {"host", "HOST"}, {"port", "PORT"},
	} {
		cli.Env(fs, f.flag, f.env)
	}
	cli.AddVersion(cmd, name)
	return cmd
}

func run(o *options, args []string) error {
	logf := func(format string, args ...any) { log.Printf(format, args...) }
	dirs, err := studyDirs(args)
	if err != nil {
		return cli.Usage(err)
	}
	if len(dirs) == 0 {
		return cli.Usagef("no study folders (with a meta_study.txt) in %s", strings.Join(args, ", "))
	}
	seed, panels := o.seed, o.genePanels
	if seed == "" || panels == "" {
		s, p := findDatahubReference(dirs[0])
		seed, panels = cmp(seed, s), cmp(panels, p)
	}
	frontend, err := frontendDir(o)
	if err != nil {
		return err
	}

	lib := o.chdbLib
	if lib == "" {
		if lib, err = chdb.Library(o.cacheDir, logf); err != nil {
			return err
		}
	}
	engine, err := chdb.Open(lib, o.dataDir, o.connections)
	if err != nil {
		return err
	}
	defer engine.Close()
	ch.UseTransport(engine.Transport())
	logf("ClickHouse (chDB %s) is running in this program, with its data in %s", chdb.Version, o.dataDir)

	admin := ch.New(chdb.URL, "default")
	for _, db := range []string{portalDB, rawDB, refDB} {
		if _, err := admin.ExecuteServerwide("CREATE DATABASE IF NOT EXISTS `" + db + "`"); err != nil {
			return err
		}
	}
	transform.Logf = logf
	opts := studyimport.Options{
		URL: chdb.URL, DB: portalDB, RawDB: rawDB, RefDB: refDB,
		Seed: seed, GenePanels: panels, Logf: logf,
	}
	status := newImportStatus(dirs)
	importStudies(opts, status, dirs)

	client := ch.New(chdb.URL, portalDB)
	client.SetDatabaseAliases(rawDB, refDB)
	state := api.NewState(client, api.Options{
		FrontendDir:      frontend,
		SessionStoreFile: filepath.Join(o.dataDir, "sessions.json"),
	})
	props, err := config.LoadPortalProperties("", nil)
	if err != nil {
		return err
	}
	state.Props = props
	if err := state.ConfigureAuth(); err != nil {
		return err
	}
	if err := state.ConfigureSessions(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	addr := net.JoinHostPort(o.host, fmt.Sprint(o.port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: status.handler(api.NewRouter(state))}
	go srv.Serve(ln)
	url := "http://" + addr
	if o.host == "0.0.0.0" || o.host == "" {
		host, _ := os.Hostname()
		url = fmt.Sprintf("http://%s:%d", host, o.port)
	}
	logf("cBioPortal is at %s, and the import status at %s/local (press Ctrl+C to stop)", url, url)
	if o.open {
		openBrowser(url)
	}
	if o.watch {
		go watch(ctx, dirs, o.watchInterval, func(changed []string) {
			logf("files changed in %s; importing again", strings.Join(changed, ", "))
			importStudies(opts, status, changed)
		})
	}
	<-ctx.Done()
	logf("stopping")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// importStudies imports dirs, records the outcome in status and logs each
// study's; a study that fails is reported and left out, the others are
// served.
func importStudies(o studyimport.Options, status *importStatus, dirs []string) {
	start := time.Now()
	status.start(dirs)
	res, err := studyimport.Run(o, dirs)
	status.done(dirs, res, err, time.Since(start))
	if err != nil {
		log.Printf("import failed: %v", err)
		return
	}
	for _, s := range res.Studies {
		if s.Err != nil {
			log.Printf("  %s: %s: %v", s.StudyID, s.Status, s.Err)
		} else {
			log.Printf("  %s: %s", s.StudyID, s.Status)
		}
	}
	log.Printf("imported %d of %d studies in %s", res.Count(studyimport.Imported), len(dirs), time.Since(start).Round(time.Second))
}

// studyDirs are the study folders among args: each arg that has a
// meta_study.txt, or else its subfolders (up to two levels down) that do.
func studyDirs(args []string) ([]string, error) {
	var out []string
	for _, a := range args {
		abs, err := filepath.Abs(a)
		if err != nil {
			return nil, err
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("%s is not a folder", a)
		}
		if isStudy(abs) {
			out = append(out, abs)
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(abs, "*", "meta_study.txt"))
		deeper, _ := filepath.Glob(filepath.Join(abs, "*", "*", "meta_study.txt"))
		for _, m := range append(matches, deeper...) {
			out = append(out, filepath.Dir(m))
		}
	}
	sort.Strings(out)
	return out, nil
}

func isStudy(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "meta_study.txt"))
	return err == nil
}

// findDatahubReference looks for a datahub checkout's seed file
// (seedDB/seed-*.sql.gz, the newest) and shared gene panels
// (reference_data/gene_panels) above dir.
func findDatahubReference(dir string) (seed, panels string) {
	for d := dir; ; d = filepath.Dir(d) {
		if seed == "" {
			if m, _ := filepath.Glob(filepath.Join(d, "seedDB", "seed-*.sql.gz")); len(m) > 0 {
				sort.Strings(m)
				seed = m[len(m)-1]
			}
		}
		if panels == "" {
			if p := filepath.Join(d, "reference_data", "gene_panels"); isDir(p) {
				panels = p
			}
		}
		if (seed != "" && panels != "") || filepath.Dir(d) == d {
			return seed, panels
		}
	}
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func cmp(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// watch calls changed with the study folders whose files changed since
// the last look, once the changes have settled for one interval.
func watch(ctx context.Context, dirs []string, interval time.Duration, changed func([]string)) {
	last := map[string]string{}
	pending := map[string]string{}
	for _, d := range dirs {
		last[d] = signature(d)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var ready []string
		for _, d := range dirs {
			sig := signature(d)
			switch {
			case sig == last[d]:
				delete(pending, d)
			case pending[d] == sig:
				ready = append(ready, d)
				last[d] = sig
				delete(pending, d)
			default:
				pending[d] = sig
			}
		}
		if len(ready) > 0 {
			changed(ready)
		}
	}
}

// signature summarizes the names, sizes and change times of a study
// folder's files.
func signature(dir string) string {
	var b strings.Builder
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") && p != dir {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info, err := d.Info(); err == nil && !d.IsDir() {
			fmt.Fprintf(&b, "%s\x00%d\x00%d\n", p, info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
	return b.String()
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		log.Printf("could not open a browser: %v", err)
	}
}

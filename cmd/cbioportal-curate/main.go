// cbioportal-curate runs a whole cBioPortal on one computer, from study
// folders, in one command: ClickHouse runs inside the process (chDB), the
// studies are imported, and the portal is served with its website. When a
// study's files change, that study is imported again.
//
//	cbioportal-curate ~/datahub/public/acc_2019
//	cbioportal-curate ~/my-studies --port 8080 --open
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
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/cbioportal/cbioportal-go/internal/api"
	"github.com/cbioportal/cbioportal-go/internal/chdb"
	"github.com/cbioportal/cbioportal-go/internal/cli"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/config"
	"github.com/cbioportal/cbioportal-go/internal/ingest"
	"github.com/cbioportal/cbioportal-go/internal/studyimport"
	"github.com/cbioportal/cbioportal-go/internal/transform"
)

const name = "cbioportal-curate"

// oldName is what the program was called before; its data and cache
// folders are taken over (adoptOldFolder).
const oldName = "cbioportal-local"

// The portal's databases inside the local engine.
const (
	portalDB = "cbioportal"
	rawDB    = "cbioportal_raw"
	refDB    = "cbioportal_ref"
)

// The places the program meets the person using it. In a terminal they are
// the web browser and the system's folder picker; the macOS app
// (app.go, built with -tags wails) shows the pages in its window and opens
// its own folder dialog.
var (
	// showPage shows a page of the portal.
	showPage = openBrowser
	// chooseFolder asks for a folder; "" when the person cancels.
	chooseFolder = pickFolder
	// logTo receives the log too.
	logTo io.Writer = io.Discard
	// baseCtx ends the run when it is cancelled.
	baseCtx = context.Background()
	// serving is set once the portal answers on its port.
	serving atomic.Bool
)

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
		Use:   name + " [study-or-folder]...",
		Short: "run cBioPortal on this computer from study folders",
		Long: `cbioportal-curate runs a whole cBioPortal on this computer, from study
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

Without a folder, as when the app is opened, it opens its import page
(/local) in the web browser: a short wizard that adds study folders with
the system's folder picker, or downloads public studies from cBioPortal,
and shows each study's import with its errors and warnings. The study
folders are remembered for next time; only the ones that changed are
imported again. A second start while it runs opens the running one.

Sessions (virtual studies, groups, custom data) are kept in a file in the
data folder. There is no login.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return run(o, args) },
	}
	cache, _ := os.UserCacheDir()
	fs := cmd.Flags()
	fs.StringVar(&o.dataDir, "data", defaultDataDir(), "`folder` the portal's databases are kept in")
	fs.StringVar(&o.cacheDir, "cache", filepath.Join(cache, "cbioportal-curate"), "`folder` for the downloaded engine and website")
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
		{"data", "CBIOPORTAL_CURATE_DATA"}, {"cache", "CBIOPORTAL_CURATE_CACHE"}, {"chdb-lib", "CHDB_LIB"},
		{"frontend-dir", "FRONTEND_DIR"}, {"host", "HOST"}, {"port", "PORT"},
	} {
		cli.Env(fs, f.flag, f.env)
	}
	cli.AddVersion(cmd, name)
	return cmd
}

func run(o *options, args []string) error {
	movedData := adoptOldFolder(o.dataDir)
	movedCache := adoptOldFolder(o.cacheDir)
	if err := os.MkdirAll(o.dataDir, 0o755); err != nil {
		return err
	}
	// The log also goes to a file in the data folder: the app has no
	// terminal.
	logFile := io.Discard
	if f, err := os.OpenFile(filepath.Join(o.dataDir, name+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		defer f.Close()
		logFile = f
	}
	log.SetOutput(io.MultiWriter(os.Stderr, logFile, logTo))
	logf := func(format string, args ...any) { log.Printf(format, args...) }
	for _, d := range []string{movedData, movedCache} {
		if d != "" {
			logf("moved %s to %s (the program's new name)", d, filepath.Dir(d)+string(filepath.Separator)+name)
		}
	}
	appMode := len(args) == 0

	// Started again while it runs: show the running one.
	page := fmt.Sprintf("http://%s/local", net.JoinHostPort(loopbackHost(o.host), fmt.Sprint(o.port)))
	if appMode && running(page) {
		logf("already running; opening %s", page)
		showPage(page)
		return nil
	}
	given, err := studyDirs(args)
	if err != nil {
		return cli.Usage(err)
	}
	if !appMode && len(given) == 0 {
		return cli.Usagef("no study folders (with a meta_study.txt) in %s", strings.Join(args, ", "))
	}
	folders := loadFolders(o.dataDir)
	folders.add(given)
	all := folders.list()

	seed, panels := o.seed, o.genePanels
	if len(all) > 0 && (seed == "" || panels == "") {
		s, p := findDatahubReference(all[0])
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
	// Without a datahub checkout, the reference genes and shared gene panels
	// are downloaded once, unless the data folder has them already.
	count := func(sql string) int {
		out, err := admin.Execute(sql)
		if err != nil {
			return 0
		}
		var n int
		fmt.Sscan(strings.TrimSpace(out), &n)
		return n
	}
	if seed == "" && count("SELECT count() FROM `"+refDB+"`.gene") == 0 {
		if seed, err = downloadSeed(o.cacheDir, logf); err != nil {
			return err
		}
	}
	if panels == "" && count("SELECT count() FROM `"+rawDB+"`.gene_panel WHERE study_id = ''") == 0 {
		if panels, err = downloadGenePanels(o.cacheDir, logf); err != nil {
			return err
		}
	}
	transform.Logf = logf
	imp := &importer{
		opts: studyimport.Options{
			URL: chdb.URL, DB: portalDB, RawDB: rawDB, RefDB: refDB,
			Seed: seed, GenePanels: panels, Logf: logf,
		},
		status:  newImportStatus(),
		folders: folders,
	}
	status := imp.status

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

	ctx, stop := signal.NotifyContext(baseCtx, syscall.SIGTERM, os.Interrupt)
	defer stop()
	status.quit = stop
	status.addFolder = func() error {
		dir, err := chooseFolder("Choose a study folder, or a folder of studies")
		if err != nil || dir == "" {
			return err
		}
		found, err := studyDirs([]string{dir})
		if err != nil {
			return err
		}
		if len(found) == 0 {
			return fmt.Errorf("%s has no study (no meta_study.txt in it or the folders inside it)", dir)
		}
		go imp.add(found)
		return nil
	}
	status.addPublic = func(ids []string) error {
		for _, id := range ids {
			if !studyIDPattern.MatchString(id) {
				return fmt.Errorf("%q is not a study id", id)
			}
		}
		go imp.addPublic(filepath.Join(o.dataDir, "public-studies"), ids)
		return nil
	}
	status.remove = func(dir string) error {
		return imp.remove(dir, filepath.Join(o.dataDir, "public-studies"))
	}

	ln, port, err := listen(o.host, o.port)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: status.handler(api.NewRouter(state))}
	go srv.Serve(ln)
	serving.Store(true)
	url := fmt.Sprintf("http://%s", net.JoinHostPort(loopbackHost(o.host), fmt.Sprint(port)))
	if o.host == "0.0.0.0" || o.host == "" {
		host, _ := os.Hostname()
		url = fmt.Sprintf("http://%s:%d", host, port)
	}
	logf("cBioPortal is at %s, and its import page at %s/local", url, url)
	if appMode {
		showPage(url + "/local")
	} else if o.open {
		showPage(url)
	}

	// Folders unchanged since their last import are served as they are;
	// the others are imported.
	var changed []string
	for _, d := range all {
		if folders.unchanged(d) {
			status.add([]string{d}, "imported")
			if last, ok := folders.last(d); ok {
				status.setLast(d, last.At, last.Seconds)
			}
		} else {
			changed = append(changed, d)
		}
	}
	go imp.add(changed)
	if o.watch {
		go watch(ctx, imp.watched, o.watchInterval, func(dirs []string) {
			logf("files changed in %s; importing again", strings.Join(dirs, ", "))
			imp.run(dirs)
		})
	}
	<-ctx.Done()
	logf("stopping")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// importer imports study folders one batch at a time, and records them in
// the wizard's status and the remembered folders.
type importer struct {
	mu      sync.Mutex
	opts    studyimport.Options
	status  *importStatus
	folders *folderStore

	// downloading are the public studies being downloaded, which the
	// watcher leaves alone: their files change until they are complete,
	// and they are imported then.
	downloadMu  sync.Mutex
	downloading map[string]bool
}

// watched are the listed study folders, less those being downloaded.
func (m *importer) watched() []string {
	m.downloadMu.Lock()
	defer m.downloadMu.Unlock()
	var out []string
	for _, d := range m.status.dirs() {
		if !m.downloading[d] {
			out = append(out, d)
		}
	}
	return out
}

func (m *importer) setDownloading(dir string, on bool) {
	m.downloadMu.Lock()
	defer m.downloadMu.Unlock()
	if m.downloading == nil {
		m.downloading = map[string]bool{}
	}
	if on {
		m.downloading[dir] = true
	} else {
		delete(m.downloading, dir)
	}
}

// add lists and remembers dirs, then imports them.
func (m *importer) add(dirs []string) {
	if len(dirs) == 0 {
		return
	}
	m.status.add(dirs, "waiting")
	m.folders.add(dirs)
	m.run(dirs)
}

// addPublic downloads public studies into dir and imports them.
func (m *importer) addPublic(dir string, ids []string) {
	var dirs []string
	for _, id := range ids {
		dest := filepath.Join(dir, id)
		m.setDownloading(dest, true)
		defer m.setDownloading(dest, false)
		m.status.set(dest, id, "downloading", "")
		got, err := downloadStudy(dir, id)
		if err != nil {
			log.Printf("downloading %s: %v", id, err)
			m.status.set(dest, id, "failed", err.Error())
			continue
		}
		dirs = append(dirs, got)
	}
	m.add(dirs)
}

// remove takes dir's study out of the portal and forgets the folder. A
// public study's download (in publicDir) is deleted too; a folder of the
// user's own is left as it is.
func (m *importer) remove(dir, publicDir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, err := studyimport.StudyID(dir)
	if err != nil {
		for _, st := range m.status.list() {
			if st.Dir == dir {
				id = st.StudyID
			}
		}
	}
	if id != "" {
		raw := ch.New(m.opts.URL, m.opts.RawDB)
		if _, err := ingest.RemoveStudy(raw, id); err != nil {
			return fmt.Errorf("removing %s: %w", id, err)
		}
		start := time.Now()
		if _, err := transform.Apply(raw, m.opts.RawDB, m.opts.DB, m.opts.RefDB); err != nil {
			return fmt.Errorf("rebuilding the portal without %s: %w", id, err)
		}
		log.Printf("removed %s from the portal in %s", id, time.Since(start).Round(time.Second))
	}
	m.status.drop(dir)
	m.folders.remove(dir)
	if rel, err := filepath.Rel(publicDir, dir); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("deleting the download %s: %v", dir, err)
		}
	}
	return nil
}

// run imports dirs, records the outcome and logs each study's; a study
// that fails is reported and left out, the others are served.
func (m *importer) run(dirs []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sigs := map[string]string{}
	for _, d := range dirs {
		sigs[d] = signature(d)
	}
	start := time.Now()
	m.status.start(dirs)
	res, err := studyimport.Run(m.opts, dirs)
	took := time.Since(start)
	m.status.done(dirs, res, err, took)
	if err != nil {
		log.Printf("import failed: %v", err)
		return
	}
	for _, s := range res.Studies {
		if s.Status == studyimport.Imported {
			m.folders.imported(s.Dir, sigs[s.Dir], lastImport{At: time.Now(), Seconds: took.Seconds()})
		}
		if s.Err != nil {
			log.Printf("  %s: %s: %v", s.StudyID, s.Status, s.Err)
		} else {
			log.Printf("  %s: %s", s.StudyID, s.Status)
		}
	}
	log.Printf("imported %d of %d studies in %s", res.Count(studyimport.Imported), len(dirs), time.Since(start).Round(time.Second))
}

// running reports whether a cbioportal-curate answers at its page URL.
func running(page string) bool {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(page + "/status")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// listen opens host:port, or the next free port of the 20 after it when
// another program has it.
func listen(host string, port int) (net.Listener, int, error) {
	var first error
	for p := port; p < port+20; p++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(p)))
		if err == nil {
			return ln, p, nil
		}
		if first == nil {
			first = err
		}
	}
	return nil, 0, first
}

// loopbackHost is the address to reach a server listening on host from
// this computer.
func loopbackHost(host string) string {
	if host == "" || host == "0.0.0.0" || host == "::" {
		return "127.0.0.1"
	}
	return host
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

// defaultDataDir keeps the databases where the system keeps an app's own
// data, not in a cache folder the system may empty: Application Support on
// macOS, $XDG_DATA_HOME (~/.local/share) elsewhere.
func defaultDataDir() string {
	if runtime.GOOS == "darwin" {
		if d, err := os.UserConfigDir(); err == nil {
			return filepath.Join(d, "cbioportal-curate")
		}
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "cbioportal-curate")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "cbioportal-curate")
}

// adoptOldFolder moves a default folder of the program's old name
// (cbioportal-local) to its new one, the studies, databases and the
// downloaded engine with it, when only the old one exists. It returns the
// old folder when it moved it. Folders given with --data and --cache keep
// their names.
func adoptOldFolder(dir string) string {
	if filepath.Base(dir) != name {
		return ""
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		return ""
	}
	old := filepath.Join(filepath.Dir(dir), oldName)
	if st, err := os.Stat(old); err != nil || !st.IsDir() {
		return ""
	}
	if err := os.Rename(old, dir); err != nil {
		return ""
	}
	// The log keeps its history under the new name.
	os.Rename(filepath.Join(dir, oldName+".log"), filepath.Join(dir, name+".log"))
	return old
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

// watch calls changed with the study folders (as dirs lists them at each
// look) whose files changed since the last look, once the changes have
// settled for one interval.
func watch(ctx context.Context, dirs func() []string, interval time.Duration, changed func([]string)) {
	last := map[string]string{}
	pending := map[string]string{}
	for _, d := range dirs() {
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
		for _, d := range dirs() {
			sig := signature(d)
			if _, seen := last[d]; !seen {
				last[d] = sig // added since; imported when added
				continue
			}
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

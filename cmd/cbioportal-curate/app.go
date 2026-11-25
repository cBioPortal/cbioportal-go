//go:build wails

// The macOS app: the same program in a window of its own (Wails), with a
// Dock icon, a menu and Cmd+Q, and the system's folder dialog. The portal
// is still served on its port, so a web browser can open it too.
//
//	make curate-app    # bin/cBioPortal Curate.app
//
// The window first shows a page of its own (starting.html) with what the
// program is doing, the downloads of a first run among it, and then the
// import page of the running portal.
package main

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/cbioportal/cbioportal-go/internal/chdb"
	"github.com/cbioportal/cbioportal-go/internal/cli"
)

//go:embed starting.html
var startingPage []byte

// linksJS runs in every page of the portal's windows. A WebKit view opens
// no new windows and saves no downloads by itself: links to other sites,
// and window.open to them, go to the web browser; the portal's own
// new-window links open a new tab (tabs_darwin.go); and downloads (links
// with a download attribute, or to blob: and data: URLs, as the portal
// makes for its tables and plots) are read here and handed to the app,
// which asks where to save them.
const linksJS = `(function () {
  if (window.__cbioportalCurate) return;
  window.__cbioportalCurate = true;
  var send = function (kind, u) { window.webkit.messageHandlers.external.postMessage(kind + " " + u); };
  var save = function (href, name) {
    fetch(href).then(function (r) { return r.blob(); }).then(function (b) {
      var fr = new FileReader();
      fr.onload = function () { send("save", JSON.stringify({ name: name || "download", data: fr.result })); };
      fr.readAsDataURL(b);
    }).catch(function (err) { console.error("cBioPortal Curate: could not save " + href, err); });
  };
  // A download from another site (a study's archive on datahub) cannot be
  // read here; it goes to the web browser like any link away.
  var isDownload = function (a) {
    if (!a.href) return false;
    if (/^(blob|data):/.test(a.href)) return true;
    return a.hasAttribute("download") && new URL(a.href, location.href).origin === location.origin;
  };
  var route = function (u) {
    var url = new URL(u, location.href);
    if (/^(blob|data):$/.test(url.protocol)) save(url.href, "download");
    else if (url.origin === location.origin) send("window", url.href);
    else if (/^(https?|mailto):$/.test(url.protocol)) send("open", url.href);
    else return false;
    return true;
  };
  // The page's title names its tab. The portal's own pages are all titled
  // "cBioPortal for Cancer Genomics" and change their address without
  // loading again, so their tabs are named from the address: the study, the
  // genes of a query, the patient. Wails runs this script once the page has
  // loaded, so DOMContentLoaded has usually passed already.
  var generic = "cBioPortal for Cancer Genomics";
  var studyNames = {};
  var studyName = function (ids) {
    ids = (ids || "").split(",").filter(Boolean);
    if (ids.length !== 1) return ids.length ? ids.length + " studies" : "";
    var id = ids[0];
    if (!(id in studyNames)) {
      studyNames[id] = id;
      fetch("/api/studies/" + encodeURIComponent(id)).then(function (r) { return r.ok ? r.json() : null; })
        .then(function (s) { if (s && s.name) { studyNames[id] = s.name; sendTitle(); } }).catch(function () {});
    }
    return studyNames[id];
  };
  var pageTitle = function () {
    var t = document.title.trim();
    if (t && t !== generic) return t;
    var q = new URLSearchParams(location.search), page = location.pathname.split("/")[1], study;
    if (page === "study") return studyName(q.get("id") || q.get("studyId")) || "Study";
    if (page === "results") {
      var genes = (q.get("gene_list") || "").split(/[\s,;]+/).map(function (g) { return g.split(":")[0]; }).filter(Boolean);
      if (genes.length > 3) genes = genes.slice(0, 3).concat("…");
      study = studyName(q.get("cancer_study_list") || q.get("cancer_study_id"));
      return [genes.join(" "), study].filter(Boolean).join(" · ") || "Query";
    }
    if (page === "patient") {
      study = studyName(q.get("studyId"));
      return [q.get("caseId") || q.get("sampleId") || "Patient", study].filter(Boolean).join(" · ");
    }
    if (page === "comparison") return "Comparison";
    return "cBioPortal";
  };
  var title = null;
  var sendTitle = function () { var t = pageTitle(); if (t !== title) { title = t; send("title", t); } };
  ["pushState", "replaceState"].forEach(function (k) {
    var f = history[k];
    history[k] = function () { var r = f.apply(this, arguments); sendTitle(); return r; };
  });
  window.addEventListener("popstate", sendTitle);
  var watchTitle = function () {
    sendTitle();
    new MutationObserver(sendTitle).observe(document.head, { subtree: true, childList: true, characterData: true });
  };
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", watchTitle);
  else watchTitle();
  var open = window.open;
  window.open = function (u) {
    if (u && route(String(u))) return null;
    return open.apply(window, arguments);
  };
  // Downloads made in code click a link that is not in the page
  // (FileSaver and the like), which a listener on the document misses.
  var click = HTMLAnchorElement.prototype.click;
  HTMLAnchorElement.prototype.click = function () {
    if (isDownload(this)) return save(this.href, this.getAttribute("download"));
    return click.apply(this, arguments);
  };
  var dispatch = HTMLAnchorElement.prototype.dispatchEvent;
  HTMLAnchorElement.prototype.dispatchEvent = function (e) {
    if (e && e.type === "click" && !this.isConnected && isDownload(this)) { save(this.href, this.getAttribute("download")); return false; }
    return dispatch.apply(this, arguments);
  };
  document.addEventListener("click", function (e) {
    var a = e.target.closest && e.target.closest("a[href]");
    if (!a || e.defaultPrevented) return;
    if (isDownload(a)) { e.preventDefault(); save(a.href, a.getAttribute("download")); return; }
    var url = new URL(a.href, location.href);
    var away = url.origin !== location.origin;
    var blank = a.target && a.target !== "_self" && a.target !== "_top" && a.target !== "_parent";
    if ((away || blank) && route(url.href)) e.preventDefault();
  }, true);
})();`

// progress is what starting.html shows: the log so far, the engine
// download, and how the run ended.
type progress struct {
	mu     sync.Mutex
	Lines  []string `json:"lines"`
	Done   int64    `json:"done"`
	Total  int64    `json:"total"`
	Failed string   `json:"failed,omitempty"`
}

func (p *progress) Write(b []byte) (int, error) {
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	p.mu.Lock()
	defer p.mu.Unlock()
	for sc.Scan() {
		p.Lines = append(p.Lines, sc.Text())
	}
	if n := len(p.Lines); n > 200 {
		p.Lines = p.Lines[n-200:]
	}
	return len(b), nil
}

func (p *progress) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/progress" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(startingPage)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(p)
}

func main() {
	var (
		p             = &progress{Total: -1}
		ctx, cancel   = context.WithCancel(context.Background())
		finished      = make(chan struct{})
		exitCode      int
		portal        string // the running portal's address, once it runs
		portalMu      sync.Mutex
		mainWindow    *application.WebviewWindow
		app           *application.App
		newPageWindow func(page string, hidden bool) *application.WebviewWindow
		newTab        func(parent application.Window, page string)
	)
	baseCtx, logTo = ctx, p
	chdb.Progress = func(done, total int64) {
		p.mu.Lock()
		p.Done, p.Total = done, total
		p.mu.Unlock()
	}

	app = application.New(application.Options{
		Name:        "cBioPortal Curate",
		Description: "cBioPortal on this computer, from study folders",
		Assets:      application.AssetOptions{Handler: p},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		// Cmd+Q, the menu's Quit and the Dock's: the portal stops the way
		// Ctrl-C stops it in a terminal, the databases closed.
		OnShutdown: func() {
			cancel()
			select {
			case <-finished:
			case <-time.After(15 * time.Second):
				log.Printf("still stopping after 15 s; quitting anyway")
			}
		},
		// Pages of the portal ask for new windows, the web browser and
		// saved files (linksJS); only the portal's own pages may open
		// windows or save files.
		RawMessageHandler: func(w application.Window, msg string, origin *application.OriginInfo) {
			kind, target, _ := strings.Cut(msg, " ")
			portalMu.Lock()
			base := portal
			portalMu.Unlock()
			if kind == "title" {
				if target = strings.TrimSpace(target); target == "" {
					target = "cBioPortal Curate"
				}
				w.SetTitle(target)
				return
			}
			if kind == "save" {
				if base != "" && origin != nil && origin.Origin == base {
					go saveDownload(app, w, target)
				}
				return
			}
			u, err := url.Parse(target)
			if err != nil {
				return
			}
			switch {
			case kind == "open" && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "mailto"):
				openBrowser(u.String())
			case kind == "window" && base != "" && strings.HasPrefix(u.String(), base+"/"):
				newTab(w, u.String())
			}
		},
	})

	newPageWindow = func(page string, hidden bool) *application.WebviewWindow {
		return app.Window.NewWithOptions(application.WebviewWindowOptions{
			Title: "cBioPortal Curate", Width: 1280, Height: 860, MinWidth: 720, MinHeight: 480,
			URL: page, JS: linksJS, DevToolsEnabled: true, Hidden: hidden,
		})
	}
	// A page opened from another opens as a tab of its window.
	newTab = func(parent application.Window, page string) {
		tab := newPageWindow(page, true)
		if p, ok := parent.(*application.WebviewWindow); ok {
			addTab(p, tab)
		} else {
			addTab(mainWindow, tab)
		}
	}
	mainWindow = newPageWindow("/", false)
	makeTabbable(mainWindow)

	showPage = func(page string) {
		if u, err := url.Parse(page); err == nil {
			portalMu.Lock()
			portal = u.Scheme + "://" + u.Host
			portalMu.Unlock()
		}
		mainWindow.SetURL(page)
		mainWindow.Show()
		mainWindow.Focus()
	}
	chooseFolder = func(prompt string) (string, error) {
		dir, err := app.Dialog.OpenFile().
			CanChooseDirectories(true).CanChooseFiles(false).CanCreateDirectories(false).
			SetTitle("Add studies").SetMessage(prompt).SetButtonText("Add").
			AttachToWindow(mainWindow).
			PromptForSingleSelection()
		return strings.TrimRight(dir, "/"), err
	}

	menu := app.Menu.New()
	menu.AddRole(application.AppMenu)
	file := menu.AddSubmenu("File")
	file.Add("New Tab").SetAccelerator("CmdOrCtrl+T").OnClick(func(*application.Context) {
		if base := currentPortal(&portalMu, &portal); base != "" {
			newTab(app.Window.Current(), base+"/")
		}
	})
	file.AddSeparator()
	file.Add("Import Page").SetAccelerator("CmdOrCtrl+I").OnClick(func(*application.Context) {
		if base := currentPortal(&portalMu, &portal); base != "" {
			w := app.Window.Current()
			if w == nil {
				w = mainWindow
			}
			w.SetURL(base + "/local")
			w.Focus()
		}
	})
	file.Add("Portal").SetAccelerator("CmdOrCtrl+Shift+H").OnClick(func(*application.Context) {
		if base := currentPortal(&portalMu, &portal); base != "" {
			w := app.Window.Current()
			if w == nil {
				w = mainWindow
			}
			w.SetURL(base + "/")
			w.Focus()
		}
	})
	file.AddSeparator()
	file.Add("Open in Web Browser").SetAccelerator("CmdOrCtrl+Shift+O").OnClick(func(*application.Context) {
		if w := app.Window.Current(); w != nil {
			w.ExecJS(`window.webkit.messageHandlers.external.postMessage("open " + location.href)`)
		}
	})
	file.AddSeparator()
	file.AddRole(application.CloseWindow)
	menu.AddRole(application.EditMenu)
	menu.AddRole(application.ViewMenu)
	menu.AddRole(application.WindowMenu)
	// Cmd+1 to Cmd+8 show those tabs, and Cmd+9 the last, as in Safari.
	if window := menu.FindByRole(application.WindowMenu); window != nil && window.GetSubmenu() != nil {
		tabs := window.GetSubmenu()
		tabs.AddSeparator()
		for i := 1; i <= 9; i++ {
			label, n := fmt.Sprintf("Show Tab %d", i), i-1
			if i == 9 {
				label, n = "Show Last Tab", -1
			}
			tabs.Add(label).SetAccelerator(fmt.Sprintf("CmdOrCtrl+%d", i)).OnClick(func(*application.Context) { selectTab(n) })
		}
	}
	app.Menu.Set(menu)

	go func() {
		defer close(finished)
		exitCode = cli.Execute(newRootCmd(), appArgs(os.Args[1:]), os.Stdout, io.MultiWriter(os.Stderr, p), os.Getenv)
		switch {
		case ctx.Err() != nil:
			// Quitting already.
		case exitCode != 0:
			p.mu.Lock()
			if p.Failed = "cBioPortal Curate stopped with an error."; len(p.Lines) > 0 {
				p.Failed = p.Lines[len(p.Lines)-1]
			}
			p.mu.Unlock()
			mainWindow.SetURL("/")
		case !serving.Load():
			// Another cbioportal-curate already serves the portal; the
			// window shows that one.
		default:
			// Stopped from the import page.
			app.Quit()
		}
	}()

	if err := app.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(exitCode)
}

func currentPortal(mu *sync.Mutex, portal *string) string {
	mu.Lock()
	defer mu.Unlock()
	return *portal
}

// appArgs drops what macOS adds when Finder opens an app (-psn_...). With
// study folders given, the window shows the portal (--open) once they are
// served; without, the import page.
func appArgs(args []string) []string {
	var out []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-psn_") {
			out = append(out, a)
		}
	}
	if len(out) > 0 {
		out = append(out, "--open")
	}
	return out
}

// saveDownload asks where to save a download a page of the portal made
// (linksJS sends its name and contents as a data: URL), and saves it.
func saveDownload(app *application.App, w application.Window, msg string) {
	var d struct{ Name, Data string }
	if err := json.Unmarshal([]byte(msg), &d); err != nil {
		log.Printf("saving a download: %v", err)
		return
	}
	body, err := decodeDataURL(d.Data)
	if err != nil {
		log.Printf("saving %s: %v", d.Name, err)
		return
	}
	home, _ := os.UserHomeDir()
	path, err := app.Dialog.SaveFile().
		SetFilename(filepath.Base(d.Name)).
		SetDirectory(filepath.Join(home, "Downloads")).
		CanCreateDirectories(true).
		AttachToWindow(w).
		PromptForSingleSelection()
	if err != nil || path == "" {
		return // cancelled
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		app.Dialog.Error().SetTitle("Could not save the file").SetMessage(err.Error()).Show()
		return
	}
	log.Printf("saved %s (%d bytes)", path, len(body))
}

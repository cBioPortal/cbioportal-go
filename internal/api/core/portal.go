package core

import (
	"encoding/json"
	"net/http"
	"strings"
)

// portal serves the Java portal's page (IndexPageController, index.html): it
// loads the frontend from the frontend.url property (www.cbioportal.org uses
// https://frontend.cbioportal.org/, released apart from the backend) through
// /js/load-frontend.js, or from this origin when frontend.url is unset. The
// page carries the frontend configuration as window.rawServerConfig, so the
// app starts without requesting /config_service.
func (s *State) portal(w http.ResponseWriter, r *http.Request) {
	var cfg map[string]any
	if s.PageConfig != nil {
		cfg = s.PageConfig(r)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(withSiteBanner(javaPortalPage(r.Host, cfg), s.Opts.SiteBanner, s.Opts.SiteBannerLink))
}

// javaPortalPage is index.html as the Java portal renders it for host, with
// the frontend configuration cfg. Values are written as JSON, which escapes
// <, > and &, so none can end a script element.
func javaPortalPage(host string, cfg map[string]any) []byte {
	str := func(k string) string {
		v, _ := cfg[k].(string)
		return v
	}
	js := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	raw := []byte("{}")
	if cfg != nil {
		raw, _ = json.Marshal(cfg)
	}
	appVersion := str("app_version")
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html class="cbioportal-frontend" lang="en">
<head>
`)
	if id := str("google_tag_manager_id"); id != "" {
		b.WriteString(`<script>
(function(w,d,s,l,i){w[l]=w[l]||[];w[l].push({'gtm.start':
new Date().getTime(),event:'gtm.js'});var f=d.getElementsByTagName(s)[0],
j=d.createElement(s),dl=l!='dataLayer'?'&l='+l:'';j.async=true;j.src=
'https://www.googletagmanager.com/gtm.js?id='+i+dl;f.parentNode.insertBefore(j,f);
})(window,document,'script','dataLayer',` + js(id) + `);
</script>
`)
	}
	b.WriteString(`<meta http-equiv="Content-type" content="text/html; charset=utf-8"/>
<link rel="icon" href="/images/cbioportal_icon.png"/>
<title>cBioPortal for Cancer Genomics</title>
<script>
var host = ` + js(host) + `;
window.frontendConfig = {
    configurationServiceUrl: "//" + host + "/config_service",
    appVersion: ` + js(appVersion) + `,
    apiRoot: "//" + host + "/",
    baseUrl: host,
    basePath: ""
};
window.rawServerConfig = ` + string(raw) + `;
// localdev can be "true" (port 3000) or a 4-digit port number, e.g. localdev=3001
var localdevParam = window.location.href.match(/localdev=(true|\d{4})\b/);
if (localdevParam) { localStorage.setItem("localdev", localdevParam[1]); }
if (/localdist=true/.test(window.location.href)) { localStorage.setItem("localdist", "true"); }
var localdevPort = /^\d{4}$/.test(localStorage.localdev) ? localStorage.localdev : "3000";
window.localdev = localStorage.localdev === "true" || /^\d{4}$/.test(localStorage.localdev);
window.localdist = localStorage.localdist === "true";
window.netlify = localStorage.netlify;
if (window.localdev || window.localdist) {
    window.frontendConfig.frontendUrl = "https://localhost:" + localdevPort + "/";
    localStorage.setItem("e2etest", "true");
} else if (window.netlify) {
    window.frontendConfig.frontendUrl = ["//", localStorage.netlify, ".netlify.app", "/"].join("");
    localStorage.setItem("e2etest", "true");
} else if (` + js(str("frontendUrl")) + `) {
    window.frontendConfig.frontendUrl = ` + js(str("frontendUrl")) + `;
}
if (!window.frontendConfig.frontendUrl) {
    window.frontendConfig.frontendUrl = "//" + host + "/";
}
</script>
<script src="/js/load-frontend.js?` + strings.NewReplacer(`"`, "%22", "<", "%3C", ">", "%3E", " ", "%20").Replace(appVersion) + `"></script>
<script>
window.frontendConfig.customTabs && window.frontendConfig.customTabs.forEach(function(tab){
    if (tab.pathsToJs) {
        tab.pathsToJs.forEach(function(src){
            document.write('<scr'+'ipt type="text/javascript" src="'+ src +'"></sc'+'ript>');
        });
    }
});
</script>
<script>
loadAppStyles(window.frontendConfig);
</script>
`)
	if dsn := str("frontendSentryEndpoint"); dsn != "" {
		b.WriteString(`<script src="https://browser.sentry-cdn.com/4.1.1/bundle.min.js" crossorigin="anonymous"></script>
<script>
var release = (typeof FRONTEND_COMMIT === "undefined") ? "norelease" : FRONTEND_COMMIT;
Sentry.init({ dsn: ` + js(dsn) + `, release: release, ignoreErrors: [/Request has been terminated/, /document\.registerElement is deprecated and will be removed/] });
</script>
`)
	}
	b.WriteString(fontAwesome4 + `
</head>
<body>
`)
	if id := str("google_tag_manager_id"); id != "" {
		b.WriteString(`<noscript><iframe src="https://www.googletagmanager.com/ns.html?id=` +
			strings.NewReplacer(`"`, "%22", "<", "%3C", ">", "%3E", "&", "%26").Replace(id) +
			`" height="0" width="0" style="display:none;visibility:hidden"></iframe></noscript>
`)
	}
	b.WriteString(`<script>
loadReactApp(window.frontendConfig);
</script>
<div id="reactRoot"></div>
<script></script>
</body>
</html>
`)
	return []byte(b.String())
}

// CatchAll serves every path no route matches. An /api/ path gets Spring's
// default error body, as Java answers it: 405 when the path is served for
// other methods, else 404. Other paths are the portal's pages, redirects and
// static resources (servePortal).
func (s *State) CatchAll(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
		ReadBody(r, nil)
		if allow := s.allowedMethods(r); len(allow) > 0 {
			w.Header().Set("Allow", strings.Join(allow, ", "))
			SpringError(w, r, http.StatusMethodNotAllowed)
			return
		}
		SpringNotFound(w, r)
		return
	}
	s.servePortal(w, r)
}

// allowedMethods lists the methods a route serves r's path for.
func (s *State) allowedMethods(r *http.Request) []string {
	var out []string
	if s.mux == nil {
		return out
	}
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		probe := r.Clone(r.Context())
		probe.Method = m
		if _, pattern := s.mux.Handler(probe); pattern != "/" {
			out = append(out, m)
		}
	}
	return out
}

// postedPage serves the portal page for a form POST with the posted parameters
// exposed as window.postData, for queries too long for a URL.
func (s *State) postedPage(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	params := map[string]string{}
	for k, v := range r.Form {
		if len(v) > 0 {
			params[k] = v[0]
		}
	}
	get := r.Clone(r.Context())
	get.Method = http.MethodGet
	get.Body = http.NoBody
	get.ContentLength = 0
	get.Header.Del("Content-Type")
	get.Header.Del("Accept-Encoding")
	rec := NewBufferedResponse()
	s.page(rec, get)
	data, _ := json.Marshal(params)
	script := "<script>var postData = " + string(data) + ";</script>"
	page := rec.Body.String()
	if i := strings.Index(page, "<head>"); i >= 0 {
		page = page[:i+len("<head>")] + script + page[i+len("<head>"):]
	} else {
		page = script + page
	}
	for k, v := range rec.header {
		if k != "Content-Length" {
			w.Header()[k] = v
		}
	}
	w.WriteHeader(rec.Status)
	w.Write([]byte(page))
}

package auth

import (
	"html/template"
	"io"
)

// loginTexts are the portal properties Java's login page (login.html,
// LoginPageController) shows: the page title, the message above the sign-in
// buttons and, after a refused login, how to get access. The messages are
// HTML written by the portal's operators.
type loginTexts struct {
	Title         string
	Message       template.HTML
	ContactHTML   template.HTML
	LogoutSuccess bool
	LoginError    bool
	Providers     []loginProvider
}

// loginProvider is one sign-in button: Google and Microsoft (registration
// azure) get Java's button images, other providers a labelled box.
type loginProvider struct {
	ID  string
	URL string
}

func loginTextsFrom(props map[string]string) loginTexts {
	return loginTexts{
		Title:       props["skin.title"],
		Message:     template.HTML(props["skin.authorization_message"]),
		ContactHTML: template.HTML(props["skin.login.contact_html"]),
	}
}

var loginPageTemplate = template.Must(template.New("login").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
body { margin: 0; padding: 20px 16px; text-align: center; color: #333; font-family: "Helvetica Neue", Helvetica, Arial, sans-serif; font-size: 14px; }
main { max-width: 900px; margin: 0 auto; }
.logo img, .provider img { max-width: 100%; }
.logo img { height: 50px; }
.notice { margin: 1em auto; padding: 0 .7em; border: 1px solid #fcefa1; background: #fbf9ee; }
.message { margin: 1.5em 0; line-height: 1.4; }
.provider { display: block; margin: 8px auto; }
.provider img { height: 50px; }
.provider .other { display: inline-block; min-width: 282px; min-height: 50px; line-height: 50px; border: 1px solid #000; background: lightgray; color: #000; }
</style>
</head>
<body>
<main>
<div class="logo"><a href="./"><img src="/images/cbioportal_logo.png" alt="cBioPortal Logo"></a></div>
{{if .LogoutSuccess}}<div class="notice"><p><strong>You are now signed out. It is recommended that you close your browser to complete the termination of this session.</strong></p></div>{{end}}
{{if .LoginError}}<div class="notice"><p><strong>You are not authorized to access this resource.&nbsp; {{.ContactHTML}}</strong></p></div>{{end}}
{{with .Message}}<p class="message">{{.}}</p>{{end}}
{{range .Providers}}<a class="provider" href="{{.URL}}">
{{- if eq .ID "google"}}<img src="/images/login/googleplus_signin.png" alt="Sign in with Google">
{{- else if eq .ID "azure"}}<img src="/images/login/microsoft_signin.png" alt="Sign in with Microsoft">
{{- else}}<span class="other">Sign in with: {{.ID}}</span>{{end -}}
</a>
{{end}}
{{if and .LogoutSuccess (not .Providers)}}<p><a href="/">Login cBioPortal</a></p>{{end}}
</main>
</body>
</html>
`))

func writeLoginPage(w io.Writer, t loginTexts) error {
	return loginPageTemplate.Execute(w, t)
}

package auth

import (
	"crypto/subtle"
	"errors"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// basicRegistration is the session's registration after a basic login.
const basicRegistration = "basic"

// basicLogin is one shared user with a username and password next to the
// provider sign-in, as Java's authenticate=saml_plus_basic adds to SAML
// (Saml2AndBasicConfig), for scripts that cannot follow a provider's login:
//   - GET or POST /j_spring_security_check with j_username and j_password
//     starts a session and redirects to /restful_login; a wrong user or
//     password redirects to /login?error.
//   - /j_spring_security_logout ends the session and redirects to /.
//
// The password is kept as a bcrypt hash (basic.password, as Java's
// BCryptPasswordEncoder reads it). basic.authorities are the user's roles,
// which decide the studies it may read like a provider's roles.
type basicLogin struct {
	user  string
	hash  []byte
	roles []string
}

func loadBasicLogin(props map[string]string) (*basicLogin, error) {
	user := strings.TrimSpace(props["basic.username"])
	if user == "" {
		return nil, nil
	}
	hash := []byte(strings.TrimSpace(props["basic.password"]))
	if _, err := bcrypt.Cost(hash); err != nil {
		return nil, errors.New("basic.password must be a bcrypt hash of the password (e.g. from htpasswd -nbB)")
	}
	b := &basicLogin{user: user, hash: hash}
	for _, r := range strings.Split(props["basic.authorities"], ",") {
		if r = strings.TrimSpace(r); r != "" {
			b.roles = append(b.roles, r)
		}
	}
	return b, nil
}

// check reports whether user and password are the basic user's.
func (b *basicLogin) check(user, password string) bool {
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(b.user)) == 1
	passwordOK := bcrypt.CompareHashAndPassword(b.hash, []byte(password)) == nil
	return userOK && passwordOK
}

// basicCheck serves /j_spring_security_check.
func (o *oauth2Login) basicCheck(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !o.basic.check(r.Form.Get("j_username"), r.Form.Get("j_password")) {
		core.LoginsTotal.Inc("failure")
		log.Printf("basic: login failed for %q", r.Form.Get("j_username"))
		http.Redirect(w, r, "/login?error", http.StatusFound)
		return
	}
	sess := &authSession{
		User: o.basic.user, Roles: o.basic.roles, Registration: basicRegistration,
		Expires: time.Now().Add(sessionTimeout),
	}
	id := randomToken()
	if err := o.put(r.Context(), sessionKey(id), sess, sessionTimeout); err != nil {
		log.Printf("basic: saving session: %v", err)
		http.Error(w, "login sessions unavailable", http.StatusServiceUnavailable)
		return
	}
	o.setCookie(w, r, sessionCookie, id, 0)
	core.LoginsTotal.Inc("success")
	http.Redirect(w, r, "/restful_login", http.StatusFound)
}

// basicLogout serves /j_spring_security_logout.
func (o *oauth2Login) basicLogout(w http.ResponseWriter, r *http.Request) {
	o.endSession(w, r)
	http.Redirect(w, r, "/", http.StatusFound)
}

var restfulLoginTemplate = template.Must(template.New("restful_login").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>{{.}}</title>
</head>
<body style="text-align: center; padding: 20px;">
<div><a href="./"><img src="/images/cbioportal_logo.png" alt="cBioPortal Logo" style="height: 50px"></a></div>
<p><strong>Connecting to the MSK cBioPortal...</strong></p>
</body>
</html>
`))

// restfulLogin serves /restful_login, the page a basic login lands on, as
// Java's SamlAndBasicLoginController.
func (o *oauth2Login) restfulLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html;charset=UTF-8")
	restfulLoginTemplate.Execute(w, o.cfg.Texts.Title)
}

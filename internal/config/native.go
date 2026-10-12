package config

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// A Setting is one of go's own names for a group of portal properties: the
// login and login-session settings, which the Java portal reads under
// Spring's names (spring.security.oauth2.client.*, spring.data.redis.*), and
// the address the frontend is loaded from (frontend.url, app.version).
// The server reads one model, the portal properties; a go setting is
// translated into the properties it stands for (Expand), so a Java portal's
// properties and go's names configure the same thing.
type Setting struct {
	Name  string // flag name, e.g. login-google-client-id
	Env   string // environment variable it falls back to
	Arg   string // value placeholder in -h output
	Usage string
	// Secret settings are read from Env only: a flag would show the value
	// to every user of the machine through the process list.
	Secret bool
	// Expand returns the portal properties the setting's value v stands
	// for; all holds every go setting's value, for settings that depend on
	// others (the OIDC registration id).
	Expand func(v string, all map[string]string) (map[string]string, error)
}

// SettingSection is a titled group of settings in -h output.
type SettingSection struct {
	Title    string
	Settings []Setting
}

const (
	oauthReg  = "spring.security.oauth2.client.registration."
	oauthProv = "spring.security.oauth2.client.provider."
	redis     = "spring.data.redis."
	// MicrosoftRegistration is the Spring registration id of Microsoft
	// sign-in. The provider returns to /login/oauth2/code/azure, the
	// redirect URI www.cbioportal.org's app registrations use.
	MicrosoftRegistration = "azure"
)

// loginValues maps the values of --login to authenticate's.
var loginValues = map[string]string{"off": "false", "optional": "optional_oauth2", "required": "oauth2"}

func one(key string) func(string, map[string]string) (map[string]string, error) {
	return func(v string, _ map[string]string) (map[string]string, error) {
		return map[string]string{key: v}, nil
	}
}

func boolean(name, key string) func(string, map[string]string) (map[string]string, error) {
	return func(v string, _ map[string]string) (map[string]string, error) {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("--%s=%s is not true or false", name, v)
		}
		return map[string]string{key: strconv.FormatBool(b)}, nil
	}
}

func oidcID(all map[string]string) string {
	if id := strings.TrimSpace(all["login-oidc-name"]); id != "" {
		return id
	}
	return "oidc"
}

func oidc(key func(id string) string) func(string, map[string]string) (map[string]string, error) {
	return func(v string, all map[string]string) (map[string]string, error) {
		return map[string]string{key(oidcID(all)): v}, nil
	}
}

// Settings are go's login, frontend-address and login-session settings, in
// -h order.
var Settings = []SettingSection{
	{"Login", []Setting{
		{Name: "login", Env: "CBIOPORTAL_LOGIN", Arg: "off|optional|required",
			Usage: "sign-in: off; optional (everyone may browse, signing in saves sessions); required",
			Expand: func(v string, _ map[string]string) (map[string]string, error) {
				a, ok := loginValues[strings.ToLower(strings.TrimSpace(v))]
				if !ok {
					return nil, fmt.Errorf("--login=%s is not off, optional or required", v)
				}
				return map[string]string{"authenticate": a}, nil
			}},
		{Name: "login-google-client-id", Env: "CBIOPORTAL_LOGIN_GOOGLE_CLIENT_ID", Arg: "id",
			Usage: "Google sign-in: the OAuth client's id; the user name is the email address",
			Expand: func(v string, _ map[string]string) (map[string]string, error) {
				return map[string]string{
					oauthReg + "google.client-id":            v,
					oauthProv + "google.user-name-attribute": "email",
				}, nil
			}},
		{Name: "login-google-client-secret", Env: "CBIOPORTAL_LOGIN_GOOGLE_CLIENT_SECRET", Secret: true,
			Usage:  "Google sign-in: the OAuth client's secret",
			Expand: one(oauthReg + "google.client-secret")},
		{Name: "login-microsoft-client-id", Env: "CBIOPORTAL_LOGIN_MICROSOFT_CLIENT_ID", Arg: "id",
			Usage: "Microsoft sign-in, work and personal accounts: the app registration's client id;\n    \tthe user name is the email address",
			Expand: func(v string, _ map[string]string) (map[string]string, error) {
				const ms = "https://login.microsoftonline.com/common"
				reg, prov := oauthReg+MicrosoftRegistration+".", oauthProv+MicrosoftRegistration+"."
				return map[string]string{
					reg + "client-id":            v,
					reg + "scope":                "openid,profile,email",
					prov + "user-name-attribute": "email",
					prov + "authorization-uri":   ms + "/oauth2/v2.0/authorize",
					prov + "token-uri":           ms + "/oauth2/v2.0/token",
					prov + "jwk-set-uri":         ms + "/discovery/v2.0/keys",
				}, nil
			}},
		{Name: "login-microsoft-client-secret", Env: "CBIOPORTAL_LOGIN_MICROSOFT_CLIENT_SECRET", Secret: true,
			Usage:  "Microsoft sign-in: the app registration's client secret",
			Expand: one(oauthReg + MicrosoftRegistration + ".client-secret")},
		{Name: "login-oidc-issuer", Env: "CBIOPORTAL_LOGIN_OIDC_ISSUER", Arg: "url",
			Usage:  "any OpenID Connect provider (e.g. Keycloak): its issuer URL, e.g.\n    \thttps://keycloak.example.org/realms/cbio",
			Expand: oidc(func(id string) string { return oauthProv + id + ".issuer-uri" })},
		{Name: "login-oidc-client-id", Env: "CBIOPORTAL_LOGIN_OIDC_CLIENT_ID", Arg: "id",
			Usage:  "OpenID Connect provider: the client's id",
			Expand: oidc(func(id string) string { return oauthReg + id + ".client-id" })},
		{Name: "login-oidc-client-secret", Env: "CBIOPORTAL_LOGIN_OIDC_CLIENT_SECRET", Secret: true,
			Usage:  "OpenID Connect provider: the client's secret",
			Expand: oidc(func(id string) string { return oauthReg + id + ".client-secret" })},
		{Name: "login-oidc-name", Env: "CBIOPORTAL_LOGIN_OIDC_NAME", Arg: "name",
			Usage: "OpenID Connect provider: its name in the sign-in address\n    \t/login/oauth2/code/<name> (default oidc)",
			Expand: func(string, map[string]string) (map[string]string, error) {
				return nil, nil // read by the other login-oidc-* settings
			}},
		{Name: "login-oidc-scopes", Env: "CBIOPORTAL_LOGIN_OIDC_SCOPES", Arg: "list",
			Usage:  "OpenID Connect provider: comma-separated scopes (default openid)",
			Expand: oidc(func(id string) string { return oauthReg + id + ".scope" })},
		{Name: "login-oidc-user-name-claim", Env: "CBIOPORTAL_LOGIN_OIDC_USER_NAME_CLAIM", Arg: "claim",
			Usage:  "OpenID Connect provider: the ID token field that names the user (default sub), e.g. email",
			Expand: oidc(func(id string) string { return oauthProv + id + ".user-name-attribute" })},
		{Name: "login-roles-path", Env: "CBIOPORTAL_LOGIN_ROLES_PATH", Arg: "path",
			Usage:  "where a login token lists the user's roles, as claim::claim::...\n    \t(default resource_access::cbioportal::roles)",
			Expand: one("spring.security.oauth2.client.jwt-roles-path")},
		{Name: "study-authorization", Env: "CBIOPORTAL_STUDY_AUTHORIZATION", Arg: "bool",
			Usage:  "with --login=optional: limit studies to the user's roles (always on with required)",
			Expand: boolean("study-authorization", "security.method_authorization_enabled")},
		{Name: "login-auto-redirect", Env: "CBIOPORTAL_LOGIN_AUTO_REDIRECT", Arg: "bool",
			Usage:  "with one sign-in provider, go straight to it instead of listing it (default true)",
			Expand: boolean("login-auto-redirect", "security.force_redirect_on_one_idp")},
	}},
	{"Frontend from another address", []Setting{
		{Name: "frontend-assets-url", Env: "CBIOPORTAL_FRONTEND_ASSETS_URL", Arg: "url",
			Usage:  "load the frontend's scripts and styles from this address, as www.cbioportal.org\n    \tdoes from https://frontend.cbioportal.org/; wins over --frontend-dir",
			Expand: one("frontend.url")},
		{Name: "frontend-assets-version", Env: "CBIOPORTAL_FRONTEND_ASSETS_VERSION", Arg: "text",
			Usage:  "added to the frontend's script and style addresses, so browsers fetch a new\n    \trelease instead of a cached one, e.g. its release date",
			Expand: one("app.version")},
	}},
	{"Login sessions", []Setting{
		{Name: "login-session-store", Env: "CBIOPORTAL_LOGIN_SESSION_STORE", Arg: "memory|redis",
			Usage: "where signed-in users' sessions are kept; redis keeps them across restarts and\n    \tshares them between replicas (default memory)",
			Expand: func(v string, _ map[string]string) (map[string]string, error) {
				switch t := strings.ToLower(strings.TrimSpace(v)); t {
				case "memory":
					return map[string]string{"spring.session.store-type": "none"}, nil
				case "redis":
					return map[string]string{"spring.session.store-type": t}, nil
				}
				return nil, fmt.Errorf("--login-session-store=%s is not memory or redis", v)
			}},
		{Name: "redis-url", Env: "CBIOPORTAL_REDIS_URL", Arg: "url",
			Usage: "Redis of the login sessions as redis:// or rediss:// (TLS) URL, instead of\n    \t--redis-host and --redis-port", Expand: one(redis + "url")},
		{Name: "redis-host", Env: "CBIOPORTAL_REDIS_HOST", Arg: "host", Usage: "Redis host (default localhost)", Expand: one(redis + "host")},
		{Name: "redis-port", Env: "CBIOPORTAL_REDIS_PORT", Arg: "port", Usage: "Redis port (default 6379)", Expand: one(redis + "port")},
		{Name: "redis-username", Env: "CBIOPORTAL_REDIS_USERNAME", Arg: "user", Usage: "Redis user (ACL)", Expand: one(redis + "username")},
		{Name: "redis-password", Env: "CBIOPORTAL_REDIS_PASSWORD", Secret: true, Usage: "Redis password", Expand: one(redis + "password")},
		{Name: "redis-database", Env: "CBIOPORTAL_REDIS_DATABASE", Arg: "n", Usage: "Redis database number (default 0)", Expand: one(redis + "database")},
		{Name: "redis-tls", Env: "CBIOPORTAL_REDIS_TLS", Arg: "bool", Usage: "connect to Redis with TLS", Expand: boolean("redis-tls", redis+"ssl.enabled")},
		{Name: "redis-timeout", Env: "CBIOPORTAL_REDIS_TIMEOUT", Arg: "duration", Usage: "Redis connect and command timeout (default 5s)", Expand: one(redis + "timeout")},
	}},
}

// AllSettings lists every go setting.
func AllSettings() []Setting {
	var out []Setting
	for _, s := range Settings {
		out = append(out, s.Settings...)
	}
	return out
}

// LookupSetting returns the go setting named name.
func LookupSetting(name string) (Setting, bool) {
	for _, s := range AllSettings() {
		if s.Name == name {
			return s, true
		}
	}
	return Setting{}, false
}

// ApplySettings adds the portal properties that the set go settings
// (values, by name; unset ones are empty) stand for to props, and returns
// for each property it set the go setting it came from. A property that
// props already has with another value is an error naming both: the two
// spellings of one setting must agree.
func ApplySettings(props, values map[string]string) (map[string]string, error) {
	from := map[string]string{}
	for _, s := range AllSettings() {
		v := strings.TrimSpace(values[s.Name])
		if v == "" {
			continue
		}
		expanded, err := s.Expand(v, values)
		if err != nil {
			return nil, err
		}
		for _, k := range slices.Sorted(maps.Keys(expanded)) {
			nv := expanded[k]
			if old, ok := props[k]; ok && strings.TrimSpace(old) != nv {
				if other, ok := from[k]; ok {
					return nil, fmt.Errorf("--%s and --%s set %s differently", other, s.Name, k)
				}
				shown := nv
				if s.Secret || Secret(k) {
					shown = "(another value)"
				}
				return nil, fmt.Errorf("--%s sets %s=%s, but the portal properties set it to %s; set it one way",
					s.Name, k, shown, Redact(k, old))
			}
			props[k] = nv
			from[k] = s.Name
		}
	}
	return from, nil
}

// JavaName returns the portal property that holds the setting's own value
// in the Java portal's properties ("authenticate" for --login), or "" for
// a setting without one (--login-oidc-name).
func JavaName(s Setting, all map[string]string) string {
	for _, probe := range []string{"x", "optional", "true", "redis"} {
		props, err := s.Expand(probe, all)
		if err != nil {
			continue
		}
		if len(props) == 1 {
			for k := range props {
				return k
			}
		}
		for k, v := range props {
			if v == probe {
				return k
			}
		}
		return ""
	}
	return ""
}

// Secret reports whether a setting or property name holds a credential,
// whose value is never printed.
func Secret(name string) bool {
	n := strings.ToLower(name)
	if i := strings.LastIndexByte(n, '.'); i >= 0 {
		n = n[i+1:]
	}
	for _, w := range []string{"password", "secret", "api-key", "apikey", "api_key", "private"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return strings.HasSuffix(n, "token")
}

// Redact returns v, or a placeholder when name holds a credential.
func Redact(name, v string) string {
	if v != "" && Secret(name) {
		return "(redacted)"
	}
	return v
}

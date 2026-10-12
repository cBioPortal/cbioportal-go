package config

import (
	"maps"
	"strings"
	"testing"
)

// Each go setting and the Java portal's properties it stands for configure
// the same thing: applying the go names gives exactly the Java portal's
// properties.
func TestSettingsMatchJavaProperties(t *testing.T) {
	const reg, prov = "spring.security.oauth2.client.registration.", "spring.security.oauth2.client.provider."
	for _, tc := range []struct {
		name string
		goes map[string]string
		java map[string]string
	}{
		{"login", map[string]string{"login": "optional"}, map[string]string{"authenticate": "optional_oauth2"}},
		{"login required", map[string]string{"login": "Required"}, map[string]string{"authenticate": "oauth2"}},
		{"login off", map[string]string{"login": "off"}, map[string]string{"authenticate": "false"}},
		{"google", map[string]string{"login-google-client-id": "gid", "login-google-client-secret": "gs"}, map[string]string{
			reg + "google.client-id": "gid", reg + "google.client-secret": "gs", prov + "google.user-name-attribute": "email"}},
		// www.cbioportal.org's Microsoft registration.
		{"microsoft", map[string]string{"login-microsoft-client-id": "mid", "login-microsoft-client-secret": "ms"}, map[string]string{
			reg + "azure.client-id":            "mid",
			reg + "azure.client-secret":        "ms",
			reg + "azure.scope":                "openid,profile,email",
			prov + "azure.user-name-attribute": "email",
			prov + "azure.authorization-uri":   "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			prov + "azure.token-uri":           "https://login.microsoftonline.com/common/oauth2/v2.0/token",
			prov + "azure.jwk-set-uri":         "https://login.microsoftonline.com/common/discovery/v2.0/keys"}},
		// test/e2e_api.sh's Keycloak.
		{"oidc", map[string]string{"login-oidc-name": "keycloak", "login-oidc-issuer": "http://kc/realms/cbio",
			"login-oidc-client-id": "cbioportal", "login-oidc-client-secret": "s", "login-oidc-scopes": "openid,email",
			"login-oidc-user-name-claim": "email"}, map[string]string{
			prov + "keycloak.issuer-uri": "http://kc/realms/cbio", reg + "keycloak.client-id": "cbioportal",
			reg + "keycloak.client-secret": "s", reg + "keycloak.scope": "openid,email",
			prov + "keycloak.user-name-attribute": "email"}},
		{"oidc default name", map[string]string{"login-oidc-client-id": "c"}, map[string]string{reg + "oidc.client-id": "c"}},
		{"authorization", map[string]string{"study-authorization": "TRUE", "login-auto-redirect": "false",
			"login-roles-path": "realm_access::roles"}, map[string]string{
			"security.method_authorization_enabled": "true", "security.force_redirect_on_one_idp": "false",
			"spring.security.oauth2.client.jwt-roles-path": "realm_access::roles"}},
		{"redis", map[string]string{"login-session-store": "redis", "redis-host": "h", "redis-port": "6380",
			"redis-username": "u", "redis-password": "p", "redis-database": "2", "redis-tls": "true", "redis-timeout": "3s"},
			map[string]string{"spring.session.store-type": "redis", "spring.data.redis.host": "h", "spring.data.redis.port": "6380",
				"spring.data.redis.username": "u", "spring.data.redis.password": "p", "spring.data.redis.database": "2",
				"spring.data.redis.ssl.enabled": "true", "spring.data.redis.timeout": "3s"}},
		{"redis url", map[string]string{"redis-url": "rediss://h:6379"}, map[string]string{"spring.data.redis.url": "rediss://h:6379"}},
		{"memory sessions", map[string]string{"login-session-store": "memory"}, map[string]string{"spring.session.store-type": "none"}},
	} {
		props := map[string]string{}
		from, err := ApplySettings(props, tc.goes)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !maps.Equal(props, tc.java) {
			t.Errorf("%s: properties\n %v, want\n %v", tc.name, props, tc.java)
		}
		for k := range props {
			if from[k] == "" {
				t.Errorf("%s: %s has no source", tc.name, k)
			}
		}
		// The same values under both names agree.
		both := maps.Clone(tc.java)
		if _, err := ApplySettings(both, tc.goes); err != nil {
			t.Errorf("%s with the Java properties too: %v", tc.name, err)
		}
	}
}

func TestSettingsConflicts(t *testing.T) {
	for _, tc := range []struct {
		props, goes map[string]string
		want        string
	}{
		{map[string]string{"authenticate": "oauth2"}, map[string]string{"login": "optional"},
			"--login sets authenticate=optional_oauth2, but the portal properties set it to oauth2"},
		{map[string]string{"spring.data.redis.password": "old"}, map[string]string{"redis-password": "new"},
			"--redis-password sets spring.data.redis.password=(another value), but the portal properties set it to (redacted)"},
		{map[string]string{}, map[string]string{"login": "maybe"}, "--login=maybe is not off, optional or required"},
		{map[string]string{}, map[string]string{"redis-tls": "yes"}, "--redis-tls=yes is not true or false"},
		{map[string]string{}, map[string]string{"login-session-store": "file"}, "--login-session-store=file is not memory or redis"},
	} {
		_, err := ApplySettings(tc.props, tc.goes)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v over %v: err = %v, want %q", tc.goes, tc.props, err, tc.want)
		}
		if err != nil && (strings.Contains(err.Error(), "old") || strings.Contains(err.Error(), "new")) {
			t.Errorf("a secret in the error: %v", err)
		}
	}
}

func TestJavaName(t *testing.T) {
	for name, want := range map[string]string{
		"login":                     "authenticate",
		"login-google-client-id":    "spring.security.oauth2.client.registration.google.client-id",
		"login-microsoft-client-id": "spring.security.oauth2.client.registration.azure.client-id",
		"login-oidc-issuer":         "spring.security.oauth2.client.provider.oidc.issuer-uri",
		"study-authorization":       "security.method_authorization_enabled",
		"login-session-store":       "spring.session.store-type",
		"redis-host":                "spring.data.redis.host",
		"login-oidc-name":           "",
	} {
		s, ok := LookupSetting(name)
		if !ok {
			t.Fatalf("no setting %s", name)
		}
		if got := JavaName(s, map[string]string{}); got != want {
			t.Errorf("JavaName(%s) = %q, want %q", name, got, want)
		}
	}
}

func TestSecret(t *testing.T) {
	for name, want := range map[string]bool{
		"spring.data.redis.password": true, "login-google-client-secret": true, "session.endpoint.publisher-api-key": true,
		"ONCOKB_TOKEN": true, "spring.security.oauth2.client.provider.azure.token-uri": false,
		"skin.title": false, "login-google-client-id": false,
	} {
		if Secret(name) != want {
			t.Errorf("Secret(%s) = %v", name, !want)
		}
	}
}

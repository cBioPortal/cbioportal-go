package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// AuthConfig is read from the environment at startup.
type AuthConfig struct {
	Enabled bool
	Secret  []byte // HS256 shared secret
	// DefaultUser, when set, is the principal of every request that carries no
	// verified token — a single-user development login.
	DefaultUser string
}

func loadAuthConfig() AuthConfig {
	return AuthConfig{
		Enabled:     strings.EqualFold(os.Getenv("AUTH_ENABLED"), "true"),
		Secret:      []byte(os.Getenv("AUTH_JWT_SECRET")),
		DefaultUser: os.Getenv("AUTH_DEFAULT_USER"),
	}
}

// auth wraps a handler with bearer-token authentication. It is DISABLED by
// default (AUTH_ENABLED unset/false), so local and e2e runs — which use no
// session auth — work unchanged. When enabled it validates an HS256-signed JWT
// (shared secret) and extracts the user + Keycloak realm roles into the request
// context. Bearer tokens of the login's OpenID provider are data access
// tokens instead (dat.method=oauth2, dat.go), which excludes AUTH_ENABLED.
func auth(cfg AuthConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := core.GetUser(r.Context()); ok {
			next.ServeHTTP(w, r) // logged in (authenticate=oauth2)
			return
		}
		if !cfg.Enabled {
			if cfg.DefaultUser != "" {
				r = core.WithUser(r, core.User{Name: cfg.DefaultUser})
			}
			next.ServeHTTP(w, r)
			return
		}
		tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		user, err := verifyHS256(tok, cfg.Secret)
		if err != nil {
			// An empty 401, as Java's HttpStatusEntryPoint answers a refused
			// API token; the reason goes to the server log.
			slog.Warn("bearer token refused", slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.Any("error", err))
			core.ReadBody(r, nil)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, core.WithUser(r, user))
	})
}

func verifyHS256(token string, secret []byte) (core.User, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(secret) == 0 {
		return core.User{}, fmt.Errorf("malformed token or no secret configured")
	}
	signing := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return core.User{}, fmt.Errorf("bad signature encoding")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signing))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return core.User{}, fmt.Errorf("signature mismatch")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return core.User{}, fmt.Errorf("bad payload encoding")
	}
	var claims struct {
		Sub               string `json:"sub"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Exp               int64  `json:"exp"`
		RealmAccess       struct {
			Roles []string `json:"roles"`
		} `json:"realm_access"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return core.User{}, fmt.Errorf("bad claims")
	}
	if claims.Exp > 0 && time.Now().Unix() > claims.Exp {
		return core.User{}, fmt.Errorf("token expired")
	}
	name := claims.PreferredUsername
	if name == "" {
		name = claims.Name
	}
	if name == "" {
		name = claims.Sub
	}
	return core.User{Name: name, Roles: claims.RealmAccess.Roles}, nil
}

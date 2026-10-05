package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// User is the authenticated principal extracted from a bearer token.
type User struct {
	Name  string
	Roles []string
}

type userCtxKey struct{}

// GetUser returns the authenticated user from the request context, if any.
func GetUser(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userCtxKey{}).(User)
	return u, ok
}

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
		if _, ok := GetUser(r.Context()); ok {
			next.ServeHTTP(w, r) // logged in (authenticate=oauth2)
			return
		}
		if !cfg.Enabled {
			if cfg.DefaultUser != "" {
				r = withUser(r, User{Name: cfg.DefaultUser})
			}
			next.ServeHTTP(w, r)
			return
		}
		tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		user, err := verifyHS256(tok, cfg.Secret)
		if err != nil {
			http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, withUser(r, user))
	})
}

func verifyHS256(token string, secret []byte) (User, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(secret) == 0 {
		return User{}, fmt.Errorf("malformed token or no secret configured")
	}
	signing := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return User{}, fmt.Errorf("bad signature encoding")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signing))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return User{}, fmt.Errorf("signature mismatch")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return User{}, fmt.Errorf("bad payload encoding")
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
		return User{}, fmt.Errorf("bad claims")
	}
	if claims.Exp > 0 && time.Now().Unix() > claims.Exp {
		return User{}, fmt.Errorf("token expired")
	}
	name := claims.PreferredUsername
	if name == "" {
		name = claims.Name
	}
	if name == "" {
		name = claims.Sub
	}
	return User{Name: name, Roles: claims.RealmAccess.Roles}, nil
}

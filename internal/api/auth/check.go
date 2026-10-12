package auth

import (
	"context"
	"fmt"
)

// CheckResult is the outcome of one check of the login's services.
type CheckResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Check reaches every service the login depends on: each sign-in
// provider's metadata (OpenID discovery, when configured by issuer) and
// signing keys, and the login session store.
func (l *Login) Check(ctx context.Context) []CheckResult {
	var out []CheckResult
	for _, reg := range l.login.cfg.Registrations {
		r := CheckResult{Name: "login provider " + reg.ID}
		meta, keys, err := l.login.provider(reg)
		if err == nil {
			var n int
			if n, err = keyCount(keys); err == nil {
				r.OK = true
				r.Detail = fmt.Sprintf("%d signing keys at %s, client %s", n, meta.JWKSURI, reg.ClientID)
			}
		}
		if err != nil {
			r.Detail = err.Error()
		}
		out = append(out, r)
	}
	r := CheckResult{Name: "login sessions", Detail: l.login.store.describe()}
	if _, _, err := l.login.store.get(ctx, loginKeyPrefix+"check"); err != nil {
		r.Detail += ": " + err.Error()
	} else {
		r.OK = true
	}
	return append(out, r)
}

func keyCount(k *jwks) (int, error) {
	keys, err := k.fetch()
	if err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, fmt.Errorf("no RSA signing keys at %s", k.url)
	}
	return len(keys), nil
}

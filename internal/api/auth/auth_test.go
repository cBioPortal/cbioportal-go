package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func makeToken(secret []byte, claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	head := enc(map[string]string{"alg": "HS256", "typ": "JWT"})
	body := enc(claims)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(head + "." + body))
	return head + "." + body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestVerifyHS256(t *testing.T) {
	secret := []byte("s3cr3t")
	tok := makeToken(secret, map[string]any{
		"preferred_username": "alice",
		"exp":                time.Now().Add(time.Hour).Unix(),
		"realm_access":       map[string]any{"roles": []string{"cbioportal:study_a"}},
	})
	u, err := verifyHS256(tok, secret)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if u.Name != "alice" || len(u.Roles) != 1 || u.Roles[0] != "cbioportal:study_a" {
		t.Errorf("bad user: %+v", u)
	}

	if _, err := verifyHS256(tok, []byte("wrong")); err == nil {
		t.Error("wrong secret accepted")
	}
	expired := makeToken(secret, map[string]any{"sub": "bob", "exp": time.Now().Add(-time.Hour).Unix()})
	if _, err := verifyHS256(expired, secret); err == nil {
		t.Error("expired token accepted")
	}
}

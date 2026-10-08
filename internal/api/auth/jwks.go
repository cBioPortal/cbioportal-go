package auth

import (
	"crypto"
	"crypto/rsa"
	_ "crypto/sha256"
	_ "crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// jwks verifies RSA-signed JWTs (RS256, RS384, RS512) against an OpenID
// provider's JSON Web Key Set, fetched on first use and again when a token
// names an unknown key id (the provider rotated its keys).
type jwks struct {
	url    string
	client *http.Client

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

func newJWKS(url string, client *http.Client) *jwks {
	return &jwks{url: url, client: client}
}

// verify checks the signature of token and returns its decoded claims. It
// does not check the claims themselves (issuer, audience, expiry, nonce).
func (k *jwks) verify(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &header); err != nil {
		return nil, fmt.Errorf("token header: %w", err)
	}
	var hash crypto.Hash
	switch header.Alg {
	case "RS256":
		hash = crypto.SHA256
	case "RS384":
		hash = crypto.SHA384
	case "RS512":
		hash = crypto.SHA512
	default:
		return nil, fmt.Errorf("unsupported token algorithm %q", header.Alg)
	}
	key, err := k.key(header.Kid)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("token signature encoding")
	}
	h := hash.New()
	h.Write([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, hash, h.Sum(nil), sig); err != nil {
		return nil, errors.New("token signature mismatch")
	}
	var claims map[string]any
	if err := decodeSegment(parts[1], &claims); err != nil {
		return nil, fmt.Errorf("token claims: %w", err)
	}
	return claims, nil
}

// key returns the public key with id kid, refetching the key set (at most
// every 10 seconds) when the id is unknown.
func (k *jwks) key(kid string) (*rsa.PublicKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if key := k.lookup(kid); key != nil {
		return key, nil
	}
	if time.Since(k.fetched) < 10*time.Second && k.keys != nil {
		return nil, fmt.Errorf("unknown token key %q", kid)
	}
	keys, err := k.fetch()
	k.fetched = time.Now()
	if err != nil {
		return nil, err
	}
	k.keys = keys
	if key := k.lookup(kid); key != nil {
		return key, nil
	}
	return nil, fmt.Errorf("unknown token key %q", kid)
}

// lookup finds kid among the cached keys; a token without a key id matches a
// key set holding a single key.
func (k *jwks) lookup(kid string) *rsa.PublicKey {
	if kid == "" && len(k.keys) == 1 {
		for _, key := range k.keys {
			return key
		}
	}
	return k.keys[kid]
}

func (k *jwks) fetch() (map[string]*rsa.PublicKey, error) {
	resp, err := k.client.Get(k.url)
	if err != nil {
		return nil, fmt.Errorf("fetching JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching JWKS: HTTP %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, fmt.Errorf("decoding JWKS: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, key := range set.Keys {
		if key.Kty != "RSA" || (key.Use != "" && key.Use != "sig") {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(key.N)
		e, err2 := base64.RawURLEncoding.DecodeString(key.E)
		if err1 != nil || err2 != nil || len(e) > 4 {
			continue
		}
		keys[key.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	return keys, nil
}

func decodeSegment(seg string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

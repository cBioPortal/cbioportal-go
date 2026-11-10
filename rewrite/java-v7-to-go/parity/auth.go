package parity

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

// basicAuth is the user and password sent to an origin behind HTTP basic
// auth, such as the private Java reference. The zero value sends nothing.
type basicAuth struct{ user, password string }

func (c basicAuth) set(req *http.Request) {
	if c.user != "" {
		req.SetBasicAuth(c.user, c.password)
	}
}

// readBasicAuth reads user's password from the first line of file. No
// user means no basic auth.
func readBasicAuth(user, file string) (basicAuth, error) {
	if user == "" {
		return basicAuth{}, nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return basicAuth{}, err
	}
	password, _, _ := strings.Cut(string(b), "\n")
	password = strings.TrimSuffix(password, "\r")
	if password == "" {
		return basicAuth{}, fmt.Errorf("%s: empty", file)
	}
	return basicAuth{user, password}, nil
}

package core

import (
	"context"
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

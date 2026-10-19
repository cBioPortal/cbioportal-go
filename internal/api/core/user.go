package core

import (
	"context"
	"net/http"
)

// User is the authenticated principal, from a bearer token, an OAuth2
// session, a basic login or a data access token.
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

type readableCtxKey struct{}

// WithStudyReadable records which studies the request's caller may read.
func WithStudyReadable(r *http.Request, readable func(studyID string) bool) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), readableCtxKey{}, readable))
}

// StudyReadable reports whether the request's caller may read the study.
// Without a recorded study policy every study is readable.
func StudyReadable(r *http.Request, studyID string) bool {
	readable, ok := r.Context().Value(readableCtxKey{}).(func(string) bool)
	return !ok || readable(studyID)
}

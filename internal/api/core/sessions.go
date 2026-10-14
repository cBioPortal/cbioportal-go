package core

import (
	"encoding/json"
	"log"
	"os"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/sessionstore"
)

// ConfigureSessions selects the session store: cBioPortal's session service
// when session.service.url is set — with basic authentication when
// session.service.password is — else the built-in store (SESSION_STORE_FILE).
// The session.service.* properties fall back to the environment variables
// SESSION_SERVICE_URL, SESSION_SERVICE_USER and SESSION_SERVICE_PASSWORD,
// as Spring's relaxed binding reads them for the Java portal.
func (s *State) ConfigureSessions() error {
	prop := func(key, env string) string {
		if v := s.Props[key]; strings.TrimSpace(v) != "" {
			return v
		}
		return os.Getenv(env)
	}
	base := strings.TrimSpace(prop("session.service.url", "SESSION_SERVICE_URL"))
	if base == "" {
		s.SessionStore()
		if f := s.Opts.SessionStoreFile; f != "" {
			log.Printf("sessions: built-in store, persisted to %s", f)
		} else {
			log.Print("sessions: built-in store, in memory")
		}
		return nil
	}
	st, err := sessionstore.NewRemoteStore(base,
		prop("session.service.user", "SESSION_SERVICE_USER"),
		prop("session.service.password", "SESSION_SERVICE_PASSWORD"))
	if err != nil {
		return err
	}
	s.Sessions = st
	auth := ""
	if st.Password != "" {
		auth = " (basic authentication)"
	}
	log.Printf("sessions: session service %s%s", sessionstore.RedactURL(base), auth)
	if s.Opts.SessionStoreFile != "" {
		log.Print("sessions: --session-store is ignored with a session service")
	}
	return nil
}

// SessionStore returns the session store, the built-in one
// (Opts.SessionStoreFile) unless ConfigureSessions set one.
func (s *State) SessionStore() sessionstore.Store {
	s.sessionsOnce.Do(func() {
		if s.Sessions == nil {
			s.Sessions = sessionstore.NewMemoryStore(s.Opts.SessionStoreFile)
		}
	})
	return s.Sessions
}

// SessionData returns the stored `data` JSON of session (typ, id).
func (s *State) SessionData(typ, id string) (json.RawMessage, bool) {
	sess, err := s.SessionStore().Get(typ, id)
	if err != nil {
		sessionstore.LogError(err)
		return nil, false
	}
	raw, err := json.Marshal(sess.Data)
	if err != nil {
		return nil, false
	}
	return raw, true
}

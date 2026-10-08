package api

import (
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// llmsTxtProperty is the location of the deployment's /llms.txt: a file
// path, optionally with Spring's "file:" prefix. Unset serves 404.
const llmsTxtProperty = "llms_txt.location"

// llmsTxtTTL is how long a read of the llms.txt file, successful or not,
// is reused before the file is read again (LlmsTxtController.CACHE_TTL).
const llmsTxtTTL = 5 * time.Minute

// llmsTxtCache holds the last good copy of the llms.txt file and when it
// was last read.
type llmsTxtCache struct {
	mu          sync.Mutex
	body        []byte
	lastAttempt time.Time
	// now is the clock, time.Now when nil.
	now func() time.Time
}

// get returns the file's content, re-reading it at most once per TTL and
// keeping the last good copy when a read fails; nil when nothing was ever
// read.
func (c *llmsTxtCache) get(location string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	if !c.lastAttempt.IsZero() && now.Before(c.lastAttempt.Add(llmsTxtTTL)) {
		return c.body
	}
	c.lastAttempt = now
	b, err := os.ReadFile(strings.TrimPrefix(location, "file:"))
	if err != nil {
		log.Printf("Could not read %s %s: %v", llmsTxtProperty, location, err)
		return c.body
	}
	c.body = b
	return c.body
}

// llmsTxt serves GET /llms.txt (LlmsTxtController): the file at
// llms_txt.location as text/markdown, 404 when unset or unreadable.
func (s *State) serveLlmsTxt(w http.ResponseWriter, r *http.Request) {
	location := strings.TrimSpace(s.Props[llmsTxtProperty])
	if location == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	body := s.llmsTxt.get(location)
	if body == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/markdown;charset=UTF-8")
	w.Write(body)
}

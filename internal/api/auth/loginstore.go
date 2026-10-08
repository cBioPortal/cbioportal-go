package auth

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// loginStore keeps the login sessions and the logins in progress, as JSON
// under string keys that expire. The memory store serves one process; the
// Redis store (spring.session.store-type=redis) lets sessions survive a
// restart and span replicas, as Spring Session does for the Java portal.
type loginStore interface {
	get(ctx context.Context, key string) ([]byte, bool, error)
	set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	// take returns the value and deletes it, so it is used at most once.
	take(ctx context.Context, key string) ([]byte, bool, error)
	del(ctx context.Context, key string) error
	describe() string
}

// loginKeyPrefix namespaces this server's keys in a shared Redis; they are
// not Spring Session's, whose sessions hold serialized Java objects.
const loginKeyPrefix = "cbioportal-go:login:"

type memEntry struct {
	value   []byte
	expires time.Time
}

type memoryLoginStore struct {
	mu      sync.Mutex
	entries map[string]memEntry
	writes  int
}

func newMemoryLoginStore() *memoryLoginStore {
	return &memoryLoginStore{entries: map[string]memEntry{}}
}

func (m *memoryLoginStore) describe() string { return "in memory" }

func (m *memoryLoginStore) get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok || time.Now().After(e.expires) {
		delete(m.entries, key)
		return nil, false, nil
	}
	return e.value, true, nil
}

func (m *memoryLoginStore) set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.writes++; m.writes%256 == 0 {
		now := time.Now()
		for k, e := range m.entries {
			if now.After(e.expires) {
				delete(m.entries, k)
			}
		}
	}
	m.entries[key] = memEntry{value: value, expires: time.Now().Add(ttl)}
	return nil
}

func (m *memoryLoginStore) take(ctx context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	delete(m.entries, key)
	if !ok || time.Now().After(e.expires) {
		return nil, false, nil
	}
	return e.value, true, nil
}

func (m *memoryLoginStore) del(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, key)
	return nil
}

// loadLoginStore picks the login session store from the Java portal's Spring
// Session properties: spring.session.store-type=redis with the
// spring.data.redis.* connection (url, or host, port, username, password,
// database, ssl.enabled, timeout); otherwise memory.
func loadLoginStore(props map[string]string) (loginStore, error) {
	p := func(k string) string { return strings.TrimSpace(props[k]) }
	switch t := strings.ToLower(p("spring.session.store-type")); t {
	case "", "none":
		return newMemoryLoginStore(), nil
	case "redis":
	default:
		return nil, fmt.Errorf("spring.session.store-type=%s is not supported (use redis, or leave it unset for memory)", t)
	}
	r := &redisClient{
		addr:     net.JoinHostPort(cmp.Or(p("spring.data.redis.host"), "localhost"), cmp.Or(p("spring.data.redis.port"), "6379")),
		username: p("spring.data.redis.username"),
		password: props["spring.data.redis.password"],
		timeout:  5 * time.Second,
		idle:     make(chan *redisConn, 16),
	}
	if v := p("spring.data.redis.database"); v != "" {
		db, err := strconv.Atoi(v)
		if err != nil {
			return nil, errors.New("spring.data.redis.database is not a number")
		}
		r.db = db
	}
	if u := p("spring.data.redis.url"); u != "" {
		parsed, err := url.Parse(u)
		if err != nil || (parsed.Scheme != "redis" && parsed.Scheme != "rediss") {
			return nil, errors.New("spring.data.redis.url is not a redis:// or rediss:// URL")
		}
		port := parsed.Port()
		if port == "" {
			port = "6379"
		}
		r.addr = net.JoinHostPort(parsed.Hostname(), port)
		if parsed.User != nil {
			r.username = parsed.User.Username()
			r.password, _ = parsed.User.Password()
			if r.password == "" {
				r.password, r.username = r.username, ""
			}
		}
		if db := strings.Trim(parsed.Path, "/"); db != "" {
			if r.db, err = strconv.Atoi(db); err != nil {
				return nil, errors.New("spring.data.redis.url names no database number")
			}
		}
		if parsed.Scheme == "rediss" {
			r.tls = &tls.Config{ServerName: parsed.Hostname()}
		}
	}
	if b, _ := strconv.ParseBool(p("spring.data.redis.ssl.enabled")); b && r.tls == nil {
		host, _, _ := net.SplitHostPort(r.addr)
		r.tls = &tls.Config{ServerName: host}
	}
	if v := p("spring.data.redis.timeout"); v != "" {
		d, err := springDuration(v)
		if err != nil {
			return nil, fmt.Errorf("spring.data.redis.timeout: %w", err)
		}
		r.timeout = d
	}
	return &redisLoginStore{r}, nil
}

// springDuration parses a Spring Boot duration: a number of milliseconds or
// a number with a unit (ms, s, m, h, d).
func springDuration(v string) (time.Duration, error) {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Duration(n) * time.Millisecond, nil
	}
	if n, ok := strings.CutSuffix(v, "d"); ok {
		days, err := strconv.ParseInt(n, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", v)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", v)
	}
	return d, nil
}

type redisLoginStore struct{ c *redisClient }

func (s *redisLoginStore) describe() string {
	d := "redis " + s.c.addr
	if s.c.db != 0 {
		d += fmt.Sprintf(" database %d", s.c.db)
	}
	if s.c.tls != nil {
		d += " (TLS)"
	}
	return d
}

func (s *redisLoginStore) get(ctx context.Context, key string) ([]byte, bool, error) {
	return s.bulk(s.c.do(ctx, "GET", loginKeyPrefix+key))
}

func (s *redisLoginStore) set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	_, err := s.c.do(ctx, "SET", loginKeyPrefix+key, string(value), "PX", strconv.FormatInt(ttl.Milliseconds(), 10))
	return err
}

func (s *redisLoginStore) take(ctx context.Context, key string) ([]byte, bool, error) {
	return s.bulk(s.c.do(ctx, "GETDEL", loginKeyPrefix+key))
}

func (s *redisLoginStore) del(ctx context.Context, key string) error {
	_, err := s.c.do(ctx, "DEL", loginKeyPrefix+key)
	return err
}

func (s *redisLoginStore) bulk(v any, err error) ([]byte, bool, error) {
	if err != nil || v == nil {
		return nil, false, err
	}
	b, ok := v.([]byte)
	if !ok {
		return nil, false, fmt.Errorf("redis: unexpected reply %T", v)
	}
	return b, true, nil
}

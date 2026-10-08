package auth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRedis is a RESP server for the commands the login store sends: AUTH,
// SELECT, GET, SET … PX, GETDEL and DEL, with expiry.
type fakeRedis struct {
	ln       net.Listener
	password string

	mu      sync.Mutex
	data    map[string]string
	expires map[string]time.Time
	dbs     map[string]bool // databases selected
}

func newFakeRedis(t *testing.T, password string) *fakeRedis {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRedis{ln: ln, password: password, data: map[string]string{}, expires: map[string]time.Time{}, dbs: map[string]bool{}}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeRedis) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	authed := f.password == ""
	for {
		v, err := readRESP(r)
		if err != nil {
			return
		}
		items, _ := v.([]any)
		var args []string
		for _, it := range items {
			b, _ := it.([]byte)
			args = append(args, string(b))
		}
		if len(args) == 0 {
			return
		}
		reply := f.command(args, &authed)
		if _, err := conn.Write([]byte(reply)); err != nil {
			return
		}
	}
}

func bulkReply(v string, ok bool) string {
	if !ok {
		return "$-1\r\n"
	}
	return "$" + strconv.Itoa(len(v)) + "\r\n" + v + "\r\n"
}

func (f *fakeRedis) command(args []string, authed *bool) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := strings.ToUpper(args[0])
	if cmd == "AUTH" {
		if args[len(args)-1] != f.password {
			return "-WRONGPASS invalid username-password pair\r\n"
		}
		*authed = true
		return "+OK\r\n"
	}
	if !*authed {
		return "-NOAUTH Authentication required.\r\n"
	}
	live := func(k string) (string, bool) {
		if e, ok := f.expires[k]; ok && time.Now().After(e) {
			delete(f.data, k)
			delete(f.expires, k)
		}
		v, ok := f.data[k]
		return v, ok
	}
	switch {
	case cmd == "SELECT" && len(args) == 2:
		f.dbs[args[1]] = true
		return "+OK\r\n"
	case cmd == "GET" && len(args) == 2:
		return bulkReply(live(args[1]))
	case cmd == "GETDEL" && len(args) == 2:
		v, ok := live(args[1])
		delete(f.data, args[1])
		return bulkReply(v, ok)
	case cmd == "DEL" && len(args) == 2:
		_, ok := live(args[1])
		delete(f.data, args[1])
		if ok {
			return ":1\r\n"
		}
		return ":0\r\n"
	case cmd == "SET" && len(args) == 5 && strings.ToUpper(args[3]) == "PX":
		ms, err := strconv.Atoi(args[4])
		if err != nil || ms <= 0 {
			return "-ERR invalid expire time in 'set' command\r\n"
		}
		f.data[args[1]] = args[2]
		f.expires[args[1]] = time.Now().Add(time.Duration(ms) * time.Millisecond)
		return "+OK\r\n"
	}
	return "-ERR unknown command '" + cmd + "'\r\n"
}

func (f *fakeRedis) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for k := range f.data {
		keys = append(keys, k)
	}
	return keys
}

func redisProps(addr, password string) map[string]string {
	host, port, _ := net.SplitHostPort(addr)
	return map[string]string{
		"spring.session.store-type":     "redis",
		"spring.data.redis.host":        host,
		"spring.data.redis.port":        port,
		"spring.data.redis.password":    password,
		"spring.data.redis.database":    "2",
		"spring.data.redis.timeout":     "2s",
		"spring.data.redis.ssl.enabled": "false",
	}
}

// testLoginStore checks a store's get, set, take, del and expiry.
func testLoginStore(t *testing.T, s loginStore) {
	ctx := context.Background()
	key := "test:" + randomToken()
	if _, ok, err := s.get(ctx, key); ok || err != nil {
		t.Fatalf("get of a missing key: %v %v", ok, err)
	}
	if err := s.set(ctx, key, []byte(`{"user":"ann"}`), time.Minute); err != nil {
		t.Fatal(err)
	}
	if b, ok, err := s.get(ctx, key); !ok || err != nil || string(b) != `{"user":"ann"}` {
		t.Fatalf("get: %q %v %v", b, ok, err)
	}
	if b, ok, err := s.take(ctx, key); !ok || err != nil || string(b) != `{"user":"ann"}` {
		t.Fatalf("take: %q %v %v", b, ok, err)
	}
	if _, ok, _ := s.take(ctx, key); ok {
		t.Fatal("taken twice")
	}
	s.set(ctx, key, []byte("x"), time.Minute)
	if err := s.del(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.get(ctx, key); ok {
		t.Fatal("deleted key still there")
	}
	s.set(ctx, key, []byte("x"), 50*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	if _, ok, _ := s.get(ctx, key); ok {
		t.Fatal("expired key still there")
	}
}

func TestMemoryLoginStore(t *testing.T) {
	testLoginStore(t, newMemoryLoginStore())
}

func TestRedisLoginStore(t *testing.T) {
	f := newFakeRedis(t, "pw")
	s, err := loadLoginStore(redisProps(f.ln.Addr().String(), "pw"))
	if err != nil {
		t.Fatal(err)
	}
	testLoginStore(t, s)
	if !f.dbs["2"] {
		t.Error("database 2 not selected")
	}
	if d := s.describe(); strings.Contains(d, "pw") || !strings.Contains(d, f.ln.Addr().String()) {
		t.Errorf("describe %q", d)
	}
	s.set(context.Background(), "k", []byte("v"), time.Minute)
	if keys := f.keys(); len(keys) != 1 || keys[0] != loginKeyPrefix+"k" {
		t.Errorf("keys %v, want the %s prefix", keys, loginKeyPrefix)
	}

	// A wrong password fails without echoing it.
	bad, _ := loadLoginStore(redisProps(f.ln.Addr().String(), "wrong-pw"))
	_, _, err = bad.get(context.Background(), "k")
	var replyErr redisError
	if err == nil || !errors.As(err, &replyErr) || strings.Contains(err.Error(), "wrong-pw") {
		t.Errorf("wrong password: %v", err)
	}
	// An unreachable server is an error, not a missing session.
	f.ln.Close()
	down, _ := loadLoginStore(redisProps(f.ln.Addr().String(), "pw"))
	if _, _, err := down.get(context.Background(), "k"); err == nil {
		t.Error("no error from an unreachable redis")
	}
}

func TestLoadLoginStore(t *testing.T) {
	if s, err := loadLoginStore(map[string]string{}); err != nil || s.describe() != "in memory" {
		t.Errorf("default store: %v %v", s, err)
	}
	s, err := loadLoginStore(map[string]string{"spring.session.store-type": "redis"})
	if err != nil || s.(*redisLoginStore).c.addr != "localhost:6379" {
		t.Errorf("redis defaults: %v", err)
	}
	s, err = loadLoginStore(map[string]string{"spring.session.store-type": "redis",
		"spring.data.redis.url": "rediss://user:pw@redis.example:6380/3"})
	if err != nil {
		t.Fatal(err)
	}
	c := s.(*redisLoginStore).c
	if c.addr != "redis.example:6380" || c.username != "user" || c.password != "pw" || c.db != 3 || c.tls == nil {
		t.Errorf("redis url: %s %s %d tls=%v", c.addr, c.username, c.db, c.tls != nil)
	}
	for _, props := range []map[string]string{
		{"spring.session.store-type": "jdbc"},
		{"spring.session.store-type": "redis", "spring.data.redis.database": "x"},
		{"spring.session.store-type": "redis", "spring.data.redis.timeout": "soon"},
		{"spring.session.store-type": "redis", "spring.data.redis.url": "http://x"},
	} {
		if _, err := loadLoginStore(props); err == nil {
			t.Errorf("accepted %v", props)
		}
	}
}

// A session started on one replica is valid on another sharing the store,
// and after a restart (a new process with a new store client).
func TestLoginSessionAcrossReplicas(t *testing.T) {
	p := newFakeProvider(t)
	f := newFakeRedis(t, "")
	props := keycloakProps(p, "optional_oauth2")
	for k, v := range redisProps(f.ln.Addr().String(), "") {
		props[k] = v
	}
	a, b := optionalPortal(t, props), optionalPortal(t, props)

	c := browser(t)
	// The login starts on one replica and the provider returns to the other.
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if strings.HasPrefix(req.URL.Path, "/login/oauth2/code/") {
			req.URL.Host = strings.TrimPrefix(b.URL, "http://")
		}
		return nil
	}
	resp, err := c.Get(a.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := "http://" + resp.Request.URL.Host + resp.Request.URL.Path; got != b.URL+"/" {
		t.Fatalf("login ended at %s, want %s/", got, b.URL)
	}
	c.CheckRedirect = nil
	for _, srv := range []*httptest.Server{a, b} {
		if got := mustBody(t)(c.Get(srv.URL + "/api/studies")); !strings.Contains(got, "user=testuser@example.org") {
			t.Fatalf("%s: %q", srv.URL, got)
		}
	}
	restarted := optionalPortal(t, props)
	if got := mustBody(t)(c.Get(restarted.URL + "/api/studies")); !strings.Contains(got, "user=testuser@example.org") {
		t.Fatalf("after restart: %q", got)
	}
	// Logging out on one replica ends the session everywhere.
	mustBody(t)(c.Get(b.URL + "/logout"))
	if got := mustBody(t)(c.Get(a.URL + "/api/studies")); !strings.Contains(got, "known=false") {
		t.Fatalf("after logout: %q", got)
	}
}

// With optional_oauth2 an unreachable session store leaves the portal open
// (anonymous); with oauth2 it answers 503 rather than loop through logins.
func TestLoginStoreDown(t *testing.T) {
	p := newFakeProvider(t)
	f := newFakeRedis(t, "")
	f.ln.Close()
	for method, want := range map[string]int{"optional_oauth2": http.StatusOK, "oauth2": http.StatusServiceUnavailable} {
		props := keycloakProps(p, method)
		for k, v := range redisProps(f.ln.Addr().String(), "") {
			props[k] = v
		}
		portal := optionalPortal(t, props)
		req, _ := http.NewRequest(http.MethodGet, portal.URL+"/api/studies", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "x"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", method, resp.StatusCode, want)
		}
	}
}

// TestRedisLoginStoreLive runs the store against a real Redis:
//
//	docker run -d --name cbioportal-go-redis -p 16379:6379 redis:7 --requirepass pw
//	REDIS_TEST_ADDR=localhost:16379 REDIS_TEST_PASSWORD=pw go test -run RedisLoginStoreLive ./internal/api/auth
func TestRedisLoginStoreLive(t *testing.T) {
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR unset: needs a Redis server")
	}
	s, err := loadLoginStore(redisProps(addr, os.Getenv("REDIS_TEST_PASSWORD")))
	if err != nil {
		t.Fatal(err)
	}
	testLoginStore(t, s)

	p := newFakeProvider(t)
	props := keycloakProps(p, "optional_oauth2")
	for k, v := range redisProps(addr, os.Getenv("REDIS_TEST_PASSWORD")) {
		props[k] = v
	}
	a := optionalPortal(t, props)
	c := browser(t)
	mustBody(t)(c.Get(a.URL + "/login"))
	b := optionalPortal(t, props)
	if got := mustBody(t)(c.Get(b.URL + "/api/studies")); !strings.Contains(got, "user=testuser@example.org") {
		t.Fatalf("second replica: %q", got)
	}
	fmt.Fprintln(os.Stderr, "live redis:", s.describe())
}

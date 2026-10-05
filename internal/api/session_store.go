package api

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Session is the stored/served session document (Java Session).
type Session struct {
	ID       string         `json:"id"`
	Checksum string         `json:"checksum,omitempty"`
	Data     map[string]any `json:"data"`
	Source   string         `json:"source"`
	Type     string         `json:"type"`
}

func (s *Session) clone() *Session {
	c := *s
	c.Data = deepCopyMap(s.Data)
	return &c
}

var (
	errSessionNotFound = errors.New("session not found")
	errSessionConflict = errors.New("session id or identical data already exists")
)

// sessionFilter selects sessions of one type by their data, as the Java
// SessionServiceRequestHandler's session-service queries do. Empty fields
// do not filter.
type sessionFilter struct {
	users string // data.users holds this user
	owner string // data.owner is this user
	// origin, when hasOrigin, must equal data.origin as a set ($all + $size).
	origin    []string
	hasOrigin bool
	page      string // data.page
}

// sessionStore keeps sessions by (type, id): the built-in memoryStore or
// the external cBioPortal session service (remoteStore). Lookups of a
// missing session fail with errSessionNotFound; insert of a taken id or of
// data identical to another session's fails with errSessionConflict.
type sessionStore interface {
	get(typ, id string) (*Session, error)
	// create stores data under a new id, or answers the id of a session of
	// the same type with identical data.
	create(typ string, data map[string]any) (string, error)
	insert(typ, id string, data map[string]any) error
	update(typ, id string, data map[string]any) error
	remove(typ, id string) error
	find(typ string, f sessionFilter) ([]*Session, error)
}

// sessionSource is the source the built-in store keeps every session under.
const sessionSource = "cbioportal"

// memoryStore is a thread-safe (type,id)-keyed session map, optionally
// mirrored to a JSON file that is rewritten atomically on every change.
type memoryStore struct {
	mu   sync.RWMutex
	path string
	m    map[string]*Session
}

func storeKey(typ, id string) string { return typ + "/" + id }

func newSessionStore(path string) *memoryStore {
	st := &memoryStore{path: path, m: map[string]*Session{}}
	if path == "" {
		return st
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("session store: reading %s: %v", path, err)
		}
		return st
	}
	var list []*Session
	if err := decodeJSON(raw, &list); err != nil {
		log.Printf("session store: parsing %s: %v", path, err)
		return st
	}
	for _, s := range list {
		if s != nil && s.ID != "" && s.Type != "" {
			if s.Data == nil {
				s.Data = map[string]any{}
			}
			st.m[storeKey(s.Type, s.ID)] = s
		}
	}
	return st
}

// persistLocked writes the store to its file; the caller holds mu.
func (st *memoryStore) persistLocked() {
	if st.path == "" {
		return
	}
	list := make([]*Session, 0, len(st.m))
	for _, s := range st.m {
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Type != list[j].Type {
			return list[i].Type < list[j].Type
		}
		return list[i].ID < list[j].ID
	})
	raw, err := json.Marshal(list)
	if err != nil {
		log.Printf("session store: encoding: %v", err)
		return
	}
	dir := filepath.Dir(st.path)
	tmp, err := os.CreateTemp(dir, filepath.Base(st.path)+".tmp-*")
	if err != nil {
		log.Printf("session store: %v", err)
		return
	}
	_, werr := tmp.Write(raw)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		log.Printf("session store: writing %s: %v %v", tmp.Name(), werr, cerr)
		return
	}
	if err := os.Rename(tmp.Name(), st.path); err != nil {
		os.Remove(tmp.Name())
		log.Printf("session store: %v", err)
	}
}

func (st *memoryStore) get(typ, id string) (*Session, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	s, ok := st.m[storeKey(typ, id)]
	if !ok {
		return nil, errSessionNotFound
	}
	return s.clone(), nil
}

func (st *memoryStore) create(typ string, data map[string]any) (string, error) {
	sum := checksum(data)
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, s := range st.m {
		if s.Type == typ && s.Checksum == sum {
			return s.ID, nil
		}
	}
	id := newObjectID()
	st.m[storeKey(typ, id)] = &Session{ID: id, Checksum: sum, Data: deepCopyMap(data), Source: sessionSource, Type: typ}
	st.persistLocked()
	return id, nil
}

func (st *memoryStore) insert(typ, id string, data map[string]any) error {
	sum := checksum(data)
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.m[storeKey(typ, id)]; ok {
		return errSessionConflict
	}
	for _, s := range st.m {
		if s.Type == typ && s.Checksum == sum {
			return errSessionConflict
		}
	}
	st.m[storeKey(typ, id)] = &Session{ID: id, Checksum: sum, Data: deepCopyMap(data), Source: sessionSource, Type: typ}
	st.persistLocked()
	return nil
}

func (st *memoryStore) update(typ, id string, data map[string]any) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	s, ok := st.m[storeKey(typ, id)]
	if !ok {
		return errSessionNotFound
	}
	s.Data = deepCopyMap(data)
	s.Checksum = checksum(data)
	st.persistLocked()
	return nil
}

func (st *memoryStore) remove(typ, id string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.m[storeKey(typ, id)]; !ok {
		return errSessionNotFound
	}
	delete(st.m, storeKey(typ, id))
	st.persistLocked()
	return nil
}

// find returns copies of the sessions of a type matching f, ordered by id
// (ObjectIds sort by creation time). Users and owner match whole names,
// case-insensitively.
func (st *memoryStore) find(typ string, f sessionFilter) ([]*Session, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := []*Session{}
	for _, s := range st.m {
		if s.Type == typ && f.matches(s) {
			out = append(out, s.clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f sessionFilter) matches(s *Session) bool {
	if f.users != "" && !containsUser(stringSet(s.Data["users"]), f.users) {
		return false
	}
	if f.owner != "" {
		if owner, _ := s.Data["owner"].(string); !strings.EqualFold(owner, f.owner) {
			return false
		}
	}
	if f.hasOrigin && !sameSet(stringSet(s.Data["origin"]), f.origin) {
		return false
	}
	if f.page != "" {
		if p, _ := s.Data["page"].(string); p != f.page {
			return false
		}
	}
	return true
}

var objectIDCounter atomic.Uint32

func init() {
	var b [4]byte
	rand.Read(b[:])
	objectIDCounter.Store(binary.BigEndian.Uint32(b[:]))
}

// newObjectID returns a 24-hex-char Mongo-style ObjectId: 4-byte timestamp,
// 5 random bytes, 3-byte counter.
func newObjectID() string {
	var b [12]byte
	binary.BigEndian.PutUint32(b[0:4], uint32(time.Now().Unix()))
	rand.Read(b[4:9])
	c := objectIDCounter.Add(1)
	b[9], b[10], b[11] = byte(c>>16), byte(c>>8), byte(c)
	return hex.EncodeToString(b[:])
}

package sessionstore

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

// Clone returns a deep copy of s.
func (s *Session) Clone() *Session {
	c := *s
	c.Data = deepCopyMap(s.Data)
	return &c
}

// ErrNotFound answers a missing session and ErrConflict an insert of a taken
// id or of data identical to another session's.
var (
	ErrNotFound = errors.New("session not found")
	ErrConflict = errors.New("session id or identical data already exists")
)

// Filter selects sessions of one type by their data, as the Java
// SessionServiceRequestHandler's session-service queries do. Empty fields
// do not filter.
type Filter struct {
	Users string // data.users holds this user
	Owner string // data.owner is this user
	// Origin, when HasOrigin, must equal data.origin as a set ($all + $size).
	Origin    []string
	HasOrigin bool
	Page      string // data.page
}

// Store keeps sessions by (type, id): the built-in MemoryStore or
// the external cBioPortal session service (RemoteStore). Lookups of a
// missing session fail with ErrNotFound; insert of a taken id or of
// data identical to another session's fails with ErrConflict.
type Store interface {
	Get(typ, id string) (*Session, error)
	// create stores data under a new id, or answers the id of a session of
	// the same type with identical data.
	Create(typ string, data map[string]any) (string, error)
	Insert(typ, id string, data map[string]any) error
	Update(typ, id string, data map[string]any) error
	Remove(typ, id string) error
	Find(typ string, f Filter) ([]*Session, error)
}

// sessionSource is the source the built-in store keeps every session under.
const sessionSource = "cbioportal"

// MemoryStore is a thread-safe (type,id)-keyed session map, optionally
// mirrored to a JSON file that is rewritten atomically on every change.
type MemoryStore struct {
	mu   sync.RWMutex
	path string
	m    map[string]*Session
}

func storeKey(typ, id string) string { return typ + "/" + id }

// NewMemoryStore returns the built-in store, mirrored to the JSON file at
// path when path is set.
func NewMemoryStore(path string) *MemoryStore {
	st := &MemoryStore{path: path, m: map[string]*Session{}}
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
	if err := DecodeJSON(raw, &list); err != nil {
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
func (st *MemoryStore) persistLocked() {
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

// Get returns the session typ/id.
func (st *MemoryStore) Get(typ, id string) (*Session, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	s, ok := st.m[storeKey(typ, id)]
	if !ok {
		return nil, ErrNotFound
	}
	return s.Clone(), nil
}

// Create stores data under a new id, or answers the id of a session of the
// same type with identical data.
func (st *MemoryStore) Create(typ string, data map[string]any) (string, error) {
	sum := Checksum(data)
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

// Insert stores data under id.
func (st *MemoryStore) Insert(typ, id string, data map[string]any) error {
	sum := Checksum(data)
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.m[storeKey(typ, id)]; ok {
		return ErrConflict
	}
	for _, s := range st.m {
		if s.Type == typ && s.Checksum == sum {
			return ErrConflict
		}
	}
	st.m[storeKey(typ, id)] = &Session{ID: id, Checksum: sum, Data: deepCopyMap(data), Source: sessionSource, Type: typ}
	st.persistLocked()
	return nil
}

// Update replaces the data of session typ/id.
func (st *MemoryStore) Update(typ, id string, data map[string]any) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	s, ok := st.m[storeKey(typ, id)]
	if !ok {
		return ErrNotFound
	}
	s.Data = deepCopyMap(data)
	s.Checksum = Checksum(data)
	st.persistLocked()
	return nil
}

// Remove deletes session typ/id.
func (st *MemoryStore) Remove(typ, id string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.m[storeKey(typ, id)]; !ok {
		return ErrNotFound
	}
	delete(st.m, storeKey(typ, id))
	st.persistLocked()
	return nil
}

// Find returns copies of the sessions of a type matching f, ordered by id
// (ObjectIds sort by creation time). Users and owner match whole names,
// case-insensitively.
func (st *MemoryStore) Find(typ string, f Filter) ([]*Session, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := []*Session{}
	for _, s := range st.m {
		if s.Type == typ && f.matches(s) {
			out = append(out, s.Clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f Filter) matches(s *Session) bool {
	if f.Users != "" && !ContainsUser(StringSet(s.Data["users"]), f.Users) {
		return false
	}
	if f.Owner != "" {
		if owner, _ := s.Data["owner"].(string); !strings.EqualFold(owner, f.Owner) {
			return false
		}
	}
	if f.HasOrigin && !SameSet(StringSet(s.Data["origin"]), f.Origin) {
		return false
	}
	if f.Page != "" {
		if p, _ := s.Data["page"].(string); p != f.Page {
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

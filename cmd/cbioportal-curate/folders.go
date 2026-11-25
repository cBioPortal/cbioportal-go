package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// folderStore remembers the study folders the portal holds, across runs,
// with each folder's file signature at its last import, so a restart
// imports again only the folders that changed.
type folderStore struct {
	path    string
	mu      sync.Mutex
	Folders map[string]string `json:"folders"` // folder -> signature ("" until imported)
	// Imports is when each folder was last imported, and how long that took.
	Imports map[string]lastImport `json:"imports,omitempty"`
}

type lastImport struct {
	At      time.Time `json:"at"`
	Seconds float64   `json:"seconds"`
}

func loadFolders(dataDir string) *folderStore {
	s := &folderStore{path: filepath.Join(dataDir, "folders.json"), Folders: map[string]string{}}
	if b, err := os.ReadFile(s.path); err == nil {
		json.Unmarshal(b, s)
		if s.Folders == nil {
			s.Folders = map[string]string{}
		}
	}
	if s.Imports == nil {
		s.Imports = map[string]lastImport{}
	}
	return s
}

// list is the remembered folders that still exist.
func (s *folderStore) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for d := range s.Folders {
		if isStudy(d) {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// last is when dir was last imported, if known.
func (s *folderStore) last(dir string) (lastImport, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.Imports[dir]
	return l, ok
}

// unchanged reports whether dir's files are as they were at its last import.
func (s *folderStore) unchanged(dir string) bool {
	s.mu.Lock()
	sig, ok := s.Folders[dir]
	s.mu.Unlock()
	return ok && sig != "" && sig == signature(dir)
}

// add remembers folders, not yet imported.
func (s *folderStore) add(dirs []string) {
	s.mu.Lock()
	for _, d := range dirs {
		if _, ok := s.Folders[d]; !ok {
			s.Folders[d] = ""
		}
	}
	s.mu.Unlock()
	s.save()
}

// remove forgets dir.
func (s *folderStore) remove(dir string) {
	s.mu.Lock()
	delete(s.Folders, dir)
	delete(s.Imports, dir)
	s.mu.Unlock()
	s.save()
}

// imported records dir's signature at a successful import, and when it was.
func (s *folderStore) imported(dir, sig string, last lastImport) {
	s.mu.Lock()
	s.Folders[dir] = sig
	s.Imports[dir] = last
	s.mu.Unlock()
	s.save()
}

func (s *folderStore) save() {
	s.mu.Lock()
	b, err := json.MarshalIndent(s, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, s.path)
	}
}

package main

import (
	"testing"
	"time"
)

func TestFolderStoreKeepsLastImport(t *testing.T) {
	data := t.TempDir()
	at := time.Date(2026, 11, 25, 15, 22, 36, 0, time.Local)
	s := loadFolders(data)
	s.add([]string{"/studies/a", "/studies/b"})
	s.imported("/studies/a", "sig", lastImport{At: at, Seconds: 4})

	s = loadFolders(data)
	if l, ok := s.last("/studies/a"); !ok || !l.At.Equal(at) || l.Seconds != 4 {
		t.Errorf("last import after a reload = %+v, %v", l, ok)
	}
	if _, ok := s.last("/studies/b"); ok {
		t.Errorf("a folder never imported has a last import")
	}
	s.remove("/studies/a")
	if _, ok := loadFolders(data).last("/studies/a"); ok {
		t.Errorf("a removed folder kept its last import")
	}
}

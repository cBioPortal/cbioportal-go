// Package parser handles cBioPortal study files: meta files, study-directory
// discovery, TSV header handling, and clinical attribute metadata.
package parser

import (
	"os"
	"strings"
)

// MetaFile is a parsed meta_*.txt: an ordered-insensitive map of key: value
// fields (colon-space separated), not TSV.
type MetaFile struct {
	Fields map[string]string
}

func ParseMeta(text string) *MetaFile {
	fields := map[string]string{}
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		val := strings.TrimSpace(line[i+1:])
		fields[key] = val
	}
	return &MetaFile{Fields: fields}
}

func ReadMeta(path string) (*MetaFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseMeta(string(b)), nil
}

func (m *MetaFile) Get(key string) string { return m.Fields[key] }

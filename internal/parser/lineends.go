package parser

import (
	"io"
	"os"
	"strings"
)

// lineEnds turns CRLF and lone CR line ends into LF, splitting lines where
// Java's BufferedReader.readLine (cbioportal-core's file readers) splits
// them: datahub has files with Windows and with classic Mac line ends.
type lineEnds struct {
	r       io.Reader
	afterCR bool
}

func (l *lineEnds) Read(p []byte) (int, error) {
	for {
		n, err := l.r.Read(p)
		out := 0
		for _, c := range p[:n] {
			switch {
			case c == '\n' && l.afterCR:
				l.afterCR = false
			case c == '\r':
				p[out] = '\n'
				out++
				l.afterCR = true
			default:
				p[out] = c
				out++
				l.afterCR = false
			}
		}
		// A read that held only the LF of a CRLF yields nothing; read on.
		if out > 0 || err != nil || n == 0 {
			return out, err
		}
	}
}

type textFile struct {
	io.Reader
	io.Closer
}

// OpenText opens a study file for reading with LF line ends.
func OpenText(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return textFile{&lineEnds{r: f}, f}, nil
}

// normalizeLineEnds is lineEnds for a whole string.
func normalizeLineEnds(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

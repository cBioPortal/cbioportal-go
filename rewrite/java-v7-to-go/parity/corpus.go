package parity

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// writeCorpus writes the logged-traffic corpus to out: about CORPUS_N
// read-only requests sampled by loggedgen from the requests
// cbioportal_qc.logged_requests saw in the capture window (CORPUS_SINCE to
// CORPUS_UNTIL, UTC) on the public portal, stratified by endpoint (each
// endpoint's share grows with the square root of its volume, at least
// CORPUS_MIN), CORPUS_API_SHARE of them from api.cbioportal.org, with the
// known differences marked. The sample seed is the ISO week, so a weekly
// regeneration draws another sample. The corpus is real user traffic: it is
// written mode 600 and belongs outside git (.parity/ is ignored).
//
// ClickHouse credentials come from CLICKHOUSE_URL/USER/PASSWORD, or else
// from CLOUD_ENV (default ~/.config/cbioportal-go/cloud.env).
func writeCorpus(root, out string, stderr io.Writer) error {
	year, week := time.Now().UTC().ISOWeek()
	args := []string{"run", "./rewrite/java-v7-to-go/loggedgen",
		"-n", env("CORPUS_N", "5000"),
		"-min", env("CORPUS_MIN", "3"),
		"-seed", env("CORPUS_SEED", fmt.Sprintf("%d%02d", year, week)),
		"-alloc", "sqrt",
		"-since", env("CORPUS_SINCE", "2026-10-07 22:30:00"),
		"-until", os.Getenv("CORPUS_UNTIL"),
		"-api-share", env("CORPUS_API_SHARE", "0.25"),
	}
	if os.Getenv("CLICKHOUSE_URL") == "" {
		home, _ := os.UserHomeDir()
		if err := loadEnvFile(env("CLOUD_ENV", filepath.Join(home, ".config/cbioportal-go/cloud.env"))); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(out), filepath.Base(out)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	cmd := exec.Command("go", args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = root, tmp, stderr
	err = cmd.Run()
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return err
	}
	n := 0
	for l := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(l, "## ") {
			n++
		}
	}
	if n == 0 {
		return errors.New("no requests sampled")
	}
	if err := os.Rename(tmp.Name(), out); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "corpus: %d requests in %s\n", n, out)
	return nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// loadEnvFile sets the KEY=VALUE lines of a shell environment file in the
// process environment (comments, blank lines and `export ` are allowed;
// values may be quoted).
func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	vars, err := parseEnvFile(f)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for k, v := range vars {
		os.Setenv(k, v)
	}
	return nil
}

func parseEnvFile(r io.Reader) (map[string]string, error) {
	vars := map[string]string{}
	s := bufio.NewScanner(r)
	for n := 1; s.Scan(); n++ {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" || strings.ContainsAny(k, " \t") {
			return nil, fmt.Errorf("line %d: not KEY=VALUE", n)
		}
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		vars[k] = v
	}
	return vars, s.Err()
}

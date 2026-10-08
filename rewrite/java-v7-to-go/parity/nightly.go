package parity

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// nightlyOptions configure the cron entry (the nightly subcommand).
type nightlyOptions struct {
	home  string // logs/, state/ (lock, ref, nights.txt) and src/
	repo  string // the local clone that src also fetches from
	extra []string
}

// originURL is the GitHub repository src tracks as origin.
const originURL = "https://github.com/cBioPortal/cbioportal-go.git"

// cronNightly runs one nightly comparison from a checkout of the ref named
// in state/ref (default origin/main), logging to logs/<date>.log. Sundays
// it regenerates the corpus first, so it follows current traffic. It
// returns the exit status.
func cronNightly(o nightlyOptions) int {
	home, _ := os.UserHomeDir()
	os.Setenv("PATH", strings.Join([]string{filepath.Join(home, ".local/bin"), filepath.Join(home, "go/bin"),
		filepath.Join(home, "go-local/go/bin"), "/snap/bin", "/usr/local/bin", "/usr/bin", "/bin", os.Getenv("PATH")}, ":"))
	// GitHub over HTTPS with gh's credentials (cron has no SSH agent).
	for k, v := range map[string]string{
		"GIT_CONFIG_COUNT": "2",
		"GIT_CONFIG_KEY_0": "url.https://github.com/.insteadOf", "GIT_CONFIG_VALUE_0": "git@github.com:",
		"GIT_CONFIG_KEY_1": "credential.https://github.com.helper", "GIT_CONFIG_VALUE_1": "!gh auth git-credential",
	} {
		os.Setenv(k, v)
	}
	logs, state, src := filepath.Join(o.home, "logs"), filepath.Join(o.home, "state"), filepath.Join(o.home, "src")
	for _, d := range []string{logs, state} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "parity nightly:", err)
			return 2
		}
	}
	now := time.Now().UTC()
	logFile, err := os.OpenFile(filepath.Join(logs, now.Format(time.DateOnly)+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parity nightly:", err)
		return 2
	}
	defer logFile.Close()
	log := func(format string, args ...any) { fmt.Fprintf(logFile, format+"\n", args...) }
	log("=== %s ===", time.Now().Format(time.RFC3339))

	// One run at a time.
	lock, err := os.OpenFile(filepath.Join(state, "lock"), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log("cannot open the lock: %v", err)
		return 2
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		log("another parity run is going")
		return 0
	}

	git := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Stdout, cmd.Stderr = logFile, logFile
		return cmd.Run()
	}
	// src tracks GitHub (origin) and the local clone (local), whose unpushed
	// branches state/ref may name, e.g. local/<branch>.
	if _, err := os.Stat(filepath.Join(src, ".git")); err != nil {
		if err := git("clone", "-q", "--no-checkout", o.repo, src); err != nil {
			log("clone failed")
			return 2
		}
		git("-C", src, "remote", "rename", "origin", "local")
		git("-C", src, "remote", "add", "origin", originURL)
	}
	for _, remote := range []string{"origin", "local"} {
		if err := git("-C", src, "fetch", "-q", remote); err != nil {
			log("fetch %s failed", remote)
		}
	}
	ref := "origin/main"
	if b, err := os.ReadFile(filepath.Join(state, "ref")); err == nil && strings.TrimSpace(string(b)) != "" {
		ref = strings.TrimSpace(string(b))
	}
	if err := git("-C", src, "checkout", "-q", "--detach", ref); err != nil {
		log("checkout %s failed", ref)
		return 2
	}
	// The commit date shows a stale checkout: go.cbioportal.org runs what is
	// deployed, but the known differences and the tools come from here.
	head, _ := exec.Command("git", "-C", src, "log", "-1", "--format=%h, committed %cI", "HEAD").Output()
	log("src %s (%s)", strings.TrimSpace(string(head)), ref)

	args := []string{"run", "./rewrite/cmd/cbioportal-rewrite-test", "java-v7-to-go", "parity", "run"}
	if now.Weekday() == time.Sunday {
		args = append(args, "--regen")
	}
	args = append(args, o.extra...)
	cmd := exec.Command("go", args...)
	if _, err := os.Stat(filepath.Join(src, "rewrite/cmd/cbioportal-rewrite-test")); err != nil {
		// A ref without this command runs its own test/parity/nightly.sh.
		cmd = exec.Command(filepath.Join(src, "test/parity/nightly.sh"), args[5:]...)
	}
	cmd.Dir, cmd.Stdout, cmd.Stderr = src, logFile, logFile
	cmd.Env = append(os.Environ(), "PARITY_STATE="+state, "PARITY_DIR="+filepath.Join(src, ".parity/nightly"))
	rc := exitCode(cmd.Run())
	log("parity run exited %d", rc)
	pruneFiles(logs, ".log", 61*24*time.Hour)
	return rc
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() >= 0 {
		return ee.ExitCode()
	}
	return 2
}

// pruneFiles removes the files of dir with the suffix older than maxAge.
func pruneFiles(dir, suffix string, maxAge time.Duration) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) >= maxAge {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

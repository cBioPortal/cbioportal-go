package parity

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// memoryCap returns the command prefix that runs a process under a hard
// memory cap of max (e.g. "2G") and a description of it:
//
//  1. a systemd user scope with MemoryMax=max and no swap. Cron starts jobs
//     without XDG_RUNTIME_DIR and DBUS_SESSION_BUS_ADDRESS, so systemd-run
//     --user cannot find the user manager ("Failed to connect to bus: No
//     medium found") although it runs; both are set from /run/user/UID
//     when missing. The scope's memory.max is read back, so a manager that
//     does not enforce the limit (no delegated memory controller) does not
//     count.
//  2. else an address-space limit, prlimit --as=as (default twice max: Go
//     reserves address space well ahead of what it uses, so the limit is
//     looser than MemoryMax, but still hard).
//
// It fails when neither is available: the caller must not run uncapped.
func memoryCap(max, as string) (prefix []string, how string, err error) {
	limit, err := parseSize(max)
	if err != nil {
		return nil, "", fmt.Errorf("memory cap %q: %v", max, err)
	}
	userBusEnv()
	scope := []string{"systemd-run", "--user", "--scope", "-q", "-p", "MemoryMax=" + strconv.FormatInt(limit, 10), "-p", "MemorySwapMax=0"}
	probe := append(append([]string{}, scope...), "sh", "-c",
		`cat "/sys/fs/cgroup$(sed -n 's/^0:://p' /proc/self/cgroup)/memory.max"`)
	out, scopeErr := exec.Command(probe[0], probe[1:]...).CombinedOutput()
	if scopeErr == nil {
		got, perr := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		if perr == nil && got > 0 && got <= limit {
			return scope, fmt.Sprintf("systemd user scope, MemoryMax=%s", max), nil
		}
		scopeErr = fmt.Errorf("the scope's memory.max is %q, not %d", strings.TrimSpace(string(out)), limit)
	} else {
		scopeErr = fmt.Errorf("%v: %s", scopeErr, strings.TrimSpace(string(out)))
	}

	asLimit := 2 * limit
	if as != "" {
		if asLimit, err = parseSize(as); err != nil {
			return nil, "", fmt.Errorf("address-space cap %q: %v", as, err)
		}
	}
	rlimit := []string{"prlimit", "--as=" + strconv.FormatInt(asLimit, 10), "--"}
	probe = append(append([]string{}, rlimit...), "true")
	out, err = exec.Command(probe[0], probe[1:]...).CombinedOutput()
	if err == nil {
		return rlimit, fmt.Sprintf("address-space limit %d bytes (prlimit; no systemd user scope: %v)", asLimit, scopeErr), nil
	}
	return nil, "", errors.Join(fmt.Errorf("systemd user scope: %w", scopeErr),
		fmt.Errorf("prlimit: %v: %s", err, strings.TrimSpace(string(out))))
}

// userBusEnv points XDG_RUNTIME_DIR and DBUS_SESSION_BUS_ADDRESS at the
// user's runtime directory when they are unset and it exists.
func userBusEnv() {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
		if _, err := os.Stat(dir); err != nil {
			return
		}
		os.Setenv("XDG_RUNTIME_DIR", dir)
	}
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		if _, err := os.Stat(filepath.Join(dir, "bus")); err == nil {
			os.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(dir, "bus"))
		}
	}
}

// parseSize reads a byte count with an optional K, M, G or T suffix (powers
// of 1024, as systemd reads them).
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	mult := int64(1)
	if s != "" {
		switch strings.ToUpper(s[len(s)-1:]) {
		case "K":
			mult = 1 << 10
		case "M":
			mult = 1 << 20
		case "G":
			mult = 1 << 30
		case "T":
			mult = 1 << 40
		}
		if mult > 1 {
			s = s[:len(s)-1]
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("want a positive byte count, e.g. 2G")
	}
	return n * mult, nil
}

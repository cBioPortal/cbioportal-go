package main

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// errNoPicker means the computer has no folder picker the program can open.
var errNoPicker = errors.New("no folder picker can open here (on Linux, it needs a desktop session and zenity or kdialog); give the study folder on the command line")

// pickFolder opens the system's folder picker and returns the chosen
// folder, or "" when the person cancels it.
func pickFolder(prompt string) (string, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("osascript", "-e", `POSIX path of (choose folder with prompt "`+strings.ReplaceAll(prompt, `"`, `'`)+`")`)
	case "linux":
		// Without a graphical session a picker cannot open (zenity waits).
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			break
		}
		if p, err := exec.LookPath("zenity"); err == nil {
			cmd = exec.Command(p, "--file-selection", "--directory", "--title="+prompt)
		} else if p, err := exec.LookPath("kdialog"); err == nil {
			cmd = exec.Command(p, "--getexistingdirectory", ".", "--title", prompt)
		}
	}
	if cmd == nil {
		return "", errNoPicker
	}
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return "", nil // cancelled
	}
	if err != nil {
		return "", err
	}
	return strings.TrimRight(strings.TrimSpace(string(out)), "/"), nil
}

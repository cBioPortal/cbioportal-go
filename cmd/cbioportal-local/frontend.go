//go:build !embedfrontend

package main

import (
	"log"
	"os"
	"path/filepath"
)

// frontendDir is the website build to serve: --frontend-dir, or a
// frontend/dist folder next to the program or in the current folder. A
// build with the embedfrontend tag carries its own instead.
func frontendDir(o *options) (string, error) {
	if o.frontendDir != "" {
		return o.frontendDir, nil
	}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "frontend"), filepath.Join(filepath.Dir(exe), "..", "frontend", "dist"))
	}
	candidates = append(candidates, filepath.Join("frontend", "dist"))
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "index.html")); err == nil {
			return filepath.Abs(c)
		}
	}
	log.Print("no website build found: serving the API alone (pass --frontend-dir, or use a build with the website built in)")
	return "", nil
}

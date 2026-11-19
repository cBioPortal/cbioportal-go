//go:build !wails

package main

import (
	"os"

	"github.com/cbioportal/cbioportal-go/internal/cli"
)

func main() {
	os.Exit(cli.Execute(newRootCmd(), os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

package transform

import "embed"

// Source holds the package's Go files and the data they embed. A
// transform's build fingerprint hashes them, so a change to the code that
// builds the portal tables rebuilds every study.
//
//go:embed *.go *.tsv
var Source embed.FS

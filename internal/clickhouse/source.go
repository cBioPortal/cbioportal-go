package clickhouse

import "embed"

// Source holds the package's Go files. A transform's build fingerprint
// hashes them, so a change to the code that builds the portal tables
// rebuilds every study.
//
//go:embed *.go
var Source embed.FS

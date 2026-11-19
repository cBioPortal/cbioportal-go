package transform

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/chdb"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

var (
	chdbOnce sync.Once
	chdbErr  error
)

// chdbServer starts a chDB engine in the test process, once, and sends the
// ClickHouse clients' requests to it.
func chdbServer(t *testing.T, lib string) string {
	t.Helper()
	chdbOnce.Do(func() {
		dir := filepath.Join(os.TempDir(), fmt.Sprintf("cbioportal-chdb-test-%d", os.Getpid()))
		var e *chdb.Engine
		if e, chdbErr = chdb.Open(lib, dir, 8); chdbErr == nil {
			ch.UseTransport(e.Transport())
		}
	})
	if chdbErr != nil {
		t.Fatal(chdbErr)
	}
	return chdb.URL
}

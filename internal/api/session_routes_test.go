package api

import (
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/api/sessionstore"
)

func TestSessionRoutesRegisterWithoutConflict(t *testing.T) {
	NewRouter(newState(&core.State{Sessions: sessionstore.NewMemoryStore("")}))
}

package config

import (
	_ "embed"
	"maps"
	"strings"
	"sync"
)

//go:embed frontend.properties
var defaultProperties string

var (
	frontendOnce sync.Once
	frontendBase map[string]string
)

// FrontendConfig returns the /config_service property map (frontend property
// names → values), built from the embedded default properties and merged with
// the given base URL. An optional override map (e.g. from an external
// application.properties) takes precedence over the embedded defaults.
func FrontendConfig(baseURL string, overrides map[string]string) map[string]any {
	frontendOnce.Do(func() {
		frontendBase = ParseApplicationProperties(strings.NewReader(defaultProperties))
	})
	props := make(map[string]string, len(frontendBase)+len(overrides))
	maps.Copy(props, frontendBase)
	maps.Copy(props, overrides)
	return BuildFrontendConfig(props, baseURL)
}

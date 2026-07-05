package config

import (
	_ "embed"
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
func FrontendConfig(baseURL string, overrides map[string]string) map[string]interface{} {
	frontendOnce.Do(func() {
		frontendBase = ParseApplicationProperties(strings.NewReader(defaultProperties))
	})
	props := make(map[string]string, len(frontendBase)+len(overrides))
	for k, v := range frontendBase {
		props[k] = v
	}
	for k, v := range overrides {
		props[k] = v
	}
	return BuildFrontendConfig(props, baseURL)
}

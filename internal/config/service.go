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
// names → values) for the given portal properties. As in the Java portal, a
// properties file replaces the stock defaults (the embedded
// application.properties.EXAMPLE) rather than adding to them: with
// replaceDefaults, keys the properties don't set fall back to the code-level
// defaults only.
func FrontendConfig(baseURL string, props map[string]string, replaceDefaults bool) map[string]any {
	return BuildFrontendConfig(EffectiveProperties(props, replaceDefaults), baseURL)
}

// EffectiveProperties are the portal properties in force: props on top of
// the stock defaults, or props alone when they replace the defaults.
func EffectiveProperties(props map[string]string, replaceDefaults bool) map[string]string {
	frontendOnce.Do(func() {
		frontendBase = ParseApplicationProperties(strings.NewReader(defaultProperties))
	})
	merged := make(map[string]string, len(frontendBase)+len(props))
	if !replaceDefaults {
		maps.Copy(merged, frontendBase)
	}
	maps.Copy(merged, props)
	return merged
}

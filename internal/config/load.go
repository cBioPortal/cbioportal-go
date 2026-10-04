package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// LoadPortalProperties merges the portal's configuration the way the Java
// portal reads it: an optional properties file (application.properties or a
// legacy portal.properties — same key=value syntax), overridden by
// command-line arguments of the form --key=value, as given to the Java
// portal's `java -jar … --key=value` invocation.
func LoadPortalProperties(file string, args []string) (map[string]string, error) {
	props := map[string]string{}
	if file != "" {
		fromFile, err := ParseApplicationPropertiesFile(file)
		if err != nil {
			return nil, err
		}
		maps.Copy(props, fromFile)
	}
	for _, a := range args {
		kv, ok := strings.CutPrefix(a, "--")
		key, value, hasValue := strings.Cut(kv, "=")
		if !ok || !hasValue || key == "" {
			return nil, fmt.Errorf("argument %q is not of the form --key=value", a)
		}
		props[key] = value
	}
	return props, nil
}

// ClassifyProperties splits the keys of props into those this server uses
// (the frontend properties served by /config_service) and those it ignores,
// such as the Java backend's spring.datasource.* or db.* settings in a
// reused application.properties. Both lists are sorted.
func ClassifyProperties(props map[string]string) (used, ignored []string) {
	known := make(map[string]bool, len(FrontendProperties))
	for _, p := range FrontendProperties {
		known[p.PropertyName] = true
	}
	for _, p := range BackendProperties {
		known[p] = true
	}
	for _, k := range slices.Sorted(maps.Keys(props)) {
		if known[k] || backendProperty(k) {
			used = append(used, k)
		} else {
			ignored = append(ignored, k)
		}
	}
	return used, ignored
}

// PropertiesSummary is a one-line account of which portal properties take
// effect, naming at most max keys per list. It never includes values, which
// may hold passwords.
func PropertiesSummary(props map[string]string, max int) string {
	if len(props) == 0 {
		return "portal properties: none set (built-in defaults)"
	}
	used, ignored := ClassifyProperties(props)
	s := fmt.Sprintf("portal properties: %d used", len(used))
	if len(used) > 0 {
		s += " (" + abbreviate(used, max) + ")"
	}
	s += fmt.Sprintf(", %d ignored", len(ignored))
	if len(ignored) > 0 {
		s += " (not used by this server: " + abbreviate(ignored, max) + ")"
	}
	return s
}

func abbreviate(keys []string, max int) string {
	if len(keys) <= max {
		return strings.Join(keys, ", ")
	}
	return strings.Join(keys[:max], ", ") + fmt.Sprintf(", … %d more", len(keys)-max)
}

// backendProperty reports the non-frontend properties this server reads:
// authentication, data access tokens and study authorization.
func backendProperty(k string) bool {
	switch k {
	case "filter_groups_by_appname", "always_show_study_group", "dat.filter_user_role":
		return true
	}
	return strings.HasPrefix(k, "spring.security.oauth2.client.") || strings.HasPrefix(k, "dat.oauth2.")
}

package config

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// LoadPortalProperties merges the portal's configuration the way the Java
// portal reads it: an optional properties file (application.properties or a
// legacy portal.properties — same key=value syntax), overridden by
// command-line arguments of the form --key=value, as given to the Java
// portal's `java -jar … --key=value` invocation. Values may hold Spring
// placeholders, resolved as described at resolvePlaceholders.
func LoadPortalProperties(file string, args []string) (map[string]string, error) {
	return loadPortalProperties(file, args, os.LookupEnv)
}

func loadPortalProperties(file string, args []string, lookupEnv func(string) (string, bool)) (map[string]string, error) {
	fromFile := map[string]string{}
	if file != "" {
		var err error
		if fromFile, err = ParseApplicationPropertiesFile(file); err != nil {
			return nil, err
		}
		resolveFilePaths(fromFile, filepath.Dir(file))
	}
	fromArgs := map[string]string{}
	for _, a := range args {
		kv, ok := strings.CutPrefix(a, "--")
		key, value, hasValue := strings.Cut(kv, "=")
		if !ok || !hasValue || key == "" {
			return nil, fmt.Errorf("argument %q is not of the form --key=value", a)
		}
		fromArgs[key] = value
	}
	return resolvePlaceholders(fromFile, fromArgs, lookupEnv)
}

// resolvePlaceholders merges the file's properties with the arguments and
// replaces Spring placeholders in their values: ${name} or ${name:default}.
// As in Spring Boot, name is looked up in the command-line arguments, then
// the environment (as written, then upper-cased with '.' and '-' as '_'),
// then the properties file. Secrets can so stay in the environment, e.g.
// --spring.data.redis.password=${REDIS_SESSION_PASSWORD}, out of the image
// and the process list. A placeholder that is not set and has no default is
// an error naming the property and the placeholder, never a value.
func resolvePlaceholders(fromFile, fromArgs map[string]string, lookupEnv func(string) (string, bool)) (map[string]string, error) {
	props := maps.Clone(fromFile)
	maps.Copy(props, fromArgs)
	envName := strings.NewReplacer(".", "_", "-", "_")
	lookup := func(name string) (string, bool) {
		if v, ok := fromArgs[name]; ok {
			return v, true
		}
		if v, ok := lookupEnv(name); ok {
			return v, true
		}
		if v, ok := lookupEnv(strings.ToUpper(envName.Replace(name))); ok {
			return v, true
		}
		v, ok := fromFile[name]
		return v, ok
	}
	for _, k := range slices.Sorted(maps.Keys(props)) {
		v := props[k]
		if !strings.Contains(v, "${") {
			continue
		}
		var b strings.Builder
		for {
			start := strings.Index(v, "${")
			if start < 0 {
				b.WriteString(v)
				break
			}
			end := strings.IndexByte(v[start:], '}')
			if end < 0 {
				return nil, fmt.Errorf("property %s: unterminated placeholder", k)
			}
			end += start
			name, def, hasDef := strings.Cut(v[start+2:end], ":")
			val, ok := lookup(name)
			if !ok || strings.Contains(val, "${") {
				if !hasDef {
					return nil, fmt.Errorf("property %s: placeholder ${%s} is not set", k, name)
				}
				val = def
			}
			b.WriteString(v[:start])
			b.WriteString(val)
			v = v[end+1:]
		}
		props[k] = b.String()
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

// KnownProperty reports whether this server reads the portal property k.
func KnownProperty(k string) bool {
	used, _ := ClassifyProperties(map[string]string{k: ""})
	return len(used) == 1
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
// authentication, login sessions, data access tokens and study
// authorization.
func backendProperty(k string) bool {
	switch k {
	case "filter_groups_by_appname", "always_show_study_group", "dat.filter_user_role",
		"authorization", "security.method_authorization_enabled", "security.force_redirect_on_one_idp",
		"spring.session.store-type":
		return true
	}
	for _, prefix := range []string{"spring.security.oauth2.client.", "dat.oauth2.", "spring.data.redis."} {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// fileValuedProperties name other files; their contents are served to the
// frontend, or as /llms.txt.
var fileValuedProperties = []string{
	"frontend.config",
	"llms_txt.location",
	"querypage.setsofgenes.location",
	"skin.patient_view.custom_sample_type_colors_json",
	"oncoprint.clinical_tracks.config_json",
	"download_custom_buttons_json",
}

// resolveFilePaths makes the relative file paths of a properties file
// relative to the file's own directory when the file exists there, so a
// properties file and the files it names can move together. Other paths are
// left as given (relative to the working directory, as Java reads them).
func resolveFilePaths(props map[string]string, dir string) {
	for _, key := range fileValuedProperties {
		v := strings.TrimSpace(props[key])
		if v == "" || filepath.IsAbs(v) || strings.HasPrefix(v, "classpath:") {
			continue
		}
		if p := filepath.Join(dir, v); fileExists(p) {
			props[key] = p
		}
	}
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

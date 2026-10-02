package config

import (
	"fmt"
	"maps"
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

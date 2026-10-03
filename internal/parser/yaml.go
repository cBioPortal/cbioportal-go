package parser

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// YAMLMap is a YAML mapping that keeps its keys in document order, so its JSON
// encoding matches cbioportal-core's Jackson YAML→JSON conversion (which reads
// mappings into insertion-ordered LinkedHashMaps).
type YAMLMap []YAMLEntry

// YAMLEntry is one key/value pair of a YAMLMap.
type YAMLEntry struct {
	Key   string
	Value any
}

// MarshalJSON encodes the mapping as a JSON object in document order.
func (m YAMLMap) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, e := range m {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(e.Key)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		v, err := json.Marshal(e.Value)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// ParseYAML parses a YAML document whose top level is a mapping or a
// sequence, as study tags files are. Mappings decode to YAMLMap (document
// order), sequences to []any, and scalars to nil, bool, json.Number or string
// by the YAML core schema, as Jackson's untyped read does.
func ParseYAML(text string) (any, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, err
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return nil, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode && root.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("yaml line %d: expected a mapping or a sequence", root.Line)
	}
	return yamlValue(root)
}

// yamlValue converts a parsed YAML node as ParseYAML does.
func yamlValue(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return yamlValue(n.Content[0])
	case yaml.AliasNode:
		return yamlValue(n.Alias)
	case yaml.MappingNode:
		m := make(YAMLMap, 0, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("yaml line %d: unsupported mapping key", k.Line)
			}
			val, err := yamlValue(v)
			if err != nil {
				return nil, err
			}
			m = append(m, YAMLEntry{Key: k.Value, Value: val})
		}
		return m, nil
	case yaml.SequenceNode:
		s := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := yamlValue(c)
			if err != nil {
				return nil, err
			}
			s = append(s, v)
		}
		return s, nil
	case yaml.ScalarNode:
		return yamlScalar(n), nil
	}
	return nil, errors.New("yaml: unsupported node")
}

func yamlScalar(n *yaml.Node) any {
	switch n.ShortTag() {
	case "!!null":
		return nil
	case "!!bool":
		var b bool
		if n.Decode(&b) == nil {
			return b
		}
	case "!!int":
		var i int64
		if n.Decode(&i) == nil {
			return json.Number(strconv.FormatInt(i, 10))
		}
	case "!!float":
		var f float64
		if n.Decode(&f) == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
			if json.Valid([]byte(n.Value)) {
				return json.Number(n.Value)
			}
			return json.Number(strconv.FormatFloat(f, 'g', -1, 64))
		}
	}
	return n.Value
}

// ReadYAMLAsJSON reads a YAML file and returns its JSON encoding.
func ReadYAMLAsJSON(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	v, err := ParseYAML(strings.ReplaceAll(string(b), "\r\n", "\n"))
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

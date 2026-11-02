package state

import (
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

var configType = reflect.TypeOf(Config{})

// unknownKeys walks a decoded YAML mapping alongside the Go type it will be
// decoded into and returns a warning for every key that has no matching field.
func unknownKeys(n *yaml.Node, t reflect.Type, prefix string) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if n == nil {
		return nil
	}
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	var warns []string
	switch t.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			return nil // type mismatch is reported by Decode
		}
		fields := yamlFields(t)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			path := joinPath(prefix, key)
			ft, ok := fields[key]
			if !ok {
				warns = append(warns, fmt.Sprintf("unknown key %q (line %d)", path, n.Content[i].Line))
				continue
			}
			warns = append(warns, unknownKeys(n.Content[i+1], ft, path)...)
		}
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			warns = append(warns, unknownKeys(n.Content[i+1], t.Elem(), joinPath(prefix, n.Content[i].Value))...)
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			return nil
		}
		for i, item := range n.Content {
			warns = append(warns, unknownKeys(item, t.Elem(), fmt.Sprintf("%s[%d]", prefix, i))...)
		}
	}
	return warns
}

// yamlFields maps YAML key → field type for t, honoring inline structs.
func yamlFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("yaml")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if strings.Contains(opts, "inline") {
			for k, v := range yamlFields(f.Type) {
				out[k] = v
			}
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		out[name] = f.Type
	}
	return out
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package wirecontract

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Generate describes the JSON structure of an authoring type using draft 2020-12.
// It is not a replacement for semantic validation. Overrides describe custom JSON
// marshalers and named vocabularies; recursive types and unsupported shapes fail
// explicitly rather than publishing a misleading schema.
func Generate(t reflect.Type, overrides map[reflect.Type]map[string]any) (map[string]any, error) {
	s, err := generate(t, overrides, map[reflect.Type]bool{})
	if err != nil {
		return nil, err
	}
	s["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	return s, nil
}

func generate(t reflect.Type, overrides map[reflect.Type]map[string]any, active map[reflect.Type]bool) (map[string]any, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if s, ok := overrides[t]; ok {
		out := map[string]any{}
		for k, v := range s {
			out[k] = v
		}
		return out, nil
	}
	if active[t] {
		return nil, fmt.Errorf("recursive schema type %s", t)
	}
	if t.Implements(reflect.TypeFor[json.Marshaler]()) || reflect.PointerTo(t).Implements(reflect.TypeFor[json.Unmarshaler]()) {
		return nil, fmt.Errorf("%s needs a custom JSON schema override", t)
	}
	active[t] = true
	defer delete(active, t)
	s := map[string]any{}
	switch t.Kind() {
	case reflect.Struct:
		properties := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.Anonymous {
				child, err := generate(f.Type, overrides, active)
				if err != nil {
					return nil, err
				}
				for k, v := range child["properties"].(map[string]any) {
					properties[k] = v
				}
				if r, ok := child["required"].([]string); ok {
					required = append(required, r...)
				}
				continue
			}
			if !f.IsExported() || f.Tag.Get("json") == "-" {
				continue
			}
			tag := strings.Split(f.Tag.Get("json"), ",")
			if tag[0] == "" {
				return nil, fmt.Errorf("%s.%s needs a named json tag", t, f.Name)
			}
			child, err := generate(f.Type, overrides, active)
			if err != nil {
				return nil, err
			}
			properties[tag[0]] = child
			if len(tag) == 1 {
				required = append(required, tag[0])
			}
		}
		s["type"], s["properties"], s["additionalProperties"] = "object", properties, false
		if len(required) > 0 {
			s["required"] = required
		}
	case reflect.Slice, reflect.Array:
		child, err := generate(t.Elem(), overrides, active)
		if err != nil {
			return nil, err
		}
		s["type"], s["items"] = "array", child
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("%s needs string keys", t)
		}
		child, err := generate(t.Elem(), overrides, active)
		if err != nil {
			return nil, err
		}
		s["type"], s["additionalProperties"] = "object", child
	default:
		typ, err := jsonType(t)
		if err != nil || typ == "" {
			return nil, fmt.Errorf("unsupported schema type %s", t)
		}
		s["type"] = typ
	}
	return s, nil
}

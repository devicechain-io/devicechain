// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package jsonschema

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"go.yaml.in/yaml/v3"
)

// LoadCRD reads a CustomResourceDefinition and returns the openAPIV3Schema of each of
// its versions, keyed by version name. Each is normalized through JSON so the differ
// sees the same types it sees for a .json schema.
func LoadCRD(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Kind string `yaml:"kind"`
		Spec struct {
			Versions []struct {
				Name   string `yaml:"name"`
				Schema struct {
					OpenAPIV3Schema any `yaml:"openAPIV3Schema"`
				} `yaml:"schema"`
			} `yaml:"versions"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if doc.Kind != "CustomResourceDefinition" {
		return nil, fmt.Errorf("%s is a %q, not a CustomResourceDefinition", path, doc.Kind)
	}
	out := map[string]any{}
	for _, v := range doc.Spec.Versions {
		if v.Schema.OpenAPIV3Schema == nil {
			continue
		}
		j, err := json.Marshal(v.Schema.OpenAPIV3Schema)
		if err != nil {
			return nil, fmt.Errorf("%s version %s: %w", path, v.Name, err)
		}
		var n any
		if err := json.Unmarshal(j, &n); err != nil {
			return nil, err
		}
		out[v.Name] = n
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s declares no version with an openAPIV3Schema", path)
	}
	return out, nil
}

// SortedVersions lists a CRD's versions in a stable order.
func SortedVersions(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

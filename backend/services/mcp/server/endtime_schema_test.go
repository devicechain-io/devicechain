// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"strings"
	"testing"
)

func TestQueryHistoryEndTimeSchemaIsInclusive(t *testing.T) {
	_, tools := listServedTools(t)
	for _, name := range []string{"query_measurements", "query_locations"} {
		t.Run(name, func(t *testing.T) {
			for _, tool := range tools {
				if tool.Name != name {
					continue
				}
				schema, ok := tool.InputSchema.(map[string]any)
				if !ok {
					t.Fatalf("input schema is not an object: %T", tool.InputSchema)
				}
				props, ok := schema["properties"].(map[string]any)
				if !ok {
					t.Fatalf("input schema has no properties: %v", schema)
				}
				endTime, ok := props["endTime"].(map[string]any)
				if !ok {
					t.Fatalf("input schema has no endTime property: %v", props)
				}
				description, _ := endTime["description"].(string)
				lower := strings.ToLower(description)
				if !strings.Contains(lower, "inclusive") || strings.Contains(lower, "exclusive") {
					t.Errorf("endTime description must say inclusive and must not say exclusive, got %q", description)
				}
				return
			}
			t.Fatal("tool is missing from the served catalog")
		})
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// v130ConfigSnapshot was produced by reflection over v1.5.0's config.InstanceConfiguration,
// following encoding/json rules, plus its RetiredConfigKeys. This snapshot is of the
// release an upgrade starts from: when v1.6.0 becomes the oldest release that can be
// rolled from, regenerate it from that tag.
type v130ConfigSnapshot struct {
	Retired map[string]string `json:"retired"`
	Tree    v130ConfigNode    `json:"tree"`
}

type v130ConfigNode struct {
	Fields map[string]v130ConfigNode `json:"fields"`
	Map    *v130ConfigNode           `json:"map"`
	Elem   *v130ConfigNode           `json:"elem"`
	Any    bool                      `json:"any"`
}

func loadV130ConfigSnapshot(t *testing.T) v130ConfigSnapshot {
	t.Helper()
	raw, err := os.ReadFile("testdata/instance_config_keys_v1.5.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot v130ConfigSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tree.Fields) == 0 {
		t.Fatal("v1.5.0 config snapshot has no root fields")
	}
	return snapshot
}

// unknownV130ConfigPaths checks keys, including inside arbitrary maps and arrays.
// Paths omit array indices so they have the same dotted form as RetiredConfigKeys.
func unknownV130ConfigPaths(value any, node v130ConfigNode, retired map[string]string, path string) []string {
	for key := range retired {
		if strings.EqualFold(key, path) {
			return nil
		}
	}
	if node.Any || value == nil {
		return nil
	}
	var unknown []string
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if node.Map != nil {
				unknown = append(unknown, unknownV130ConfigPaths(child, *node.Map, retired, childPath)...)
				continue
			}
			// encoding/json prefers exact field names before case-insensitive matches.
			field, found := node.Fields[key]
			if !found {
				for name, candidate := range node.Fields {
					if strings.EqualFold(name, key) {
						field, found = candidate, true
						break
					}
				}
			}
			if found {
				unknown = append(unknown, unknownV130ConfigPaths(child, field, retired, childPath)...)
			} else {
				allowed := false
				for retiredPath := range retired {
					if strings.EqualFold(retiredPath, childPath) {
						allowed = true
						break
					}
				}
				if !allowed {
					unknown = append(unknown, childPath)
				}
			}
		}
	case []any:
		if node.Elem != nil {
			for _, child := range value {
				unknown = append(unknown, unknownV130ConfigPaths(child, *node.Elem, retired, path)...)
			}
		}
	}
	slices.Sort(unknown)
	return slices.Compact(unknown)
}

func TestDefaultInstanceConfigDocumentDecodesOnV130(t *testing.T) {
	snapshot := loadV130ConfigSnapshot(t)
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T, raw []byte) {
		t.Helper()
		if raw == nil {
			t.Fatal("no rendered instance configuration document")
		}
		var document map[string]any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		if len(document) == 0 {
			t.Fatal("empty instance configuration document")
		}
		if paths := unknownV130ConfigPaths(document, snapshot.Tree, snapshot.Retired, ""); len(paths) != 0 {
			t.Errorf("unknown v1.5.0 configuration keys: %s; a pod of v1.5.0 decodes this document strictly and refuses an unknown key, so a key added after v1.5.0 must be left out of the document while at its default", strings.Join(paths, ", "))
		}
	}
	t.Run("bare defaults", func(t *testing.T) {
		raw, err := renderInstanceConfigDocument(t.Context(), ch, map[string]interface{}{"instance": map[string]interface{}{
			"id": "dctest",
			"config": map[string]interface{}{"infrastructure": map[string]interface{}{
				"secrets": map[string]interface{}{"rootKey": base64.StdEncoding.EncodeToString(make([]byte, 32))},
			}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		check(t, raw)
	})
	// An operator who copies values.yaml's commented example, or dcctl clearing the allowance,
	// writes an EMPTY list; the chart must leave it out exactly as it leaves out an absent one.
	t.Run("explicitly empty devAllowedDestinations", func(t *testing.T) {
		raw, err := renderInstanceConfigDocument(t.Context(), ch, map[string]interface{}{"instance": map[string]interface{}{
			"id": "dctest",
			"config": map[string]interface{}{"infrastructure": map[string]interface{}{
				"secrets": map[string]interface{}{"rootKey": base64.StdEncoding.EncodeToString(make([]byte, 32))},
				"egress":  map[string]interface{}{"devAllowedDestinations": []interface{}{}},
			}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		check(t, raw)
	})
	// Every current profile is compatible. dcctl does not let the chart write the document for
	// an instance it installs: it composes the document itself (releaseValues ->
	// composeInstanceConfig) and installs it as the instance's existing Secret, so the document
	// checked here is the one releaseValues returns, not a render of the install values. A
	// future profile deliberately opting into a post-v1.5.0 setting needs an explicit allowlist
	// entry with its setting and reason.
	for i, p := range renderProfiles {
		t.Run(p.name, func(t *testing.T) {
			_, doc, err := releaseValues(t.Context(), ch, p.st(profileInstance(i)), nil)
			if err != nil {
				t.Fatal(err)
			}
			check(t, doc)
		})
	}
}

func TestV130ConfigWalker(t *testing.T) {
	snapshot := loadV130ConfigSnapshot(t)
	for _, tc := range []struct {
		name, document string
		want           []string
	}{
		{"unknown", `{"infrastructure":{"egress":{"noSuchKeyPlanted":[]}}}`, []string{"infrastructure.egress.noSuchKeyPlanted"}},
		{"added in v1.4.0 and so known", `{"infrastructure":{"egress":{"devAllowedDestinations":[]}}}`, nil},
		{"known case insensitive", `{"INFRASTRUCTURE":{"egress":{"allowedDestinations":[]}}}`, nil},
		{"all unknown paths", `{"unknownA":0,"unknownB":0}`, []string{"unknownA", "unknownB"}},
		{"retired case insensitive", `{"INFRASTRUCTURE":{"metrics":{"HTTPPORT":123}}}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var document any
			if err := json.Unmarshal([]byte(tc.document), &document); err != nil {
				t.Fatal(err)
			}
			if got := unknownV130ConfigPaths(document, snapshot.Tree, snapshot.Retired, ""); !slices.Equal(got, tc.want) {
				t.Errorf("unknown paths = %v, want %v", got, tc.want)
			}
		})
	}
}

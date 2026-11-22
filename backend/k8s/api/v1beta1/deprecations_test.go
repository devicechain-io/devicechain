// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The deprecations announced in 1.9 have to be visible where an operator meets them: the API
// server's own warning for the unused InstanceConfiguration kind, and the field descriptions
// that `kubectl explain` prints for the two Instance fields. These read the GENERATED manifests,
// so a marker removed from the Go type without `make manifests`, or a regeneration that drops
// it, fails here. The fields themselves stay in the schema: removing them is a later major's.

func loadCRD(t *testing.T, file string) map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", "bases", file))
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}
	return doc
}

func TestTheInstanceConfigurationVersionIsDeprecatedWithAWarning(t *testing.T) {
	crd := loadCRD(t, "core.devicechain.io_instanceconfigurations.yaml")
	versions := crd["spec"].(map[string]interface{})["versions"].([]interface{})
	if len(versions) != 1 {
		t.Fatalf("instanceconfigurations serves %d versions, want the one v1beta1", len(versions))
	}
	v := versions[0].(map[string]interface{})
	if v["deprecated"] != true {
		t.Fatalf("the InstanceConfiguration version is not marked deprecated: %v", v["deprecated"])
	}
	warning, _ := v["deprecationWarning"].(string)
	if !strings.Contains(warning, "deprecated") || !strings.Contains(warning, "mounted Secret") {
		t.Fatalf("the deprecation warning does not say what replaced it: %q", warning)
	}
	if v["served"] != true || v["storage"] != true {
		t.Fatalf("deprecating the version must not stop serving or storing it: served=%v storage=%v", v["served"], v["storage"])
	}
}

func TestTheDeprecatedInstanceFieldsSayWhatReplacesThem(t *testing.T) {
	props := specSchema(t, loadInstanceCRD(t))["properties"].(map[string]interface{})
	for field, replacement := range map[string]string{
		"compact":    "size: compact",
		"grafanaSSO": "cluster configuration",
	} {
		node, ok := props[field].(map[string]interface{})
		if !ok {
			t.Fatalf("spec.%s is gone from the schema; a deprecation keeps it until a major release removes it", field)
		}
		desc, _ := node["description"].(string)
		if !strings.Contains(desc, "Deprecated:") || !strings.Contains(desc, replacement) {
			t.Fatalf("spec.%s's description does not mark it deprecated and name %q:\n%s", field, replacement, desc)
		}
	}
}

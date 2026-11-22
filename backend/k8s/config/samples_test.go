// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// sampleFiles are the samples the samples kustomization lists, read from it so a sample added
// there is checked here without anyone remembering to add it.
func sampleFiles(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("samples", "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var k struct {
		Resources []string `json:"resources"`
	}
	if err := yaml.Unmarshal(b, &k); err != nil {
		t.Fatal(err)
	}
	if len(k.Resources) == 0 {
		t.Fatal("the samples kustomization lists no samples")
	}
	return k.Resources
}

// Every sample is a filled-in resource of the kind it names, decoded STRICTLY into the API type:
// a field the type does not have, or a scaffolding placeholder, fails here. Whether the API
// server admits it (CEL and all) is controllers' TestTheSamplesAreAdmittedByTheAPIServer.
func TestTheSamplesAreFilledInResourcesOfTheirKind(t *testing.T) {
	for _, f := range sampleFiles(t) {
		b, err := os.ReadFile(filepath.Join("samples", f))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "TODO") {
			t.Errorf("%s still carries a TODO", f)
		}
		var meta struct {
			Kind string `json:"kind"`
		}
		if err := yaml.Unmarshal(b, &meta); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		switch meta.Kind {
		case "Instance":
			var inst dcv1beta1.Instance
			if err := yaml.UnmarshalStrict(b, &inst); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			if inst.Spec.Provider == "" || inst.Spec.Cluster == "" || inst.Spec.Profile == "" {
				t.Errorf("%s: the sample Instance leaves provider, cluster or profile empty: %+v", f, inst.Spec)
			}
		case "InstanceConfiguration":
			var ic dcv1beta1.InstanceConfiguration
			if err := yaml.UnmarshalStrict(b, &ic); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			if len(ic.Spec.Configuration.RawMessage) == 0 {
				t.Errorf("%s: the sample InstanceConfiguration has no configuration document", f)
			}
		default:
			t.Errorf("%s: a sample of kind %q, which this test does not know how to check", f, meta.Kind)
		}
	}
}

// The manager Deployment carries no scaffolding placeholder.
func TestTheManagerDeploymentCarriesNoScaffoldingTODO(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("manager", "manager.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "TODO(user)") {
		t.Error("config/manager/manager.yaml still carries a TODO(user)")
	}
}

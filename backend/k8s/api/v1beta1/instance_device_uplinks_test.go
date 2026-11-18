// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1_test

import (
	"fmt"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Assert by wire value rather than by a Go field: this fails even when compiled
// against the old type, which silently forgets a field it does not know.
func TestInstanceDeviceUplinksSurvivesTypedConversion(t *testing.T) {
	for _, choice := range []bool{true, false} {
		t.Run(fmt.Sprint(choice), func(t *testing.T) {
			wire := map[string]any{
				"apiVersion": "core.devicechain.io/v1beta1",
				"kind":       "Instance",
				"metadata":   map[string]any{"name": "prod"},
				"spec": map[string]any{
					"provider": "local", "profile": "default", "managed": false,
					"mqttDeviceUplinks": choice,
				},
			}
			var inst dcv1beta1.Instance
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(wire, &inst); err != nil {
				t.Fatal(err)
			}
			back, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&inst)
			if err != nil {
				t.Fatal(err)
			}
			spec, ok := back["spec"].(map[string]any)
			if !ok {
				t.Fatal("spec disappeared")
			}
			if got, present := spec["mqttDeviceUplinks"]; !present || got != choice {
				t.Fatalf("typed conversion lost mqttDeviceUplinks=%t: %v", choice, spec)
			}
		})
	}
}

func TestInstanceDeviceUplinksHasBooleanSchema(t *testing.T) {
	// loadInstanceCRD unmarshals ../../config/crd/bases/core.devicechain.io_instances.yaml
	// using the k8s module's existing sigs.k8s.io/yaml dependency.
	props := specSchema(t, loadInstanceCRD(t))["properties"].(map[string]any)
	field, ok := props["mqttDeviceUplinks"].(map[string]any)
	if !ok || field["type"] != "boolean" {
		t.Fatalf("mqttDeviceUplinks must have a boolean schema: %v", props["mqttDeviceUplinks"])
	}
}

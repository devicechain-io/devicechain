// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// NOT THROUGH ANY ROUTE. With the broadest profile, an ingress and several listeners on, the
// profiling port appears in Deployment env values and NOWHERE else: no Service, Ingress,
// HTTPRoute, Gateway or NetworkPolicy mentions it. This is the guard for the run-only
// switch, and it fails the day a template starts routing the profiler.
func TestTheProfilerPortIsInNoRoutableObject(t *testing.T) {
	t.Parallel()
	areas := []string{"device-management", "event-sources", "event-management", "event-processing"}
	fa := map[string]interface{}{}
	for _, a := range areas {
		fa[a] = map[string]interface{}{"profiler": map[string]interface{}{"enabled": true, "blockRate": 10000, "mutexFraction": 5}}
	}
	manifest, err := renderChart(t, map[string]interface{}{
		"profile": "full",
		// full renders mcp, which derives its required URLs from the ingress.
		"ingress":         map[string]interface{}{"enabled": true, "host": "localhost", "tls": map[string]interface{}{"enabled": false}},
		"functionalAreas": fa,
	})
	if err != nil {
		t.Fatalf("rendering the full profile with listeners on: %v", err)
	}
	envHits := 0
	for _, doc := range splitManifests(manifest) {
		if !strings.Contains(doc, "6060") {
			continue
		}
		var obj struct {
			Kind string `json:"kind"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("decoding a rendered manifest: %v", err)
		}
		if obj.Kind == "Deployment" {
			envHits++
			continue
		}
		t.Errorf("a %s mentions the profiling port 6060; only a Deployment's env value may", obj.Kind)
	}
	// The reader must see the port where it is meant to be, or the loop above proves nothing.
	if envHits != len(areas) {
		t.Errorf("%d Deployments carry the profiling port, want %d (one per enabled area)", envHits, len(areas))
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	mcpconfig "github.com/devicechain-io/dc-mcp/config"
	"github.com/devicechain-io/dc-microservice/core"
	"sigs.k8s.io/yaml"
)

// Render client-side and load the exact ConfigMap entry the MCP pod receives through
// its own strict, validating loader. No apiserver or KUBEBUILDER_ASSETS is needed.
func TestChartMcpAgentActuationIsOptIn(t *testing.T) {
	t.Parallel()
	ch := chartForTest(t)
	for _, tc := range []struct {
		name    string
		enabled *bool
		want    bool
	}{
		{name: "default is off"},
		{name: "operator enables Preview", enabled: boolPtr(true), want: true},
		{name: "operator explicitly disables Preview", enabled: boolPtr(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vals := tokenAudienceValues(tlsIngress("iot.example.com"), "full", "", nil)
			if tc.enabled != nil {
				overlay := map[string]interface{}{
					"functionalAreas": map[string]interface{}{
						"mcp": map[string]interface{}{
							"config": map[string]interface{}{
								"agentActuation": map[string]interface{}{"enabled": *tc.enabled},
							},
						},
					},
				}
				// This is the established dcctl --values path, not a new feature flag.
				if err := ValidateValuesOverlay(overlay); err != nil {
					t.Fatalf("dcctl refuses the operator's overlay: %v", err)
				}
				mergeOverlay(vals, overlay)
			}
			manifest, err := renderChartClientSide(t.Context(), ch, vals)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			for _, doc := range splitManifests(manifest) {
				var cm struct {
					Kind     string                `json:"kind"`
					Metadata struct{ Name string } `json:"metadata"`
					Data     map[string]string     `json:"data"`
				}
				if err := yaml.Unmarshal([]byte(doc), &cm); err != nil || cm.Kind != "ConfigMap" || cm.Metadata.Name != "dct-dctest-config" {
					continue
				}
				raw, ok := cm.Data["mcp"]
				if !ok {
					t.Fatal("the full profile's per-service ConfigMap has no mcp entry")
				}
				cfg := &mcpconfig.McpConfiguration{}
				if err := core.LoadConfiguration([]byte(raw), cfg); err != nil {
					t.Fatalf("MCP would refuse its rendered configuration: %v\n%s", err, raw)
				}
				if cfg.AgentActuation.Enabled != tc.want {
					t.Fatalf("agentActuation.enabled = %v, want %v; config: %s", cfg.AgentActuation.Enabled, tc.want, raw)
				}
				return
			}
			t.Fatal("no per-service ConfigMap dct-dctest-config in the render")
		})
	}
}

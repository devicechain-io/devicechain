// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"
)

// Values that would render a document the services refuse at startup must fail at render (or be
// left out) with a reason, because the alternative is every pod crash-looping on a helm upgrade.

// A key that is not a tokenAudience field is refused by the values schema, before any pod sees
// the document (the services strict-decode it and would refuse it too).
func TestTokenAudienceSchemaRefusesUnknownKeysAndBadExternalResources(t *testing.T) {
	t.Parallel()
	ch := chartForTest(t)
	for _, tc := range []struct {
		name string
		ta   map[string]interface{}
	}{
		{"typo in a key", map[string]interface{}{"enforcment": "enforce"}},
		{"bad enforcement value", map[string]interface{}{"enforcement": "enforced"}},
		{"external over plain http on a real host", map[string]interface{}{"externalResources": []interface{}{"http://partner.example.org"}}},
		{"external with an uppercase host", map[string]interface{}{"externalResources": []interface{}{"https://Partner.example.org"}}},
		{"external with a trailing slash", map[string]interface{}{"externalResources": []interface{}{"https://partner.example.org/"}}},
		{"external with an encoded trailing slash", map[string]interface{}{"externalResources": []interface{}{"https://partner.example.org/v1%2F"}}},
		{"external with a query", map[string]interface{}{"externalResources": []interface{}{"https://partner.example.org/x?a=b"}}},
		{"external with userinfo", map[string]interface{}{"externalResources": []interface{}{"https://u@partner.example.org"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vals := tokenAudienceValues(tlsIngress("iot.example.com"), "full", "", tc.ta)
			if _, err := renderChartClientSide(t.Context(), ch, vals); err == nil {
				t.Fatalf("%v rendered: the services would refuse this document", tc.ta)
			}
		})
	}

	// Counterweight: well-formed values still render and load.
	vals := tokenAudienceValues(tlsIngress("iot.example.com"), "full", "", map[string]interface{}{
		"enforcement":       "enforce",
		"externalResources": []interface{}{"https://partner.example.org", "https://partner.example.org:8443/v1", "http://localhost:9000"},
	})
	raw, err := renderInstanceConfigDocument(t.Context(), ch, vals)
	if err != nil {
		t.Fatalf("well-formed values were refused: %v", err)
	}
	if _, err := loadInstanceConfigDocument(raw); err != nil {
		t.Fatalf("well-formed values rendered a document the services refuse: %v", err)
	}
}

// Entries the schema cannot judge (they depend on the derived values) are refused by the chart
// with a clear message and no decision-record citation.
func TestChartRefusesExternalResourcesThatCollideWithDerivedOnes(t *testing.T) {
	t.Parallel()
	ch := chartForTest(t)
	for _, tc := range []struct {
		name string
		ext  []interface{}
		want string
	}{
		{"mcp's own resource", []interface{}{"https://iot.example.com/api/mcp"}, "MCP resource"},
		{"an area's identifier", []interface{}{"https://iot.example.com/api/device-management"}, "re-label"},
		{"the API base itself", []interface{}{"https://iot.example.com/api"}, "re-label"},
		{"a duplicate", []interface{}{"https://partner.example.org", "https://partner.example.org"}, "twice"},
	} {
		for _, src := range []string{"inline", "external Secret"} {
			t.Run(tc.name+"/"+src, func(t *testing.T) {
				ta := map[string]interface{}{"externalResources": tc.ext}
				vals := tokenAudienceValues(tlsIngress("iot.example.com"), "full", "", ta)
				if src == "external Secret" {
					vals = externalValues(digest("a"))
					vals["profile"] = "full"
					vals["ingress"] = tlsIngress("iot.example.com")
					vals["instance"].(map[string]interface{})["config"] = map[string]interface{}{
						"infrastructure": map[string]interface{}{"tokenAudience": ta},
					}
				}
				_, err := renderChartClientSide(t.Context(), ch, vals)
				if err == nil {
					t.Fatal("rendered: the services would refuse this document")
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Errorf("the refusal is not the clear one (want %q): %v", tc.want, err)
				}
				if strings.Contains(strings.ToLower(err.Error()), "adr-") {
					t.Errorf("a render message must not cite a decision record: %v", err)
				}
			})
		}
	}
}

// An unregistrable MCP override fails under enforce and unset. Legacy observe is refused
// by the schema, before any document is rendered.
// A name that merely resembles an area is not a clash.
func TestUnregisterableMcpOverrideAndObserveAreRefused(t *testing.T) {
	t.Parallel()
	ch := chartForTest(t)
	enforce := map[string]interface{}{"enforcement": "enforce"}
	observe := map[string]interface{}{"enforcement": "observe"}

	for _, tc := range []struct {
		name     string
		ingress  map[string]interface{}
		override string
	}{
		{"an area identifier", plainIngress("localhost"), "http://localhost/api/device-management"},
		{"a mixed-case host", nil, "https://MCP.example.com/api/mcp"},
		{"an uppercase scheme", nil, "HTTPS://mcp.example.com/api/mcp"},
		{"an uppercase loopback scheme", tlsIngress("iot.example.com"), "HTTP://localhost:8080"},
		{"a mixed-case loopback host", tlsIngress("iot.example.com"), "http://LOCALHOST:8080"},
	} {
		t.Run(tc.name+"/observe", func(t *testing.T) {
			_, err := renderInstanceConfigDocument(t.Context(), ch, tokenAudienceValues(tc.ingress, "full", tc.override, observe))
			if err == nil || !strings.Contains(err.Error(), "enforcement") {
				t.Fatalf("v1.4 must refuse observe in schema: %v", err)
			}
		})
		for _, mode := range []struct {
			name string
			ta   map[string]interface{}
		}{{"enforce", enforce}, {"unset (the default, enforce)", nil}} {
			t.Run(tc.name+"/"+mode.name, func(t *testing.T) {
				_, err := renderChartClientSide(t.Context(), ch, tokenAudienceValues(tc.ingress, "full", tc.override, mode.ta))
				if err == nil {
					t.Fatal("rendered an enforcing instance whose MCP calls would all be refused")
				}
				if !strings.Contains(err.Error(), "lowercase") || strings.Contains(err.Error(), "enforcement to observe") ||
					strings.Contains(strings.ToLower(err.Error()), "adr-") {
					t.Errorf("the refusal is not the clear one: %v", err)
				}
			})
		}
	}

	// A single segment that is not an area name is kept.
	raw, err := renderInstanceConfigDocument(t.Context(), ch, tokenAudienceValues(plainIngress("localhost"), "full", "http://localhost/api/foo", enforce))
	if err != nil {
		t.Fatalf("an override that is not an area identifier was refused: %v", err)
	}
	cfg, err := loadInstanceConfigDocument(raw)
	if err != nil {
		t.Fatalf("the document does not load: %v", err)
	}
	if got := cfg.Infrastructure.TokenAudience.McpResources; len(got) != 1 || got[0] != "http://localhost/api/foo" {
		t.Errorf("mcpResources = %v, want the non-area override kept", got)
	}
}

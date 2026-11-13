// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
)

// The chart WRITES two of the token-audience keys (areaBaseUrl and mcpResources) from the
// ingress and the enabled areas. Until it does, every instance runs with no MCP resource
// registered and every MCP-forwarded call is counted as a would-reject. These render the
// real embedded chart through the real Helm engine and load the result through the services'
// own strict loader (core.LoadConfiguration, which runs Validate), so "the derived block is
// one the services accept" is measured and not assumed.
//
// THE LOAD IS THE POINT. A derived value the validator refuses is not a bad default, it is
// every pod crash-looping on the upgrade. The http-on-a-real-host case is the one the guard
// exists for.

func tokenAudienceValues(ingress map[string]interface{}, profile string, mcpResourceURL string, ta map[string]interface{}) map[string]interface{} {
	infra := map[string]interface{}{
		"secrets": map[string]interface{}{
			"rootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
		},
	}
	if ta != nil {
		infra["tokenAudience"] = ta
	}
	vals := map[string]interface{}{
		"instance": map[string]interface{}{
			"id":     "dctest",
			"config": map[string]interface{}{"infrastructure": infra},
		},
	}
	if ingress != nil {
		vals["ingress"] = ingress
	}
	if profile != "" {
		vals["profile"] = profile
	}
	if mcpResourceURL != "" {
		vals["functionalAreas"] = map[string]interface{}{
			"mcp": map[string]interface{}{"config": map[string]interface{}{"resourceUrl": mcpResourceURL}},
		}
		if ingress == nil {
			// With no ingress to derive from, mcp's issuer has to be stated too.
			vals["functionalAreas"].(map[string]interface{})["mcp"].(map[string]interface{})["config"].(map[string]interface{})["issuerUrl"] = "https://iot.example.com/api/user-management"
		}
	}
	return vals
}

func tlsIngress(host string) map[string]interface{} {
	return map[string]interface{}{"enabled": true, "host": host, "tls": map[string]interface{}{"enabled": true}}
}

func plainIngress(host string) map[string]interface{} {
	return map[string]interface{}{"enabled": true, "host": host, "tls": map[string]interface{}{"enabled": false}}
}

func TestChartDerivesTheTokenAudienceBlockAndTheServicesAcceptIt(t *testing.T) {
	t.Parallel()
	ch := chartForTest(t)
	for _, tc := range []struct {
		name      string
		vals      map[string]interface{}
		wantBase  string
		wantMcp   []string
		wantOther func(t *testing.T, enforcement string, external []string)
	}{
		{"no ingress, default profile", tokenAudienceValues(nil, "", "", nil), "", nil, nil},
		{"ingress disabled, full profile has no origin to derive from",
			tokenAudienceValues(map[string]interface{}{"enabled": false}, "", "", nil), "", nil, nil},
		{"https ingress, default profile (no mcp)", tokenAudienceValues(tlsIngress("iot.example.com"), "", "", nil),
			"https://iot.example.com/api", nil, nil},
		{"https ingress, full profile derives mcp, which must not collide with the area set",
			tokenAudienceValues(tlsIngress("iot.example.com"), "full", "", nil),
			"https://iot.example.com/api", []string{"https://iot.example.com/api/mcp"}, nil},
		{"mixed-case host is lowercased", tokenAudienceValues(tlsIngress("IoT.Example.COM"), "full", "", nil),
			"https://iot.example.com/api", []string{"https://iot.example.com/api/mcp"}, nil},
		{"custom port", tokenAudienceValues(tlsIngress("iot.example.com:8443"), "full", "", nil),
			"https://iot.example.com:8443/api", []string{"https://iot.example.com:8443/api/mcp"}, nil},
		{"http ingress on a real host derives NO base (it would crash-loop every pod)",
			tokenAudienceValues(plainIngress("iot.example.com"), "", "", nil), "", nil, nil},
		{"loopback http ingress, full profile", tokenAudienceValues(plainIngress("localhost"), "full", "", nil),
			"http://localhost/api", []string{"http://localhost/api/mcp"}, nil},
		{"loopback mcp override behind an https ingress",
			tokenAudienceValues(tlsIngress("iot.example.com"), "full", "http://localhost:8080", nil),
			"https://iot.example.com/api", []string{"http://localhost:8080"}, nil},
		{"a bare IPv6 loopback host does not parse as loopback and derives no base",
			tokenAudienceValues(plainIngress("::1"), "telemetry", "", nil), "", nil, nil},
		{"loopback host with a port derives no base, as the issuer does not",
			tokenAudienceValues(plainIngress("localhost:8080"), "telemetry", "", nil), "", nil, nil},
		{"reduced profile without mcp derives the base only",
			tokenAudienceValues(tlsIngress("iot.example.com"), "ingest-only", "", nil), "https://iot.example.com/api", nil, nil},
		{"operator keys pass through",
			tokenAudienceValues(tlsIngress("iot.example.com"), "full", "", map[string]interface{}{
				"enforcement":               "enforce",
				"unscopedClientCredentials": "observe",
				"externalResources":         []interface{}{"https://partner.example.org/api"},
			}),
			"https://iot.example.com/api", []string{"https://iot.example.com/api/mcp"},
			func(t *testing.T, enforcement string, external []string) {
				if enforcement != "enforce" {
					t.Errorf("enforcement = %q, want it passed through as enforce", enforcement)
				}
				if !reflect.DeepEqual(external, []string{"https://partner.example.org/api"}) {
					t.Errorf("externalResources = %v, want the operator's entry passed through", external)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := renderInstanceConfigDocument(t.Context(), ch, tc.vals)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if raw == nil {
				t.Fatal("no instance document was rendered")
			}
			// The services' own strict, validating load.
			cfg, err := loadInstanceConfigDocument(raw)
			if err != nil {
				t.Fatalf("the services would refuse to start on this document: %v\n%s", err, raw)
			}
			ta := cfg.Infrastructure.TokenAudience
			if ta.AreaBaseUrl != tc.wantBase {
				t.Errorf("areaBaseUrl = %q, want %q", ta.AreaBaseUrl, tc.wantBase)
			}
			if !reflect.DeepEqual(ta.McpResources, tc.wantMcp) {
				t.Errorf("mcpResources = %v, want %v", ta.McpResources, tc.wantMcp)
			}
			if tc.wantOther != nil {
				tc.wantOther(t, ta.Enforcement, ta.ExternalResources)
			}
		})
	}
}

// NEGATIVE CONTROL. The loader must be able to refuse what the chart would otherwise have
// written for a plain-http real host; otherwise "the document loads" above proves nothing about
// the guard. The base the guard withholds is refused by the validator when written by hand.
func TestTheValidatorRefusesTheBaseTheChartWithholdsForPlainHTTP(t *testing.T) {
	t.Parallel()
	ch := chartForTest(t)
	good, err := renderInstanceConfigDocument(t.Context(), ch, tokenAudienceValues(tlsIngress("iot.example.com"), "", "", nil))
	if err != nil || good == nil {
		t.Fatalf("rendering the known-good document: %v", err)
	}
	if _, err := loadInstanceConfigDocument(good); err != nil {
		t.Fatalf("the https document must load, or this control proves nothing: %v", err)
	}
	doc := []byte(strings.Replace(string(good), `"areaBaseUrl":"https://iot.example.com/api"`, `"areaBaseUrl":"http://iot.example.com/api"`, 1))
	if string(doc) == string(good) {
		t.Fatal("the mutation did not apply; this control would prove nothing")
	}
	_, err = loadInstanceConfigDocument(doc)
	if err == nil {
		t.Fatal("http://iot.example.com/api loaded: the chart's https-or-loopback guard is guarding nothing")
	}
	if !strings.Contains(err.Error(), "areaBaseUrl") {
		t.Errorf("the refusal does not name the key: %v", err)
	}
}

// The derived keys are refused when set by hand, under BOTH config sources, and the message
// says what to do instead.
func TestHandSetDerivedTokenAudienceKeysAreRefusedOnBothConfigSources(t *testing.T) {
	t.Parallel()
	ch := chartForTest(t)
	for _, key := range []string{"areaBaseUrl", "mcpResources"} {
		var val interface{} = "https://iot.example.com/api"
		if key == "mcpResources" {
			val = []interface{}{"https://iot.example.com/api/mcp"}
		}
		for _, src := range []string{"inline config", "external Secret"} {
			t.Run(key+"/"+src, func(t *testing.T) {
				var vals map[string]interface{}
				if src == "inline config" {
					vals = tokenAudienceValues(tlsIngress("iot.example.com"), "full", "", map[string]interface{}{key: val})
				} else {
					vals = externalValues(digest("a"))
					vals["instance"].(map[string]interface{})["config"] = map[string]interface{}{
						"infrastructure": map[string]interface{}{"tokenAudience": map[string]interface{}{key: val}},
					}
				}
				_, err := renderChartClientSide(t.Context(), ch, vals)
				if err == nil {
					t.Fatalf("a hand-set %s rendered without complaint", key)
				}
				msg := err.Error()
				if !strings.Contains(msg, "tokenAudience."+key) || !strings.Contains(msg, "set by the chart, not by hand") {
					t.Errorf("the refusal is not the clear one: %v", err)
				}
				if strings.Contains(strings.ToLower(msg), "adr-") {
					t.Errorf("a render message is printed to the operator and must not cite a decision record: %v", err)
				}
			})
		}
	}
}

// The authored-document check refuses a missing MCP resource and the legacy observe
// opt-out. A complete enforcing document passes silently.
func TestAuthoredDocumentMustListMcpsResource(t *testing.T) {
	// Not parallel: it swaps the package-level warning writer.
	ch := chartForTest(t)

	inline := tokenAudienceValues(tlsIngress("iot.example.com"), "full", "", nil)
	good, err := renderInstanceConfigDocument(t.Context(), ch, inline)
	if err != nil || good == nil {
		t.Fatalf("rendering the known-good document: %v", err)
	}

	external := func() map[string]interface{} {
		v := externalValues(digest("a"))
		v["profile"] = "full"
		v["ingress"] = tlsIngress("iot.example.com")
		return v
	}

	if err := validateRenderedInstanceConfig(t.Context(), ch, external(), good); err != nil {
		t.Errorf("an authored document listing mcp's resource was refused: %v", err)
	}

	missing := []byte(strings.Replace(string(good), `"mcpResources":["https://iot.example.com/api/mcp"]`, `"mcpResources":[]`, 1))
	if string(missing) == string(good) {
		t.Fatal("the mutation did not apply; this control would prove nothing")
	}
	// Absent enforcement is enforce from v1.3: the omission is REFUSED at deploy time, naming the
	// key and the value to add.
	var warned strings.Builder
	prev := authoredWarnOut
	authoredWarnOut = &warned
	err = validateRenderedInstanceConfig(t.Context(), ch, external(), missing)
	authoredWarnOut = prev
	if err == nil {
		t.Fatal("a document with no enforcement (enforce by default) omitting mcp's resource passed the pre-flight")
	}
	for _, want := range []string{"mcpResources", "https://iot.example.com/api/mcp", "unset", "scheme and host must be lowercase"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}

	// A legacy explicit observe is refused by the services' strict loader.
	explicitObserve := []byte(strings.Replace(string(missing), `"tokenAudience":{`, `"tokenAudience":{"enforcement":"observe",`, 1))
	if string(explicitObserve) == string(missing) {
		t.Fatal("the mutation did not apply; this control would prove nothing")
	}
	warned.Reset()
	authoredWarnOut = &warned
	err = validateRenderedInstanceConfig(t.Context(), ch, external(), explicitObserve)
	authoredWarnOut = prev
	if err == nil || !strings.Contains(err.Error(), "removed in v1.4") || !strings.Contains(err.Error(), "Remove the key") {
		t.Fatalf("authored existingSecret document must refuse observe with the fix: %v", err)
	}

	// NEGATIVE CONTROL: enforce still refuses, and says why.
	enforce := []byte(strings.Replace(string(missing), `"tokenAudience":{`, `"tokenAudience":{"enforcement":"enforce",`, 1))
	if string(enforce) == string(missing) {
		t.Fatal("the mutation did not apply; this control would prove nothing")
	}
	warned.Reset()
	authoredWarnOut = &warned
	err = validateRenderedInstanceConfig(t.Context(), ch, external(), enforce)
	authoredWarnOut = prev
	if err == nil {
		t.Fatal("an enforcing document omitting mcp's resource passed the pre-flight")
	}
	if !strings.Contains(err.Error(), "mcpResources") || !strings.Contains(err.Error(), "https://iot.example.com/api/mcp") {
		t.Errorf("the refusal does not name the key and the value to add: %v", err)
	}

	// A listed resource is silent under the default enforcement.
	warned.Reset()
	authoredWarnOut = &warned
	err = validateRenderedInstanceConfig(t.Context(), ch, external(), good)
	authoredWarnOut = prev
	if err != nil || warned.Len() != 0 {
		t.Errorf("a complete document produced err=%v warning=%q", err, warned.String())
	}

	// mcp not deployed: nothing to list, so an empty list is fine.
	def := tokenAudienceValues(tlsIngress("iot.example.com"), "", "", nil)
	defDoc, err := renderInstanceConfigDocument(t.Context(), ch, def)
	if err != nil || defDoc == nil {
		t.Fatalf("rendering the default-profile document: %v", err)
	}
	ext := externalValues(digest("a"))
	ext["ingress"] = tlsIngress("iot.example.com")
	if err := validateRenderedInstanceConfig(t.Context(), ch, ext, defDoc); err != nil {
		t.Errorf("a deploy without mcp was asked for an MCP resource: %v", err)
	}
}

// The overlay admits the operator's three keys and refuses the two the chart derives, with the
// reason.
func TestValuesOverlayTokenAudienceKeys(t *testing.T) {
	t.Parallel()
	for _, k := range []string{"enforcement", "unscopedClientCredentials", "externalResources"} {
		if why := refuseOverlayKey(splitPath("instance.config.infrastructure.tokenAudience." + k)); why != "" {
			t.Errorf("the overlay refuses the operator's key %s: %s", k, why)
		}
	}
	for _, k := range []string{"areaBaseUrl", "mcpResources"} {
		why := refuseOverlayKey(splitPath("instance.config.infrastructure.tokenAudience." + k))
		if why == "" {
			t.Errorf("the overlay admits the derived key %s", k)
		} else if !strings.Contains(why, "derived") {
			t.Errorf("the refusal for %s does not say it is derived: %s", k, why)
		}
	}
}

// The chart's schema refuses unscopedClientCredentials=enforce: the rule is counted and
// warned in this release, never refused, so an accepted "enforce" would be a switch that does
// nothing. The counterweights render.
func TestChartRefusesUnscopedClientCredentialsEnforce(t *testing.T) {
	t.Parallel()
	ch := chartForTest(t)
	_, err := renderChartClientSide(t.Context(), ch,
		tokenAudienceValues(tlsIngress("iot.example.com"), "full", "", map[string]interface{}{"unscopedClientCredentials": "enforce"}))
	if err == nil {
		t.Fatal("unscopedClientCredentials=enforce rendered without complaint")
	}
	if !strings.Contains(err.Error(), "unscopedClientCredentials") {
		t.Errorf("the refusal does not name the key: %v", err)
	}
	for _, v := range []string{"observe", ""} {
		if _, err := renderChartClientSide(t.Context(), ch,
			tokenAudienceValues(tlsIngress("iot.example.com"), "full", "", map[string]interface{}{"unscopedClientCredentials": v})); err != nil {
			t.Errorf("unscopedClientCredentials=%q was refused: %v", v, err)
		}
	}
}

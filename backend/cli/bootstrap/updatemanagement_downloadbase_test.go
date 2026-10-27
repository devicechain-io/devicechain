// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// A device is sent to fetch an assigned image from updates.downloadBaseUrl, and the service
// refuses to issue a download grant while it is unset. dcctl refuses an overlay that sets it
// ("the download URL devices are sent to follows the instance's host", valuesoverlay.go), so the
// chart has to derive it, or no dcctl-built instance can ever deliver an update. These tests
// render the real embedded chart and read the value off the update-management config that the
// per-service ConfigMap hands the pod.

// renderedUpdateManagementDownloadBase renders a three-area release (the two core areas plus
// update-management) with the given ingress and area-config overrides and returns the
// updates.downloadBaseUrl the update-management pod would read, "" when none is delivered.
func renderedUpdateManagementDownloadBase(t *testing.T, ingress map[string]interface{}, areaConfig map[string]interface{}) string {
	t.Helper()
	vals := map[string]interface{}{
		"enabledFunctionalAreas": []interface{}{"user-management", "device-management", "update-management"},
		"ingress":                ingress,
	}
	if areaConfig != nil {
		vals["functionalAreas"] = map[string]interface{}{"update-management": map[string]interface{}{"config": areaConfig}}
	}
	manifest, err := renderChart(t, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	return updateManagementDownloadBaseOf(t, manifest, "dct-dctest-config")
}

// updateManagementDownloadBaseOf reads updates.downloadBaseUrl out of the update-management key
// of the named per-service ConfigMap in a rendered manifest.
func updateManagementDownloadBaseOf(t *testing.T, manifest, configMap string) string {
	t.Helper()
	for k, v := range updateManagementUpdatesBlockOf(t, manifest, configMap) {
		if strings.EqualFold(k, "downloadBaseUrl") {
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				t.Fatalf("%s is not a string: %v", k, err)
			}
			return s
		}
	}
	return ""
}

// updateManagementUpdatesBlockOf returns the raw `updates` block of the update-management config
// exactly as delivered, keys as spelled, so a test can see a second spelling of one key.
func updateManagementUpdatesBlockOf(t *testing.T, manifest, configMap string) map[string]json.RawMessage {
	t.Helper()
	for _, doc := range splitManifests(manifest) {
		var cm struct {
			Kind     string                `json:"kind"`
			Metadata struct{ Name string } `json:"metadata"`
			Data     map[string]string     `json:"data"`
		}
		if err := yaml.Unmarshal([]byte(doc), &cm); err != nil || cm.Kind != "ConfigMap" || cm.Metadata.Name != configMap {
			continue
		}
		raw, ok := cm.Data["update-management"]
		if !ok {
			t.Fatalf("the per-service ConfigMap has no update-management key: %v", cm.Data)
		}
		var cfg map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatalf("update-management config %q is not JSON: %v", raw, err)
		}
		var block map[string]json.RawMessage
		for k, v := range cfg {
			if strings.EqualFold(k, "updates") {
				if err := json.Unmarshal(v, &block); err != nil {
					t.Fatalf("updates block %s: %v", v, err)
				}
			}
		}
		return block
	}
	t.Fatalf("no per-service ConfigMap %s in the render", configMap)
	return nil
}

func TestChartDerivesTheUpdateDownloadBaseFromTheIngressHost(t *testing.T) {
	t.Parallel()
	tlsOn := map[string]interface{}{"enabled": true}
	tlsOff := map[string]interface{}{"enabled": false}
	cases := []struct {
		name       string
		ingress    map[string]interface{}
		areaConfig map[string]interface{}
		want       string
	}{
		{
			name:    "TLS on derives https from the host, lowercased (Ingress front end)",
			ingress: map[string]interface{}{"enabled": true, "host": "IoT.Example.com", "tls": tlsOn},
			want:    "https://iot.example.com/api/update-management",
		},
		{
			name:    "the Gateway front end serves the same host, so it derives the same base",
			ingress: map[string]interface{}{"enabled": true, "host": "iot.example.com", "controller": "gateway", "tls": tlsOn},
			want:    "https://iot.example.com/api/update-management",
		},
		{
			name:    "TLS off derives plain http rather than refusing (the service warns, the device refuses)",
			ingress: map[string]interface{}{"enabled": true, "host": "localhost", "tls": tlsOff},
			want:    "http://localhost/api/update-management",
		},
		{
			name:    "no ingress has no external origin, so nothing is derived",
			ingress: map[string]interface{}{"enabled": false},
			want:    "",
		},
		{
			name:    "an explicit value wins over the derivation",
			ingress: map[string]interface{}{"enabled": true, "host": "iot.example.com", "tls": tlsOn},
			areaConfig: map[string]interface{}{"updates": map[string]interface{}{
				"downloadBaseUrl": "https://updates.example.net/api/update-management"}},
			want: "https://updates.example.net/api/update-management",
		},
		{
			name:    "an explicit empty value stays empty: downloads are switched off on purpose",
			ingress: map[string]interface{}{"enabled": true, "host": "iot.example.com", "tls": tlsOn},
			areaConfig: map[string]interface{}{"updates": map[string]interface{}{
				"downloadBaseUrl": ""}},
			want: "",
		},
		{
			name:    "an operator-set updates block without a base still gets one",
			ingress: map[string]interface{}{"enabled": true, "host": "iot.example.com", "tls": tlsOn},
			areaConfig: map[string]interface{}{"updates": map[string]interface{}{
				"capabilityTtlSeconds": 120}},
			want: "https://iot.example.com/api/update-management",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := renderedUpdateManagementDownloadBase(t, c.ingress, c.areaConfig); got != c.want {
				t.Fatalf("updates.downloadBaseUrl = %q, want %q", got, c.want)
			}
		})
	}
}

// The same derivation through the values dcctl itself composes for the full profile (the one that
// ships update-management), so a break between dcctl's ingress values and the chart's helper is
// seen here and not on a live instance.
func TestDcctlFullProfileDeliversTheUpdateDownloadBase(t *testing.T) {
	t.Parallel()
	ch := chartForTest(t)
	p := renderProfile{name: "full", st: profileState(false, false, "full")}
	vals := profileValues(t, ch, p, "dctest")
	ingress, _ := vals["ingress"].(map[string]interface{})
	if ingress == nil || ingress["enabled"] != true || ingress["host"] != "localhost" {
		t.Fatalf("dcctl's ingress values are not what this test assumes: %v", ingress)
	}
	manifest, err := renderAny(t, vals)
	if err != nil {
		t.Fatalf("rendering the full profile: %v", err)
	}
	want := "http://localhost/api/update-management"
	if tls, _ := ingress["tls"].(map[string]interface{}); tls != nil && tls["enabled"] == true {
		want = "https://localhost/api/update-management"
	}
	if got := updateManagementDownloadBaseOf(t, manifest, "dct-dctest-config"); got != want {
		t.Fatalf("updates.downloadBaseUrl = %q, want %q", got, want)
	}
}

// The service decodes with encoding/json, which matches keys case-insensitively, and dcctl's own
// overlay refusal spells the key downloadBaseURL. The chart must treat every spelling as the
// operator's, or two spellings render and the derived one silently wins.
func TestChartLeavesAnyCaseSpellingOfTheDownloadBaseAlone(t *testing.T) {
	t.Parallel()
	ingress := map[string]interface{}{"enabled": true, "host": "iot.example.com", "tls": map[string]interface{}{"enabled": true}}
	for _, c := range []struct {
		name  string
		block map[string]interface{}
		key   string
		want  string
	}{
		{"downloadBaseURL", map[string]interface{}{"updates": map[string]interface{}{"DownloadBaseURL": "https://updates.example.net/x"}}, "DownloadBaseURL", "https://updates.example.net/x"},
		{"updates block spelled differently", map[string]interface{}{"Updates": map[string]interface{}{"downloadbaseurl": "https://updates.example.net/y"}}, "downloadbaseurl", "https://updates.example.net/y"},
		{"empty in another spelling", map[string]interface{}{"updates": map[string]interface{}{"DownloadBaseURL": ""}}, "DownloadBaseURL", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			vals := map[string]interface{}{
				"enabledFunctionalAreas": []interface{}{"user-management", "device-management", "update-management"},
				"ingress":                ingress,
				"functionalAreas":        map[string]interface{}{"update-management": map[string]interface{}{"config": c.block}},
			}
			manifest, err := renderChart(t, vals)
			if err != nil {
				t.Fatal(err)
			}
			block := updateManagementUpdatesBlockOf(t, manifest, "dct-dctest-config")
			if len(block) != 1 {
				t.Fatalf("updates block has %d keys, want exactly the operator's one: %v", len(block), block)
			}
			raw, ok := block[c.key]
			if !ok {
				t.Fatalf("the operator's key %q is gone: %v", c.key, block)
			}
			var got string
			if err := json.Unmarshal(raw, &got); err != nil || got != c.want {
				t.Fatalf("%s = %s, want %q", c.key, raw, c.want)
			}
		})
	}
}

// The derived base is only worth anything if the route it names exists: a device that GETs
// <base>/updates/download/<artifact> must reach update-management, with the prefix stripped, on
// both front ends and with TLS on and off.
func TestDerivedDownloadBaseIsRoutedToUpdateManagement(t *testing.T) {
	t.Parallel()
	var full renderProfile
	for _, p := range renderProfiles {
		if p.name == "--profile full" {
			full = p
		}
	}
	if full.name == "" {
		t.Fatal("no --profile full row in renderProfiles")
	}
	for _, ctl := range []string{"nginx", "gateway"} {
		for _, tlsOn := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/tls=%v", ctl, tlsOn), func(t *testing.T) {
				t.Parallel()
				// Each variant renders ONE front end, so each is asked through its own resolver.
				routing, manifest := renderRoutingVariant(t, full, "dctest", ctl, tlsOn, "")
				base := updateManagementDownloadBaseOf(t, manifest, "dct-dctest-config")
				u, err := url.Parse(base + "/updates/download/tok")
				if err != nil || u.Host == "" {
					t.Fatalf("derived base %q does not form a URL: %v", base, err)
				}
				if wantScheme := map[bool]string{true: "https", false: "http"}[tlsOn]; u.Scheme != wantScheme {
					t.Fatalf("scheme of %q = %s, want %s", base, u.Scheme, wantScheme)
				}
				var a answer
				if ctl == "nginx" {
					a, err = routing.resolveIngress(u.Hostname(), u.Path, false)
				} else {
					a, err = routing.resolveGateway(u.Hostname(), u.Path)
				}
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(string(a), "update-management:") || !strings.HasSuffix(string(a), " /updates/download/tok") {
					t.Errorf("%s answers %q for %s, want update-management:<port> /updates/download/tok", ctl, a, u.Path)
				}
			})
		}
	}
}

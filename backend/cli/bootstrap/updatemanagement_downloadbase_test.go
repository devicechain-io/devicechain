// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
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
		var cfg struct {
			Updates struct {
				DownloadBaseURL string `json:"downloadBaseUrl"`
			} `json:"updates"`
		}
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatalf("update-management config %q is not JSON: %v", raw, err)
		}
		return cfg.Updates.DownloadBaseURL
	}
	t.Fatalf("no per-service ConfigMap %s in the render", configMap)
	return ""
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

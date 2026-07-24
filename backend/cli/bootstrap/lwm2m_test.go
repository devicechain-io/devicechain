// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/releaseutil"
	"sigs.k8s.io/yaml"
)

// b64PSK returns a valid (≥16 byte) base64 PSK for tests.
func b64PSK(n int) string {
	if n < minLwm2mPSKBytes {
		n = minLwm2mPSKBytes
	}
	return base64.StdEncoding.EncodeToString(make([]byte, n))
}

func writeIdentitiesFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "identities.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("writing identities file: %v", err)
	}
	return p
}

// ParseLwm2mIdentities is the fail-fast gate: every rejection here is a one-line CLI
// error instead of a ten-minute helm timeout on a crash-looping lwm2m-ingest.
func TestParseLwm2mIdentities(t *testing.T) {
	t.Run("empty path means the feature is off", func(t *testing.T) {
		got, err := ParseLwm2mIdentities("")
		if err != nil || got != nil {
			t.Fatalf("empty path: got (%v, %v), want (nil, nil)", got, err)
		}
	})

	t.Run("a well-formed file parses", func(t *testing.T) {
		p := writeIdentitiesFile(t, `[
		  {"identity":"h1","psk":"`+b64PSK(16)+`","tenant":"acme","externalId":"plant-a/s1","deviceTypeToken":"sensor","autoRegister":true},
		  {"identity":"h2","psk":"`+b64PSK(32)+`","tenant":"acme","externalId":"plant-a/s2"}
		]`)
		got, err := ParseLwm2mIdentities(p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 || got[0].Identity != "h1" || got[1].ExternalID != "plant-a/s2" {
			t.Fatalf("parsed wrong: %+v", got)
		}
	})

	// Each rejection, one subtest, so a regression names the exact guard that broke.
	reject := map[string]string{
		"short psk":               `[{"identity":"h","psk":"` + base64.StdEncoding.EncodeToString(make([]byte, 8)) + `","tenant":"acme","externalId":"e"}]`,
		"psk not base64":          `[{"identity":"h","psk":"not!base64","tenant":"acme","externalId":"e"}]`,
		"missing tenant":          `[{"identity":"h","psk":"` + b64PSK(16) + `","externalId":"e"}]`,
		"missing externalId":      `[{"identity":"h","psk":"` + b64PSK(16) + `","tenant":"acme"}]`,
		"missing identity":        `[{"psk":"` + b64PSK(16) + `","tenant":"acme","externalId":"e"}]`,
		"autoRegister no type":    `[{"identity":"h","psk":"` + b64PSK(16) + `","tenant":"acme","externalId":"e","autoRegister":true}]`,
		"duplicate identity":      `[{"identity":"h","psk":"` + b64PSK(16) + `","tenant":"acme","externalId":"e1"},{"identity":"h","psk":"` + b64PSK(16) + `","tenant":"acme","externalId":"e2"}]`,
		"unknown field":           `[{"identity":"h","psk":"` + b64PSK(16) + `","tenant":"acme","externalId":"e","bogus":1}]`,
		"empty array":             `[]`,
		"not an array of objects": `{"identity":"h"}`,
		// Whitespace-only fields: non-empty to a naive == "" check, but the service
		// TrimSpace-validates, so these would sail past and crash-loop the pod.
		"whitespace identity":              `[{"identity":" ","psk":"` + b64PSK(16) + `","tenant":"acme","externalId":"e"}]`,
		"whitespace tenant":                `[{"identity":"h","psk":"` + b64PSK(16) + `","tenant":"  ","externalId":"e"}]`,
		"whitespace externalId":            `[{"identity":"h","psk":"` + b64PSK(16) + `","tenant":"acme","externalId":" "}]`,
		"whitespace deviceType autoReg":    `[{"identity":"h","psk":"` + b64PSK(16) + `","tenant":"acme","externalId":"e","autoRegister":true,"deviceTypeToken":" "}]`,
		"trailing content after the array": `[{"identity":"h","psk":"` + b64PSK(16) + `","tenant":"acme","externalId":"e"}] {"junk":1}`,
	}
	for name, body := range reject {
		t.Run("rejects "+name, func(t *testing.T) {
			if _, err := ParseLwm2mIdentities(writeIdentitiesFile(t, body)); err == nil {
				t.Fatalf("expected rejection for %q, got nil", name)
			}
		})
	}
}

// lwm2mProvisioning must produce the exact chart-values shapes the deployed path
// reads: a PSK Secret entry, a security.identities[] binding, and an extraEnv
// secretKeyRef that ties the two together by the SAME env var name.
func TestLwm2mProvisioningComposition(t *testing.T) {
	ids := []Lwm2mIdentity{
		{Identity: "h1", PSK: b64PSK(16), Tenant: "acme", ExternalID: "plant-a/s1", DeviceTypeToken: "sensor", AutoRegister: true},
	}
	secret, area := lwm2mProvisioning("dctest", ids)

	if secret["name"] != "dci-dctest-lwm2m-psk" {
		t.Fatalf("secret name = %v", secret["name"])
	}
	sd := secret["stringData"].(map[string]interface{})
	if sd["DC_LWM2M_PSK_1"] != ids[0].PSK {
		t.Fatalf("secret stringData must carry the verbatim base64 PSK under the env key: %v", sd)
	}

	idents := area["config"].(map[string]interface{})["security"].(map[string]interface{})["identities"].([]interface{})
	i0 := idents[0].(map[string]interface{})
	if i0["identity"] != "h1" || i0["tenant"] != "acme" || i0["externalId"] != "plant-a/s1" || i0["pskEnv"] != "DC_LWM2M_PSK_1" {
		t.Fatalf("identity binding wrong: %v", i0)
	}
	if i0["deviceTypeToken"] != "sensor" || i0["autoRegister"] != true {
		t.Fatalf("deviceTypeToken/autoRegister not carried: %v", i0)
	}

	env := area["extraEnv"].([]interface{})[0].(map[string]interface{})
	ref := env["valueFrom"].(map[string]interface{})["secretKeyRef"].(map[string]interface{})
	// The projection must point back at the SAME secret + key the identity's pskEnv names.
	if env["name"] != "DC_LWM2M_PSK_1" || ref["name"] != "dci-dctest-lwm2m-psk" || ref["key"] != "DC_LWM2M_PSK_1" {
		t.Fatalf("extraEnv secretKeyRef does not tie to the secret: env=%v ref=%v", env, ref)
	}
}

// The clobber guard: Grafana SSO (user-management) and lwm2m identities (lwm2m-ingest)
// each configure a functional area. Before mergeFunctionalArea one assigned the whole
// functionalAreas map and silently dropped the other. Both must survive together.
func TestHelmValuesLwm2mAndGrafanaCoexist(t *testing.T) {
	st := &State{
		Instance:      "dctest",
		Profile:       "default",
		ImageRegistry: DefaultImageRegistry,
		ImageVersion:  "v0.0.0-test",
		GrafanaSSO:    true,
		Lwm2mIdentities: []Lwm2mIdentity{
			{Identity: "h1", PSK: b64PSK(16), Tenant: "acme", ExternalID: "plant-a/s1", DeviceTypeToken: "sensor", AutoRegister: true},
		},
		Values: map[string]string{
			"ingressHost":              "localhost",
			"scheme":                   "http",
			"grafanaOAuthSecretBcrypt": "$2a$10$abcdefghijklmnopqrstuv",
		},
	}
	fa, ok := helmValues(st)["functionalAreas"].(map[string]interface{})
	if !ok {
		t.Fatal("functionalAreas missing")
	}
	if _, ok := fa["user-management"]; !ok {
		t.Error("grafana-sso user-management config was clobbered by the lwm2m block")
	}
	if _, ok := fa["lwm2m-ingest"]; !ok {
		t.Error("lwm2m-ingest config was clobbered by the grafana-sso block")
	}
}

// renderDocs renders the embedded chart through helmValues and returns every manifest
// as a decoded map, so a test can pick out the Secret/ConfigMap/Deployment the
// provisioning produced.
func renderDocs(t *testing.T, vals map[string]interface{}) []map[string]interface{} {
	t.Helper()
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading embedded chart: %v", err)
	}
	inst := action.NewInstall(&action.Configuration{})
	inst.ReleaseName = helmReleaseName
	inst.Namespace = "default"
	inst.DryRun = true
	inst.ClientOnly = true
	inst.APIVersions = []string{"monitoring.coreos.com/v1"}
	rel, err := inst.RunWithContext(t.Context(), ch, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	var out []map[string]interface{}
	for _, doc := range releaseutil.SplitManifests(rel.Manifest) {
		m := map[string]interface{}{}
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil || len(m) == 0 {
			continue
		}
		out = append(out, m)
	}
	return out
}

// The end-to-end proof: a State with --lwm2m-identities, rendered through the SAME
// helmValues → embedded chart a real bootstrap uses, produces the full projection
// chain — a PSK Secret, the identity binding in lwm2m-ingest's mounted config, and a
// Deployment env var that ties the two together.
func TestLwm2mIdentitiesRenderEndToEnd(t *testing.T) {
	enabled, err := ResolveEnabledAreas("default", []string{"lwm2m-ingest"})
	if err != nil {
		t.Fatalf("resolving areas: %v", err)
	}
	psk := b64PSK(24)
	st := &State{
		Instance:      "dctest",
		Profile:       "default",
		ImageRegistry: DefaultImageRegistry,
		ImageVersion:  "v0.0.0-test",
		EnabledAreas:  enabled,
		Lwm2mIdentities: []Lwm2mIdentity{
			{Identity: "opaque-1", PSK: psk, Tenant: "acme", ExternalID: "plant-a/s1", DeviceTypeToken: "sensor", AutoRegister: true},
		},
		Values: map[string]string{
			"ingressHost":    "localhost",
			"secretsRootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
		},
	}
	docs := renderDocs(t, helmValues(st))

	// 1. The PSK Secret renders with the verbatim base64 PSK under the env key.
	var pskSecret map[string]interface{}
	var lwm2mConfigJSON string
	var lwm2mEnv []interface{}
	for _, d := range docs {
		kind, _ := d["kind"].(string)
		meta, _ := d["metadata"].(map[string]interface{})
		name, _ := meta["name"].(string)
		switch {
		case kind == "Secret" && name == "dci-dctest-lwm2m-psk":
			pskSecret = d
		case kind == "ConfigMap" && name == "dct-dctest-config":
			if data, ok := d["data"].(map[string]interface{}); ok {
				lwm2mConfigJSON, _ = data["lwm2m-ingest"].(string)
			}
		case kind == "Deployment" && name == "lwm2m-ingest":
			// containers[0].env
			spec := d["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
			c0 := spec["containers"].([]interface{})[0].(map[string]interface{})
			lwm2mEnv, _ = c0["env"].([]interface{})
		}
	}

	if pskSecret == nil {
		t.Fatal("PSK Secret dci-dctest-lwm2m-psk was not rendered")
	}
	if sd, _ := pskSecret["stringData"].(map[string]interface{}); sd["DC_LWM2M_PSK_1"] != psk {
		t.Fatalf("PSK Secret does not carry the verbatim base64 PSK: %v", pskSecret["stringData"])
	}

	// 2. The identity binding reached lwm2m-ingest's mounted per-area config.
	if lwm2mConfigJSON == "" {
		t.Fatal("lwm2m-ingest area config key missing from the microservice ConfigMap")
	}
	var areaCfg struct {
		Security struct {
			Identities []struct {
				Identity   string `json:"identity"`
				PskEnv     string `json:"pskEnv"`
				Tenant     string `json:"tenant"`
				ExternalID string `json:"externalId"`
			} `json:"identities"`
		} `json:"security"`
	}
	if err := json.Unmarshal([]byte(lwm2mConfigJSON), &areaCfg); err != nil {
		t.Fatalf("decoding lwm2m-ingest config: %v (%q)", err, lwm2mConfigJSON)
	}
	if len(areaCfg.Security.Identities) != 1 {
		t.Fatalf("expected 1 identity in the mounted config, got %d", len(areaCfg.Security.Identities))
	}
	id0 := areaCfg.Security.Identities[0]
	if id0.Identity != "opaque-1" || id0.Tenant != "acme" || id0.ExternalID != "plant-a/s1" || id0.PskEnv != "DC_LWM2M_PSK_1" {
		t.Fatalf("identity binding wrong in mounted config: %+v", id0)
	}

	// 3. The Deployment env ties the env var to the Secret (the projection the DTLS
	//    server's ResolveCredentials reads at startup).
	found := false
	for _, e := range lwm2mEnv {
		ev := e.(map[string]interface{})
		if ev["name"] != "DC_LWM2M_PSK_1" {
			continue
		}
		ref := ev["valueFrom"].(map[string]interface{})["secretKeyRef"].(map[string]interface{})
		if ref["name"] != "dci-dctest-lwm2m-psk" || ref["key"] != "DC_LWM2M_PSK_1" {
			t.Fatalf("env projection points at the wrong secret/key: %v", ref)
		}
		found = true
	}
	if !found {
		t.Fatal("lwm2m-ingest Deployment has no DC_LWM2M_PSK_1 env projecting the PSK Secret")
	}
}

// A value-only PSK rotation (same identity, new key) changes only the extra Secret —
// not the instance config or the per-area config — so without the checksum/extra-secrets
// pod annotation the Deployment podspec would be byte-identical and the running pod
// would keep its old key. Rendering the same identity with two different PSKs must
// produce two different lwm2m-ingest pod hashes, so `helm upgrade` actually rolls.
func TestLwm2mPSKRotationRollsPod(t *testing.T) {
	extraSecretsChecksum := func(psk string) string {
		enabled, err := ResolveEnabledAreas("default", []string{"lwm2m-ingest"})
		if err != nil {
			t.Fatalf("resolving areas: %v", err)
		}
		st := &State{
			Instance:      "dctest",
			Profile:       "default",
			ImageRegistry: DefaultImageRegistry,
			ImageVersion:  "v0.0.0-test",
			EnabledAreas:  enabled,
			Lwm2mIdentities: []Lwm2mIdentity{
				{Identity: "opaque-1", PSK: psk, Tenant: "acme", ExternalID: "plant-a/s1", DeviceTypeToken: "sensor", AutoRegister: true},
			},
			Values: map[string]string{
				"ingressHost":    "localhost",
				"secretsRootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
			},
		}
		for _, d := range renderDocs(t, helmValues(st)) {
			meta, _ := d["metadata"].(map[string]interface{})
			if d["kind"] != "Deployment" || meta["name"] != "lwm2m-ingest" {
				continue
			}
			ann := d["spec"].(map[string]interface{})["template"].(map[string]interface{})["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})
			cs, _ := ann["checksum/extra-secrets"].(string)
			if cs == "" {
				t.Fatal("lwm2m-ingest pod has no checksum/extra-secrets annotation — a PSK rotation would not roll it")
			}
			return cs
		}
		t.Fatal("lwm2m-ingest Deployment not rendered")
		return ""
	}

	// Two distinct non-zero keys.
	a := extraSecretsChecksum(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x11}, 16)))
	b := extraSecretsChecksum(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xAB}, 16)))
	if a == b {
		t.Fatal("checksum/extra-secrets did not change when the PSK value changed: an in-place rotation would silently not roll the pod")
	}
}

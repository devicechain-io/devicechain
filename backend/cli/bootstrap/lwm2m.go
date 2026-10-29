// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// minLwm2mPSKBytes mirrors lwm2m-ingest's ResolveCredentials floor (config
// MinPskBytes): a PSK shorter than this crashes that service at startup. dcctl
// checks it here only to turn that startup crash into a one-line CLI error before a
// cluster exists; the service remains the fail-closed authority.
const minLwm2mPSKBytes = 16

// lwm2mIdentitiesSecretName is the one Opaque Secret dcctl writes to hold every provisioned
// device PSK, one key per identity. It is written outside the Helm release.
func lwm2mIdentitiesSecretName(instance string) string {
	return fmt.Sprintf("dci-%s-lwm2m-identities", instance)
}

// lwm2mLegacyPSKSecretName is the Secret earlier dcctl versions had the CHART render from
// extraSecrets, which put every PSK into the release values. It is read only to migrate an
// instance off that layout (see lwm2mLegacyPSKs); nothing writes it any more.
func lwm2mLegacyPSKSecretName(instance string) string {
	return fmt.Sprintf("dci-%s-lwm2m-psk", instance)
}

// lwm2mPSKEnv is the variable identity n's key reaches the pod in, and the key it has in the Secret.
func lwm2mPSKEnv(n int) string { return fmt.Sprintf("DC_LWM2M_PSK_%d", n) }

// Lwm2mIdentity is one provisioned DTLS-PSK credential and its tenancy binding, as
// read from the --lwm2m-identities file. It maps onto the lwm2m-ingest config's
// security.identities[] entry plus the Secret-projected PSK the DTLS server accepts
// at handshake. Tenancy is bound to the CREDENTIAL, never the device's registration
// payload (ADR-075 D1).
type Lwm2mIdentity struct {
	// Identity is the DTLS PSK identity the device presents (an opaque handle, sent
	// in the clear).
	Identity string `json:"identity"`
	// PSK is the base64-encoded pre-shared key. It is written verbatim into the
	// Secret dcctl writes; the service base64-decodes it at load, so the
	// value that reaches the env var must stay the base64 STRING.
	PSK string `json:"psk"`
	// Tenant and ExternalID are the required tenancy binding.
	Tenant     string `json:"tenant"`
	ExternalID string `json:"externalId"`
	// DeviceTypeToken stamps the device row when AutoRegister creates it (required
	// when AutoRegister is set).
	DeviceTypeToken string `json:"deviceTypeToken"`
	// AutoRegister creates the device row on first registration (ALLOW_NEW).
	AutoRegister bool `json:"autoRegister"`
}

// ParseLwm2mIdentities reads and validates the --lwm2m-identities file. It validates
// up front — before any cluster exists — so a short PSK, a missing tenancy field, or
// an autoRegister without a device type fails as a one-line CLI error rather than a
// ten-minute helm-timeout (a bad PSK crashes lwm2m-ingest at Init) or a half-configured
// deployment. An empty path returns (nil, nil): the feature is off.
func ParseLwm2mIdentities(path string) ([]Lwm2mIdentity, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading identities file: %w", err)
	}
	var ids []Lwm2mIdentity
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ids); err != nil {
		return nil, fmt.Errorf("parsing identities file (expected a JSON array of {identity, psk, tenant, externalId, deviceTypeToken, autoRegister}): %w", err)
	}
	// Reject trailing content after the array: a concatenated or mangled file would
	// otherwise silently provision only the first array and drop the rest (the service's
	// own config loader guards the same way with dec.More()).
	if dec.More() {
		return nil, fmt.Errorf("identities file %q has trailing content after the JSON array", path)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("identities file %q holds no identities", path)
	}

	// Blank-after-trim, not just empty: the service Validates every field with
	// strings.TrimSpace (config.PskIdentity.Validate), so a " " here would sail past a
	// naive == "" check and then crash-loop lwm2m-ingest into exactly the ten-minute
	// helm timeout this up-front validation exists to prevent. Mirror the service's rule.
	blank := func(s string) bool { return strings.TrimSpace(s) == "" }

	seenIdentity := make(map[string]bool, len(ids))
	for i, id := range ids {
		where := fmt.Sprintf("identity[%d]", i)
		if blank(id.Identity) {
			return nil, fmt.Errorf("%s: identity is required", where)
		}
		if seenIdentity[id.Identity] {
			// Duplicate PSK identities would collide in the DTLS credential map (last
			// one wins) — reject rather than silently drop one device's binding.
			return nil, fmt.Errorf("%s: duplicate identity %q", where, id.Identity)
		}
		seenIdentity[id.Identity] = true
		if blank(id.Tenant) {
			return nil, fmt.Errorf("%s (%q): tenant is required", where, id.Identity)
		}
		if blank(id.ExternalID) {
			return nil, fmt.Errorf("%s (%q): externalId is required", where, id.Identity)
		}
		if id.AutoRegister && blank(id.DeviceTypeToken) {
			return nil, fmt.Errorf("%s (%q): deviceTypeToken is required when autoRegister is true", where, id.Identity)
		}
		key, err := base64.StdEncoding.DecodeString(id.PSK)
		if err != nil {
			return nil, fmt.Errorf("%s (%q): psk is not valid base64: %w", where, id.Identity, err)
		}
		if len(key) < minLwm2mPSKBytes {
			return nil, fmt.Errorf("%s (%q): psk decodes to %d bytes, need at least %d", where, id.Identity, len(key), minLwm2mPSKBytes)
		}
	}
	return ids, nil
}

// lwm2mSecretData is what dcctl writes into the identities Secret: one key per identity.
func lwm2mSecretData(ids []Lwm2mIdentity) map[string]string {
	data := make(map[string]string, len(ids))
	for i, id := range ids {
		data[lwm2mPSKEnv(i+1)] = id.PSK
	}
	return data
}

// lwm2mProvisioning turns the parsed identities into the one value fragment the chart consumes:
// the lwm2m-ingest functional-area config (security.identities[]) and the extraEnv secretKeyRefs
// that project each PSK, by Secret NAME, into the env var its identity names. It carries no key:
// the Secret is written by syncLwm2mIdentitiesSecret, outside the release. Env vars are indexed
// (DC_LWM2M_PSK_<n>) so they are collision-free regardless of identity contents.
func lwm2mProvisioning(instance string, ids []Lwm2mIdentity) (areaConfig map[string]interface{}) {
	secretName := lwm2mIdentitiesSecretName(instance)
	identities := make([]interface{}, 0, len(ids))
	extraEnv := make([]interface{}, 0, len(ids))

	for i, id := range ids {
		env := lwm2mPSKEnv(i + 1)

		ident := map[string]interface{}{
			"identity":   id.Identity,
			"pskEnv":     env,
			"tenant":     id.Tenant,
			"externalId": id.ExternalID,
		}
		if id.DeviceTypeToken != "" {
			ident["deviceTypeToken"] = id.DeviceTypeToken
		}
		if id.AutoRegister {
			ident["autoRegister"] = true
		}
		identities = append(identities, ident)

		extraEnv = append(extraEnv, map[string]interface{}{
			"name": env,
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name": secretName,
					"key":  env,
				},
			},
		})
	}

	return map[string]interface{}{
		"config": map[string]interface{}{
			"security": map[string]interface{}{
				"identities": identities,
			},
		},
		"extraEnv": extraEnv,
	}
}

// mergeFunctionalArea sets one functional area's config into vals["functionalAreas"],
// getting-or-creating that map rather than ASSIGNING it. Assigning the whole map is
// the bug this exists to prevent: two features that each configure an area would
// each overwrite the map, and whichever ran last would silently drop the other's
// block. Distinct areas
// merge cleanly; if the same area is written twice, its top-level keys are merged in.
func mergeFunctionalArea(vals map[string]interface{}, area string, cfg map[string]interface{}) {
	fa, _ := vals["functionalAreas"].(map[string]interface{})
	if fa == nil {
		fa = map[string]interface{}{}
		vals["functionalAreas"] = fa
	}
	existing, _ := fa[area].(map[string]interface{})
	if existing == nil {
		fa[area] = cfg
		return
	}
	for k, v := range cfg {
		existing[k] = v
	}
}

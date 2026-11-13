// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chartutil"
)

// lwm2m-ingest refuses a deviceTypeToken outside the token grammar at startup.
// ParseLwm2mIdentities refuses one in a new identities file, but dcctl upgrade carries the
// previous release's identities forward without re-reading a file, and the chart passes them
// through as written. The chart's schema refuses the malformed token at render, so an
// instance that already holds one fails the upgrade before anything changes instead of
// rolling a pod that will not start. An absent token is still accepted.
func TestChartSchemaRefusesAMalformedLwm2mDeviceType(t *testing.T) {
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatal(err)
	}
	validate := func(identity map[string]interface{}) error {
		vals := overlaySchemaProbe()
		vals["functionalAreas"] = map[string]interface{}{
			"lwm2m-ingest": map[string]interface{}{"config": map[string]interface{}{
				"security": map[string]interface{}{"identities": []interface{}{identity}},
			}},
		}
		coalesced, err := chartutil.CoalesceValues(ch, vals)
		if err != nil {
			t.Fatal(err)
		}
		return chartutil.ValidateAgainstSchema(ch, coalesced)
	}
	identity := func(extra map[string]interface{}) map[string]interface{} {
		id := map[string]interface{}{"identity": "h1", "pskEnv": "DC_LWM2M_PSK_1", "tenant": "acme", "externalId": "e1"}
		for k, v := range extra {
			id[k] = v
		}
		return id
	}

	for _, bad := range []string{"bad/type", "bad type", " "} {
		err := validate(identity(map[string]interface{}{"deviceTypeToken": bad, "autoRegister": true}))
		if err == nil {
			t.Fatalf("deviceTypeToken %q passed the chart schema; it must be refused", bad)
		}
		if !strings.Contains(err.Error(), "deviceTypeToken") {
			t.Fatalf("the refusal must name deviceTypeToken, got: %v", err)
		}
	}
	for _, good := range []map[string]interface{}{
		{"deviceTypeToken": "sensor-v2", "autoRegister": true},
		{"deviceTypeToken": ""},
		{},
	} {
		if err := validate(identity(good)); err != nil {
			t.Fatalf("identity %v must pass the chart schema: %v", good, err)
		}
	}
}

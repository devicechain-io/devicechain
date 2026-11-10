// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"reflect"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
)

func TestTracingOverlaySurvivesDeclarationAndRenders(t *testing.T) {
	overlay := map[string]interface{}{"instance": map[string]interface{}{"config": map[string]interface{}{"infrastructure": map[string]interface{}{"tracing": map[string]interface{}{
		"enabled": true, "protocol": "http", "endpoint": "https://collector.example:4318", "insecure": false, "samplingRatio": 0.0,
	}}}}}
	if err := ValidateValuesOverlay(overlay); err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeValuesOverlay(overlay)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeValuesOverlay(map[string]string{AnnotationValuesOverlay: encoded})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(overlay, decoded) {
		t.Fatalf("declaration lost tracing: %v", decoded)
	}
	st := persistenceState(true, false, "default")
	st.ValuesOverlay = decoded
	vals, doc, err := releaseValues(t.Context(), chartForTest(t), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRenderedInstanceConfig(t.Context(), chartForTest(t), vals, doc); err != nil {
		t.Fatalf("rendered tracing fails strict loader: %v", err)
	}
	cfg := config.NewDefaultInstanceConfiguration()
	if err := core.LoadConfiguration(doc, cfg); err != nil {
		t.Fatal(err)
	}
	c := cfg.Infrastructure.Tracing
	if !c.Enabled || c.Protocol != "http" || c.Endpoint != "https://collector.example:4318" || c.Insecure || c.SamplingRatio == nil || *c.SamplingRatio != 0 {
		t.Fatalf("tracing changed during rendering: %+v", c)
	}
}

func TestTracingOverlayRejectsUnsupportedSettings(t *testing.T) {
	for _, block := range []map[string]interface{}{
		{"headers": map[string]interface{}{"Authorization": "secret"}},
		{"protocol": "udp"},
		{"samplingRatio": 1.1},
		{"samplingRatio": -0.1},
	} {
		overlay := map[string]interface{}{"instance": map[string]interface{}{"config": map[string]interface{}{"infrastructure": map[string]interface{}{"tracing": block}}}}
		if err := ValidateValuesOverlay(overlay); err == nil {
			t.Fatalf("accepted invalid tracing: %v", block)
		}
	}
}

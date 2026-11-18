// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
)

func TestTracingStrictConfiguration(t *testing.T) {
	for _, tc := range []struct{ name, block, want string }{
		{"unknown", `{"headers":{}}`, "unknown field"},
		{"endpoint required", `{"enabled":true}`, "endpoint"},
		{"protocol", `{"protocol":"udp"}`, "protocol"},
		{"low ratio", `{"samplingRatio":-0.1}`, "samplingRatio"},
		{"high ratio", `{"samplingRatio":1.1}`, "samplingRatio"},
		{"grpc URL", `{"enabled":true,"endpoint":"http://collector:4317"}`, "host:port"},
		{"bad port", `{"endpoint":"collector:65536"}`, "host:port"},
		{"HTTP credentials", `{"protocol":"http","endpoint":"https://user:secret@collector:4318"}`, "credentials"},
		{"plaintext needs opt in", `{"protocol":"http","endpoint":"http://collector:4318"}`, "insecure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.NewDefaultInstanceConfiguration()
			err := LoadConfiguration([]byte(`{"infrastructure":{"tracing":`+tc.block+`}}`), cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
	for _, block := range []string{`{}`, `{"enabled":true,"endpoint":"collector:4317"}`, `{"enabled":true,"endpoint":"collector:4317","samplingRatio":0}`, `{"enabled":true,"protocol":"http","endpoint":"http://collector:4318","insecure":true}`} {
		cfg := config.NewDefaultInstanceConfiguration()
		if err := LoadConfiguration([]byte(`{"infrastructure":{"tracing":`+block+`}}`), cfg); err != nil {
			t.Fatal(err)
		}
		c := cfg.Infrastructure.Tracing
		if c.Enabled != strings.Contains(block, `"enabled":true`) {
			t.Fatalf("enabled did not load from %s: %+v", block, c)
		}
		if block == `{}` && (c.Enabled || c.Protocol != "grpc" || c.Insecure || c.Endpoint != "" || c.SamplingRatio != nil) {
			t.Fatalf("wrong defaults: %+v", c)
		}
		if c.Enabled && c.SamplingRatio == nil {
			t.Fatal("enabled default ratio missing")
		}
		if strings.Contains(block, `"samplingRatio":0`) && c.Ratio() != 0 {
			t.Fatal("explicit zero was overwritten")
		}
		if c.Enabled && !strings.Contains(block, `"samplingRatio"`) && c.Ratio() != 0.1 {
			t.Fatalf("default ratio %g", c.Ratio())
		}
	}
}

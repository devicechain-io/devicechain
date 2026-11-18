// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

func TestNegativeIngestCeilingIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name     string
		document string
		field    string
	}{
		{"negative rate", `{"ingestRateLimit":{"readingsPerSecond":-1}}`, "ingestRateLimit.readingsPerSecond"},
		{"negative burst", `{"ingestRateLimit":{"burst":-1}}`, "ingestRateLimit.burst"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &EventSourcesConfiguration{}
			err := core.LoadConfiguration([]byte(tc.document), cfg)
			if err == nil {
				t.Fatal("negative ingest ceiling loaded without error")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("refusal must name %s, got %v", tc.field, err)
			}
		})
	}
}

func TestUnsetIngestCeilingDefaults(t *testing.T) {
	for _, tc := range []struct {
		name     string
		document string
	}{
		{"zero", `{"ingestRateLimit":{"readingsPerSecond":0,"burst":0}}`},
		{"omitted", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &EventSourcesConfiguration{}
			if err := core.LoadConfiguration([]byte(tc.document), cfg); err != nil {
				t.Fatalf("load configuration: %v", err)
			}
			if cfg.IngestRateLimit.ReadingsPerSecond != DefaultIngestReadingsPerSecond || cfg.IngestRateLimit.Burst != DefaultIngestBurst {
				t.Fatalf("ingest ceiling = %g/%d, want %g/%d", cfg.IngestRateLimit.ReadingsPerSecond, cfg.IngestRateLimit.Burst, float64(DefaultIngestReadingsPerSecond), DefaultIngestBurst)
			}
		})
	}
}

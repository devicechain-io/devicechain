// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// The per-tenant ingest ceiling meters READINGS (decoded samples), so its key says so. The
// old spelling, ingestRateLimit.messagesPerSecond, is refused at load rather than retired:
// an operator who still sets it believes a ceiling is in force, and the fail-closed decode
// is what tells them it is not.
func TestIngestCeilingIsReadFromReadingsPerSecond(t *testing.T) {
	// The decode alone, so the assertion is about the key and not about whatever else
	// a full configuration must carry to validate.
	var cfg Lwm2mConfiguration
	dec := json.NewDecoder(strings.NewReader(`{"ingestRateLimit":{"readingsPerSecond":50,"burst":300}}`))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("ingestRateLimit.readingsPerSecond must be a known key: %v", err)
	}
	if cfg.IngestRateLimit.ReadingsPerSecond != 50 || cfg.IngestRateLimit.Burst != 300 {
		t.Fatalf("ingest ceiling = %g/%d, want 50/300", cfg.IngestRateLimit.ReadingsPerSecond, cfg.IngestRateLimit.Burst)
	}
}

func TestIngestMessagesPerSecondIsRefused(t *testing.T) {
	err := core.LoadConfiguration([]byte(`{"ingestRateLimit":{"messagesPerSecond":50}}`), &Lwm2mConfiguration{})
	if err == nil {
		t.Fatal("ingestRateLimit.messagesPerSecond loaded cleanly; the renamed key must be refused, not ignored")
	}
	if !strings.Contains(err.Error(), "messagesPerSecond") {
		t.Fatalf("the refusal must name the key, got %v", err)
	}
}

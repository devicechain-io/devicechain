// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// The per-tenant ingest ceiling meters READINGS (decoded samples), so its key says so. The
// old spelling, ingestRateLimit.messagesPerSecond, is refused at load rather than retired:
// an operator who still sets it believes a ceiling is in force, and the fail-closed decode
// is what tells them it is not.
func TestIngestCeilingIsReadFromReadingsPerSecond(t *testing.T) {
	if err := core.LoadConfiguration([]byte(`{"ingestRateLimit":{"readingsPerSecond":50,"burst":300}}`), &Lwm2mConfiguration{}); err != nil &&
		strings.Contains(err.Error(), "readingsPerSecond") {
		t.Fatalf("ingestRateLimit.readingsPerSecond must be a known key: %v", err)
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

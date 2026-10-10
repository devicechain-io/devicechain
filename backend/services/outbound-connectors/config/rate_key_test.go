// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// The per-tenant outbound ceiling meters CALLS — one outbound connector action dispatched —
// so its key says so. The old spelling, outboundMessagesPerSecond, is refused at load rather
// than retired: an operator who still sets it believes a ceiling is in force, and the
// fail-closed decode is what tells them it is not.
func TestOutboundCeilingIsReadFromOutboundCallsPerSecond(t *testing.T) {
	cfg := &OutboundConnectorsConfiguration{}
	if err := core.LoadConfiguration([]byte(`{"outboundCallsPerSecond":7,"outboundBurst":9}`), cfg); err != nil {
		t.Fatalf("outboundCallsPerSecond must load: %v", err)
	}
}

func TestOutboundMessagesPerSecondIsRefused(t *testing.T) {
	err := core.LoadConfiguration([]byte(`{"outboundMessagesPerSecond":7}`), &OutboundConnectorsConfiguration{})
	if err == nil {
		t.Fatal("outboundMessagesPerSecond loaded cleanly; the renamed key must be refused, not ignored")
	}
	if !strings.Contains(err.Error(), "outboundMessagesPerSecond") {
		t.Fatalf("the refusal must name the key, got %v", err)
	}
}

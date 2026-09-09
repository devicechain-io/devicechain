// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

// The GraphQL subscription frame ceiling is fail-safe in the same never-unlimited
// sense as the stream bounds, and for a sharper reason: gorilla/websocket gates its
// own read-limit check on a POSITIVE value, so a 0 reaching it is not a small limit
// but no limit at all.
func TestApplyDefaultsGraphQLSubscriptionMessageBytes(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  int64
	}{
		{"unset", 0},
		{"negative", -1},
	} {
		cfg := &InstanceConfiguration{}
		cfg.Infrastructure.GraphQL.MaxSubscriptionMessageBytes = tc.set
		cfg.ApplyDefaults()
		if got := cfg.Infrastructure.GraphQL.MaxSubscriptionMessageBytes; got != DefaultGraphQLMaxSubscriptionMessageBytes {
			t.Errorf("%s: maxSubscriptionMessageBytes = %d, want the platform default %d — a non-positive "+
				"value reaches gorilla as UNLIMITED", tc.name, got, DefaultGraphQLMaxSubscriptionMessageBytes)
		}
	}

	cfg := &InstanceConfiguration{}
	cfg.Infrastructure.GraphQL.MaxSubscriptionMessageBytes = 999
	cfg.ApplyDefaults()
	if got := cfg.Infrastructure.GraphQL.MaxSubscriptionMessageBytes; got != 999 {
		t.Errorf("an explicit ceiling was overwritten: %d", got)
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-user-management/iam"
)

// TestAiExternalEnabledFailClosed pins the consent invariant the ai-inference
// service depends on (ADR-056 §6): the TenantGovernance resolver reports external-
// AI consent as a non-null boolean that is false unless the column is explicitly
// true. A nil column (never set) and an explicit false both read as "not opted in"
// — there is no default to inherit, so absent consent is never "allowed".
func TestAiExternalEnabledFailClosed(t *testing.T) {
	tru, fls := true, false
	cases := []struct {
		name string
		col  *bool
		want bool
	}{
		{"nil column reads not-opted-in", nil, false},
		{"explicit false reads not-opted-in", &fls, false},
		{"explicit true reads opted-in", &tru, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &TenantGovernanceResolver{t: &iam.Tenant{AiExternalEnabled: c.col}}
			if got := r.AiExternalEnabled(); got != c.want {
				t.Fatalf("AiExternalEnabled() = %v, want %v", got, c.want)
			}
		})
	}
}

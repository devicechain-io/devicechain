// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

func TestParseTenantFromSubject(t *testing.T) {
	cases := []struct {
		subject string
		tenant  string
		ok      bool
	}{
		{"instance1.tenant1.inbound-events", "tenant1", true},
		{"instance1.acme.devices.token.events", "acme", true}, // ADR-006/ADR-048 device-plane MQTT mapping
		{"instance1.tenant1.a.b.c", "tenant1", true},
		{"instance1..inbound-events", "", false}, // empty tenant
		{"instance1.tenant1", "", false},         // too few segments
		{"instance1", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		tenant, ok := ParseTenantFromSubject(tc.subject)
		if ok != tc.ok || tenant != tc.tenant {
			t.Errorf("ParseTenantFromSubject(%q) = (%q, %v); want (%q, %v)",
				tc.subject, tenant, ok, tc.tenant, tc.ok)
		}
	}
}

// The WriteMessages tenant guard must run BEFORE the tenant is spliced into a
// subject (and before any nmgr/js dereference), so the security property — no
// unsafe tenant reaches a subject — cannot be lost to a future reordering. A
// zero-value natsWriter has no nmgr; if either rejection path is ever moved below
// the subject construction, this test fails by nil-panic rather than silently
// passing. The valid-tenant path is deliberately NOT exercised here: it would
// legitimately nil-panic on the missing nmgr.
func TestWriteMessagesRejectsBadTenant(t *testing.T) {
	w := &natsWriter{}

	if err := w.WriteMessages(context.Background()); !errors.Is(err, core.ErrNoTenant) {
		t.Errorf("no-tenant write: got %v, want ErrNoTenant", err)
	}

	ctx := core.WithTenant(context.Background(), "acme.*") // wildcard-injecting tenant
	if err := w.WriteMessages(ctx, Message{Value: []byte("x")}); err == nil {
		t.Error("malformed-tenant write: got nil, want rejection before publish")
	}
}

func TestSubjectHelpers(t *testing.T) {
	if got := ScopedSubject("inst", "ten", "inbound-events"); got != "inst.ten.inbound-events" {
		t.Errorf("ScopedSubject = %q", got)
	}
	if got := WildcardSubject("inst", "inbound-events"); got != "inst.*.inbound-events" {
		t.Errorf("WildcardSubject = %q", got)
	}
	// Names must not contain JetStream-illegal characters.
	if got := StreamName("inst.x", "inbound-events"); got != "inst_x_inbound-events" {
		t.Errorf("StreamName = %q", got)
	}
	if got := DurableName("inst", "device-management", "resolved-events"); got != "inst_device-management_resolved-events" {
		t.Errorf("DurableName = %q", got)
	}
}

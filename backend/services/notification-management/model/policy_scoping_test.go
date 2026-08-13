// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strings"
	"testing"
)

// A device-type-scoped policy is skipped by the dispatcher, so accepting one
// returns success on a policy that will never deliver. These pin the fail-closed
// behaviour: the write is refused, nothing is persisted, and — the counterweight —
// a tenant-wide policy still writes untouched. Rejecting is only safe while valid
// input keeps working.
func TestDeviceTypeScopedPolicyIsRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")

	if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "smtp-ops", ChannelType: ChannelTypeSMTP, Enabled: true,
	}); err != nil {
		t.Fatalf("create channel: %v", err)
	}

	scoped := func(token string, deviceType *string) *NotificationPolicyCreateRequest {
		return &NotificationPolicyCreateRequest{
			Token:           token,
			Enabled:         true,
			DeviceTypeToken: deviceType,
			Rules: []*NotificationRuleCreateRequest{
				{Severity: SeverityAny, ChannelToken: "smtp-ops", Recipients: strPtr(`["ops@example.com"]`)},
			},
		}
	}

	t.Run("create is refused and writes nothing", func(t *testing.T) {
		_, err := api.CreateNotificationPolicy(ctx, scoped("scoped-policy", strPtr("thermostat-v3")))
		if err == nil {
			t.Fatal("creating a device-type-scoped policy succeeded; it must be refused, because the dispatcher skips it and it would silently deliver nothing")
		}
		if !strings.Contains(err.Error(), "deviceTypeToken") {
			t.Errorf("error should name the offending field so the operator can act on it, got: %v", err)
		}

		// A refused write must leave nothing behind.
		found, ferr := api.NotificationPoliciesByToken(ctx, []string{"scoped-policy"})
		if ferr != nil {
			t.Fatalf("read back: %v", ferr)
		}
		if len(found) != 0 {
			t.Errorf("refused policy was persisted anyway: %+v", found)
		}
	})

	t.Run("tenant-wide policy still writes", func(t *testing.T) {
		created, err := api.CreateNotificationPolicy(ctx, scoped("tenant-wide", nil))
		if err != nil {
			t.Fatalf("a tenant-wide policy must still be accepted: %v", err)
		}
		if len(created.Rules) != 1 {
			t.Errorf("expected the rule set to survive, got %d rules", len(created.Rules))
		}
	})

	t.Run("an empty deviceTypeToken is not scoping", func(t *testing.T) {
		// Distinguishing "unset" from "set to blank" matters: a client that sends ""
		// is not asking for scoping, and refusing it would be a false positive.
		if _, err := api.CreateNotificationPolicy(ctx, scoped("blank-scope", strPtr("  "))); err != nil {
			t.Fatalf("a blank deviceTypeToken is not a scoped policy and must be accepted: %v", err)
		}

		// Accepting it is only safe if it PERSISTS as NULL. The dispatcher skips on
		// `Valid && String != ""`, so a blank that stored as Valid("  ") would be
		// accepted here and then silently deliver nothing — the exact bug this file
		// exists to prevent, reintroduced through whitespace. That hinges on
		// validateDeviceTypeScoping's TrimSpace agreeing with NullStrOf's trim, which
		// is two separate decisions in two packages, so assert the outcome rather
		// than trusting they stay in step.
		found, err := api.NotificationPoliciesByToken(ctx, []string{"blank-scope"})
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if len(found) != 1 {
			t.Fatalf("expected the blank-scope policy to persist, got %d", len(found))
		}
		if found[0].DeviceTypeToken.Valid {
			t.Errorf("a blank deviceTypeToken persisted as %q rather than NULL; the dispatcher would skip this policy and it would deliver nothing",
				found[0].DeviceTypeToken.String)
		}
	})

	t.Run("update is refused too", func(t *testing.T) {
		// The gate has to cover update as well — otherwise a tenant-wide policy can
		// be scoped after the fact and go quiet.
		if _, err := api.UpdateNotificationPolicy(ctx, "tenant-wide", scoped("tenant-wide", strPtr("thermostat-v3"))); err == nil {
			t.Fatal("updating a policy to be device-type-scoped succeeded; it must be refused")
		}
	})
}

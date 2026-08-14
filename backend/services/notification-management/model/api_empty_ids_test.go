// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "testing"

// An empty id list must be answered with nothing, from BOTH by-id lookups here.
//
// Both, not one: they are two doors onto the same defect, and each was written
// separately — a fix applied to whichever the test happened to name is the shape
// this is guarding against. gorm's inline-condition form drops an empty slice
// instead of rendering a predicate that matches nothing, which turned
// `notificationChannelsById(ids: [])` — a legal document — into an unfiltered,
// unpaginated read of every channel and policy the tenant has. Policies preload
// their rules and each rule's channel, so that read was the more expensive one.
//
// rdb.FindByIds carries the guard now; this asserts the lookups still go through
// it, which the helper's own test cannot see.
func TestByIdLookupsWithNoIdsReturnNothing(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")

	for _, token := range []string{"smtp-a", "smtp-b", "smtp-c"} {
		if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
			Token: token, ChannelType: ChannelTypeSMTP, Enabled: true,
		}); err != nil {
			t.Fatalf("seed channel %s: %v", token, err)
		}
	}
	if _, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
		Token: "ops", Enabled: true,
		Rules: []*NotificationRuleCreateRequest{{Severity: SeverityAny, ChannelToken: "smtp-a"}},
	}); err != nil {
		t.Fatalf("seed policy: %v", err)
	}

	for _, ids := range [][]uint{{}, nil} {
		channels, err := api.NotificationChannelsById(ctx, ids)
		if err != nil {
			t.Fatalf("NotificationChannelsById(%v): %v", ids, err)
		}
		if len(channels) != 0 {
			t.Errorf("NotificationChannelsById(%v) returned %d channels, want none — the id filter was dropped",
				ids, len(channels))
		}

		policies, err := api.NotificationPoliciesById(ctx, ids)
		if err != nil {
			t.Fatalf("NotificationPoliciesById(%v): %v", ids, err)
		}
		if len(policies) != 0 {
			t.Errorf("NotificationPoliciesById(%v) returned %d policies, want none — the id filter was dropped",
				ids, len(policies))
		}
	}
}

// The counterweight: lookups that always returned nothing would satisfy the test
// above and break every read in the service.
func TestByIdLookupsStillReturnWhatWasAsked(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")

	channel, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "smtp-a", ChannelType: ChannelTypeSMTP, Enabled: true,
	})
	if err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	policy, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
		Token: "ops", Enabled: true,
		Rules: []*NotificationRuleCreateRequest{{Severity: SeverityAny, ChannelToken: "smtp-a"}},
	})
	if err != nil {
		t.Fatalf("seed policy: %v", err)
	}

	channels, err := api.NotificationChannelsById(ctx, []uint{channel.ID})
	if err != nil {
		t.Fatalf("NotificationChannelsById: %v", err)
	}
	if len(channels) != 1 || channels[0].Token != "smtp-a" {
		t.Fatalf("NotificationChannelsById returned %d channels, want just smtp-a", len(channels))
	}

	policies, err := api.NotificationPoliciesById(ctx, []uint{policy.ID})
	if err != nil {
		t.Fatalf("NotificationPoliciesById: %v", err)
	}
	if len(policies) != 1 || policies[0].Token != "ops" {
		t.Fatalf("NotificationPoliciesById returned %d policies, want just ops", len(policies))
	}
	// The preload chain must survive the move to the helper — a policy without its
	// rules is a policy that notifies nobody.
	if len(policies[0].Rules) != 1 || policies[0].Rules[0].Channel == nil {
		t.Fatalf("NotificationPoliciesById lost the preloaded rules/channel: %+v", policies[0].Rules)
	}
}

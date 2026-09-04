// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "testing"

// WHICH RECORD AN UPDATE WRITES.
//
// Both mutations here still take their create input, so each carries the token
// twice — once as the argument that says which record, once inside the payload —
// and the two could disagree. They took OPPOSITE rules, for a reason that is about
// the entity rather than about tidiness:
//
//   - A CHANNEL rename is intended and pinned (TestUpdateChannelRenamePreservesSecret).
//     Its delivery secret is keyed by the channel's immutable id, and a policy's rules
//     store ChannelId and resolve the token only at write time, so a rename orphans
//     nothing. It takes the RENAME rule: only a BLANK new token is refused.
//   - A POLICY rename is pinned by nothing and referenced by nothing. It takes the
//     RECONCILE: the argument names the record, a disagreeing payload token is
//     refused, an empty one is ignored.
//
// What both had in common was the defect: the payload token was written straight
// onto the row, so an empty one — legal, since `token: String!` admits "" — left a
// live record addressable by nothing, and the mutation returned success.

// The headline case, and the one that shipped. `newTestApi` registers the production
// token-grammar callback, which does NOT catch this: the write succeeded.
func TestUpdateChannel_ABlankPayloadTokenIsRefused(t *testing.T) {
	for _, blank := range []string{"", "   ", "\t"} {
		t.Run("blank="+blank, func(t *testing.T) {
			api := newTestApi(t)
			ctx := tenantCtx("A")
			if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
				Token: "chan-a", ChannelType: ChannelTypeWebhook, Enabled: true,
			}); err != nil {
				t.Fatalf("create: %v", err)
			}

			if _, err := api.UpdateNotificationChannel(ctx, "chan-a", &NotificationChannelCreateRequest{
				Token: blank, ChannelType: ChannelTypeWebhook, Name: strPtr("Renamed"), Enabled: true,
			}); err == nil {
				t.Fatalf("a blank payload token %q was accepted", blank)
			}
			rows, err := api.NotificationChannelsByToken(ctx, []string{"chan-a"})
			if err != nil || len(rows) != 1 {
				t.Fatalf("the channel is no longer findable by its own token: err=%v rows=%d", err, len(rows))
			}
			if rows[0].Token != "chan-a" {
				t.Fatalf("token moved to %q", rows[0].Token)
			}
		})
	}
}

// THE COUNTERWEIGHT. The refusal above must not have been bought by removing the
// rename. (TestUpdateChannelRenamePreservesSecret covers the secret; this covers the
// rename itself reaching the row.)
func TestUpdateChannel_ADifferingTokenStillRenames(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "chan-a", ChannelType: ChannelTypeWebhook, Enabled: true,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := api.UpdateNotificationChannel(ctx, "chan-a", &NotificationChannelCreateRequest{
		Token: "chan-b", ChannelType: ChannelTypeWebhook, Enabled: true,
	}); err != nil {
		t.Fatalf("a rename was refused: %v", err)
	}
	rows, err := api.NotificationChannelsByToken(ctx, []string{"chan-b"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("the renamed channel is not findable: err=%v rows=%d", err, len(rows))
	}
}

// A policy takes the reconcile. TWO rows, because with one the assertion that the
// other was untouched is vacuous.
func TestUpdatePolicy_ADisagreeingPayloadTokenIsRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	for _, tok := range []string{"pol-a", "pol-b"} {
		if _, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
			Token: tok, Name: strPtr("Original " + tok), Enabled: true,
		}); err != nil {
			t.Fatalf("create %q: %v", tok, err)
		}
	}

	if _, err := api.UpdateNotificationPolicy(ctx, "pol-a", &NotificationPolicyCreateRequest{
		Token: "pol-b", Name: strPtr("Hijacked"), Enabled: true,
	}); err == nil {
		t.Fatal("an update whose payload named a different policy was accepted")
	}
	for _, tok := range []string{"pol-a", "pol-b"} {
		rows, err := api.NotificationPoliciesByToken(ctx, []string{tok})
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s missing after a refused update: err=%v rows=%d", tok, err, len(rows))
		}
		if got := rows[0].Name.String; got != "Original "+tok {
			t.Errorf("the refused update still changed %s: name = %q", tok, got)
		}
	}
}

// …and an empty payload token is "unspecified" rather than a request to blank the row.
func TestUpdatePolicy_AnEmptyPayloadTokenDoesNotBlankTheRow(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	if _, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
		Token: "pol-a", Name: strPtr("Original"), Enabled: true,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := api.UpdateNotificationPolicy(ctx, "pol-a", &NotificationPolicyCreateRequest{
		Name: strPtr("Renamed"), Enabled: true,
	}); err != nil {
		t.Fatalf("an update with no payload token was refused: %v", err)
	}
	rows, err := api.NotificationPoliciesByToken(ctx, []string{"pol-a"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("the policy is no longer findable by its own token: err=%v rows=%d", err, len(rows))
	}
	if rows[0].Token != "pol-a" {
		t.Fatalf("token moved to %q", rows[0].Token)
	}
	if got := rows[0].Name.String; got != "Renamed" {
		t.Fatalf("the edit did not apply: name = %q", got)
	}
}

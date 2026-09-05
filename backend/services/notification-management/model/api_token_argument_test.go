// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strings"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

// WHICH RECORD AN UPDATE WRITES, AND HOW A CHANNEL MOVES.
//
// Both update mutations here used to take their create input, so each carried the token
// twice — once as the argument that says which record, once inside the payload — and the
// two could disagree. They took OPPOSITE rules for that disagreement, and the split was
// about the entity rather than about tidiness: a CHANNEL rename was intended and pinned,
// a POLICY rename was pinned by nothing and referenced by nothing.
//
// # 🔴 THE DISAGREEMENT IS NOW UNREPRESENTABLE, AND THE TWO POLICY TESTS ARE GONE
//
// Both inputs are dedicated *UpdateRequests carrying no token at all. The policy's
// reconcile rule (dcgraphql.ErrPayloadTokenDisagrees) therefore has nothing left to
// govern in this service, and the two tests that drove it — a disagreeing payload token
// refused, an empty one read as "unspecified" — would now be asserting the behaviour of a
// field that does not exist. Deleting them rather than rewriting them against the new
// shape is deliberate: there is no request they could send.
//
// What replaces the claim structurally is the guard in
// partial_update_guard_test.go, which asks the request TYPE whether it carries a Token
// field, so a token reintroduced into either input fails on the day it is added. The rule
// itself is still exercised exhaustively in core (graphql.TestErrPayloadTokenDisagrees,
// TestErrRenameTokenUnusable).
//
// # What stays here is the CHANNEL rename, which survived rather than being deleted
//
// It moved to its own mutation instead of riding a payload token. The three rules below
// are that mutation's whole contract, and the first is the defect that shipped: `token:
// String!` admits "", and the blank used to be written straight onto the row, leaving a
// live channel addressable by nothing and returning success.

// A blank new token is refused. `newToken: String!` admits "", and the token GRAMMAR does
// not catch a whitespace-only one — that is the hole the original defect went through, so
// whitespace is driven alongside the empty string rather than assumed to be covered.
func TestRenameChannel_ABlankNewTokenIsRefused(t *testing.T) {
	for _, blank := range []string{"", "   ", "\t"} {
		t.Run("blank="+blank, func(t *testing.T) {
			api := newTestApi(t)
			ctx := tenantCtx("A")
			if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
				Token: "chan-a", ChannelType: ChannelTypeWebhook, Enabled: true,
			}); err != nil {
				t.Fatalf("create: %v", err)
			}

			if _, err := api.RenameNotificationChannel(ctx, "chan-a", blank); err == nil {
				t.Fatalf("a blank new token %q was accepted", blank)
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

// THE COUNTERWEIGHT. The refusal above must not have been bought by refusing every
// rename. (TestRenameChannelPreservesSecret covers the secret surviving; this covers the
// rename reaching the row at all.)
func TestRenameChannel_ADifferingTokenMovesTheRecord(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "chan-a", ChannelType: ChannelTypeWebhook, Enabled: true,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	renamed, err := api.RenameNotificationChannel(ctx, "chan-a", "chan-b")
	if err != nil {
		t.Fatalf("a rename was refused: %v", err)
	}
	if renamed.Token != "chan-b" {
		t.Fatalf("the returned channel still reads %q", renamed.Token)
	}
	rows, err := api.NotificationChannelsByToken(ctx, []string{"chan-b"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("the renamed channel is not findable: err=%v rows=%d", err, len(rows))
	}
	if old, _ := api.NotificationChannelsByToken(ctx, []string{"chan-a"}); len(old) != 0 {
		t.Fatalf("the old token still resolves to %d channels", len(old))
	}
}

// Renaming to the token the channel already has is an idempotent no-op SUCCESS, so a
// retry after a partial failure is safe rather than a not-found or a self-collision.
func TestRenameChannel_TheSameTokenIsANoOpSuccess(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "chan-a", ChannelType: ChannelTypeWebhook, Name: strPtr("Original"), Enabled: true,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	same, err := api.RenameNotificationChannel(ctx, "chan-a", "chan-a")
	if err != nil {
		t.Fatalf("renaming to the current token was refused: %v", err)
	}
	if same.Token != "chan-a" || same.Name.String != "Original" {
		t.Fatalf("the no-op did not return the record: %+v", same)
	}
	rows, err := api.NotificationChannelsByToken(ctx, []string{"chan-a"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected exactly one channel after the no-op: err=%v rows=%d", err, len(rows))
	}
}

// A token already held by ANOTHER of the tenant's channels is refused by name, before the
// unique index has to. Two rows, because with one the assertion that the other survived
// unchanged would be vacuous.
//
// 🔴 The SQLite fixture does not create the Postgres partial unique index (see
// newTestApi), so nothing here would fail without the explicit check — which is exactly
// why the check exists rather than being left to the constraint. Its message is asserted
// too: a collision surfacing as a driver error names a column and an index, not the thing
// the caller did wrong.
func TestRenameChannel_ATakenTokenIsRefusedByName(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	for _, tok := range []string{"chan-a", "chan-b"} {
		if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
			Token: tok, ChannelType: ChannelTypeWebhook, Name: strPtr("Original " + tok), Enabled: true,
		}); err != nil {
			t.Fatalf("create %q: %v", tok, err)
		}
	}

	err := func() error {
		_, err := api.RenameNotificationChannel(ctx, "chan-a", "chan-b")
		return err
	}()
	if err == nil {
		t.Fatal("renaming onto a token another channel holds was accepted")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Errorf("the refusal does not say the token is taken: %v", err)
	}
	for _, tok := range []string{"chan-a", "chan-b"} {
		rows, err := api.NotificationChannelsByToken(ctx, []string{tok})
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s missing after a refused rename: err=%v rows=%d", tok, err, len(rows))
		}
		if got := rows[0].Name.String; got != "Original "+tok {
			t.Errorf("the refused rename still changed %s: name = %q", tok, got)
		}
	}
}

// Renaming a channel that does not exist is a not-found, not a silent create and not a
// success that wrote nothing. The no-op branch above returns early, so this pins that it
// returns early on the LOADED record rather than on the tokens matching.
func TestRenameChannel_AnUnknownTokenIsNotFound(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	for _, newToken := range []string{"no-such-token", "chan-b"} {
		if _, err := api.RenameNotificationChannel(ctx, "no-such-token", newToken); err == nil {
			t.Fatalf("renaming the unknown token to %q succeeded", newToken)
		}
	}
	if rows, _ := api.NotificationChannelsByToken(ctx, []string{"chan-b"}); len(rows) != 0 {
		t.Fatalf("a refused rename created %d channels", len(rows))
	}
}

// A tenant's rename cannot see another tenant's tokens, in either direction: the
// collision check is tenant-scoped like every other read, so tenant A renaming onto a
// token tenant B holds must SUCCEED (the token is unique per tenant, not per instance),
// and must not touch B's channel.
func TestRenameChannel_TheCollisionCheckIsTenantScoped(t *testing.T) {
	api := newTestApi(t)
	ctxA, ctxB := tenantCtx("A"), tenantCtx("B")

	if _, err := api.CreateNotificationChannel(ctxA, &NotificationChannelCreateRequest{
		Token: "chan-a", ChannelType: ChannelTypeWebhook, Enabled: true,
	}); err != nil {
		t.Fatalf("create in A: %v", err)
	}
	if _, err := api.CreateNotificationChannel(ctxB, &NotificationChannelCreateRequest{
		Token: "shared", ChannelType: ChannelTypeSMTP, Name: strPtr("B's channel"), Enabled: true,
	}); err != nil {
		t.Fatalf("create in B: %v", err)
	}

	if _, err := api.RenameNotificationChannel(ctxA, "chan-a", "shared"); err != nil {
		t.Fatalf("A was refused a token only B holds: %v", err)
	}
	rowsB, err := api.NotificationChannelsByToken(ctxB, []string{"shared"})
	if err != nil || len(rowsB) != 1 {
		t.Fatalf("B's channel is gone: err=%v rows=%d", err, len(rowsB))
	}
	if rowsB[0].Name.String != "B's channel" {
		t.Fatalf("A's rename reached B's row: %+v", rowsB[0])
	}
}

// An update cannot move a channel's identity at all, because the input has no token to
// carry one. This is the structural half of the claim above, asserted where a reader of
// the rename tests will look for it: the compiler is what enforces it, so the test that
// says so is the one that would stop compiling if a Token field came back.
func TestUpdateChannel_CannotMoveTheToken(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "chan-a", ChannelType: ChannelTypeWebhook, Enabled: true,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := api.UpdateNotificationChannel(ctx, "chan-a", &NotificationChannelUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	rows, err := api.NotificationChannelsByToken(ctx, []string{"chan-a"})
	if err != nil || len(rows) != 1 || rows[0].Token != "chan-a" {
		t.Fatalf("the channel's token moved under an update: err=%v rows=%d", err, len(rows))
	}
	if rows[0].Name.String != "Renamed" {
		t.Fatalf("the edit did not apply: name = %q", rows[0].Name.String)
	}
}

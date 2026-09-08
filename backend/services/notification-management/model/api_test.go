// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// testRootKey is a fixed 32-byte instance root key for the secret store in unit
// tests (never a real key). It only has to be 256-bit and stable across the test.
var testRootKey = []byte("0123456789abcdef0123456789abcdef")

// newTestApi spins up an in-memory sqlite database with the tenant-scope and
// token-grammar callbacks registered and the notification tables + the shared
// secrets table migrated, plus an envelope-encrypting secret store over the same DB,
// so the CRUD path (including the channel secret round-trip) is exercised exactly as
// production does. The Postgres partial unique indexes the real migrations add are
// not created here (they are validated on a live cluster); these tests cover CRUD
// behavior, not the DB constraints.
func newTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := rdb.RegisterTokenGrammar(db); err != nil {
		t.Fatalf("register token grammar: %v", err)
	}
	if err := db.AutoMigrate(&NotificationChannel{}, &NotificationPolicy{},
		&NotificationRule{}, &NotificationState{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := secrets.NewSecretStoreSchema().Migrate(db); err != nil {
		t.Fatalf("migrate secrets: %v", err)
	}
	kek, err := secrets.NewInstanceKeyProvider(testRootKey)
	if err != nil {
		t.Fatalf("kek: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db}, secrets.NewStore(db, kek))
}

// channelSecretValue resolves the stored delivery secret for a channel (looked up by
// token to get its immutable id, the secret's key) in the test's tenant context, or
// "" when none is stored.
func channelSecretValue(t *testing.T, api *Api, ctx context.Context, token string) string {
	t.Helper()
	found, err := api.NotificationChannelsByToken(ctx, []string{token})
	if err != nil || len(found) != 1 {
		t.Fatalf("load channel %q: got %d err=%v", token, len(found), err)
	}
	ref, err := ChannelSecretRef(ctx, found[0].ID)
	if err != nil {
		t.Fatalf("channel secret ref: %v", err)
	}
	value, err := api.Secrets.Resolve(ctx, ref)
	if errors.Is(err, secrets.ErrSecretNotFound) {
		return ""
	}
	if err != nil {
		t.Fatalf("resolve channel secret: %v", err)
	}
	return string(value)
}

func strPtr(s string) *string { return &s }

func tenantCtx(tenant string) context.Context {
	return core.WithTenant(context.Background(), tenant)
}

// The secret is write-only: it is sealed into the secret store under the channel's
// handle (round-trips via Resolve, never a column), and an unknown channel type is
// rejected.
func TestCreateChannelSecretAndValidation(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")

	if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token:       "smtp-primary",
		ChannelType: ChannelTypeSMTP,
		Config:      strPtr(`{"host":"smtp.example.com","port":587}`),
		Secret:      strPtr("hunter2"),
		Enabled:     true,
	}); err != nil {
		t.Fatalf("CreateNotificationChannel: %v", err)
	}
	if got := channelSecretValue(t, api, ctx, "smtp-primary"); got != "hunter2" {
		t.Fatalf("secret not stored: %q", got)
	}

	// Unknown channel type fails closed.
	if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "bogus", ChannelType: "carrier-pigeon", Enabled: true,
	}); err == nil {
		t.Fatal("expected unknown channel type to be rejected")
	}

	// Malformed config JSON fails closed.
	if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "bad-config", ChannelType: ChannelTypeWebhook, Config: strPtr("{not json"), Enabled: true,
	}); err == nil {
		t.Fatal("expected malformed config to be rejected")
	}

	// Malformed metadata JSON fails closed (parity with policy metadata).
	if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "bad-meta", ChannelType: ChannelTypeWebhook, Metadata: strPtr("{nope"), Enabled: true,
	}); err == nil {
		t.Fatal("expected malformed metadata to be rejected")
	}
}

// THE SECRET'S THREE STATES, all four spellings.
//
// The secret is the one field on the channel whose value lives outside the row, and the
// one where getting the fold wrong is silent: a channel whose delivery credential quietly
// went missing keeps its config, keeps returning success on every edit, and stops
// authenticating at the moment an alarm needed to reach a human.
//
//	OMITTED  preserves — it cannot be read back to re-send, so this is the only reading
//	value    rotates
//	NULL     clears — NEW, and the state the create input could not express
//	""       clears — unchanged, so a client already sending it keeps working
func TestUpdateChannelSecretThreeStates(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")

	seed := func() {
		if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
			Token: "smtp-primary", ChannelType: ChannelTypeSMTP, Secret: strPtr("orig"), Enabled: true,
		}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	seed()

	// Omit the secret: it must be preserved, and the rest of the update must still apply
	// (otherwise "preserved" could be bought by the update doing nothing at all).
	updated, err := api.UpdateNotificationChannel(ctx, "smtp-primary", &NotificationChannelUpdateRequest{
		Name:    dcgraphql.OptionalStringOf("Primary"),
		Enabled: dcgraphql.OptionalBoolOf(false),
	})
	if err != nil {
		t.Fatalf("update (omit secret): %v", err)
	}
	if got := channelSecretValue(t, api, ctx, "smtp-primary"); got != "orig" {
		t.Fatalf("secret not preserved on omit: %q", got)
	}
	if updated.Enabled {
		t.Fatal("other fields should still update when secret omitted")
	}

	// A value rotates it.
	if _, err := api.UpdateNotificationChannel(ctx, "smtp-primary", &NotificationChannelUpdateRequest{
		Secret: dcgraphql.OptionalStringOf("rotated"),
	}); err != nil {
		t.Fatalf("update (new secret): %v", err)
	}
	if got := channelSecretValue(t, api, ctx, "smtp-primary"); got != "rotated" {
		t.Fatalf("secret not replaced: %q", got)
	}

	// An explicit null clears it. This is the state that did not exist before: the only
	// way to remove a secret used to be the empty string, which is the exact inverse of
	// what null means on every other field of the same request.
	if _, err := api.UpdateNotificationChannel(ctx, "smtp-primary", &NotificationChannelUpdateRequest{
		Secret: dcgraphql.ClearedString(),
	}); err != nil {
		t.Fatalf("update (null secret): %v", err)
	}
	if got := channelSecretValue(t, api, ctx, "smtp-primary"); got != "" {
		t.Fatalf("secret not cleared on an explicit null: %q", got)
	}

	// And the empty string still clears, because a client that already spells the removal
	// that way must not silently start storing "" as a credential instead.
	if _, err := api.UpdateNotificationChannel(ctx, "smtp-primary", &NotificationChannelUpdateRequest{
		Secret: dcgraphql.OptionalStringOf("restored"),
	}); err != nil {
		t.Fatalf("update (restore secret): %v", err)
	}
	if got := channelSecretValue(t, api, ctx, "smtp-primary"); got != "restored" {
		t.Fatalf("secret not restored before the empty-string case: %q", got)
	}
	if _, err := api.UpdateNotificationChannel(ctx, "smtp-primary", &NotificationChannelUpdateRequest{
		Secret: dcgraphql.OptionalStringOf(""),
	}); err != nil {
		t.Fatalf("update (empty-string secret): %v", err)
	}
	if got := channelSecretValue(t, api, ctx, "smtp-primary"); got != "" {
		t.Fatalf("secret not cleared on empty string: %q", got)
	}
}

// A token rename keeps the channel's delivery secret bound to it. The secret is keyed
// by the channel's immutable id, not its mutable token, so renaming must not orphan the
// credential — the pre-ADR-059 token-keyed design would have silently lost it and broken
// SMTP/webhook auth with no config change.
//
// 🔴 THIS TEST WAS RE-POINTED, NOT REPLACED. It used to drive the rename through
// UpdateNotificationChannel's payload token, which the partial-update conversion removed;
// it now drives RenameNotificationChannel. Deleting it with the old shape would have
// removed the only evidence for the claim while leaving the claim standing in three
// comments and a core doc.
func TestRenameChannelPreservesSecret(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")

	if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "smtp-old", ChannelType: ChannelTypeSMTP, Secret: strPtr("keepme"), Enabled: true,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := api.RenameNotificationChannel(ctx, "smtp-old", "smtp-new"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got := channelSecretValue(t, api, ctx, "smtp-new"); got != "keepme" {
		t.Fatalf("secret orphaned by rename: %q", got)
	}
}

// A policy creates its rules (channel resolved by token), an unknown channel token
// fails the whole write, and update replaces the rule set.
func TestPolicyRulesLifecycle(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")

	mkChannel := func(token string) {
		if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
			Token: token, ChannelType: ChannelTypeSMTP, Enabled: true,
		}); err != nil {
			t.Fatalf("create channel %s: %v", token, err)
		}
	}
	mkChannel("smtp-crit")
	mkChannel("smtp-warn")

	created, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
		Token:   "ops-policy",
		Enabled: true,
		Rules: []*NotificationRuleCreateRequest{
			{Severity: "CRITICAL", ChannelToken: "smtp-crit", Recipients: strPtr(`["oncall@example.com"]`)},
			{Severity: SeverityAny, ChannelToken: "smtp-warn"},
		},
	})
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}
	if len(created.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(created.Rules))
	}
	// The create response must carry each rule's resolved channel (not just its id),
	// so the mutation result matches what a subsequent read preloads.
	for _, rule := range created.Rules {
		if rule.Channel == nil {
			t.Fatalf("create response rule missing resolved channel: %+v", rule)
		}
	}

	// Unknown channel token fails the whole write (no partial policy).
	if _, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
		Token: "bad-policy", Enabled: true,
		Rules: []*NotificationRuleCreateRequest{{Severity: "CRITICAL", ChannelToken: "does-not-exist"}},
	}); err == nil {
		t.Fatal("expected unknown channel token to fail policy create")
	}
	if got, _ := api.NotificationPoliciesByToken(ctx, []string{"bad-policy"}); len(got) != 0 {
		t.Fatal("failed policy create left a row behind")
	}

	// An update that NAMES a rule set replaces it wholesale (2 -> 1).
	updated, err := api.UpdateNotificationPolicy(ctx, "ops-policy", &NotificationPolicyUpdateRequest{
		Rules: OptionalNotificationRuleListOf(
			[]*NotificationRuleCreateRequest{{Severity: "CRITICAL", ChannelToken: "smtp-crit"}}),
	})
	if err != nil {
		t.Fatalf("update policy: %v", err)
	}
	if len(updated.Rules) != 1 {
		t.Fatalf("expected 1 rule after replace, got %d", len(updated.Rules))
	}
	// Reload confirms the old rules are gone (not just detached).
	reloaded, _ := api.NotificationPoliciesByToken(ctx, []string{"ops-policy"})
	if len(reloaded) != 1 || len(reloaded[0].Rules) != 1 {
		t.Fatalf("expected 1 persisted rule, got %d", len(reloaded[0].Rules))
	}
	if reloaded[0].Rules[0].Channel == nil || reloaded[0].Rules[0].Channel.Token != "smtp-crit" {
		t.Fatal("rule channel not preloaded on read")
	}
}

// Everything a channel update can refuse is decided BEFORE anything is written.
//
// 🔴 THE ASSERTION IS THAT `name` DID NOT MOVE, not merely that an error came back. Under
// a naive conversion each field is folded and saved as it is reached, so a request pairing
// a good `name` with a bad `channelType` returns an error AND leaves the rename applied —
// a half-configured channel the caller was told did not happen. The harness's own
// refusal property covers the required-null case; these are the VALIDATION refusals, which
// it has no way to declare.
func TestUpdateChannelRefusesTheWholeUpdateBeforeWriting(t *testing.T) {
	for name, request := range map[string]*NotificationChannelUpdateRequest{
		"unknown channel type": {
			Name:        dcgraphql.OptionalStringOf("Renamed"),
			ChannelType: dcgraphql.OptionalStringOf("carrier-pigeon"),
		},
		"malformed config": {
			Name:   dcgraphql.OptionalStringOf("Renamed"),
			Config: dcgraphql.OptionalStringOf("{not json"),
		},
		"config is not an object": {
			Name:   dcgraphql.OptionalStringOf("Renamed"),
			Config: dcgraphql.OptionalStringOf(`["an","array"]`),
		},
		"malformed metadata": {
			Name:     dcgraphql.OptionalStringOf("Renamed"),
			Metadata: dcgraphql.OptionalStringOf("{nope"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			api := newTestApi(t)
			ctx := tenantCtx("A")
			if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
				Token: "chan-a", Name: strPtr("Original"), ChannelType: ChannelTypeSMTP,
				Config: strPtr(`{"host":"smtp.example.invalid"}`), Enabled: true,
			}); err != nil {
				t.Fatalf("create: %v", err)
			}

			if _, err := api.UpdateNotificationChannel(ctx, "chan-a", request); err == nil {
				t.Fatal("the update was accepted")
			}
			rows, err := api.NotificationChannelsByToken(ctx, []string{"chan-a"})
			if err != nil || len(rows) != 1 {
				t.Fatalf("reload: err=%v rows=%d", err, len(rows))
			}
			if got := rows[0].Name.String; got != "Original" {
				t.Errorf("the refused update still renamed the channel to %q", got)
			}
			if rows[0].ChannelType != ChannelTypeSMTP {
				t.Errorf("the refused update still changed the channel type to %q", rows[0].ChannelType)
			}
			if got := string(*rows[0].Config); got != `{"host":"smtp.example.invalid"}` {
				t.Errorf("the refused update still changed the config to %q", got)
			}
		})
	}
}

// The counterweight: a channel type the catalog DOES define is still accepted, so the
// refusals above were not bought by refusing every type.
func TestUpdateChannelAcceptsAKnownChannelType(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "chan-a", ChannelType: ChannelTypeSMTP, Enabled: true,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := api.UpdateNotificationChannel(ctx, "chan-a", &NotificationChannelUpdateRequest{
		ChannelType: dcgraphql.OptionalStringOf(ChannelTypeWebhook),
	}); err != nil {
		t.Fatalf("a known channel type was refused: %v", err)
	}
	rows, _ := api.NotificationChannelsByToken(ctx, []string{"chan-a"})
	if len(rows) != 1 || rows[0].ChannelType != ChannelTypeWebhook {
		t.Fatalf("the type did not change: %+v", rows)
	}
}

// A policy update refuses malformed metadata the same way, and writes nothing.
func TestUpdatePolicyRefusesMalformedMetadataBeforeWriting(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	if _, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
		Token: "ops-policy", Name: strPtr("Original"), Enabled: true,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := api.UpdateNotificationPolicy(ctx, "ops-policy", &NotificationPolicyUpdateRequest{
		Name:     dcgraphql.OptionalStringOf("Renamed"),
		Metadata: dcgraphql.OptionalStringOf("{nope"),
	}); err == nil {
		t.Fatal("malformed metadata was accepted")
	}
	rows, err := api.NotificationPoliciesByToken(ctx, []string{"ops-policy"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("reload: err=%v rows=%d", err, len(rows))
	}
	if got := rows[0].Name.String; got != "Original" {
		t.Errorf("the refused update still renamed the policy to %q", got)
	}
}

// An unknown channel token inside a rule refuses the WHOLE update — the header included —
// rather than saving the header and then failing on the rules.
//
// 🔴 THE HARNESS CANNOT SEE THIS ONE. Its unknown-reference property runs over fields
// declared RequiredRef, and `rules` is Clearable: a null on it is legal, so declaring it as
// a reference would make the harness assert the opposite of the contract. The reference
// lives one level down, inside a list element, which is a shape the field table has no way
// to spell — so the property is driven here instead of being assumed covered.
func TestUpdatePolicyUnknownChannelTokenRefusesTheWholeUpdate(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")

	if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "smtp-ops", ChannelType: ChannelTypeSMTP, Enabled: true,
	}); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
		Token: "ops-policy", Name: strPtr("Original"), Enabled: true,
		Rules: []*NotificationRuleCreateRequest{{Severity: "CRITICAL", ChannelToken: "smtp-ops"}},
	}); err != nil {
		t.Fatalf("create policy: %v", err)
	}

	// The request also renames the policy, so "nothing was written" is an observation
	// rather than a tautology about a request that asked for nothing else.
	if _, err := api.UpdateNotificationPolicy(ctx, "ops-policy", &NotificationPolicyUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
		Rules: OptionalNotificationRuleListOf([]*NotificationRuleCreateRequest{
			{Severity: "MAJOR", ChannelToken: "does-not-exist"},
		}),
	}); err == nil {
		t.Fatal("a rule naming an unknown channel was accepted, leaving a rule that routes nowhere")
	}

	found, err := api.NotificationPoliciesByToken(ctx, []string{"ops-policy"})
	if err != nil || len(found) != 1 {
		t.Fatalf("reload: err=%v rows=%d", err, len(found))
	}
	if got := found[0].Name.String; got != "Original" {
		t.Errorf("the refused update still renamed the policy to %q", got)
	}
	if len(found[0].Rules) != 1 || found[0].Rules[0].Severity != "CRITICAL" {
		t.Fatalf("the refused update disturbed the rule set: %+v", found[0].Rules)
	}
}

// A channel referenced by a policy rule cannot be deleted; once the reference is
// gone the delete succeeds.
func TestDeleteChannelInUseGuard(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")

	if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "smtp-crit", ChannelType: ChannelTypeSMTP, Enabled: true,
	}); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
		Token: "ops-policy", Enabled: true,
		Rules: []*NotificationRuleCreateRequest{{Severity: "CRITICAL", ChannelToken: "smtp-crit"}},
	}); err != nil {
		t.Fatalf("create policy: %v", err)
	}

	if _, err := api.DeleteNotificationChannel(ctx, "smtp-crit"); !errors.Is(err, ErrChannelInUse) {
		t.Fatalf("expected ErrChannelInUse, got %v", err)
	}

	// Remove the policy, then the channel deletes.
	if _, err := api.DeleteNotificationPolicy(ctx, "ops-policy"); err != nil {
		t.Fatalf("delete policy: %v", err)
	}
	deleted, err := api.DeleteNotificationChannel(ctx, "smtp-crit")
	if err != nil || !deleted {
		t.Fatalf("expected channel delete to succeed, got deleted=%v err=%v", deleted, err)
	}
}

// Tenant scope isolates channels: a channel created under tenant A is invisible to
// tenant B.
func TestChannelTenantIsolation(t *testing.T) {
	api := newTestApi(t)
	if _, err := api.CreateNotificationChannel(tenantCtx("A"), &NotificationChannelCreateRequest{
		Token: "smtp-primary", ChannelType: ChannelTypeSMTP, Enabled: true,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	found, err := api.NotificationChannelsByToken(tenantCtx("B"), []string{"smtp-primary"})
	if err != nil {
		t.Fatalf("query as B: %v", err)
	}
	if len(found) != 0 {
		t.Fatal("tenant B could see tenant A's channel")
	}
}

// The per-alarm notification-state substrate (written by N.C) reads back by alarm
// token and search. This seeds a row directly (white-box) since N.B ships only the
// read surface.
func TestNotificationStateRead(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")

	now := time.Now()
	seed := &NotificationState{
		AlarmToken:      "alarm-xyz",
		AlarmKey:        "device:7:overtemp",
		Severity:        "critical",
		FirstNotifiedAt: sql.NullTime{Time: now, Valid: true},
		LastNotifiedAt:  sql.NullTime{Time: now, Valid: true},
		NotifyCount:     1,
	}
	if err := api.RDB.DB(ctx).Create(seed).Error; err != nil {
		t.Fatalf("seed state: %v", err)
	}

	byToken, err := api.NotificationStatesByAlarmToken(ctx, []string{"alarm-xyz"})
	if err != nil || len(byToken) != 1 {
		t.Fatalf("by token: got %d err=%v", len(byToken), err)
	}
	if byToken[0].NotifyCount != 1 || byToken[0].Severity != "critical" {
		t.Fatalf("unexpected state: %+v", byToken[0])
	}

	res, err := api.NotificationStates(ctx, NotificationStateSearchCriteria{Severity: strPtr("critical")})
	if err != nil || len(res.Results) != 1 {
		t.Fatalf("search: got %d err=%v", len(res.Results), err)
	}
	// A different-tenant read sees nothing.
	if other, _ := api.NotificationStatesByAlarmToken(tenantCtx("B"), []string{"alarm-xyz"}); len(other) != 0 {
		t.Fatal("tenant B saw tenant A's notification state")
	}
}

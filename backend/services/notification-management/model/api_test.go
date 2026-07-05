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
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newTestApi spins up an in-memory sqlite database with the tenant-scope and
// token-grammar callbacks registered and the notification tables migrated, so the
// CRUD path is exercised exactly as production does. The Postgres partial unique
// indexes the real migrations add are not created here (they are validated on a
// live cluster); these tests cover CRUD behavior, not the DB constraints.
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
	return NewApi(&rdb.RdbManager{Database: db})
}

func strPtr(s string) *string { return &s }

func tenantCtx(tenant string) context.Context {
	return core.WithTenant(context.Background(), tenant)
}

// The secret is write-only but stored reversibly: it round-trips into the column
// (HasSecret / Secret.Valid), and an unknown channel type is rejected.
func TestCreateChannelSecretAndValidation(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")

	created, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token:       "smtp-primary",
		ChannelType: ChannelTypeSMTP,
		Config:      strPtr(`{"host":"smtp.example.com","port":587}`),
		Secret:      strPtr("hunter2"),
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("CreateNotificationChannel: %v", err)
	}
	if !created.Secret.Valid || created.Secret.String != "hunter2" {
		t.Fatalf("secret not stored: valid=%v value=%q", created.Secret.Valid, created.Secret.String)
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
}

// Update preserves the secret when the request omits it (nil), replaces it when a
// value is given, and clears it on an explicit empty string.
func TestUpdateChannelSecretPreserveOnOmit(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")

	if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "smtp-primary", ChannelType: ChannelTypeSMTP, Secret: strPtr("orig"), Enabled: true,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Omit the secret (nil): it must be preserved.
	updated, err := api.UpdateNotificationChannel(ctx, "smtp-primary", &NotificationChannelCreateRequest{
		Token: "smtp-primary", ChannelType: ChannelTypeSMTP, Name: strPtr("Primary"), Enabled: false,
	})
	if err != nil {
		t.Fatalf("update (omit secret): %v", err)
	}
	if !updated.Secret.Valid || updated.Secret.String != "orig" {
		t.Fatalf("secret not preserved on omit: valid=%v value=%q", updated.Secret.Valid, updated.Secret.String)
	}
	if updated.Enabled {
		t.Fatal("other fields should still update when secret omitted")
	}

	// Provide a new secret: it must replace.
	updated, err = api.UpdateNotificationChannel(ctx, "smtp-primary", &NotificationChannelCreateRequest{
		Token: "smtp-primary", ChannelType: ChannelTypeSMTP, Secret: strPtr("rotated"), Enabled: true,
	})
	if err != nil {
		t.Fatalf("update (new secret): %v", err)
	}
	if updated.Secret.String != "rotated" {
		t.Fatalf("secret not replaced: %q", updated.Secret.String)
	}

	// Explicit empty string clears it.
	updated, err = api.UpdateNotificationChannel(ctx, "smtp-primary", &NotificationChannelCreateRequest{
		Token: "smtp-primary", ChannelType: ChannelTypeSMTP, Secret: strPtr(""), Enabled: true,
	})
	if err != nil {
		t.Fatalf("update (clear secret): %v", err)
	}
	if updated.Secret.Valid {
		t.Fatalf("secret not cleared on empty string: %q", updated.Secret.String)
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
			{Severity: "critical", ChannelToken: "smtp-crit", Recipients: strPtr(`["oncall@example.com"]`)},
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
		Rules: []*NotificationRuleCreateRequest{{Severity: "critical", ChannelToken: "does-not-exist"}},
	}); err == nil {
		t.Fatal("expected unknown channel token to fail policy create")
	}
	if got, _ := api.NotificationPoliciesByToken(ctx, []string{"bad-policy"}); len(got) != 0 {
		t.Fatal("failed policy create left a row behind")
	}

	// Update replaces the rule set (2 -> 1).
	updated, err := api.UpdateNotificationPolicy(ctx, "ops-policy", &NotificationPolicyCreateRequest{
		Token:   "ops-policy",
		Enabled: true,
		Rules:   []*NotificationRuleCreateRequest{{Severity: "critical", ChannelToken: "smtp-crit"}},
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
		Rules: []*NotificationRuleCreateRequest{{Severity: "critical", ChannelToken: "smtp-crit"}},
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

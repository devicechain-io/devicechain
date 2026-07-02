// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestCommandStatusValid verifies the known-state predicate.
func TestCommandStatusValid(t *testing.T) {
	valid := []CommandStatus{
		CommandQueued, CommandSent, CommandDelivered,
		CommandSuccessful, CommandTimeout, CommandExpired, CommandFailed,
	}
	for _, s := range valid {
		if !s.Valid() {
			t.Fatalf("expected %s to be valid", s)
		}
	}
	if CommandStatus("BOGUS").Valid() {
		t.Fatalf("expected BOGUS to be invalid")
	}
	if CommandStatus("").Valid() {
		t.Fatalf("expected empty status to be invalid")
	}
}

// TestCommandStatusTerminal verifies the terminal-state predicate.
func TestCommandStatusTerminal(t *testing.T) {
	terminal := map[CommandStatus]bool{
		CommandQueued:     false,
		CommandSent:       false,
		CommandDelivered:  false,
		CommandSuccessful: true,
		CommandTimeout:    true,
		CommandExpired:    true,
		CommandFailed:     true,
	}
	for s, want := range terminal {
		if s.Terminal() != want {
			t.Fatalf("Terminal(%s) = %v, want %v", s, s.Terminal(), want)
		}
	}
}

// TestCanTransition exercises the transition guard directly.
func TestCanTransition(t *testing.T) {
	cases := []struct {
		from CommandStatus
		to   CommandStatus
		want bool
	}{
		{CommandQueued, CommandSent, true},
		{CommandSent, CommandDelivered, true},
		{CommandSent, CommandSuccessful, true},
		{CommandDelivered, CommandSuccessful, true},
		{CommandDelivered, CommandFailed, true},
		{CommandQueued, CommandExpired, true},
		{CommandSent, CommandTimeout, true},
		// Illegal forward edges.
		{CommandQueued, CommandDelivered, false},
		{CommandDelivered, CommandSent, false},
		// No transition out of a terminal state.
		{CommandSuccessful, CommandSent, false},
		{CommandExpired, CommandDelivered, false},
		{CommandFailed, CommandSuccessful, false},
		{CommandTimeout, CommandExpired, false},
	}
	for _, c := range cases {
		if got := canTransition(c.from, c.to); got != c.want {
			t.Fatalf("canTransition(%s, %s) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

// newTestApi spins up an in-memory sqlite database with the tenant-scope
// callbacks registered and the Command table migrated.
func newTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("failed to register tenant scoping: %v", err)
	}
	// Register the token-grammar callbacks too, so the CRUD path is exercised
	// exactly as production does (ADR-042 P2).
	if err := rdb.RegisterTokenGrammar(db); err != nil {
		t.Fatalf("failed to register token grammar: %v", err)
	}
	if err := db.AutoMigrate(&Command{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

// TestCreateSentResponseLifecycle exercises create -> sent -> response.
func TestCreateSentResponseLifecycle(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	created, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token:       "cmd-1",
		DeviceToken: "device-1",
		Name:        "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand failed: %v", err)
	}
	if created.Status != CommandQueued.String() {
		t.Fatalf("expected QUEUED, got %s", created.Status)
	}

	sent, err := api.MarkSent(ctx, created.ID)
	if err != nil {
		t.Fatalf("MarkSent failed: %v", err)
	}
	if sent.Status != CommandSent.String() || !sent.SentTime.Valid {
		t.Fatalf("expected SENT with SentTime set, got %s valid=%v", sent.Status, sent.SentTime.Valid)
	}

	payload := `{"result":"ok"}`
	responded, err := api.MarkResponse(ctx, "cmd-1", true, &payload, nil)
	if err != nil {
		t.Fatalf("MarkResponse failed: %v", err)
	}
	if responded.Status != CommandSuccessful.String() || !responded.RespondedTime.Valid {
		t.Fatalf("expected SUCCESSFUL with RespondedTime set, got %s", responded.Status)
	}

	// A response to an already-terminal command is ignored (idempotent).
	again, err := api.MarkResponse(ctx, "cmd-1", false, nil, strPtr("late"))
	if err != nil {
		t.Fatalf("MarkResponse (late) failed: %v", err)
	}
	if again.Status != CommandSuccessful.String() {
		t.Fatalf("late response mutated terminal command to %s", again.Status)
	}
}

// TestMarkSentRejectedOnTerminal verifies the transition guard at the API level.
func TestMarkSentRejectedOnTerminal(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	created, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token:       "cmd-2",
		DeviceToken: "device-1",
		Name:        "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand failed: %v", err)
	}

	// Cancel moves the command to EXPIRED (terminal).
	if _, err := api.CancelCommand(ctx, "cmd-2"); err != nil {
		t.Fatalf("CancelCommand failed: %v", err)
	}

	if _, err := api.MarkSent(ctx, created.ID); err == nil {
		t.Fatalf("expected MarkSent on a terminal command to be rejected")
	}
}

// TestExpireStale exercises create -> expire transitions across states.
func TestExpireStale(t *testing.T) {
	api := newTestApi(t)
	sysctx := core.WithSystemContext(core.WithTenant(context.Background(), "A"))
	past := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	future := time.Now().Add(1 * time.Hour).Format(time.RFC3339)

	// QUEUED + expired -> EXPIRED.
	if _, err := api.CreateCommand(sysctx, &CommandCreateRequest{
		Token: "q-old", DeviceToken: "d", Name: "x", ExpiresAt: &past,
	}); err != nil {
		t.Fatalf("create q-old failed: %v", err)
	}
	// SENT + expired -> TIMEOUT.
	sentOld, err := api.CreateCommand(sysctx, &CommandCreateRequest{
		Token: "s-old", DeviceToken: "d", Name: "x", ExpiresAt: &past,
	})
	if err != nil {
		t.Fatalf("create s-old failed: %v", err)
	}
	if _, err := api.MarkSent(sysctx, sentOld.ID); err != nil {
		t.Fatalf("mark s-old sent failed: %v", err)
	}
	// QUEUED but not yet expired -> untouched.
	if _, err := api.CreateCommand(sysctx, &CommandCreateRequest{
		Token: "q-fresh", DeviceToken: "d", Name: "x", ExpiresAt: &future,
	}); err != nil {
		t.Fatalf("create q-fresh failed: %v", err)
	}

	count, err := api.ExpireStale(sysctx, time.Now())
	if err != nil {
		t.Fatalf("ExpireStale failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 commands expired, got %d", count)
	}

	assertStatus(t, api, sysctx, "q-old", CommandExpired)
	assertStatus(t, api, sysctx, "s-old", CommandTimeout)
	assertStatus(t, api, sysctx, "q-fresh", CommandQueued)
}

func assertStatus(t *testing.T, api *Api, ctx context.Context, token string, want CommandStatus) {
	t.Helper()
	matches, err := api.CommandsByToken(ctx, []string{token})
	if err != nil {
		t.Fatalf("lookup %s failed: %v", token, err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 row for %s, got %d", token, len(matches))
	}
	if matches[0].Status != want.String() {
		t.Fatalf("status of %s = %s, want %s", token, matches[0].Status, want)
	}
}

func strPtr(s string) *string { return &s }

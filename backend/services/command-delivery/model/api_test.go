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
		CommandQueued, CommandSent,
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
	// DELIVERED was carried for a long time as a valid-but-unemittable status,
	// which made the lifecycle look like it confirmed delivery when it never
	// could. It is gone, and a persisted row still carrying it must now read as
	// unknown rather than as a state the platform recognizes.
	if CommandStatus("DELIVERED").Valid() {
		t.Fatalf("DELIVERED was removed; it must no longer be a known status")
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
	// An unrecognized status must read as non-terminal. This is what keeps a row
	// left over from a removed state (a hand-written DELIVERED, say) reachable by
	// the expiry sweep and by CancelCommand instead of stranded in flight
	// forever. If Terminal() ever defaulted to true, such a row would be frozen.
	for _, unknown := range []CommandStatus{"DELIVERED", "BOGUS", ""} {
		if unknown.Terminal() {
			t.Fatalf("Terminal(%q) = true; an unknown status must be non-terminal so it stays sweepable", unknown)
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
	// Create the per-tenant partial unique index on token exactly as the real migration does
	// (ADR-042 P1), so the CRUD path — including createCommand's ON CONFLICT idempotency — is
	// exercised as production.
	if err := rdb.CreateTenantTokenIndex(db, &Command{}); err != nil {
		t.Fatalf("failed to create tenant token index: %v", err)
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

// TestCreateCommandDefaultTTL proves the ADR-075 L4b horizon: a command whose creator omits
// expiresAt is stamped with the Api's default TTL so it reaches a terminal state instead of sitting
// in SENT forever; a caller-supplied expiresAt always wins; and a zero default disables stamping
// (the pre-config behavior every existing direct-construction test relies on).
func TestCreateCommandDefaultTTL(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "A")

	t.Run("default stamped when caller omits expiresAt", func(t *testing.T) {
		api := newTestApi(t)
		api.DefaultCommandTTL = 48 * time.Hour
		before := time.Now()
		created, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "cmd-ttl", DeviceToken: "d1", Name: "reboot"})
		if err != nil {
			t.Fatalf("CreateCommand failed: %v", err)
		}
		if !created.ExpiresAt.Valid {
			t.Fatalf("expected a stamped expires_at, got NULL — the stuck-in-SENT-forever gap is back")
		}
		want := before.Add(48 * time.Hour)
		if delta := created.ExpiresAt.Time.Sub(want); delta < -time.Minute || delta > time.Minute {
			t.Fatalf("expires_at = %v, want ~%v (now+TTL)", created.ExpiresAt.Time, want)
		}
	})

	t.Run("caller-supplied expiresAt wins over the default", func(t *testing.T) {
		api := newTestApi(t)
		api.DefaultCommandTTL = 48 * time.Hour
		explicit := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
		exp := explicit.Format(time.RFC3339)
		created, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "cmd-explicit", DeviceToken: "d1", Name: "reboot", ExpiresAt: &exp})
		if err != nil {
			t.Fatalf("CreateCommand failed: %v", err)
		}
		if !created.ExpiresAt.Valid || !created.ExpiresAt.Time.Equal(explicit) {
			t.Fatalf("expires_at = %v (valid=%v), want the caller's %v", created.ExpiresAt.Time, created.ExpiresAt.Valid, explicit)
		}
	})

	t.Run("zero default disables stamping", func(t *testing.T) {
		api := newTestApi(t) // DefaultCommandTTL left at 0
		created, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "cmd-nottl", DeviceToken: "d1", Name: "reboot"})
		if err != nil {
			t.Fatalf("CreateCommand failed: %v", err)
		}
		if created.ExpiresAt.Valid {
			t.Fatalf("expected NO stamp with a zero default, got %v", created.ExpiresAt.Time)
		}
	})
}

// TestCreateCommandIdempotentOnToken proves a repeat createCommand with a token that already names a
// live command returns the ORIGINAL command (not a second row, not an error) — the safe-retry
// property the REACT dispatcher's at-least-once redelivery depends on.
func TestCreateCommandIdempotentOnToken(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	first, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "cmd-dup", DeviceToken: "device-1", Name: "reboot"})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	// A second create with the same token but a DIFFERENT body must not mutate or duplicate — the
	// token is the identity, first write wins.
	second, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "cmd-dup", DeviceToken: "device-9", Name: "shutdown"})
	if err != nil {
		t.Fatalf("idempotent replay must not error: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("replay returned a different command: first id=%d second id=%d", first.ID, second.ID)
	}
	if second.DeviceToken != "device-1" || second.Name != "reboot" {
		t.Fatalf("replay must return the ORIGINAL command unchanged, got device=%s name=%s", second.DeviceToken, second.Name)
	}
	// Exactly one row exists for the token.
	rows, err := api.CommandsByToken(ctx, []string{"cmd-dup"})
	if err != nil {
		t.Fatalf("CommandsByToken: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 command for the token, got %d", len(rows))
	}

	// The same token in a DIFFERENT tenant is a distinct command (per-tenant uniqueness).
	other, err := api.CreateCommand(core.WithTenant(context.Background(), "B"), &CommandCreateRequest{Token: "cmd-dup", DeviceToken: "device-1", Name: "reboot"})
	if err != nil {
		t.Fatalf("same token other tenant: %v", err)
	}
	if other.ID == first.ID {
		t.Fatalf("a token must not collide across tenants")
	}
}

// TestMarkSentNoOpOnTerminal verifies MarkSent is a no-op on a command that has
// already left QUEUED, leaving it unchanged rather than forcing it to SENT. This is
// the from-state-predicated guard: MarkSent only advances a still-QUEUED row.
func TestMarkSentNoOpOnTerminal(t *testing.T) {
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

	// MarkSent must NOT error and must NOT drag the terminal command to SENT.
	got, err := api.MarkSent(ctx, created.ID)
	if err != nil {
		t.Fatalf("MarkSent on a terminal command should be a no-op, got error: %v", err)
	}
	if got.Status != CommandExpired.String() {
		t.Fatalf("MarkSent clobbered a terminal command: status=%s, want %s", got.Status, CommandExpired)
	}
}

// TestMarkSentDoesNotClobberResponse is the regression test for the lost-update
// race: a device response can drive a command to SUCCESSFUL before the sweep's
// MarkSent write lands (the sweep publishes BEFORE marking SENT). MarkSent must not
// revert that SUCCESSFUL back to SENT and wipe the response. Because MarkSent is now
// a conditional UPDATE ... WHERE status='QUEUED', a MarkSent that runs after the row
// has advanced to SUCCESSFUL matches zero rows and leaves it intact — which is
// exactly what a full-row Save of a stale QUEUED snapshot would NOT do.
func TestMarkSentDoesNotClobberResponse(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	created, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token:       "cmd-race",
		DeviceToken: "device-1",
		Name:        "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand failed: %v", err)
	}

	// The response beats the SENT write (the row goes QUEUED -> SUCCESSFUL).
	payload := `{"ok":true}`
	if _, err := api.MarkResponse(ctx, "cmd-race", true, &payload, nil); err != nil {
		t.Fatalf("MarkResponse failed: %v", err)
	}

	// The now-late MarkSent must be a no-op — not a revert to SENT.
	got, err := api.MarkSent(ctx, created.ID)
	if err != nil {
		t.Fatalf("MarkSent failed: %v", err)
	}
	if got.Status != CommandSuccessful.String() {
		t.Fatalf("MarkSent clobbered a completed response: status=%s, want SUCCESSFUL", got.Status)
	}
	if !got.RespondedTime.Valid {
		t.Fatalf("MarkSent wiped RespondedTime on a completed command")
	}
	if got.ResponsePayload == nil {
		t.Fatalf("MarkSent wiped ResponsePayload on a completed command")
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

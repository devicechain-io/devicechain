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
		CommandQueued, CommandHeld, CommandSent,
		CommandSuccessful, CommandTimeout, CommandExpired, CommandCancelled, CommandFailed,
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
		CommandQueued: false,
		// HELD is NOT terminal. A held command is one the platform is deliberately
		// withholding because the device is absent; it is still going to be
		// delivered when the device returns. Marking it terminal would freeze an
		// entire offline fleet's backlog permanently.
		CommandHeld:       false,
		CommandSent:       false,
		CommandSuccessful: true,
		CommandTimeout:    true,
		CommandExpired:    true,
		// CANCELLED IS terminal. A cancelled command must never be resurrected by
		// the sweep, marked sent, or driven by a late device response.
		CommandCancelled: true,
		CommandFailed:    true,
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
	// CommandBatch is migrated here even though most tests in this file never touch a
	// batch: ReleaseClaim reads it, to decide whether a failed publish returns its
	// command to the queue or retires it because the batch was called off. Production
	// always has both tables, so a fixture with only one measures a schema that does not
	// exist — and the symptom is an unrelated release test failing on "no such table".
	if err := db.AutoMigrate(&Command{}, &CommandBatch{}); err != nil {
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

	claimed, err := api.MarkSent(ctx, created.ID)
	if err != nil {
		t.Fatalf("MarkSent failed: %v", err)
	}
	if !claimed {
		t.Fatal("MarkSent did not claim a QUEUED command")
	}
	sent := loadOrFail(t, api, ctx, created.ID)
	if sent.Status != CommandSent.String() || !sent.SentTime.Valid {
		t.Fatalf("expected SENT with SentTime set, got %s valid=%v", sent.Status, sent.SentTime.Valid)
	}

	// 🔑 The claim is EXCLUSIVE, which is what makes claim-before-publish safe: a second
	// caller must be told it lost rather than told the row's status, because a status read
	// back cannot distinguish "I claimed it" from "someone else just did".
	if again, err := api.MarkSent(ctx, created.ID); err != nil {
		t.Fatalf("second MarkSent errored: %v", err)
	} else if again {
		t.Fatal("MarkSent claimed the same command twice; two dispatchers would both actuate the device")
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
// already left the dispatchable set, leaving it unchanged rather than forcing it to
// SENT. This is the from-state-predicated guard: MarkSent only advances a row that is
// still QUEUED or HELD.
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

	// Cancel moves the command to CANCELLED (terminal).
	if _, err := api.CancelCommand(ctx, "cmd-2"); err != nil {
		t.Fatalf("CancelCommand failed: %v", err)
	}

	// MarkSent must NOT error, must report that it did not claim, and must NOT drag the
	// terminal command to SENT.
	claimed, err := api.MarkSent(ctx, created.ID)
	if err != nil {
		t.Fatalf("MarkSent on a terminal command should be a no-op, got error: %v", err)
	}
	if claimed {
		t.Fatal("MarkSent claimed a CANCELLED command")
	}
	got := loadOrFail(t, api, ctx, created.ID)
	if got.Status != CommandCancelled.String() {
		t.Fatalf("MarkSent clobbered a terminal command: status=%s, want %s", got.Status, CommandCancelled)
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
	claimed, err := api.MarkSent(ctx, created.ID)
	if err != nil {
		t.Fatalf("MarkSent failed: %v", err)
	}
	if claimed {
		t.Fatal("MarkSent claimed a command the device had already answered")
	}
	got := loadOrFail(t, api, ctx, created.ID)
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
	// HELD + expired -> EXPIRED, NOT TIMEOUT. This is the case the vocabulary
	// exists for: the device was absent for the command's whole life, so the
	// platform never attempted delivery. Reporting it as TIMEOUT — which is what
	// a single "in flight" state forces — blames the device for a delivery that
	// was never made, and for a fleet switched off overnight that is the common
	// case rather than an edge one.
	heldOld, err := api.CreateCommand(sysctx, &CommandCreateRequest{
		Token: "h-old", DeviceToken: "d", Name: "x", ExpiresAt: &past,
	})
	if err != nil {
		t.Fatalf("create h-old failed: %v", err)
	}
	if err := forceStatus(api, sysctx, heldOld.ID, CommandHeld); err != nil {
		t.Fatalf("hold h-old failed: %v", err)
	}
	// QUEUED but not yet expired -> untouched.
	if _, err := api.CreateCommand(sysctx, &CommandCreateRequest{
		Token: "q-fresh", DeviceToken: "d", Name: "x", ExpiresAt: &future,
	}); err != nil {
		t.Fatalf("create q-fresh failed: %v", err)
	}

	count, byFromStatus, err := api.ExpireStale(sysctx, time.Now())
	if err != nil {
		t.Fatalf("ExpireStale failed: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected 3 commands expired, got %d", count)
	}

	assertStatus(t, api, sysctx, "q-old", CommandExpired)
	assertStatus(t, api, sysctx, "s-old", CommandTimeout)
	assertStatus(t, api, sysctx, "h-old", CommandExpired)
	assertStatus(t, api, sysctx, "q-fresh", CommandQueued)

	// The breakdown is keyed by the state each command lapsed FROM, and it is the
	// only thing that separates "the fleet was absent" (HELD) from "devices were
	// reached and stayed silent" (SENT). Asserting only the total would pass even
	// if every row were attributed to the wrong state.
	want := map[string]int64{
		CommandQueued.String(): 1,
		CommandHeld.String():   1,
		CommandSent.String():   1,
	}
	if len(byFromStatus) != len(want) {
		t.Fatalf("expiry breakdown = %v, want %v", byFromStatus, want)
	}
	for status, n := range want {
		if byFromStatus[status] != n {
			t.Fatalf("expiry breakdown[%s] = %d, want %d (full: %v)", status, byFromStatus[status], n, byFromStatus)
		}
	}
}

// forceStatus drives a command into a state no public transition reaches yet.
//
// HELD is written by the presence gate, which is a later slice; until it lands
// nothing in this service produces a held row, so a test that needs one has to
// place it directly. Writing it through the API instead would be measuring a
// transition that does not exist and would silently start measuring the wrong
// thing on the day it does.
func forceStatus(api *Api, ctx context.Context, id uint, status CommandStatus) error {
	return api.RDB.DB(ctx).Model(&Command{}).
		Where("id = ?", id).Update("status", status.String()).Error
}

// seedWithStatus creates a command and drives it into the requested state, returning
// its id. It is THE way this package's tests place a row in a given status.
//
// A fresh command is already QUEUED, so that case is a plain create; every other state
// goes through forceStatus for the reason above — nothing in this service produces a
// HELD row yet, and reaching one "properly" through the public API would be measuring a
// transition that does not exist.
func seedWithStatus(t *testing.T, api *Api, ctx context.Context, token string, status CommandStatus) uint {
	t.Helper()
	created, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: token, DeviceToken: "d", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("seeding %s failed: %v", token, err)
	}
	if status != CommandQueued {
		if err := forceStatus(api, ctx, created.ID, status); err != nil {
			t.Fatalf("forcing %s to %s failed: %v", token, status, err)
		}
	}
	return created.ID
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

// loadOrFail reads a command back by id, failing the test if it cannot.
//
// MarkSent used to return the row, which made it read like an accessor; it now returns
// whether THIS caller won the claim, because a status read back cannot distinguish "I
// claimed it" from "someone else just did". Tests that want the row read it explicitly.
func loadOrFail(t *testing.T, api *Api, ctx context.Context, id uint) *Command {
	t.Helper()
	got, err := api.loadCommand(ctx, id)
	if err != nil {
		t.Fatalf("could not load command %d: %v", id, err)
	}
	return got
}

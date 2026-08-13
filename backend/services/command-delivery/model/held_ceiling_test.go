// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/config"
	"github.com/devicechain-io/dc-microservice/core"
)

// seedHeld creates a command and drives it straight to HELD.
//
// 🔴 The hold is written DIRECTLY, via forceStatus, because nothing in this service
// produces a held row yet — the presence gate that withholds a command for an absent
// device is a later slice. A test that tried to reach the ceiling "properly", through
// the public API, would be counting rows that never become HELD and would therefore
// pass no matter what the ceiling did.
func seedHeld(t *testing.T, api *Api, ctx context.Context, token string) uint {
	t.Helper()
	created, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: token, DeviceToken: "d", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("seeding %s failed: %v", token, err)
	}
	if err := forceStatus(api, ctx, created.ID, CommandHeld); err != nil {
		t.Fatalf("holding %s failed: %v", token, err)
	}
	return created.ID
}

// asRejection asserts the error is a typed enqueue rejection and returns it. A plain
// error here would mean the ceiling reported "we could not decide", which is the
// channel reserved for a failure to ANSWER — a client told that would go looking for
// an outage instead of for their own backlog.
func asRejection(t *testing.T, err error) *EnqueueRejected {
	t.Helper()
	if err == nil {
		t.Fatal("expected the enqueue to be refused, got no error")
	}
	var rejection *EnqueueRejected
	if !errors.As(err, &rejection) {
		t.Fatalf("expected a TYPED rejection the caller can classify, got a plain error: %v", err)
	}
	return rejection
}

// TestReplayAtTheCeilingReturnsTheOriginalCommand is THE guard on CreateCommand's
// ordering, and it is the defect most likely to be reintroduced.
//
// A replay creates NO ROW. REACT's send-command derives a deterministic token per
// (detection, action) precisely so its at-least-once redelivery collapses into the
// original command, which makes replays a normal path rather than an edge case. If
// the ceiling — or the enqueue gate, or any other check — is consulted before the
// token is looked up, then every REACT retry for a tenant sitting at its ceiling is
// refused for a reason that is not true of it: the retry would have added nothing to
// the backlog it is being blamed for. The natural implementation (count, then insert)
// has exactly this bug, and it is invisible until a tenant is full.
//
// The assertion is on the ORIGINAL row coming back, not merely on the absence of an
// error: an implementation that re-enqueued a second command under a fresh id would
// also "succeed" here, and it would double-actuate real hardware.
func TestReplayAtTheCeilingReturnsTheOriginalCommand(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	originalID := seedHeld(t, api, ctx, "replay-me")
	seedHeld(t, api, ctx, "filler")

	// The tenant is now AT its ceiling, and the gate is one that refuses everything.
	// Both must be bypassed by a replay: it decides nothing, so it may consult nothing.
	api.DefaultHeldCommandCeiling = 2
	gate := &stubValidator{err: &EnqueueRejected{
		Code: "DEVICE_NOT_FOUND", Reason: `device "d" does not exist`,
	}}
	api.EnqueueValidator = gate

	replayed, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "replay-me", DeviceToken: "d", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("a replay of an existing token must succeed even at the ceiling — it creates no row: %v", err)
	}
	if replayed.ID != originalID {
		t.Fatalf("a replay must return the ORIGINAL command (id %d), got id %d; "+
			"a second row means the same detection actuated the device twice", originalID, replayed.ID)
	}
	if replayed.Status != CommandHeld.String() {
		t.Fatalf("a replay must return the original row UNCHANGED (HELD), got %s", replayed.Status)
	}
	if gate.callCount != 0 {
		t.Fatalf("a replay must not consult the enqueue gate (%d call(s)); it is a read of a row that "+
			"already passed the gate, and re-asking spends a cross-service round trip per retry", gate.callCount)
	}

	// The counterweight: the ceiling is only proven bypassed for a REPLAY if a genuinely
	// NEW token is still refused at the same moment. Without this, a ceiling that never
	// fires at all would pass the assertions above.
	//
	// The refuse-everything gate is removed first, deliberately. It sits in FRONT of the
	// ceiling on the non-replay path, so leaving it wired would have this assertion
	// measure the gate's rejection and never reach the ceiling at all — a counterweight
	// that proves the thing it was not aimed at.
	api.EnqueueValidator = nil
	_, err = api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "brand-new", DeviceToken: "d", Name: "reboot",
	})
	rejection := asRejection(t, err)
	if rejection.Code != RejectHeldCeilingExceeded {
		t.Fatalf("a NEW enqueue at the ceiling must be refused with %s, got %s (%s)",
			RejectHeldCeilingExceeded, rejection.Code, rejection.Reason)
	}
}

// TestHeldCeilingRefusesANewEnqueueAndPersistsNothing pins the ceiling itself: the
// refusal is a typed rejection carrying the ceiling code, and no row is written.
//
// A refusal that still persisted the command would be the worst of both outcomes —
// the caller is told no, the backlog grows anyway, and the ceiling ratchets itself
// further out of reach on every attempt.
func TestHeldCeilingRefusesANewEnqueueAndPersistsNothing(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	seedHeld(t, api, ctx, "held-1")
	seedHeld(t, api, ctx, "held-2")
	api.DefaultHeldCommandCeiling = 2

	_, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "over-the-line", DeviceToken: "d", Name: "reboot",
	})
	rejection := asRejection(t, err)
	if rejection.Code != RejectHeldCeilingExceeded {
		t.Fatalf("expected %s, got %s", RejectHeldCeilingExceeded, rejection.Code)
	}
	if got, _ := api.CommandsByToken(ctx, []string{"over-the-line"}); len(got) != 0 {
		t.Fatalf("a refused enqueue must persist nothing, found %+v", got)
	}

	// One below the ceiling still admits — the bound is a bound, not a stop.
	api.DefaultHeldCommandCeiling = 3
	if _, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "under-the-line", DeviceToken: "d", Name: "reboot",
	}); err != nil {
		t.Fatalf("an enqueue below the ceiling must be admitted: %v", err)
	}
}

// TestOnlyHeldRowsCountTowardTheCeiling is why HELD is a state of its own.
//
// The ceiling bounds the backlog withheld for ABSENT devices. QUEUED is transient
// (every row leaves it at the next sweep tick), SENT is in flight, and the terminals
// are history. Counting any of them would throttle a BUSY, HEALTHY tenant — one whose
// commands are being delivered as fast as they are issued — for a backlog it does not
// have, and the throttle would arrive precisely when the fleet is working.
func TestOnlyHeldRowsCountTowardTheCeiling(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	// Well over any ceiling used below, and not one of them is a hold.
	for _, seed := range []struct {
		token  string
		status CommandStatus
	}{
		{"c-queued-1", CommandQueued},
		{"c-queued-2", CommandQueued},
		{"c-sent-1", CommandSent},
		{"c-sent-2", CommandSent},
		{"c-successful", CommandSuccessful},
		{"c-failed", CommandFailed},
		{"c-expired", CommandExpired},
		{"c-cancelled", CommandCancelled},
		{"c-timeout", CommandTimeout},
	} {
		created, err := api.CreateCommand(ctx, &CommandCreateRequest{
			Token: seed.token, DeviceToken: "d", Name: "reboot",
		})
		if err != nil {
			t.Fatalf("seeding %s failed: %v", seed.token, err)
		}
		if seed.status != CommandQueued {
			if err := forceStatus(api, ctx, created.ID, seed.status); err != nil {
				t.Fatalf("forcing %s failed: %v", seed.token, err)
			}
		}
	}

	api.DefaultHeldCommandCeiling = 2
	if _, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "healthy-tenant", DeviceToken: "d", Name: "reboot",
	}); err != nil {
		t.Fatalf("nine non-held rows must not count toward a held-command ceiling of 2: %v", err)
	}
}

// TestHeldCeilingIsPerTenant verifies one tenant's backlog cannot refuse another's
// enqueue. The count rides the tenant-scope callback rather than a hand-written
// predicate, and this is what proves the scope is actually applied — a ceiling that
// counted instance-wide would make the noisiest tenant everyone else's ceiling.
func TestHeldCeilingIsPerTenant(t *testing.T) {
	api := newTestApi(t)
	tenantA := core.WithTenant(context.Background(), "A")
	tenantB := core.WithTenant(context.Background(), "B")

	seedHeld(t, api, tenantA, "a-held-1")
	seedHeld(t, api, tenantA, "a-held-2")
	api.DefaultHeldCommandCeiling = 2

	if _, err := api.CreateCommand(tenantB, &CommandCreateRequest{
		Token: "b-fresh", DeviceToken: "d", Name: "reboot",
	}); err != nil {
		t.Fatalf("tenant B holds nothing; tenant A's backlog must not refuse its enqueue: %v", err)
	}
}

// stubCeilingResolver is a canned HeldCommandCeilingResolver.
type stubCeilingResolver struct{ value int }

func (s stubCeilingResolver) Resolve(string) int { return s.value }

// TestHeldCommandCeilingNeverResolvesToUnlimited walks every way the ceiling can fail
// to be supplied and asserts each one lands on a real number.
//
// 🔴 This is the assertion that keeps the bound a bound. A missing resolver, an
// unresolved tenant, a zero from a resolver or from the config: every one of them is
// the harmless-looking case, and every one of them must mean the PLATFORM DEFAULT. A
// governance ceiling whose absent value reads as "unlimited" stops governing exactly
// when the authority carrying it is unreachable — which is the moment it was built for.
func TestHeldCommandCeilingNeverResolvesToUnlimited(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "A")

	cases := []struct {
		name     string
		resolver HeldCommandCeilingResolver
		fallback int
		want     int
	}{
		{"no resolver, no configured default", nil, 0, config.DefaultHeldCommandCeiling},
		{"no resolver, a configured default", nil, 500, 500},
		{"resolver answers zero", stubCeilingResolver{value: 0}, 500, 500},
		{"resolver answers zero and nothing is configured", stubCeilingResolver{}, 0, config.DefaultHeldCommandCeiling},
		{"resolver answers a negative ceiling", stubCeilingResolver{value: -1}, 500, 500},
		{"resolver answers a real ceiling", stubCeilingResolver{value: 42}, 500, 42},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &Api{HeldCeilingResolver: tc.resolver, DefaultHeldCommandCeiling: tc.fallback}
			if got := api.heldCommandCeiling(ctx); got != tc.want {
				t.Fatalf("heldCommandCeiling() = %d, want %d", got, tc.want)
			}
		})
	}

	// And a context carrying no tenant at all still resolves to a number rather than
	// consulting a resolver it cannot key.
	api := &Api{HeldCeilingResolver: stubCeilingResolver{value: 42}}
	if got := api.heldCommandCeiling(context.Background()); got != config.DefaultHeldCommandCeiling {
		t.Fatalf("with no tenant in context the ceiling must fall back to the platform default, got %d", got)
	}
}

// TestRejectionCodesAreDistinct guards the code set itself: a copy-pasted duplicate
// would silently merge two rejections into one, and a caller branching on the shared
// value would treat a temporary refusal (the ceiling) as a permanent one — dropping
// the commands an offline fleet is waiting for.
func TestRejectionCodesAreDistinct(t *testing.T) {
	codes := []RejectionCode{
		RejectPayloadNotJSON, RejectMetadataNotJSON, RejectExpiresAtInvalid,
		RejectHeldCeilingExceeded, RejectUnclassified,
	}
	seen := make(map[RejectionCode]bool, len(codes))
	for _, c := range codes {
		if c == "" {
			t.Fatal("a rejection code must never be empty; empty is the unclassified sentinel")
		}
		if seen[c] {
			t.Fatalf("duplicate rejection code %q", c)
		}
		seen[c] = true
	}
}

// TestUncodedRejectionIsMarkedUnclassified covers the peer that refuses a command
// without saying which kind of refusal it is.
//
// It stays a rejection — the verdict was reached — but it must be marked as
// unclassified rather than being given a code this service guessed at. The guess is
// the dangerous half: a caller branching on an invented DEVICE_NOT_FOUND would DROP a
// command permanently on the strength of a claim nobody made. Unclassified falls back
// to retry, which is the previous behaviour and the recoverable one.
func TestUncodedRejectionIsMarkedUnclassified(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	api.EnqueueValidator = &stubValidator{err: &EnqueueRejected{Reason: "no reason given"}}

	_, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "uncoded", DeviceToken: "d", Name: "reboot",
	})
	rejection := asRejection(t, err)
	if rejection.Code != RejectUnclassified {
		t.Fatalf("an uncoded rejection must be marked %s, got %q", RejectUnclassified, rejection.Code)
	}
}

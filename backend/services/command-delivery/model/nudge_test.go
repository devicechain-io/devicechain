// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// recordingNudger records every nudge verbatim.
//
// 🔑 IT RECORDS THE TENANT AS WELL AS THE DEVICE. The nudge's far side re-reads under that
// tenant, and a device token is unique per tenant rather than per instance — so a recorder
// that kept only the device would score identically against a nudge carrying the wrong
// tenant, the empty string, or another tenant's name.
type recordingNudger struct {
	mu     sync.Mutex
	nudges []nudged
}

type nudged struct {
	tenant      string
	deviceToken string
}

func (n *recordingNudger) NudgeDevice(tenant, deviceToken string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.nudges = append(n.nudges, nudged{tenant: tenant, deviceToken: deviceToken})
}

func (n *recordingNudger) seen() []nudged {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]nudged(nil), n.nudges...)
}

// seedQueuedFor creates n QUEUED commands for one named device, in enqueue order.
func seedQueuedFor(t *testing.T, api *Api, ctx context.Context, device string, n int) []uint {
	t.Helper()
	ids := make([]uint, 0, n)
	for i := 0; i < n; i++ {
		cmd := &Command{DeviceToken: device, Name: "reboot", Status: CommandQueued.String()}
		cmd.Token = fmt.Sprintf("%s-%04d", device, i)
		if err := api.RDB.DB(ctx).Create(cmd).Error; err != nil {
			t.Fatalf("seed command %d: %v", i, err)
		}
		ids = append(ids, cmd.ID)
	}
	return ids
}

// QueuedCommandsForDevice is the nudge's read, and every one of its four narrowings is
// load-bearing.
func TestQueuedCommandsForDevice(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")

	// Oldest-first is a DELIVERY GUARANTEE, not a display choice — the same reason
	// DrainableCommands carries it. A firmware update is a sequence whose order is its
	// meaning, so a nudge acting on anything but the front of the backlog would step over
	// an older command.
	t.Run("returns the device's queued commands oldest first", func(t *testing.T) {
		api := newTestApi(t)
		ids := seedQueuedFor(t, api, ctx, "dev-1", 5)

		found, err := api.QueuedCommandsForDevice(ctx, "dev-1", 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(found) != 5 {
			t.Fatalf("expected 5 rows, got %d", len(found))
		}
		for i, cmd := range found {
			if cmd.ID != ids[i] {
				t.Fatalf("position %d: expected command %d (enqueue order), got %d", i, ids[i], cmd.ID)
			}
		}
	})

	// The limit is what makes the sole-queued-command rule expressible: the caller asks for
	// two and learns "exactly one" or "more than one" from the row count.
	t.Run("honours the limit", func(t *testing.T) {
		api := newTestApi(t)
		seedQueuedFor(t, api, ctx, "dev-1", 10)

		found, err := api.QueuedCommandsForDevice(ctx, "dev-1", NudgeProbeLimit)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(found) != NudgeProbeLimit {
			t.Fatalf("expected the read to stop at %d rows, got %d", NudgeProbeLimit, len(found))
		}
	})

	// 🔴 THE PROBE LIMIT MUST BE ABLE TO SEE A SECOND ROW. At 1 the read would answer
	// "exactly one queued command" for a device with a hundred of them, and the nudge would
	// dispatch into the middle of a backlog — the exact reordering the stand-down exists to
	// refuse. This is a property of the CONSTANT, so it is asserted rather than assumed.
	t.Run("the probe limit can distinguish one from many", func(t *testing.T) {
		api := newTestApi(t)
		seedQueuedFor(t, api, ctx, "dev-1", 2)

		found, err := api.QueuedCommandsForDevice(ctx, "dev-1", NudgeProbeLimit)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(found) < 2 {
			t.Fatalf("a device with two queued commands must read back as more than one; got %d "+
				"rows at NudgeProbeLimit=%d", len(found), NudgeProbeLimit)
		}
	})

	// Another device's backlog must not make this device look busy — nor supply it a row.
	t.Run("sees only the named device", func(t *testing.T) {
		api := newTestApi(t)
		seedQueuedFor(t, api, ctx, "dev-1", 1)
		seedQueuedFor(t, api, ctx, "dev-2", 4)

		found, err := api.QueuedCommandsForDevice(ctx, "dev-1", 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(found) != 1 {
			t.Fatalf("expected only dev-1's single queued command, got %d rows", len(found))
		}
		if found[0].DeviceToken != "dev-1" {
			t.Fatalf("got another device's command: %s", found[0].DeviceToken)
		}
	})

	// The status narrowing is shared with the sweep (sweepableStatusStrings), so a row the
	// sweep does not own must not appear here either. HELD in particular belongs to the
	// presence gate and the wake, and a nudge that dispatched one would be releasing a hold
	// the gate placed deliberately.
	t.Run("selects QUEUED only", func(t *testing.T) {
		api := newTestApi(t)
		seedQueuedFor(t, api, ctx, "dev-1", 1)
		for _, status := range []CommandStatus{CommandHeld, CommandSent, CommandParked, CommandSuccessful} {
			cmd := &Command{DeviceToken: "dev-1", Name: "reboot", Status: status.String()}
			cmd.Token = "other-" + status.String()
			if err := api.RDB.DB(ctx).Create(cmd).Error; err != nil {
				t.Fatalf("seed %s: %v", status, err)
			}
		}

		found, err := api.QueuedCommandsForDevice(ctx, "dev-1", 50)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(found) != 1 {
			t.Fatalf("expected the one QUEUED row, got %d: %v", len(found), statusesOf(found))
		}
	})

	// The tenant fence is the scope callback's, not this query's, and it must still be in
	// force — a device token is unique per TENANT, so without it one tenant's nudge would
	// read another's rows for a device that happens to share a token.
	t.Run("is fenced to the caller's tenant", func(t *testing.T) {
		api := newTestApi(t)
		seedQueuedFor(t, api, core.WithTenant(context.Background(), "other"), "dev-1", 3)
		seedQueuedFor(t, api, ctx, "dev-1", 1)

		found, err := api.QueuedCommandsForDevice(ctx, "dev-1", 50)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(found) != 1 {
			t.Fatalf("expected only this tenant's queued command, got %d rows", len(found))
		}
		if found[0].TenantId != "acme" {
			t.Fatalf("read another tenant's row: %s", found[0].TenantId)
		}
	})

	// Fail-closed, like every tenant-scoped read: no tenant, no rows.
	t.Run("refuses a read with no tenant", func(t *testing.T) {
		api := newTestApi(t)
		seedQueuedFor(t, api, ctx, "dev-1", 1)

		if _, err := api.QueuedCommandsForDevice(context.Background(), "dev-1", 50); err == nil {
			t.Fatal("a tenant-scoped read with no tenant in context must fail closed")
		}
	})
}

func statusesOf(cmds []*Command) []string {
	out := make([]string, 0, len(cmds))
	for _, cmd := range cmds {
		out = append(out, cmd.Status)
	}
	return out
}

// Creating a command nudges its device exactly once, with the tenant the create ran under.
func TestCreateCommandNudgesItsDevice(t *testing.T) {
	api := newTestApi(t)
	nudger := &recordingNudger{}
	api.Nudger = nudger
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "cmd-1", DeviceToken: "dev-1", Name: "reboot",
	}); err != nil {
		t.Fatalf("CreateCommand failed: %v", err)
	}

	seen := nudger.seen()
	if len(seen) != 1 {
		t.Fatalf("expected exactly one nudge, got %d: %v", len(seen), seen)
	}
	if seen[0] != (nudged{tenant: "acme", deviceToken: "dev-1"}) {
		t.Fatalf("the nudge must name the created command's tenant and device, got %+v", seen[0])
	}
}

// 🔴 A REPLAY CREATES NO ROW, SO IT NUDGES NOTHING. REACT's send-command derives a
// deterministic token per (detection, action) precisely so its at-least-once redelivery
// collapses onto the existing row — which makes a replay a NORMAL path, not an edge case.
// Nudging on one would put a single command's dispatch behind an unbounded number of
// duplicate probes for as long as the retries continue, and every one of those probes would
// re-read a backlog nothing had changed.
func TestAReplayedCreateIssuesNoFurtherNudge(t *testing.T) {
	api := newTestApi(t)
	nudger := &recordingNudger{}
	api.Nudger = nudger
	ctx := core.WithTenant(context.Background(), "acme")
	request := &CommandCreateRequest{Token: "cmd-1", DeviceToken: "dev-1", Name: "reboot"}

	first, err := api.CreateCommand(ctx, request)
	if err != nil {
		t.Fatalf("first CreateCommand failed: %v", err)
	}
	again, err := api.CreateCommand(ctx, request)
	if err != nil {
		t.Fatalf("replayed CreateCommand failed: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("the replay must answer with the original row, got %d want %d", again.ID, first.ID)
	}

	if seen := nudger.seen(); len(seen) != 1 {
		t.Fatalf("a replay creates no row and must issue no nudge; got %d nudges: %v", len(seen), seen)
	}
}

// A refused create must not nudge either: there is no row for the far side to find, so
// every such nudge is a wasted read issued at exactly the rate a misbehaving client
// retries.
func TestARefusedCreateIssuesNoNudge(t *testing.T) {
	api := newTestApi(t)
	nudger := &recordingNudger{}
	api.Nudger = nudger
	ctx := core.WithTenant(context.Background(), "acme")

	bad := `{"not":`
	if _, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "cmd-1", DeviceToken: "dev-1", Name: "reboot", Payload: &bad,
	}); err == nil {
		t.Fatal("expected a malformed payload to be rejected")
	}

	if seen := nudger.seen(); len(seen) != 0 {
		t.Fatalf("a rejected create must issue no nudge, got %v", seen)
	}
}

// The nudge is OFF when no nudger is wired, and off must be silent rather than fatal. This
// is the configuration every test in this package and every pre-nudge deployment runs in.
func TestCreateCommandWorksWithNoNudgerWired(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	created, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "cmd-1", DeviceToken: "dev-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand with no nudger failed: %v", err)
	}
	if created.Status != CommandQueued.String() {
		t.Fatalf("expected QUEUED, got %s", created.Status)
	}
}

// B3: a fleet write issues NO nudges, and it does so BY CONSTRUCTION — the batch path
// inserts through CreateInBatches and never touches CreateCommand.
//
// 🔴 THE FAILURE THIS PREVENTS IS A FAN-OUT, NOT A WRONG ANSWER. A batch routed through
// CreateCommand "for consistency" would emit one nudge per admitted device — fifty thousand
// from a single API call — each a per-device read that would then stand down or dispatch a
// command the sweep was about to dispatch anyway. Nothing would look broken; the instance
// would simply spend a burst of database reads on nothing. This test is the standing record
// that the property still holds.
func TestABatchCreateIssuesNoNudges(t *testing.T) {
	api := newBatchTestApi(t)
	nudger := &recordingNudger{}
	api.Nudger = nudger
	ctx := core.WithTenant(context.Background(), "acme")

	batch, err := api.CreateCommandBatch(ctx, batchRequest("nightly", deviceTokens(25)))
	if err != nil {
		t.Fatalf("CreateCommandBatch failed: %v", err)
	}
	if batch.Accepted != 25 {
		t.Fatalf("expected 25 commands to be created, got %d — a batch that created nothing "+
			"would issue no nudges for the wrong reason", batch.Accepted)
	}

	if seen := nudger.seen(); len(seen) != 0 {
		t.Fatalf("a fleet write must issue no dispatch nudges, got %d: %v", len(seen), seen)
	}
}

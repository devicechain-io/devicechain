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
)

// THE WIRING TEST FOR THE MAINTENANCE-WINDOW GATE.
//
// The gate is a column the claim reads, so what has to be shown is that EVERY road that
// dispatches a command ends in a claim that refuses a held row — not that one helper works.
// There are three roads and a gate on any one of them would be walked around by the others:
//
//  1. the delivery sweep:   PendingCommands (select) -> MarkSent (claim)
//  2. the LwM2M wake drain: DrainableCommands (select) -> MarkSentByToken (claim)
//  3. the live confirm:     ConfirmDispatch (rotation of a row already SENT)
//
// Each test seeds a row with a hold in the FUTURE, proves the road refuses it, then moves
// the hold into the PAST and proves the same road now takes it — so a road that refuses
// everything cannot pass.

var (
	inAnHour  = func() sql.NullTime { return sql.NullTime{Time: time.Now().Add(time.Hour), Valid: true} }
	anHourAgo = func() sql.NullTime { return sql.NullTime{Time: time.Now().Add(-time.Hour), Valid: true} }
)

// seedHeld writes a command in the given status carrying a hold from window "mw".
func seedHoldRow(t *testing.T, api *Api, ctx context.Context, token, device string, status CommandStatus,
	after sql.NullTime) uint {
	t.Helper()
	cmd := &Command{
		DeviceToken:   device,
		Name:          "reboot",
		Status:        status.String(),
		DispatchAfter: after,
		WindowToken:   sql.NullString{String: "mw", Valid: true},
	}
	cmd.Token = token
	if err := api.RDB.DB(ctx).Create(cmd).Error; err != nil {
		t.Fatalf("seed %s: %v", token, err)
	}
	return cmd.ID
}

// release moves a seeded hold into the past, as the passage of time does.
func releaseHold(t *testing.T, api *Api, ctx context.Context, id uint) {
	t.Helper()
	if err := api.RDB.DB(ctx).Model(&Command{}).Where("id = ?", id).
		Update("dispatch_after", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatalf("release: %v", err)
	}
}

func TestTheSweepRoadHonoursADispatchHold(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	id := seedHoldRow(t, api, ctx, "held", "dev-1", CommandQueued, inAnHour())
	free := seedHoldRow(t, api, ctx, "free", "dev-2", CommandQueued, sql.NullTime{})

	pending, err := api.PendingCommands(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != free {
		t.Fatalf("the sweep selected %v; it must select only the row with no hold", idsOf(pending))
	}
	// The select is not the authority: the claim must refuse on its own.
	if _, claimed, err := api.MarkSent(ctx, id); err != nil || claimed {
		t.Fatalf("MarkSent claimed a held row: claimed=%v err=%v", claimed, err)
	}
	assertStatus(t, api, ctx, "held", CommandQueued)

	releaseHold(t, api, ctx, id)
	if pending, _ = api.PendingCommands(ctx); len(pending) != 2 {
		t.Fatalf("after the hold lapsed the sweep selected %d rows, want 2", len(pending))
	}
	if _, claimed, err := api.MarkSent(ctx, id); err != nil || !claimed {
		t.Fatalf("MarkSent refused a row whose hold lapsed: claimed=%v err=%v", claimed, err)
	}
}

func TestTheWakeDrainRoadHonoursADispatchHold(t *testing.T) {
	for _, status := range []CommandStatus{CommandHeld, CommandParked} {
		t.Run(status.String(), func(t *testing.T) {
			api := newTestApi(t)
			ctx := core.WithTenant(context.Background(), "A")
			id := seedHoldRow(t, api, ctx, "held", "dev-1", status, inAnHour())

			drained, err := api.DrainableCommands(ctx, "dev-1", 32)
			if err != nil {
				t.Fatal(err)
			}
			if len(drained) != 0 {
				t.Fatalf("the wake drain offered %v for a held row", tokensOf(drained))
			}
			// The drain's client claims by token; that claim is the authority.
			if _, claimed, err := api.MarkSentByToken(ctx, "held"); err != nil || claimed {
				t.Fatalf("MarkSentByToken claimed a held %s row: claimed=%v err=%v", status, claimed, err)
			}
			assertStatus(t, api, ctx, "held", status)

			releaseHold(t, api, ctx, id)
			if drained, _ = api.DrainableCommands(ctx, "dev-1", 32); len(drained) != 1 {
				t.Fatalf("after the hold lapsed the drain offered %d rows, want 1", len(drained))
			}
			if _, claimed, err := api.MarkSentByToken(ctx, "held"); err != nil || !claimed {
				t.Fatalf("MarkSentByToken refused a row whose hold lapsed: claimed=%v err=%v", claimed, err)
			}
		})
	}
}

func TestTheLiveConfirmRoadHonoursADispatchHold(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	_, nonce := sentWithNonce(t, api, ctx, "live")

	// A row can only reach SENT through a claim, which a hold refuses, so a SENT row with a
	// hold in the future is a state the claims cannot produce. It is forced here, because
	// the confirm must not depend on that: it is a road of its own, and a road that trusts
	// its neighbours to have checked is the bypass this design exists to prevent.
	if err := api.RDB.DB(ctx).Model(&Command{}).Where("token = ?", "live").
		Update("dispatch_after", time.Now().Add(time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if next, won, err := api.ConfirmDispatch(ctx, "live", nonce); err != nil || won || next != "" {
		t.Fatalf("ConfirmDispatch actuated a held row: next=%q won=%v err=%v", next, won, err)
	}

	if err := api.RDB.DB(ctx).Model(&Command{}).Where("token = ?", "live").
		Update("dispatch_after", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if _, won, err := api.ConfirmDispatch(ctx, "live", nonce); err != nil || !won {
		t.Fatalf("ConfirmDispatch refused a row whose hold lapsed: won=%v err=%v", won, err)
	}
}

func TestTheDispatchNudgeReadSkipsAHeldRow(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	seedHoldRow(t, api, ctx, "held", "dev-1", CommandQueued, inAnHour())
	got, err := api.QueuedCommandsForDevice(ctx, "dev-1", 2)
	if err != nil || len(got) != 0 {
		t.Fatalf("the nudge read %v (err %v) for a held row", tokensOf(got), err)
	}
}

func TestACommandWithoutAHoldIsNeverGated(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	id := seedHoldRow(t, api, ctx, "plain", "dev-1", CommandQueued, sql.NullTime{})
	if _, claimed, err := api.MarkSent(ctx, id); err != nil || !claimed {
		t.Fatalf("a NULL hold blocked a claim: claimed=%v err=%v", claimed, err)
	}
}

// ─── enqueue ──────────────────────────────────────────────────────────────

type fakeScheduler struct {
	calls    int
	holds    []DispatchHold
	refusals []BatchDeviceRefusal
	err      error
	lastExp  sql.NullTime
}

func (f *fakeScheduler) ScheduleDispatch(_ context.Context, _ []string, _ string,
	expiresAt sql.NullTime) ([]DispatchHold, []BatchDeviceRefusal, error) {
	f.calls++
	f.lastExp = expiresAt
	return f.holds, f.refusals, f.err
}

func TestEnqueueStampsTheHoldAndKeepsTheRowQueued(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	after := time.Now().Add(36 * time.Hour).Truncate(time.Second)
	api.DispatchScheduler = &fakeScheduler{holds: []DispatchHold{
		{DeviceToken: "dev-1", WindowToken: "plant-a", DispatchAfter: after}}}

	created, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "c1", DeviceToken: "dev-1", Name: "reboot"})
	if err != nil {
		t.Fatal(err)
	}
	row := commandRow(t, api, ctx, "c1")
	if row.Status != CommandQueued.String() {
		t.Fatalf("status = %s; a waiting command stays QUEUED", row.Status)
	}
	if !row.DispatchAfter.Valid || !row.DispatchAfter.Time.Equal(after) || row.WindowToken.String != "plant-a" {
		t.Fatalf("stamp = %v / %v, want %v / plant-a", row.DispatchAfter, row.WindowToken, after)
	}
	if created.ID != row.ID {
		t.Fatal("the returned command is not the stored one")
	}
	if _, claimed, _ := api.MarkSent(ctx, row.ID); claimed {
		t.Fatal("a freshly stamped command was claimable")
	}
}

// A gated command enqueued while its window is open is claimable at once, and still carries
// the window's close and token, so it cannot go out after the close.
func TestEnqueueInsideAnOpenWindowStampsBothBounds(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	opened := time.Now().Add(-time.Hour).Truncate(time.Second)
	closes := time.Now().Add(time.Hour).Truncate(time.Second)
	api.DispatchScheduler = &fakeScheduler{holds: []DispatchHold{
		{DeviceToken: "dev-1", WindowToken: "plant-a", DispatchAfter: opened, DispatchUntil: closes}}}
	if _, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "c1", DeviceToken: "dev-1", Name: "reboot"}); err != nil {
		t.Fatal(err)
	}
	row := commandRow(t, api, ctx, "c1")
	if !row.DispatchUntil.Valid || !row.DispatchUntil.Time.Equal(closes) || row.WindowToken.String != "plant-a" {
		t.Fatalf("stamp = [%v, %v) %v, want closing at %v under plant-a", row.DispatchAfter, row.DispatchUntil,
			row.WindowToken, closes)
	}
	if _, claimed, _ := api.MarkSent(ctx, row.ID); !claimed {
		t.Fatal("a command inside its open window was not claimable")
	}
}

func TestEnqueueHandsTheScheduleTheCommandsExpiry(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	sched := &fakeScheduler{}
	api.DispatchScheduler = sched
	exp := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	if _, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "c1", DeviceToken: "dev-1", Name: "reboot", ExpiresAt: &exp}); err != nil {
		t.Fatal(err)
	}
	if !sched.lastExp.Valid || sched.lastExp.Time.UTC().Format(time.RFC3339) != exp {
		t.Fatalf("the scheduler was asked about expiry %v, want %s", sched.lastExp, exp)
	}
}

func TestEnqueueRelaysAWindowRefusalAndCreatesNothing(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	api.DispatchScheduler = &fakeScheduler{refusals: []BatchDeviceRefusal{{
		DeviceToken: "dev-1", Code: "WINDOW_AFTER_EXPIRY", Reason: "the window opens after the command expires"}}}

	_, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "c1", DeviceToken: "dev-1", Name: "reboot"})
	var rej *EnqueueRejected
	if !errors.As(err, &rej) || rej.Code != "WINDOW_AFTER_EXPIRY" {
		t.Fatalf("err = %v, want a rejection coded WINDOW_AFTER_EXPIRY", err)
	}
	if got, _ := api.CommandsByToken(ctx, []string{"c1"}); len(got) != 0 {
		t.Fatal("a refused command was stored")
	}
}

func TestEnqueueFailsClosedWhenTheScheduleCannotBeDecided(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	api.DispatchScheduler = &fakeScheduler{err: errors.New("device-management is down")}

	_, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "c1", DeviceToken: "dev-1", Name: "reboot"})
	var rej *EnqueueRejected
	if err == nil || errors.As(err, &rej) {
		t.Fatalf("err = %v, want a plain availability error, not a coded rejection", err)
	}
	if got, _ := api.CommandsByToken(ctx, []string{"c1"}); len(got) != 0 {
		t.Fatal("a command whose window could not be checked was stored")
	}
}

func TestAReplayDoesNotAskTheScheduleAgain(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	sched := &fakeScheduler{}
	api.DispatchScheduler = sched
	req := &CommandCreateRequest{Token: "c1", DeviceToken: "dev-1", Name: "reboot"}
	if _, err := api.CreateCommand(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := api.CreateCommand(ctx, req); err != nil {
		t.Fatal(err)
	}
	if sched.calls != 1 {
		t.Fatalf("the scheduler was asked %d times for one command and its replay", sched.calls)
	}
}

func TestBatchMembersAreStampedAndCounted(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	api.BatchValidator = &countingValidator{}
	tokens := deviceTokens(4)
	after := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	api.DispatchScheduler = &fakeScheduler{holds: []DispatchHold{
		{DeviceToken: tokens[0], WindowToken: "plant-a", DispatchAfter: after, DispatchUntil: after.Add(time.Hour)},
		{DeviceToken: tokens[2], WindowToken: "plant-a", DispatchAfter: after, DispatchUntil: after.Add(time.Hour)},
		// Gated, but its window is open now: bound to it, not waiting.
		{DeviceToken: tokens[3], WindowToken: "plant-b", DispatchAfter: time.Now().Add(-time.Hour),
			DispatchUntil: time.Now().Add(time.Hour)},
	}}

	batch, err := api.CreateCommandBatch(ctx, batchRequest("b1", tokens))
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Waiting.Valid || batch.Waiting.Int32 != 2 {
		t.Fatalf("batch.Waiting = %v, want 2 of the 4 admitted", batch.Waiting)
	}
	held := 0
	rows, err := api.Commands(ctx, CommandSearchCriteria{BatchToken: &batch.Token})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rows.Results {
		if c.DispatchAfter.Valid && c.DispatchAfter.Time.After(time.Now()) {
			held++
			if c.WindowToken.String != "plant-a" || !c.DispatchAfter.Time.Equal(after) {
				t.Fatalf("member %s stamped %v / %v", c.DeviceToken, c.DispatchAfter, c.WindowToken)
			}
			if _, claimed, _ := api.MarkSent(ctx, c.ID); claimed {
				t.Fatalf("a held batch member (%s) was claimable", c.DeviceToken)
			}
		}
	}
	if held != 2 {
		t.Fatalf("%d members carry a future hold, want 2", held)
	}
	for _, c := range rows.Results {
		if c.DeviceToken == tokens[3] && (!c.DispatchUntil.Valid || c.WindowToken.String != "plant-b") {
			t.Fatalf("the open-window member was not bound to its window: %v %v", c.DispatchUntil, c.WindowToken)
		}
	}
}

func TestABatchRefusesMembersWhoseWindowOpensAfterExpiryUnlessPartial(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	api.BatchValidator = &countingValidator{}
	tokens := deviceTokens(3)
	api.DispatchScheduler = &fakeScheduler{refusals: []BatchDeviceRefusal{{
		DeviceToken: tokens[1], Code: "WINDOW_AFTER_EXPIRY", Reason: "too late"}}}

	// Whole-batch by default, exactly like any other per-device refusal.
	_, err := api.CreateCommandBatch(ctx, batchRequest("b1", tokens))
	if code, ok := rejectionCodeOf(err); !ok || code != RejectBatchPartialRefused {
		t.Fatalf("err = %v, want %s", err, RejectBatchPartialRefused)
	}

	req := batchRequest("b2", tokens)
	req.AllowPartial = true
	batch, err := api.CreateCommandBatch(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Accepted != 2 || batch.Resolved != 3 {
		t.Fatalf("accepted/resolved = %d/%d, want 2/3", batch.Accepted, batch.Resolved)
	}
}

func TestABatchFailsClosedWhenTheScheduleCannotBeDecided(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	api.BatchValidator = &countingValidator{}
	api.DispatchScheduler = &fakeScheduler{err: errors.New("down")}
	if _, err := api.CreateCommandBatch(ctx, batchRequest("b1", deviceTokens(2))); err == nil {
		t.Fatal("a batch was admitted without a window decision")
	}
	if got, _ := api.CommandBatchesByToken(ctx, []string{"b1"}); len(got) != 0 {
		t.Fatal("a failed-closed batch spent its token")
	}
}

// ─── re-stamp ─────────────────────────────────────────────────────────────

func stamp(t *testing.T, api *Api, ctx context.Context, token string) sql.NullTime {
	t.Helper()
	return commandRow(t, api, ctx, token).DispatchAfter
}

func TestRestampMovesWaitingRowsToTheNewOpening(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	seedHoldRow(t, api, ctx, "queued", "d1", CommandQueued, inAnHour())
	seedHoldRow(t, api, ctx, "held", "d2", CommandHeld, inAnHour())
	seedHoldRow(t, api, ctx, "parked", "d3", CommandParked, inAnHour())
	sent := seedHoldRow(t, api, ctx, "sent", "d4", CommandSent, anHourAgo())
	other := seedHoldRow(t, api, ctx, "elsewhere", "d5", CommandQueued, inAnHour())
	if err := api.RDB.DB(ctx).Model(&Command{}).Where("id = ?", other).
		Update("window_token", "another-window").Error; err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	next := now.Add(72 * time.Hour).Truncate(time.Second)
	moved, err := api.RestampWindow(ctx, "mw", WindowOpening{Found: true, DispatchAfter: next,
		Closes: next.Add(4 * time.Hour)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 3 {
		t.Fatalf("moved %d rows, want the 3 undelivered ones", moved)
	}
	for _, token := range []string{"queued", "held", "parked"} {
		if got := stamp(t, api, ctx, token); !got.Time.Equal(next) {
			t.Fatalf("%s = %v, want %v", token, got.Time, next)
		}
	}
	if got := stamp(t, api, ctx, "sent"); got.Time.Equal(next) {
		t.Fatal("a command already claimed (SENT) was moved: the window gates the START only")
	}
	_ = sent
	if got := stamp(t, api, ctx, "elsewhere"); got.Time.Equal(next) {
		t.Fatal("a command held by a DIFFERENT window was moved")
	}
}

func TestRestampRollsAnOpenedButUndeliveredRowToTheNextOpening(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	// Its window opened an hour ago while the device was absent, so it was never claimed;
	// the window is closed again now.
	seedHoldRow(t, api, ctx, "missed", "d1", CommandHeld, anHourAgo())
	now := time.Now()
	next := now.Add(7 * 24 * time.Hour).Truncate(time.Second)
	if _, err := api.RestampWindow(ctx, "mw", WindowOpening{Found: true, OpenNow: false, DispatchAfter: next,
		Closes: next.Add(4 * time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	if got := stamp(t, api, ctx, "missed"); !got.Time.Equal(next) {
		t.Fatalf("stamp = %v, want rolled to %v", got.Time, next)
	}
	if got := commandRow(t, api, ctx, "missed").DispatchUntil; !got.Valid || !got.Time.Equal(next.Add(4*time.Hour)) {
		t.Fatalf("close = %v, want the next opening's close", got)
	}
	if drained, _ := api.DrainableCommands(ctx, "d1", 32); len(drained) != 0 {
		t.Fatal("the returning device would have been sent a disruptive command outside any window")
	}
}

// THE FAIL-CLOSED HALF. A row whose opening CLOSED unclaimed must be unclaimable on every road
// before any re-stamp pass runs: the pass is late by up to its interval in the healthy case,
// and never runs at all while device-management is unreachable. The control is the same row
// with its close in the future, which every road takes.
func TestARowWhoseOpeningClosedUnclaimedIsRefusedWithoutARestamp(t *testing.T) {
	closeAt := func(t *testing.T, api *Api, ctx context.Context, token string, at time.Time) {
		t.Helper()
		if err := api.RDB.DB(ctx).Model(&Command{}).Where("token = ?", token).
			Update("dispatch_until", at).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Run("sweep", func(t *testing.T) {
		api := newTestApi(t)
		ctx := core.WithTenant(context.Background(), "A")
		id := seedHoldRow(t, api, ctx, "missed", "d1", CommandQueued, anHourAgo())
		closeAt(t, api, ctx, "missed", time.Now().Add(-time.Minute))
		if pending, _ := api.PendingCommands(ctx); len(pending) != 0 {
			t.Fatalf("the sweep selected %v after the window closed", idsOf(pending))
		}
		if _, claimed, _ := api.MarkSent(ctx, id); claimed {
			t.Fatal("MarkSent claimed a row whose opening had closed")
		}
		closeAt(t, api, ctx, "missed", time.Now().Add(time.Hour))
		if _, claimed, _ := api.MarkSent(ctx, id); !claimed {
			t.Fatal("control: MarkSent refused a row inside its opening")
		}
	})
	t.Run("drain", func(t *testing.T) {
		api := newTestApi(t)
		ctx := core.WithTenant(context.Background(), "A")
		seedHoldRow(t, api, ctx, "missed", "d1", CommandParked, anHourAgo())
		closeAt(t, api, ctx, "missed", time.Now().Add(-time.Minute))
		if drained, _ := api.DrainableCommands(ctx, "d1", 32); len(drained) != 0 {
			t.Fatal("the wake drain offered a row whose opening had closed")
		}
		if _, claimed, _ := api.MarkSentByToken(ctx, "missed"); claimed {
			t.Fatal("MarkSentByToken claimed a row whose opening had closed")
		}
		closeAt(t, api, ctx, "missed", time.Now().Add(time.Hour))
		if _, claimed, _ := api.MarkSentByToken(ctx, "missed"); !claimed {
			t.Fatal("control: MarkSentByToken refused a row inside its opening")
		}
	})
	t.Run("confirm", func(t *testing.T) {
		api := newTestApi(t)
		ctx := core.WithTenant(context.Background(), "A")
		_, nonce := sentWithNonce(t, api, ctx, "live")
		closeAt(t, api, ctx, "live", time.Now().Add(-time.Minute))
		if _, won, _ := api.ConfirmDispatch(ctx, "live", nonce); won {
			t.Fatal("a late envelope was actuated after its window closed")
		}
		closeAt(t, api, ctx, "live", time.Now().Add(time.Hour))
		if _, won, _ := api.ConfirmDispatch(ctx, "live", nonce); !won {
			t.Fatal("control: ConfirmDispatch refused a row inside its opening")
		}
	})
}

func TestRestampBindsRowsToTheOpeningThatIsOpenNow(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	// Its previous opening passed unclaimed; the window is open again now.
	id := seedHoldRow(t, api, ctx, "missed", "d1", CommandHeld, anHourAgo())
	if err := api.RDB.DB(ctx).Model(&Command{}).Where("id = ?", id).
		Update("dispatch_until", time.Now().Add(-30*time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	closes := now.Add(2 * time.Hour).Truncate(time.Second)
	open := WindowOpening{Found: true, OpenNow: true, DispatchAfter: now, Closes: closes}
	if moved, err := api.RestampWindow(ctx, "mw", open, now); err != nil || moved != 1 {
		t.Fatalf("moved %d (err %v), want 1", moved, err)
	}
	row := commandRow(t, api, ctx, "missed")
	if !row.DispatchUntil.Time.Equal(closes) || row.DispatchAfter.Time.After(now) {
		t.Fatalf("row = [%v, %v), want bound to the open opening closing at %v", row.DispatchAfter.Time,
			row.DispatchUntil.Time, closes)
	}
	// A second pass over the unchanged window writes nothing.
	if moved, err := api.RestampWindow(ctx, "mw", open, now.Add(time.Minute)); err != nil || moved != 0 {
		t.Fatalf("an unchanged window moved %d rows (err %v)", moved, err)
	}
	if _, claimed, _ := api.MarkSentByToken(ctx, "missed"); !claimed {
		t.Fatal("a row bound to the open window was not claimable")
	}
}

func TestRestampBringsAHoldForwardWhenTheWindowIsOpenNow(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	id := seedHoldRow(t, api, ctx, "early", "d1", CommandQueued, inAnHour())
	now := time.Now()
	if _, err := api.RestampWindow(ctx, "mw", WindowOpening{Found: true, OpenNow: true, DispatchAfter: now,
		Closes: now.Add(time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	if _, claimed, _ := api.MarkSent(ctx, id); !claimed {
		t.Fatal("a window that is open now still held the command")
	}
}

func TestRestampReleasesRowsWhoseWindowWasDeleted(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	id := seedHoldRow(t, api, ctx, "orphan", "d1", CommandQueued, inAnHour())
	if _, err := api.RestampWindow(ctx, "mw", WindowOpening{Found: false}, time.Now()); err != nil {
		t.Fatal(err)
	}
	row := commandRow(t, api, ctx, "orphan")
	if row.DispatchAfter.Valid || row.DispatchUntil.Valid || row.WindowToken.Valid {
		t.Fatalf("hold survived its window: %v %v", row.DispatchAfter, row.WindowToken)
	}
	if _, claimed, _ := api.MarkSent(ctx, id); !claimed {
		t.Fatal("a row whose window is gone is still unclaimable")
	}
}

func TestRestampIsTenantScoped(t *testing.T) {
	api := newTestApi(t)
	a := core.WithTenant(context.Background(), "A")
	b := core.WithTenant(context.Background(), "B")
	seedHoldRow(t, api, a, "a-row", "d1", CommandQueued, inAnHour())
	moved, err := api.RestampWindow(b, "mw", WindowOpening{Found: false}, time.Now())
	if err != nil || moved != 0 {
		t.Fatalf("tenant B moved %d of tenant A's rows (err %v)", moved, err)
	}
	if !stamp(t, api, a, "a-row").Valid {
		t.Fatal("tenant A's hold was cleared by tenant B")
	}
}

func TestWaitingWindowsListsEachTenantWindowPairOnce(t *testing.T) {
	api := newTestApi(t)
	a := core.WithTenant(context.Background(), "A")
	b := core.WithTenant(context.Background(), "B")
	seedHoldRow(t, api, a, "a1", "d1", CommandQueued, inAnHour())
	seedHoldRow(t, api, a, "a2", "d2", CommandHeld, inAnHour())
	seedHoldRow(t, api, b, "b1", "d1", CommandParked, inAnHour())
	seedHoldRow(t, api, b, "b-done", "d9", CommandSent, anHourAgo()) // claimed: not waiting
	plain := &Command{DeviceToken: "d3", Name: "x", Status: CommandQueued.String()}
	plain.Token = "plain"
	if err := api.RDB.DB(a).Create(plain).Error; err != nil {
		t.Fatal(err)
	}

	sys := core.WithSystemContext(context.Background())
	got, err := api.WaitingWindows(sys, WaitingWindow{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (WaitingWindow{"A", "mw"}) || got[1] != (WaitingWindow{"B", "mw"}) {
		t.Fatalf("got %v, want one pair per tenant", got)
	}
	// Paged: a page of one, then the page after it, then nothing.
	if got, _ = api.WaitingWindows(sys, WaitingWindow{}, 1); len(got) != 1 || got[0].TenantId != "A" {
		t.Fatalf("first page = %v", got)
	}
	if got, _ = api.WaitingWindows(sys, got[0], 1); len(got) != 1 || got[0].TenantId != "B" {
		t.Fatalf("second page = %v; the cursor did not advance", got)
	}
	if got, _ = api.WaitingWindows(sys, got[0], 1); len(got) != 0 {
		t.Fatalf("third page = %v, want the end", got)
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// seedBatchWithCommands creates a batch and one command per (deviceToken, status) pair.
func seedBatchWithCommands(t *testing.T, api *Api, ctx context.Context, token string,
	statuses map[string]string) *CommandBatch {
	t.Helper()
	batch := &CommandBatch{Name: "reboot", TargetKind: BatchTargetDeviceList.String()}
	batch.Token = token
	if err := api.RDB.DB(ctx).Create(batch).Error; err != nil {
		t.Fatalf("seed batch: %v", err)
	}
	for device, status := range statuses {
		cmd := &Command{
			DeviceToken: device,
			Name:        "reboot",
			Status:      status,
			BatchId:     sql.NullInt64{Int64: int64(batch.ID), Valid: true},
			BatchToken:  sql.NullString{String: token, Valid: true},
		}
		cmd.Token = "cmd-" + device
		if err := api.RDB.DB(ctx).Create(cmd).Error; err != nil {
			t.Fatalf("seed command %s: %v", device, err)
		}
	}
	return batch
}

// statusByToken RETURNS a command's status rather than asserting it.
//
// assertStatus in api_test.go is the assert-and-abort form and is the right helper for
// most callers. This one exists because several assertions here need to explain WHY a
// particular status is wrong — "a batch cancel must not touch SENT" carries the whole
// argument, and a generic "status = X, want Y" does not — and because a test checking
// four rows should report all four rather than stopping at the first.
func statusByToken(t *testing.T, api *Api, ctx context.Context, commandToken string) string {
	t.Helper()
	matches, err := api.CommandsByToken(ctx, []string{commandToken})
	if err != nil {
		t.Fatalf("read %s: %v", commandToken, err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 row for %s, got %d", commandToken, len(matches))
	}
	return matches[0].Status
}

// TestCancelBatchTakesOnlyWhatHasNotGoneOut is the central contract, and it asserts on the
// ROWS as well as the counts.
//
// 🔴 THE ROW ASSERTIONS ARE THE POINT, NOT BELT-AND-BRACES. A cancel that wrongly took
// SENT would still report alreadySent=0 and cancelled=3 — internally consistent, and
// indistinguishable from correct behaviour by counts alone. The only thing that catches it
// is asking the sent row what state it is in afterwards.
func TestCancelBatchTakesOnlyWhatHasNotGoneOut(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	batch := seedBatchWithCommands(t, api, ctx, "nightly-reboot", map[string]string{
		"queued-1": CommandQueued.String(),
		"held-1":   CommandHeld.String(),
		"sent-1":   CommandSent.String(),
		"done-1":   CommandSuccessful.String(),
		"gone-1":   CommandExpired.String(),
	})

	result, err := api.CancelCommandBatch(ctx, batch)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}

	if result.Cancelled != 2 {
		t.Errorf("cancelled = %d, want 2 (the queued and the held one)", result.Cancelled)
	}
	if result.AlreadySent != 1 {
		t.Errorf("alreadySent = %d, want 1", result.AlreadySent)
	}
	if result.AlreadyFinished != 2 {
		t.Errorf("alreadyFinished = %d, want 2 (successful + expired)", result.AlreadyFinished)
	}
	if result.Matched != 5 {
		t.Errorf("matched = %d, want 5", result.Matched)
	}

	// The negative control: the sent command must be UNTOUCHED. Its device already has
	// the command and will act on it; recording it as cancelled would say the fleet was
	// stopped when part of it was not.
	if got := statusByToken(t, api, ctx, "cmd-sent-1"); got != CommandSent.String() {
		t.Errorf("the sent command is now %s; a batch cancel must not touch SENT", got)
	}
	if got := statusByToken(t, api, ctx, "cmd-done-1"); got != CommandSuccessful.String() {
		t.Errorf("a finished command was rewritten to %s", got)
	}
	for _, token := range []string{"cmd-queued-1", "cmd-held-1"} {
		if got := statusByToken(t, api, ctx, token); got != CommandCancelled.String() {
			t.Errorf("%s is %s, want CANCELLED", token, got)
		}
	}
}

// TestCancelBatchStampsTheRecordFirstWins pins D8's semantics in both directions.
func TestCancelBatchStampsTheRecordFirstWins(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	batch := seedBatchWithCommands(t, api, ctx, "fleet-push", map[string]string{
		"queued-1": CommandQueued.String(),
		"queued-2": CommandQueued.String(),
		"sent-1":   CommandSent.String(),
	})

	first, err := api.CancelCommandBatch(ctx, batch)
	if err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	if first.Cancelled != 2 {
		t.Fatalf("first cancel took %d, want 2", first.Cancelled)
	}

	stamped := &CommandBatch{}
	if err := api.RDB.DB(ctx).First(stamped, batch.ID).Error; err != nil {
		t.Fatalf("read batch: %v", err)
	}
	if !stamped.CancelledAt.Valid {
		t.Fatal("cancelledAt was not stamped")
	}
	if !stamped.CancelledCount.Valid || stamped.CancelledCount.Int32 != 2 {
		t.Fatalf("cancelledCount = %v, want 2", stamped.CancelledCount)
	}
	firstStamp := stamped.CancelledAt.Time

	// A second cancel catches nothing and must not overwrite the record of the one that
	// actually stopped something.
	second, err := api.CancelCommandBatch(ctx, batch)
	if err != nil {
		t.Fatalf("second cancel: %v", err)
	}
	if second.Cancelled != 0 {
		t.Errorf("second cancel took %d, want 0", second.Cancelled)
	}
	if second.AlreadyFinished != 2 {
		t.Errorf("second cancel reports alreadyFinished = %d, want 2 — rows this call did "+
			"not cancel had already reached a terminal state", second.AlreadyFinished)
	}

	restamped := &CommandBatch{}
	if err := api.RDB.DB(ctx).First(restamped, batch.ID).Error; err != nil {
		t.Fatalf("re-read batch: %v", err)
	}
	if !restamped.CancelledAt.Time.Equal(firstStamp) {
		t.Errorf("cancelledAt moved from %v to %v; the stamp must be first-wins",
			firstStamp, restamped.CancelledAt.Time)
	}
	if restamped.CancelledCount.Int32 != 2 {
		t.Errorf("cancelledCount became %d; a later cancel that caught nothing overwrote "+
			"the count of the one that caught something", restamped.CancelledCount.Int32)
	}
}

// TestCancelBatchThatCatchesNothingStillStamps: pulling the brake on a fleet that has
// already gone is a fact worth recording. Without the stamp, that batch is
// indistinguishable from one nobody ever tried to stop.
func TestCancelBatchThatCatchesNothingStillStamps(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	batch := seedBatchWithCommands(t, api, ctx, "too-late", map[string]string{
		"sent-1": CommandSent.String(),
		"sent-2": CommandSent.String(),
	})

	result, err := api.CancelCommandBatch(ctx, batch)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if result.Cancelled != 0 || result.AlreadySent != 2 {
		t.Fatalf("cancelled=%d alreadySent=%d, want 0 and 2", result.Cancelled, result.AlreadySent)
	}

	stamped := &CommandBatch{}
	if err := api.RDB.DB(ctx).First(stamped, batch.ID).Error; err != nil {
		t.Fatalf("read batch: %v", err)
	}
	if !stamped.CancelledAt.Valid {
		t.Error("a cancel that caught nothing left no stamp; that is not the same fact as " +
			"never having been cancelled")
	}
	if !stamped.CancelledCount.Valid || stamped.CancelledCount.Int32 != 0 {
		t.Errorf("cancelledCount = %v, want a present zero", stamped.CancelledCount)
	}
}

// TestReleaseClaimRetiresACancelledBatchsCommand is the regression test for the defect
// that made the cancel a report rather than a brake.
//
// The sequence is entirely ordinary: the sweep claims a batch's command QUEUED->SENT; the
// cancel arrives and correctly leaves it alone; the publish then fails and the dispatcher
// releases its claim. Before this, the release returned the row to QUEUED and the next
// sweep tick delivered it — to a device whose operator had been told minutes earlier that
// the fleet write was stopped.
func TestReleaseClaimRetiresACancelledBatchsCommand(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	batch := seedBatchWithCommands(t, api, ctx, "stopped", map[string]string{
		"sent-1": CommandSent.String(),
	})
	if _, err := api.CancelCommandBatch(ctx, batch); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	claimed := &Command{}
	if err := api.RDB.DB(ctx).Where("token = ?", "cmd-sent-1").First(claimed).Error; err != nil {
		t.Fatalf("read command: %v", err)
	}
	released, err := api.ReleaseClaim(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !released {
		t.Fatal("the release did not land; a claimed row must always be releasable")
	}
	if got := statusByToken(t, api, ctx, "cmd-sent-1"); got != CommandCancelled.String() {
		t.Errorf("released to %s; a command whose batch was called off must be retired as "+
			"CANCELLED, not returned to the delivery queue", got)
	}
}

// TestReleaseClaimStillQueuesWhenTheBatchIsLive is the counterweight to the test above,
// and without it that fix is indistinguishable from a release that cancels everything.
func TestReleaseClaimStillQueuesWhenTheBatchIsLive(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	seedBatchWithCommands(t, api, ctx, "running", map[string]string{
		"sent-1": CommandSent.String(),
	})

	claimed := &Command{}
	if err := api.RDB.DB(ctx).Where("token = ?", "cmd-sent-1").First(claimed).Error; err != nil {
		t.Fatalf("read command: %v", err)
	}
	if _, err := api.ReleaseClaim(ctx, claimed.ID); err != nil {
		t.Fatalf("release: %v", err)
	}
	if got := statusByToken(t, api, ctx, "cmd-sent-1"); got != CommandQueued.String() {
		t.Errorf("released to %s; a live batch's failed publish must go back to QUEUED for "+
			"the next tick", got)
	}
}

// TestCancelBatchPassCatchesARowPutBackBetweenPasses stages the race the pass loop exists
// for, at the only level where it can be staged.
//
// It cannot be reached through CancelCommandBatch: it needs a dispatcher to return a
// command to the queue in the instant between the cancel's UPDATE and its count. Driving
// the passes by hand reproduces exactly that, and asserts the second pass takes the row —
// which is the whole reason the cancel re-checks its own work.
func TestCancelBatchPassCatchesARowPutBackBetweenPasses(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	batch := seedBatchWithCommands(t, api, ctx, "racy", map[string]string{
		"queued-1": CommandQueued.String(),
		"sent-1":   CommandSent.String(),
	})
	db := api.RDB.DB(ctx)

	cancelled, counts, err := cancelBatchPass(db, batch.ID)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if cancelled != 1 {
		t.Fatalf("first pass cancelled %d, want 1", cancelled)
	}
	if counts[CommandSent.String()] != 1 {
		t.Fatalf("first pass saw %d sent, want 1", counts[CommandSent.String()])
	}

	// Stage the race: the dispatcher's publish failed and it returned the row to the
	// queue. (In production the release now retires it instead — but only once the
	// cancel has COMMITTED, and this window is inside that transaction.)
	if err := db.Model(&Command{}).Where("token = ?", "cmd-sent-1").
		Update("status", CommandQueued.String()).Error; err != nil {
		t.Fatalf("stage release: %v", err)
	}

	second, counts, err := cancelBatchPass(db, batch.ID)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if second != 1 {
		t.Errorf("second pass cancelled %d, want 1 — a row returned to the queue between "+
			"the update and the count is exactly what the repeat exists to catch", second)
	}
	if left := counts[CommandQueued.String()] + counts[CommandHeld.String()]; left != 0 {
		t.Errorf("%d rows still dispatchable after the second pass", left)
	}
}

// TestCancelBatchReportsHonestlyWhenRowsStayDispatchable pins the degrade path: at the
// bound, the buckets need not sum, and that is preferred over the alternatives.
//
// Reaching the bound needs a writer that re-queues a row after EVERY pass, which is what
// the gorm callback below is. The assertion that matters is what the result then says: a
// live, dispatchable command must not be reported as finished, so matched exceeds the sum
// and the gap is the caller's signal to cancel again.
func TestCancelBatchReportsHonestlyWhenRowsStayDispatchable(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	batch := seedBatchWithCommands(t, api, ctx, "relentless", map[string]string{
		"queued-1": CommandQueued.String(),
		"sent-1":   CommandSent.String(),
		"sent-2":   CommandSent.String(),
		"sent-3":   CommandSent.String(),
	})

	// A dispatcher whose publish fails every time: after each cancelling UPDATE, one more
	// SENT command comes back to the queue. Registered on the shared *gorm.DB so it also
	// fires inside the cancel's transaction.
	//
	// 🔴 IT RETURNS A DIFFERENT ROW EACH PASS, AND THAT IS WHAT MAKES THIS TEST AN
	// INSTRUMENT. An earlier version put the SAME row back every time, so `cancelled`
	// counted one command three times — and the resulting arithmetic made
	// `alreadyFinished` clamp to zero no matter how it was computed. The test passed
	// against the very design it exists to rule out (folding the residue in as finished),
	// which was only caught by mutating the code and watching nothing fail.
	db := api.RDB.Database
	pass := 0
	requeue := func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "commands" {
			return
		}
		if tx.Error != nil || tx.RowsAffected == 0 {
			return
		}
		pass++
		if pass > 3 {
			return
		}
		tx.Session(&gorm.Session{NewDB: true, SkipHooks: true}).
			Exec(`UPDATE commands SET status = ? WHERE token = ?`,
				CommandQueued.String(), fmt.Sprintf("cmd-sent-%d", pass))
	}
	if err := db.Callback().Update().After("gorm:update").Register("test:requeue", requeue); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	defer func() {
		if err := db.Callback().Update().Remove("test:requeue"); err != nil {
			t.Fatalf("remove callback: %v", err)
		}
	}()

	result, err := api.CancelCommandBatch(ctx, batch)
	if err != nil {
		t.Fatalf("reaching the pass bound must not be an error: %v", err)
	}

	// 🔴 THE INSTRUMENT CHECK COMES FIRST. Every assertion below is about a row that is
	// still dispatchable after the cancel gave up; if the staging callback failed to keep
	// it that way, they would all pass against a perfectly ordinary cancel and this test
	// would be measuring nothing.
	if got := statusByToken(t, api, ctx, "cmd-sent-3"); got != CommandQueued.String() {
		t.Fatalf("the staging callback did not leave a row dispatchable (status %s); this "+
			"test is not exercising the degrade path at all", got)
	}

	// The bound fired: the loop ran every pass rather than settling early. Each pass
	// cancelled a DIFFERENT command, so this is an honest count of rows stopped.
	//
	// 🔴 THE EXPECTATION IS A LITERAL, NOT batchCancelPasses. Written in terms of the
	// constant it is meant to pin, this assertion cannot fail when that constant moves —
	// it would agree with a loop bounded at one pass just as readily. The fixture stages
	// exactly three re-queues, so three is the answer; if the bound is ever changed
	// deliberately, this number and the fixture both have to be changed with it, which is
	// the point.
	if batchCancelPasses != 3 {
		t.Fatalf("batchCancelPasses is %d; this fixture stages 3 re-queues and its "+
			"expectations are written for 3", batchCancelPasses)
	}
	if result.Cancelled != 3 {
		t.Errorf("cancelled = %d, want 3 — the loop must run to its bound here, not settle",
			result.Cancelled)
	}
	if result.Matched != 4 {
		t.Fatalf("matched = %d, want 4", result.Matched)
	}
	// The whole point of the degrade: the surviving row is in NO bucket. Folding it into
	// alreadyFinished would report a live, dispatchable command as finished.
	if result.AlreadyFinished != 0 {
		t.Errorf("alreadyFinished = %d, want 0 — a row that is dispatchable again must be "+
			"left out of every bucket, never counted as finished", result.AlreadyFinished)
	}
	if result.AlreadySent != 0 {
		t.Errorf("alreadySent = %d, want 0", result.AlreadySent)
	}
	// Deliberately NOT asserted: that the four numbers fail to sum. Whether they do is a
	// property of how many rows this fixture happens to leave dispatchable, not of the
	// cancel — so an assertion on it would pin the fixture rather than the behaviour. The
	// behaviour worth pinning is above: the surviving row is reported in no bucket at all.
}

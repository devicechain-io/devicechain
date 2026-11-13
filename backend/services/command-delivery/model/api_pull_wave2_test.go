// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"gorm.io/datatypes"
)

// TestTheClaimRefusesACommandThatExpiredAfterTheRead: the selection and the claim are two
// statements, so the claim carries the expiry predicate itself. The candidate below is the row as
// the read saw it, live; by the time the claim runs it has expired.
func TestTheClaimRefusesACommandThatExpiredAfterTheRead(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "late", nil, nil)
	matches, err := api.CommandsByToken(ctx, []string{"late"})
	if err != nil || len(matches) != 1 {
		t.Fatalf("read: %v %d", err, len(matches))
	}
	seen := matches[0]
	if err := api.RDB.DB(ctx).Model(&Command{}).Where("id = ?", seen.ID).
		Update("expires_at", sql.NullTime{Time: time.Now().Add(-time.Second), Valid: true}).Error; err != nil {
		t.Fatal(err)
	}
	got, err := api.claimForPull(ctx, seen, "d")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("an expired command was claimed: %+v", got)
	}
	assertStatus(t, api, ctx, "late", CommandQueued)
}

// TestTheClaimStillClaimsALiveCommand is the counterweight: the expiry predicate must not refuse a
// command that has a future expiry.
func TestTheClaimStillClaimsALiveCommand(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "ok", nil, nil)
	if err := api.RDB.DB(ctx).Model(&Command{}).Where("token = ?", "ok").
		Update("expires_at", sql.NullTime{Time: time.Now().Add(time.Hour), Valid: true}).Error; err != nil {
		t.Fatal(err)
	}
	got, err := api.PullCommands(ctx, "d", 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("pull: %d commands, err %v", len(got), err)
	}
}

func cancelBatchOf(t *testing.T, api *Api, token string) *CommandBatch {
	t.Helper()
	ctx := core.WithTenant(context.Background(), "A")
	batch := &CommandBatch{Name: "reboot", TargetKind: BatchTargetDeviceList.String()}
	batch.Token = "b-" + token
	if err := api.RDB.DB(ctx).Create(batch).Error; err != nil {
		t.Fatal(err)
	}
	if err := api.RDB.DB(ctx).Model(&Command{}).Where("token = ?", token).Updates(map[string]any{
		"batch_id": sql.NullInt64{Int64: int64(batch.ID), Valid: true},
	}).Error; err != nil {
		t.Fatal(err)
	}
	return batch
}

func callOff(t *testing.T, api *Api, batch *CommandBatch) {
	t.Helper()
	if err := api.RDB.DB(core.WithTenant(context.Background(), "A")).Model(&CommandBatch{}).Where("id = ?", batch.ID).
		Update("cancelled_at", sql.NullTime{Time: time.Now(), Valid: true}).Error; err != nil {
		t.Fatal(err)
	}
}

// TestACancelledBatchsUnackedHandOutIsNotRedelivered: cancellation leaves SENT rows alone, so the
// redelivery arm must not offer one again; the pull ends it as CANCELLED, NOT_DELIVERED.
func TestACancelledBatchsUnackedHandOutIsNotRedelivered(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "cb", nil, nil)
	if first, _ := api.PullCommands(ctx, "d", 0); len(first) != 1 {
		t.Fatalf("first hand-out: %d", len(first))
	}
	callOff(t, api, cancelBatchOf(t, api, "cb"))
	ageSent(t, api, ctx, "cb", 2*PullAckWait)

	got, err := api.PullCommands(ctx, "d", 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("a cancelled batch's command was handed out again: %d (err %v)", len(got), err)
	}
	assertStatus(t, api, ctx, "cb", CommandCancelled)
}

// TestACancelledBatchsAcknowledgedCommandIsLeftAlone: the device holds it and may answer; the
// pull is not the place to pull it back.
func TestACancelledBatchsAcknowledgedCommandIsLeftAlone(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "ca", nil, nil)
	if first, _ := api.PullCommands(ctx, "d", 0); len(first) != 1 {
		t.Fatal("no first hand-out")
	}
	if ok, err := api.AcknowledgeDelivery(ctx, "ca", "d"); err != nil || !ok {
		t.Fatalf("ack: %v %v", ok, err)
	}
	callOff(t, api, cancelBatchOf(t, api, "ca"))
	if _, err := api.PullCommands(ctx, "d", 0); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, api, ctx, "ca", CommandSent)
}

// TestTheClaimRefusesACommandWhoseBatchWasCalledOffAfterTheRead is the race the selection cannot
// see: the batch is cancelled between the read and the claim, on a still-QUEUED row.
func TestTheClaimRefusesACommandWhoseBatchWasCalledOffAfterTheRead(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "race", nil, nil)
	matches, _ := api.CommandsByToken(ctx, []string{"race"})
	seen := matches[0]
	callOff(t, api, cancelBatchOf(t, api, "race"))
	got, err := api.claimForPull(ctx, seen, "d")
	if err != nil || got != nil {
		t.Fatalf("claimed %v (err %v) after the batch was called off", got, err)
	}
}

// TestACommandOutsideACancelledBatchIsStillHandedOut: the counterweight to the brake.
func TestACommandOutsideACancelledBatchIsStillHandedOut(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "in", nil, nil)
	enqueuePull(t, api, ctx, "out", nil, nil)
	callOff(t, api, cancelBatchOf(t, api, "in"))
	got, err := api.PullCommands(ctx, "d", 0)
	if err != nil || len(got) != 1 || got[0].Command.Token != "out" {
		t.Fatalf("pulled %d (err %v), want only out", len(got), err)
	}
}

// TestThePullAnswerIsBoundedBeforeTheClaim: 16 commands with 64 KiB payloads would overflow the
// service client's cap. The pull takes what fits and leaves the rest QUEUED with their attempts
// untouched, rather than claiming them and dropping them from the answer.
func TestThePullAnswerIsBoundedBeforeTheClaim(t *testing.T) {
	if PullResponseBudget >= svcclient.MaxResponseBytes {
		t.Fatalf("budget %d must be under the client cap %d", PullResponseBudget, svcclient.MaxResponseBytes)
	}
	api, ctx := pullApi(t)
	big := `"` + strings.Repeat("x", 60<<10) + `"`
	for _, tok := range []string{"p01", "p02", "p03", "p04", "p05", "p06", "p07", "p08"} {
		if _, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: tok, DeviceToken: "d", Name: "load", Payload: &big}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := api.PullCommands(ctx, "d", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || len(got) >= 8 {
		t.Fatalf("pulled %d big commands, want some but not all", len(got))
	}
	worst := 0
	for _, g := range got {
		worst += pullWireCost(g.Command)
	}
	if worst > PullResponseBudget {
		t.Fatalf("answer worst case %d exceeds the budget %d", worst, PullResponseBudget)
	}
	var untouched int64
	if err := api.RDB.DB(ctx).Model(&Command{}).
		Where("status = ? AND delivery_attempts IS NULL", CommandQueued.String()).Count(&untouched).Error; err != nil {
		t.Fatal(err)
	}
	if int(untouched) != 8-len(got) {
		t.Fatalf("%d left queued and untouched, want %d", untouched, 8-len(got))
	}
	// The rest come on the next pull.
	more, _ := api.PullCommands(ctx, "d", 0)
	if len(more) == 0 {
		t.Fatal("the commands left behind were never handed out")
	}
}

// TestASingleMaximumPayloadCommandIsAlwaysHandedOut: the budget must never starve a command.
func TestASingleMaximumPayloadCommandIsAlwaysHandedOut(t *testing.T) {
	api, ctx := pullApi(t)
	max := `"` + strings.Repeat("\n", 0) + strings.Repeat("x", (64<<10)-2) + `"`
	if _, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "max", DeviceToken: "d", Name: "load", Payload: &max}); err != nil {
		t.Fatal(err)
	}
	if got, err := api.PullCommands(ctx, "d", 0); err != nil || len(got) != 1 {
		t.Fatalf("pulled %d (err %v)", len(got), err)
	}
	mj := datatypes.JSON(max)
	if one := pullWireCost(&Command{Payload: &mj}); one > PullResponseBudget {
		t.Fatalf("one maximum command costs %d, over the budget", one)
	}
}

// TestACancelledBatchsRowDoesNotUseUpTheLimit: the selection skips it, so a pull of one command
// reaches the live command behind it instead of reading the cancelled row and finding the claim lost.
func TestACancelledBatchsRowDoesNotUseUpTheLimit(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "first", nil, nil)
	enqueuePull(t, api, ctx, "second", nil, nil)
	callOff(t, api, cancelBatchOf(t, api, "first"))
	got, err := api.PullCommands(ctx, "d", 1)
	if err != nil || len(got) != 1 || got[0].Command.Token != "second" {
		t.Fatalf("pulled %d (err %v), want second", len(got), err)
	}
}

// TestACommandNoPullCanCarryIsEndedNotClaimed: a payload that reads back larger than any answer may
// be (the stored form can outgrow the 64 KiB accepted on input) is failed as NOT_DELIVERED with its
// attempts untouched, and the command behind it is still handed out.
func TestACommandNoPullCanCarryIsEndedNotClaimed(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "huge", nil, nil)
	enqueuePull(t, api, ctx, "small", nil, nil)
	huge := `"` + strings.Repeat("x", PullResponseBudget/6+10) + `"`
	if err := api.RDB.DB(ctx).Model(&Command{}).Where("token = ?", "huge").
		Update("payload", datatypes.JSON(huge)).Error; err != nil {
		t.Fatal(err)
	}
	got, err := api.PullCommands(ctx, "d", 0)
	if err != nil || len(got) != 1 || got[0].Command.Token != "small" {
		t.Fatalf("pulled %d (err %v), want only small", len(got), err)
	}
	assertStatus(t, api, ctx, "huge", CommandFailed)
	matches, _ := api.CommandsByToken(ctx, []string{"huge"})
	if matches[0].DeliveryAttempts.Valid || matches[0].Outcome.String != OutcomeNotDelivered.String() {
		t.Fatalf("attempts %v outcome %q; want untouched and NOT_DELIVERED", matches[0].DeliveryAttempts, matches[0].Outcome.String)
	}
}

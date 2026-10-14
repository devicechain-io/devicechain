// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

// outcomeCase drives ONE real terminating writer and names the status and class it must leave.
type outcomeCase struct {
	name        string
	wantStatus  CommandStatus
	wantOutcome CommandOutcome
	// drive creates its own command(s) and returns the token of the one that must have finished.
	drive func(t *testing.T, api *Api, ctx context.Context) string
}

func claimed(t *testing.T, api *Api, ctx context.Context, token string) (uint, string) {
	t.Helper()
	created, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: token, DeviceToken: "d", Name: "reboot"})
	if err != nil {
		t.Fatalf("create %s: %v", token, err)
	}
	nonce, won, err := api.MarkSent(ctx, created.ID)
	if err != nil || !won {
		t.Fatalf("claim %s: won=%v err=%v", token, won, err)
	}
	return created.ID, nonce
}

func answer(reach Reach, success bool) func(*testing.T, *Api, context.Context) string {
	return func(t *testing.T, api *Api, ctx context.Context) string {
		_, nonce := claimed(t, api, ctx, "answered")
		if _, err := api.MarkResponse(ctx, "answered", "d", nonce, success, reach, nil, strPtr("because")); err != nil {
			t.Fatalf("MarkResponse: %v", err)
		}
		return "answered"
	}
}

// TestEveryTerminatingWriterStampsItsOutcome is the coverage proof for the stored outcome: one row
// per writer that can finish a command, each driven through the REAL writer, each asserted to
// carry the status AND class that writer is documented to record. The last subtest proves the
// table covers every terminal status and every class, so a new writer or class that is not added
// here is a visible gap rather than a silent one.
func TestEveryTerminatingWriterStampsItsOutcome(t *testing.T) {
	cases := []outcomeCase{
		{"device success", CommandSuccessful, OutcomeSucceeded, answer(ReachAnswered, true)},
		{"device says no", CommandFailed, OutcomeRejected, answer(ReachAnswered, false)},
		{"transport got no answer", CommandTimeout, OutcomeUnknown, answer(ReachUnanswered, false)},
		{"transport refused locally", CommandFailed, OutcomeNotDelivered, answer(ReachUnsent, false)},
		{"undeliverable transport", CommandFailed, OutcomeNotDelivered,
			func(t *testing.T, api *Api, ctx context.Context) string {
				created, _ := api.CreateCommand(ctx, &CommandCreateRequest{Token: "undeliv", DeviceToken: "d", Name: "x"})
				if ok, err := api.MarkUndeliverable(ctx, created.ID, "no command path"); err != nil || !ok {
					t.Fatalf("MarkUndeliverable: ok=%v err=%v", ok, err)
				}
				return "undeliv"
			}},
		{"answer dead-lettered", CommandFailed, OutcomeUnknown,
			func(t *testing.T, api *Api, ctx context.Context) string {
				claimed(t, api, ctx, "lost")
				if ok, err := api.MarkResponseLost(ctx, "lost"); err != nil || !ok {
					t.Fatalf("MarkResponseLost: ok=%v err=%v", ok, err)
				}
				return "lost"
			}},
		{"publish abandoned after the failure bound", CommandFailed, OutcomeNotDelivered,
			func(t *testing.T, api *Api, ctx context.Context) string {
				api.MaxDispatchFailures = 1
				id, _ := claimed(t, api, ctx, "giveup")
				if landed, ok, err := api.ReleaseClaim(ctx, id); err != nil || !ok || landed != CommandFailed {
					t.Fatalf("ReleaseClaim: landed=%q ok=%v err=%v", landed, ok, err)
				}
				return "giveup"
			}},
		{"operator cancels one command", CommandCancelled, OutcomeNotDelivered,
			func(t *testing.T, api *Api, ctx context.Context) string {
				if _, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "cx", DeviceToken: "d", Name: "x"}); err != nil {
					t.Fatal(err)
				}
				if got, err := api.CancelCommand(ctx, "cx"); err != nil || got.Status != CommandCancelled.String() {
					t.Fatalf("CancelCommand: %v %v", got, err)
				}
				return "cx"
			}},
		{"operator cancels the batch", CommandCancelled, OutcomeNotDelivered,
			func(t *testing.T, api *Api, ctx context.Context) string {
				batch := seedBatchWithCommands(t, api, ctx, "bcancel", map[string]string{"q": CommandQueued.String()})
				if _, err := api.CancelCommandBatch(ctx, batch); err != nil {
					t.Fatal(err)
				}
				return "cmd-q"
			}},
		{"claim retired because its batch was called off", CommandCancelled, OutcomeNotDelivered,
			func(t *testing.T, api *Api, ctx context.Context) string {
				batch := seedBatchWithCommands(t, api, ctx, "bsent", map[string]string{"s": CommandSent.String()})
				if _, err := api.CancelCommandBatch(ctx, batch); err != nil {
					t.Fatal(err)
				}
				cmd := &Command{}
				if err := api.RDB.DB(ctx).Where("token = ?", "cmd-s").First(cmd).Error; err != nil {
					t.Fatal(err)
				}
				if landed, _, err := api.ReleaseClaim(ctx, cmd.ID); err != nil || landed != CommandCancelled {
					t.Fatalf("ReleaseClaim: landed=%q err=%v", landed, err)
				}
				return "cmd-s"
			}},
		{"lapsed before it went out", CommandExpired, OutcomeNotDelivered, expire(false)},
		{"lapsed after it went out", CommandTimeout, OutcomeUnknown, expire(true)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newBatchTestApi(t)
			ctx := core.WithTenant(context.Background(), "A")
			token := tc.drive(t, api, ctx)
			got := commandByToken(t, api, ctx, token)
			if got.Status != tc.wantStatus.String() {
				t.Fatalf("status = %s, want %s", got.Status, tc.wantStatus)
			}
			if !got.Outcome.Valid || got.Outcome.String != tc.wantOutcome.String() {
				t.Fatalf("outcome = %+v, want %s: the terminating write must record the class itself",
					got.Outcome, tc.wantOutcome)
			}
			assertNoUnclassifiedTerminal(t, api, ctx)
		})
	}

	t.Run("the table reaches every terminal status and every class", func(t *testing.T) {
		statuses, classes := map[CommandStatus]bool{}, map[CommandOutcome]bool{}
		for _, tc := range cases {
			statuses[tc.wantStatus], classes[tc.wantOutcome] = true, true
		}
		for _, s := range terminalStatuses {
			if !statuses[s] {
				t.Errorf("no writer case ends in %s: a writer for it is untested", s)
			}
			if len(permittedOutcomes[s]) == 0 {
				t.Errorf("terminal status %s has no permitted outcome class", s)
			}
		}
		for _, o := range allOutcomes {
			if !classes[o] {
				t.Errorf("no writer case records %s", o)
			}
		}
	})
}

func expire(sent bool) func(*testing.T, *Api, context.Context) string {
	return func(t *testing.T, api *Api, ctx context.Context) string {
		sysctx := core.WithSystemContext(ctx)
		past := time.Now().Add(-time.Hour).Format(time.RFC3339)
		created, err := api.CreateCommand(ctx, &CommandCreateRequest{
			Token: "lapse", DeviceToken: "d", Name: "x", ExpiresAt: &past,
		})
		if err != nil {
			t.Fatal(err)
		}
		if sent {
			if _, won, err := api.MarkSent(ctx, created.ID); err != nil || !won {
				t.Fatalf("claim: %v %v", won, err)
			}
		}
		if n, _, err := api.ExpireStale(sysctx, time.Now()); err != nil || n != 1 {
			t.Fatalf("ExpireStale: n=%d err=%v", n, err)
		}
		return "lapse"
	}
}

func commandByToken(t *testing.T, api *Api, ctx context.Context, token string) *Command {
	t.Helper()
	got, err := api.CommandsByToken(ctx, []string{token})
	if err != nil || len(got) != 1 {
		t.Fatalf("load %s: %v (%d rows)", token, err, len(got))
	}
	return got[0]
}

// assertNoUnclassifiedTerminal fails if ANY finished command lacks an outcome, which is the
// invariant the whole retry feature rests on.
func assertNoUnclassifiedTerminal(t *testing.T, api *Api, ctx context.Context) {
	t.Helper()
	var n int64
	if err := api.RDB.DB(core.WithSystemContext(ctx)).Model(&Command{}).
		Where("status IN ? AND outcome IS NULL", terminalStatusStrings()).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d finished command(s) carry no outcome class", n)
	}
}

// TestATerminalWriteWithoutAnOutcomeIsRefused pins the structural backstop: a writer that
// forgets the class (or invents a mismatching one) does not leave an unclassifiable row, it
// fails.
func TestATerminalWriteWithoutAnOutcomeIsRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	id := seedWithStatus(t, api, ctx, "guarded", CommandSent)
	write := func(updates map[string]any) error {
		return api.RDB.DB(ctx).Model(&Command{}).Where("id = ?", id).Updates(updates).Error
	}

	for name, updates := range map[string]map[string]any{
		"terminal status with no outcome":  {"status": CommandFailed.String()},
		"success with a failure class":     {"status": CommandSuccessful.String(), "outcome": OutcomeRejected.String()},
		"timeout claiming it was not sent": {"status": CommandTimeout.String(), "outcome": OutcomeNotDelivered.String()},
		"an invented class":                {"status": CommandFailed.String(), "outcome": "MAYBE"},
		"an outcome beside a live status":  {"status": CommandQueued.String(), "outcome": OutcomeUnknown.String()},
		"an outcome with no status at all": {"outcome": OutcomeSucceeded.String()},
	} {
		if err := write(updates); !errors.Is(err, ErrUnclassifiedTerminal) {
			t.Errorf("%s: err = %v, want ErrUnclassifiedTerminal", name, err)
		}
	}
	if got := loadOrFail(t, api, ctx, id); got.Status != CommandSent.String() || got.Outcome.Valid {
		t.Fatalf("a refused write must leave the row alone, got %s / %+v", got.Status, got.Outcome)
	}
	// Writes that touch neither column, and a correct terminal write, pass.
	if err := write(map[string]any{"error": "x"}); err != nil {
		t.Errorf("a write touching neither column was refused: %v", err)
	}
	if err := write(terminalUpdate(CommandFailed, OutcomeRejected, nil)); err != nil {
		t.Errorf("a correct terminal write was refused: %v", err)
	}
}

func TestSearchFiltersByOutcome(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	answer(ReachAnswered, true)(t, api, ctx)
	seedWithStatus(t, api, ctx, "live", CommandQueued)

	outcomes := []string{OutcomeSucceeded.String()}
	res, err := api.Commands(ctx, CommandSearchCriteria{Outcomes: &outcomes})
	if err != nil || len(res.Results) != 1 || res.Results[0].Token != "answered" {
		t.Fatalf("outcome filter: %v %+v", err, res)
	}
	none := []string{}
	res, err = api.Commands(ctx, CommandSearchCriteria{Outcomes: &none})
	if err != nil || len(res.Results) != 2 {
		t.Fatalf("an empty outcome list must be ignored: %v %+v", err, res)
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/presence"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// cancelledNudgePresence returns the read error that a shutdown-interrupted projection
// read produces. Presence fails open, so the delivery batch must still observe cancellation.
type cancelledNudgePresence struct {
	entered chan struct{}
	err     chan error
}

func (r *cancelledNudgePresence) StatesFor(ctx context.Context, _ []string) (map[string]presence.State, error) {
	close(r.entered)
	<-ctx.Done()
	r.err <- ctx.Err()
	return nil, ctx.Err()
}

func TestCancelledNudgeRemainsQueuedAndAFreshNudgeSendsItOnce(t *testing.T) {
	for _, rootCancellation := range []bool{false, true} {
		name := "Stop cancels child"
		if rootCancellation {
			name = "root cancels child"
		}
		t.Run(name, func(t *testing.T) {
			api := realApi(t)
			ctx := core.WithTenant(context.Background(), "acme")
			cmd, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
				Token: "cmd-1", DeviceToken: "pump-1", Name: "reboot",
			})
			if err != nil {
				t.Fatal(err)
			}
			// Shutdown must not spend the last retry and retire the command as FAILED.
			api.MaxDispatchFailures = 1
			writer := &recordingWriter{}
			proc := procWith(api, writer)
			nudgeMetered(proc)
			reader := &cancelledNudgePresence{entered: make(chan struct{}), err: make(chan error, 1)}
			proc.Presence = reader
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			nudger := newDispatchNudger(proc, proc.NudgeMetrics)
			nudger.Start(parent)
			nudger.NudgeDevice("acme", "pump-1")
			select {
			case <-reader.entered:
			case <-time.After(time.Second):
				t.Fatal("nudge never reached presence")
			}
			if rootCancellation {
				cancel()
				select {
				case err := <-reader.err:
					if err != context.Canceled {
						t.Fatalf("root cancellation: %v", err)
					}
				case <-time.After(time.Second):
					t.Fatal("nudge did not inherit root cancellation")
				}
			}
			stopped := make(chan struct{})
			go func() { nudger.Stop(); close(stopped) }()
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("in-flight nudge outlived Stop")
			}
			if !rootCancellation {
				if err := <-reader.err; err != context.Canceled {
					t.Fatalf("Stop cancellation: %v", err)
				}
			}
			row := loadByToken(t, api, ctx, cmd.Token)
			if row.Status != model.CommandQueued.String() || row.DispatchFailures != 0 || row.SentTime.Valid {
				t.Fatalf("cancelled nudge changed queued command: %+v", row)
			}
			if writer.count() != 0 {
				t.Fatal("cancelled pre-claim nudge published")
			}
			pending, err := api.PendingCommands(core.WithSystemContext(context.Background()))
			if err != nil || len(pending) != 1 || pending[0].Token != cmd.Token {
				t.Fatalf("next pod cannot sweep command: pending=%v err=%v", pending, err)
			}
			proc.Presence = nil
			proc.DrainDevice(context.Background(), "acme", "pump-1")
			proc.DrainDevice(context.Background(), "acme", "pump-1")
			if writer.count() != 1 || statusOf(t, api, ctx, cmd.Token) != model.CommandSent.String() {
				t.Fatalf("fresh nudges must send once: publishes=%d", writer.count())
			}
			// Applied also counts the cancelled batch handed to delivery. Its existing
			// meaning is attempts through the gates, so check the normal attempt's delta.
			if got := testutil.ToFloat64(proc.NudgeMetrics.Applied); got != 2 {
				t.Fatalf("Applied = %v, want cancelled batch + normal delivery", got)
			}
		})
	}
}

// cancelAfterClaimWriter models shutdown at the handoff to the writer. The nudge
// must finish the existing claimed dispatch, retaining the tenant and avoiding an
// artificial cancellation failure (including at the retry exhaustion boundary).
type cancelAfterClaimWriter struct {
	cancel context.CancelFunc
	writer recordingWriter
	tenant string
}

func (w *cancelAfterClaimWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	return w.WriteToDevice(ctx, "pump-1", msgs...)
}
func (w *cancelAfterClaimWriter) WriteToDevice(ctx context.Context, device string, msgs ...messaging.Message) error {
	w.cancel()
	w.tenant, _ = core.TenantFromContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	return w.writer.WriteToDevice(ctx, device, msgs...)
}
func (*cancelAfterClaimWriter) HandleResponse(error) {}

func TestNudgeClaimedBeforeCancellationFinishesWithoutResending(t *testing.T) {
	api := realApi(t)
	api.MaxDispatchFailures = 1
	ctx := core.WithTenant(context.Background(), "acme")
	cmd, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
		Token: "cmd-1", DeviceToken: "pump-1", Name: "reboot",
	})
	if err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := &cancelAfterClaimWriter{cancel: cancel}
	proc := procWith(api, writer)
	nudgeMetered(proc)
	proc.DrainDevice(parent, "acme", "pump-1")
	row := loadByToken(t, api, ctx, cmd.Token)
	if row.Status != model.CommandSent.String() || row.DispatchFailures != 0 {
		t.Fatalf("shutdown must not fail or requeue a claimed publish: %+v", row)
	}
	proc.DrainDevice(context.Background(), "acme", "pump-1")
	if writer.tenant != "acme" {
		t.Fatalf("claimed publish lost tenant: %q", writer.tenant)
	}
	if writer.writer.count() != 1 {
		t.Fatalf("publishes = %d, want 1", writer.writer.count())
	}
	if got := testutil.ToFloat64(proc.NudgeMetrics.Applied); got != 1 {
		t.Fatalf("Applied = %v, want 1", got)
	}
}

func TestStoppedNudgerCannotBeStarted(t *testing.T) {
	drainer := &countingDrainer{done: make(chan struct{}, 1)}
	nudger := newDispatchNudger(drainer, NudgeMetrics{})
	nudger.NudgeDevice("acme", "before-stop")
	nudger.Stop()
	nudger.Start(context.Background())
	nudger.NudgeDevice("acme", "after-stop")
	nudger.Stop()
	if len(nudger.queue) != 1 {
		t.Fatalf("stopped queue accepted or drained work: %d", len(nudger.queue))
	}
	select {
	case <-drainer.done:
		t.Fatal("a nudge started after Stop")
	default:
	}
}

func TestLifecyclePassesRootCancellationToAnInflightNudge(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	drainer := &blockingDrainer{entered: make(chan struct{}, 1), errs: make(chan error, 1)}
	proc := procWith(&fakeApi{}, &recordingWriter{})
	proc.CommandResponsesReader = eofReader{}
	proc.nudger = newDispatchNudger(drainer, NudgeMetrics{})
	if err := proc.ExecuteInitialize(parent); err != nil {
		t.Fatal(err)
	}
	if err := proc.ExecuteStart(parent); err != nil {
		t.Fatal(err)
	}
	proc.nudger.NudgeDevice("acme", "pump-1")
	select {
	case <-drainer.entered:
	case <-time.After(time.Second):
		t.Fatal("lifecycle did not start nudge")
	}
	cancel()
	select {
	case err := <-drainer.errs:
		if err != context.Canceled {
			t.Fatalf("drain error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ExecuteStart did not pass its root context to the nudge")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- proc.ExecuteStop(context.Background()) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("lifecycle Stop did not join cancelled nudge")
	}
}

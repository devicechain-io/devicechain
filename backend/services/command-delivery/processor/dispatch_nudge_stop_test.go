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

func stopWithin(t *testing.T, n *dispatchNudger, d time.Duration) {
	t.Helper()
	stopped := make(chan struct{})
	go func() {
		n.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(d):
		t.Fatalf("Stop did not return within %v", d)
	}
}

func TestAlreadyCancelledDeliveryDoesNotClaim(t *testing.T) {
	for _, path := range []dispatchPath{pathNudge, pathSweep} {
		t.Run(string(path), func(t *testing.T) {
			api := &fakeApi{}
			writer := &recordingWriter{}
			proc := procWith(api, writer)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := proc.deliverCommand(ctx, nudgeable(1, "c1", "pump-1"), path, routing{}); err != context.Canceled {
				t.Fatalf("deliverCommand error = %v, want context.Canceled", err)
			}
			if len(api.markedSent) != 0 || writer.count() != 0 {
				t.Fatalf("cancelled delivery claimed %v and published %d", api.markedSent, writer.count())
			}
		})
	}
}

func TestAlreadyCancelledTenantBatchDoesNothing(t *testing.T) {
	for _, path := range []dispatchPath{pathNudge, pathSweep} {
		t.Run(string(path), func(t *testing.T) {
			api := &fakeApi{}
			writer := &recordingWriter{}
			proc := procWith(api, writer)
			// A held command also pins the loop check independently of the guard
			// inside deliverCommand: removing the loop check would hold it.
			proc.Presence = absentReader{"absent"}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			proc.deliverTenantBatch(ctx, tenantBatch{tenant: "acme", commands: []*model.Command{
				nudgeable(1, "c1", "pump-1"), nudgeable(2, "c2", "absent"),
			}}, path)
			if len(api.markedSent) != 0 || len(api.held) != 0 || writer.count() != 0 {
				t.Fatalf("cancelled batch claimed %v, held %v and published %d", api.markedSent, api.held, writer.count())
			}
		})
	}
}

// inFlightClaimApi models an autocommit claim that lands before the driver
// observes cancellation. A cancellable claim context would report an error even
// though the row is already SENT, preventing the caller from publishing it.
type inFlightClaimApi struct {
	*fakeApi
	entered   chan struct{}
	cancelled <-chan struct{}
	tenant    string
}

func (a *inFlightClaimApi) MarkSent(ctx context.Context, id uint) (string, bool, error) {
	nonce, claimed, err := a.fakeApi.MarkSent(ctx, id)
	a.tenant, _ = core.TenantFromContext(ctx)
	close(a.entered)
	<-a.cancelled
	if ctx.Err() != nil {
		return "", false, ctx.Err()
	}
	return nonce, claimed, err
}

func TestCancellationDuringClaimStillPublishesExactlyOnce(t *testing.T) {
	for _, path := range []dispatchPath{pathNudge, pathSweep} {
		t.Run(string(path), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			api := &inFlightClaimApi{fakeApi: &fakeApi{}, entered: make(chan struct{}), cancelled: ctx.Done()}
			writer := &recordingWriter{}
			proc := procWith(api, writer)
			finished := make(chan error, 1)
			go func() {
				finished <- proc.deliverCommand(ctx, nudgeable(1, "c1", "pump-1"), path, routing{})
			}()
			select {
			case <-api.entered:
			case <-time.After(time.Second):
				t.Fatal("MarkSent never started")
			}
			cancel()
			select {
			case err := <-finished:
				if err != nil {
					t.Fatalf("dispatch interrupted during claim: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("dispatch did not finish after cancellation during claim")
			}
			if api.tenant != "acme" || len(api.markedSent) != 1 || len(api.released) != 0 || writer.count() != 1 {
				t.Fatalf("tenant=%q claims=%v releases=%v publishes=%d, want acme, one claim, no release, one publish",
					api.tenant, api.markedSent, api.released, writer.count())
			}
		})
	}
}

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
			stopWithin(t, nudger, time.Second)
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
	drainer := &blockingDrainer{
		entered: make(chan struct{}, NudgeWorkers),
		errs:    make(chan error, NudgeWorkers),
	}
	nudger := newDispatchNudger(drainer, NudgeMetrics{})
	nudger.NudgeDevice("acme", "before-stop")
	stopWithin(t, nudger, time.Second)
	nudger.Start(context.Background())
	nudger.NudgeDevice("acme", "after-stop")
	defer stopWithin(t, nudger, time.Second)
	select {
	case <-drainer.entered:
		t.Fatal("a nudge started after Stop")
	case <-time.After(100 * time.Millisecond):
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

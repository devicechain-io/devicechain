// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// flakyWriter fails its first failures publishes and succeeds after that. It is what lets
// one test express "this command can never go out" and another express "this command went
// out on the second try", which are the two cases the bound has to tell apart.
//
// 🔴 IT COUNTS ON THE MAIN GOROUTINE'S CALL PATH ONLY. Nothing here spawns a goroutine, and
// nothing asserts from one: every deliverCommand below is called directly by the test, so a
// failed assertion is a failed test rather than a panic in a goroutine nobody is watching.
type flakyWriter struct {
	mu sync.Mutex
	// failures is how many publishes still have to fail before this writer starts
	// succeeding. A large value means "never succeeds".
	failures  int
	published int
	attempts  int
}

func (w *flakyWriter) WriteMessages(context.Context, ...messaging.Message) error {
	return w.write()
}

func (w *flakyWriter) WriteToDevice(context.Context, string, ...messaging.Message) error {
	return w.write()
}

func (w *flakyWriter) write() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.attempts++
	if w.failures > 0 {
		w.failures--
		return errors.New("publish rejected by the broker")
	}
	w.published++
	return nil
}

func (w *flakyWriter) publishes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.published
}

// attemptCount is every call that reached this writer, failed or not — the measure of
// whether the platform is still TRYING, which is the thing the bound has to end.
func (w *flakyWriter) attemptCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.attempts
}

func (w *flakyWriter) HandleResponse(error) {}

// exhaustionProc builds a processor over the real API with the dispatch counter wired, so
// the metric assertion measures the production increment rather than a test's own bookkeeping.
func exhaustionProc(api *model.Api, writer messaging.MessageWriter) *CommandDeliveryProcessor {
	return &CommandDeliveryProcessor{
		Api:                  api,
		DeviceCommandsWriter: writer,
		DeliveryMetrics: DeliveryMetrics{
			DispatchesExhausted: prometheus.NewCounter(prometheus.CounterOpts{
				Name: "dispatches_exhausted_total",
			}),
		},
	}
}

// TestACommandThatCanNeverBePublishedStopsAtTheBoundAndBlamesThePlatform is the gate on the
// whole change, driven through the real dispatch path over a real database.
//
// 🔴 THE BEHAVIOUR IT REPLACES IS NOT A STALL BUT A LIE THAT TAKES A WEEK TO TELL. A command
// nothing can publish was claimed, failed, released to QUEUED and claimed again on every
// sweep tick — at the default cadence about two futile publishes a minute, for the whole
// seven-day TTL — and then expired as TIMEOUT, which means "dispatched toward a device
// believed live and never answered". Nothing was ever dispatched. An operator reading that
// row is told to go and look at hardware that was never sent anything, and there is no
// column anywhere that would have told them otherwise.
//
// So this asserts three things, and the third is the one that makes the row USEFUL: the
// retries stop at the bound, the terminal is FAILED rather than TIMEOUT, and the row itself
// says the platform could not publish it.
func TestACommandThatCanNeverBePublishedStopsAtTheBoundAndBlamesThePlatform(t *testing.T) {
	api := realApi(t)
	// A small bound so the test states the number it is asserting rather than looping to
	// the platform default. It is set the way production sets it, through the same field.
	api.MaxDispatchFailures = 3
	ctx := core.WithTenant(context.Background(), "acme")

	created, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
		Token: "poison-1", DeviceToken: "pump-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}

	writer := &flakyWriter{failures: 100}
	proc := exhaustionProc(api, writer)

	// Attempt 1 and 2: the command must come BACK, or the bound is not a bound, it is a
	// one-strike rule wearing one.
	for attempt := 1; attempt <= 2; attempt++ {
		if err := proc.deliverCommand(ctx, created, pathSweep); err == nil {
			t.Fatalf("attempt %d: premise lost, the publish was expected to fail", attempt)
		}
		row := loadByToken(t, api, ctx, "poison-1")
		if row.Status != model.CommandQueued.String() {
			t.Fatalf("after failure %d the command is %s, want QUEUED; a command must be retried "+
				"before the bound is reached", attempt, row.Status)
		}
		// The stored value, not a log line: this is the counter the bound is read against,
		// and a counter that does not move makes the bound unreachable.
		if row.DispatchFailures != attempt {
			t.Fatalf("after failure %d the row records %d failed dispatches; the count must move "+
				"on every failure or the bound can never be reached", attempt, row.DispatchFailures)
		}
	}

	// Attempt 3 reaches the bound.
	if err := proc.deliverCommand(ctx, created, pathSweep); err == nil {
		t.Fatal("attempt 3: premise lost, the publish was expected to fail")
	}
	row := loadByToken(t, api, ctx, "poison-1")
	if row.Status != model.CommandFailed.String() {
		t.Fatalf("at the bound the command is %s, want FAILED; TIMEOUT and EXPIRED both assert "+
			"something about the device, and this command never reached one", row.Status)
	}
	if row.DispatchFailures != 3 {
		t.Fatalf("the exhausting failure recorded %d failed dispatches, want 3; the write that "+
			"fails the row must still count the failure that caused it", row.DispatchFailures)
	}
	if !row.Error.Valid || row.Error.String != model.UndispatchableReason {
		t.Fatalf("the failed command's error is %q; the row has to NAME the platform as the "+
			"cause, or FAILED here is indistinguishable from a device reporting a failure",
			row.Error.String)
	}
	if row.SentTime.Valid {
		t.Fatal("a command that was never published must not keep a sent_time; that column is " +
			"the record that makes a TIMEOUT-shaped reading of this row look justified")
	}

	// The metric, because the whole complaint is that this failure was invisible.
	if got := testutil.ToFloat64(proc.DispatchesExhausted); got != 1 {
		t.Fatalf("dispatches_exhausted_total is %v, want 1; without it a poison command is as "+
			"silent as it was before the bound existed", got)
	}

	// And the retries really do STOP: a further tick must not claim or publish it again.
	before := writer.attemptCount()
	if err := proc.deliverCommand(ctx, created, pathSweep); err != nil {
		t.Fatalf("a further dispatch of a terminal command must be a benign no-op, got %v", err)
	}
	if got := loadByToken(t, api, ctx, "poison-1"); got.Status != model.CommandFailed.String() {
		t.Fatalf("a terminal command was moved to %s by a later tick", got.Status)
	}
	if after := writer.attemptCount(); after != before {
		t.Fatalf("a terminal command was published again (%d attempts before, %d after); the "+
			"bound has to END the retries, not merely record that it was reached", before, after)
	}
}

// TestACommandThatSucceedsOnItsSecondAttemptIsNotTreatedAsPoison is the counterweight, and
// without it the test above passes just as happily against a delivery path that has stopped
// retrying ANYTHING.
//
// 🔴 THAT IS NOT A HYPOTHETICAL FAILURE MODE, IT IS THE OBVIOUS WAY TO GET THE FIRST TEST
// GREEN. A release that failed the row on its first failure, or a claim predicate that
// stopped accepting a re-queued command, would satisfy every assertion above — the poison
// command still reaches FAILED, still names the platform, still stops. What would be gone is
// the ordinary case this whole subsystem exists for: a broker that rejected one publish and
// accepted the next.
func TestACommandThatSucceedsOnItsSecondAttemptIsNotTreatedAsPoison(t *testing.T) {
	api := realApi(t)
	api.MaxDispatchFailures = 3
	ctx := core.WithTenant(context.Background(), "acme")

	created, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
		Token: "flaky-1", DeviceToken: "pump-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}

	writer := &flakyWriter{failures: 1}
	proc := exhaustionProc(api, writer)

	if err := proc.deliverCommand(ctx, created, pathSweep); err == nil {
		t.Fatal("premise lost: the first publish was expected to fail")
	}
	if got := statusOf(t, api, ctx, "flaky-1"); got != model.CommandQueued.String() {
		t.Fatalf("after one failed publish the command is %s, want QUEUED", got)
	}

	// The second attempt is the one under test. deliverCommand re-claims from QUEUED, so
	// this also pins that a released row is still claimable.
	if err := proc.deliverCommand(ctx, created, pathSweep); err != nil {
		t.Fatalf("the second attempt must succeed, got %v", err)
	}
	if writer.publishes() != 1 {
		t.Fatalf("the command was published %d times, want 1; a retry that never reaches the "+
			"broker is not a retry", writer.publishes())
	}

	row := loadByToken(t, api, ctx, "flaky-1")
	if row.Status != model.CommandSent.String() {
		t.Fatalf("a command that was published is %s, want SENT", row.Status)
	}
	if !row.SentTime.Valid {
		t.Fatal("a published command must carry a sent_time")
	}
	if row.Error.Valid {
		t.Fatalf("a delivered command carries error %q; a transient publish failure must leave "+
			"no failure text on a command that then went out", row.Error.String)
	}
	// 🔑 THE COUNT IS ONE, NOT ZERO AND NOT TWO. One publish genuinely failed and the row
	// records that; the successful dispatch is not a failure and must not be counted. A
	// count of two here would mean a delivered command is two-thirds of the way to being
	// declared poison for having been delivered.
	if row.DispatchFailures != 1 {
		t.Fatalf("the delivered command records %d failed dispatches, want 1", row.DispatchFailures)
	}
	if got := testutil.ToFloat64(proc.DispatchesExhausted); got != 0 {
		t.Fatalf("dispatches_exhausted_total is %v after a successful delivery, want 0", got)
	}
}

// TestAParkedCommandsWakesAreNotCountedAsFailedDispatches pins the reason the counter is on
// the FAILURE path rather than on the claim.
//
// 🔴 COUNTING EVERY CLAIM WOULD MAKE AN ORDINARY SLEEPER LOOK LIKE POISON. A queue-mode
// device is claimed and published to on each wake and PARKED again whenever it turns out to
// be asleep — the transport working exactly as designed. If those claims counted, such a
// command would cross the bound after enough wakes and be declared undeliverable for having
// been handled correctly dozens of times.
func TestAParkedCommandsWakesAreNotCountedAsFailedDispatches(t *testing.T) {
	api := realApi(t)
	api.MaxDispatchFailures = 3
	ctx := core.WithTenant(context.Background(), "acme")

	created, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
		Token: "sleeper-1", DeviceToken: "meter-1", Name: "read",
	})
	if err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}

	writer := &flakyWriter{}
	proc := exhaustionProc(api, writer)

	// Three successful dispatches, each handed back by a transport that found the device
	// asleep — one more than the bound, so a claim-counting implementation would have
	// failed the row by now.
	for wake := 1; wake <= 3; wake++ {
		if err := proc.deliverCommand(ctx, created, pathSweep); err != nil {
			t.Fatalf("wake %d: publish must succeed, got %v", wake, err)
		}
		row := loadByToken(t, api, ctx, "sleeper-1")
		if _, parked, err := api.ParkClaim(ctx, "sleeper-1", row.DispatchNonce.String); err != nil || !parked {
			t.Fatalf("wake %d: park must land (parked=%v err=%v)", wake, parked, err)
		}
	}

	row := loadByToken(t, api, ctx, "sleeper-1")
	if row.Status != model.CommandParked.String() {
		t.Fatalf("the sleeper's command is %s, want PARKED; a device that was asleep is not a "+
			"command the platform failed to publish", row.Status)
	}
	if row.DispatchFailures != 0 {
		t.Fatalf("the parked command records %d failed dispatches, want 0; a park is a "+
			"successful publish that found the device asleep, not a dispatch failure",
			row.DispatchFailures)
	}
}

// TestTheConstructorWiresTheExhaustionCounter closes the gap every test above leaves open:
// all of them build the processor by literal and set the counter themselves.
//
// 🔴 A COUNTER THIS PACKAGE TOLERATES AS NIL IS A COUNTER THAT CAN BE UNWIRED IN EVERY
// SHIPPED BINARY WITHOUT A SINGLE TEST NOTICING. Nil is skipped by design, so deleting the
// line that builds this one in NewDeliveryMetrics breaks nothing, fails nothing, and leaves
// the metric permanently absent — for a failure whose entire complaint was that it was
// invisible. So this DRIVES the path through a processor built from real instruments rather
// than reading the field back, following the rule the stranded-pass constructor test states.
//
// The instruments come from NewDeliveryMetrics rather than from the processor's own
// constructor because that is where they are built: the processor is constructed inside the
// NATS manager's oncreate callback, which runs on every start, so building a collector there
// would panic on the second registration when the service restarts in place.
func TestTheConstructorWiresTheExhaustionCounter(t *testing.T) {
	ms := &core.Microservice{FunctionalArea: "commanddeliveryexhaustion"}
	api := &fakeApi{releaseExhausts: true}

	proc := NewCommandDeliveryProcessor(ms, nil, &flakyWriter{failures: 100},
		core.NewNoOpLifecycleCallbacks(), api, nil, nil, nil, NewDeliveryMetrics(ms))

	if err := proc.deliverCommand(context.Background(),
		&model.Command{DeviceToken: "pump-1", Name: "reboot"}, pathSweep); err == nil {
		t.Fatal("premise lost: the publish was expected to fail")
	}

	if proc.DispatchesExhausted == nil {
		t.Fatal("the constructor left the exhaustion counter unwired; nil is tolerated at the " +
			"call site, so this is silent in production and invisible to every other test")
	}
	if got := testutil.ToFloat64(proc.DispatchesExhausted); got != 1 {
		t.Fatalf("a constructor-built processor counted %v exhausted dispatches, want 1", got)
	}
}

// TestAnExhaustedCommandsErrorNamesNothingInternal keeps the tenant-facing sentence honest.
//
// The Error column is read by the TENANT on its own command, and the underlying publish
// error's text can name brokers, subjects and in-cluster hosts. The count and the cause live
// where an operator reads them — the column beside it, and this service's own logs.
func TestAnExhaustedCommandsErrorNamesNothingInternal(t *testing.T) {
	reason := model.UndispatchableReason
	for _, leak := range []string{"broker", "nats", "subject", "stream", "sql", "host", "://"} {
		if strings.Contains(strings.ToLower(reason), leak) {
			t.Fatalf("the undispatchable reason contains %q; it is shown to the tenant and must "+
				"be a fixed sentence about the outcome, not about the platform's internals", leak)
		}
	}
	if !strings.Contains(strings.ToLower(reason), "never delivered") {
		t.Fatal("the undispatchable reason must say the command was never delivered; that is " +
			"the one fact separating it from a device that answered with a failure")
	}
}

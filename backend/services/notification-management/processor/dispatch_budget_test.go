// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-notification-management/config"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// tenantScoped is a live tenant's context, as both entry points build before calling in.
func tenantScoped() context.Context {
	return core.WithTenant(context.Background(), "acme")
}

// TestPerChannelWorstCaseArithmetic pins the FORMULA against hand-computed numbers.
//
// It is deliberately not derived from the constants it is checking: an expectation
// computed the same way as the code cannot catch the code computing it wrongly, and an
// expectation read out of a constant cannot catch that constant moving. These are the
// sums written out longhand — secret resolve + every attempt at the full timeout + the
// linear backoffs between attempts.
func TestPerChannelWorstCaseArithmetic(t *testing.T) {
	cases := []struct {
		attempts int
		timeout  time.Duration
		want     time.Duration
	}{
		// The shipped defaults: 5s secret + 3 × 10s + (500ms + 1000ms) backoff.
		{3, 10 * time.Second, 36500 * time.Millisecond},
		// One attempt: no backoff at all.
		{1, 10 * time.Second, 15 * time.Second},
		// Retry disabled by a nonsense value is clamped to one attempt, as the
		// constructor clamps it.
		{0, 10 * time.Second, 15 * time.Second},
		// Five attempts: 5s + 5 × 2s + (0.5 + 1 + 1.5 + 2)s.
		{5, 2 * time.Second, 20 * time.Second},
	}
	for _, c := range cases {
		if got := perChannelWorstCase(c.attempts, c.timeout); got != c.want {
			t.Fatalf("perChannelWorstCase(%d, %v) = %v, want %v", c.attempts, c.timeout, got, c.want)
		}
	}
}

// TestDispatchBudgetFitsUnderAckWait pins the arithmetic the duplicate-page defect turned
// on, against the REAL constants on both sides — the shipped config defaults and the
// platform's own messaging.AckWait — so that moving either one fails here, by name, with
// the reason.
//
// The defect: the config described the per-attempt timeout as if it bounded the whole
// dispatch. It never did. dispatch walks every matched channel sequentially, so the cost
// is per-channel worst case TIMES the number of channels, and nothing capped the number.
// Two slow-but-alive channels ran past AckWait, JetStream handed the message to a second
// worker while the first was still sending, and the second re-sent every channel from the
// top.
func TestDispatchBudgetFitsUnderAckWait(t *testing.T) {
	cfg := config.NewNotificationManagementConfiguration()
	worst := perChannelWorstCase(cfg.DeliveryAttempts, cfg.DeliveryTimeout())

	// 1. The budget is derived from AckWait and leaves the stated margin. This is what
	//    makes the whole dispatch finish before the broker decides it has not.
	if dispatchBudget+dispatchMargin != messaging.AckWait {
		t.Fatalf("dispatchBudget (%v) + dispatchMargin (%v) = %v, want messaging.AckWait (%v): "+
			"the budget must be DERIVED from the platform ack window, not chosen beside it",
			dispatchBudget, dispatchMargin, dispatchBudget+dispatchMargin, messaging.AckWait)
	}

	// 2. The bookkeeping write runs OUTSIDE the budget, on the parent context, so its own
	//    timeout has to fit in the margin or the pair of them can still cross AckWait.
	if recordTimeout >= dispatchMargin {
		t.Fatalf("recordTimeout (%v) must be smaller than dispatchMargin (%v): the post-delivery "+
			"state write runs off the parent context, so it spends the margin, and the margin "+
			"also has to cover the lifecycle gate's cold-cache fetch and the ack round trip",
			recordTimeout, dispatchMargin)
	}
	if dispatchBudget+recordTimeout >= messaging.AckWait {
		t.Fatalf("dispatchBudget (%v) + recordTimeout (%v) >= messaging.AckWait (%v): a dispatch "+
			"that spends its whole budget and then records the delivery would outlive the ack "+
			"window, which is the duplicate-page path this budget exists to close",
			dispatchBudget, recordTimeout, messaging.AckWait)
	}

	// 3. At the shipped defaults ONE channel's full retry policy must fit inside the
	//    budget. If it stops fitting, the budget is silently truncating retries an
	//    operator configured — a different bug from the one being fixed, and one that
	//    would otherwise show up only as a channel that mysteriously stopped retrying.
	if worst >= dispatchBudget {
		t.Fatalf("perChannelWorstCase at the shipped defaults (%v) >= dispatchBudget (%v): the "+
			"whole-dispatch budget would cut a SINGLE channel's retries short. Either lower "+
			"defaultDeliverySeconds/defaultDeliveryAttempts or re-derive the budget",
			worst, dispatchBudget)
	}

	// 4. 🔑 THE DESIGN STATEMENT: the budget is sized for ONE channel, and a second slow
	//    channel is cut BY DESIGN. This is the number that says so.
	//
	//    An earlier version of this comment called it a negative control and claimed that
	//    if two channels fit, the clamp becomes unreachable and the enforcement tests stop
	//    measuring production. Both halves are false, and worth writing down so nobody
	//    restores the reasoning: plan() caps nothing, so a THREE-channel plan still reaches
	//    the clamp at 2*worst <= budget; and TestDeliverAllStopsWhenTheBudgetIsSpent drives
	//    a 150ms budget against three channels, so it does not depend on the production
	//    constants at all.
	//
	//    What this actually forbids is lowering the per-attempt defaults far enough that
	//    two full channels fit — around defaultDeliverySeconds < 4.5s at three attempts.
	//    That is a deliberate ceiling, not an accident to be corrected: a tenant routing one
	//    alarm to two slow channels gets the first one's full retry policy and the second
	//    one cut, because finishing inside AckWait matters more than reaching every channel.
	//    🔴 So if this fires, the fix is NOT to raise the timeout back — it is to decide
	//    whether that trade still holds at the new defaults, and to re-derive the budget if
	//    it does not.
	if 2*worst <= dispatchBudget {
		t.Fatalf("two channels at the shipped defaults (2 x %v) now fit inside dispatchBudget "+
			"(%v). The budget is deliberately sized so the SECOND slow channel is cut; if the "+
			"defaults have dropped this far, re-derive the budget on purpose rather than "+
			"raising the timeout to silence this", worst, dispatchBudget)
	}
}

// stallingAdapter blocks until its context is done (or a hard stop elapses, so a broken
// budget fails the test instead of hanging it), recording how many times it was called.
type stallingAdapter struct {
	calls    int
	hardStop time.Duration
}

func (b *stallingAdapter) Deliver(ctx context.Context, _ *model.NotificationChannel, _ string,
	_ []string, _ *RenderedNotification) error {
	b.calls++
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(b.hardStop):
		return nil
	}
}

// TestDeliverAllStopsWhenTheBudgetIsSpent is the enforcement itself: once the shared
// dispatch budget is gone, the sequential walk stops instead of starting the next
// channel.
//
// The instrument is the ADAPTER CALL COUNT, not elapsed time. Without the budget check
// the loop would still return quickly — every later attempt inherits an already-cancelled
// context and fails immediately — so a timing assertion alone would pass over the bug.
// What actually differs is whether channels two and three were touched at all.
func TestDeliverAllStopsWhenTheBudgetIsSpent(t *testing.T) {
	ba := &stallingAdapter{hardStop: 10 * time.Second}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: ba})
	n.attempts = 1
	// Far larger than the budget below, so the per-attempt timeout cannot be what stops
	// the walk — only the shared budget can.
	n.timeout = time.Minute
	n.store = &fakeSecretStore{}

	deliveries := []delivery{
		{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP), recipients: []string{"a@x.com"}},
		{channel: enabledChannel("smtp-2", model.ChannelTypeSMTP), recipients: []string{"b@x.com"}},
		{channel: enabledChannel("smtp-3", model.ChannelTypeSMTP), recipients: []string{"c@x.com"}},
	}

	ctx, cancel := context.WithTimeout(tenantScoped(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	tally := n.deliverAll(ctx, deliveries, &RenderedNotification{}, "a1")
	elapsed := time.Since(start)

	if ba.calls != 1 {
		t.Fatalf("adapter called %d time(s), want 1: the walk must stop at the first channel "+
			"that spends the budget, not carry on starting deliveries it cannot finish", ba.calls)
	}
	if tally.delivered != 0 || tally.failed != 1 || tally.unattempted != 2 {
		t.Fatalf("tally = %+v, want delivered 0, failed 1, unattempted 2", tally)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("deliverAll took %v; the shared budget must bound the whole walk", elapsed)
	}
}

// TestDeliverAllDeliversEveryChannelWithinBudget is the counterweight to the test above.
// Without it, a deliverAll that refused to send anything at all would satisfy the clamp
// assertion perfectly while being a total notification outage.
func TestDeliverAllDeliversEveryChannelWithinBudget(t *testing.T) {
	fa := &fakeAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
	n.store = &fakeSecretStore{}
	deliveries := []delivery{
		{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP), recipients: []string{"a@x.com"}},
		{channel: enabledChannel("smtp-2", model.ChannelTypeSMTP), recipients: []string{"b@x.com"}},
	}

	tally := n.deliverAll(tenantScoped(), deliveries, &RenderedNotification{}, "a1")
	if tally.delivered != 2 || tally.unattempted != 0 {
		t.Fatalf("tally = %+v, want both channels delivered and none unattempted", tally)
	}
	if fa.calls != 2 {
		t.Fatalf("adapter called %d time(s), want 2", fa.calls)
	}
}

// TestASecretStoreErrorCountsAsAFailedChannel pins a disposition that used to fall
// through the counters entirely: a channel whose secret could not be resolved was skipped
// without being counted anywhere. A plan of one such channel plus one egress-refused
// channel therefore looked like "every target was refused" and got acked, dropping the
// retry the store error had earned.
func TestASecretStoreErrorCountsAsAFailedChannel(t *testing.T) {
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: &fakeAdapter{}})
	n.store = &fakeSecretStore{err: context.DeadlineExceeded}
	deliveries := []delivery{
		{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP), recipients: []string{"a@x.com"}},
	}

	tally := n.deliverAll(tenantScoped(), deliveries, &RenderedNotification{}, "a1")
	if tally.failed != 1 || tally.delivered != 0 || tally.refused != 0 {
		t.Fatalf("tally = %+v, want the unresolvable channel counted as failed", tally)
	}
	if tally.retryable() != 1 {
		t.Fatalf("retryable() = %d, want 1: a store error is transient and owes a redelivery",
			tally.retryable())
	}
}

// TestUnattemptedChannelsAreOwedARedelivery pins the DISPOSITION of a budget cut. A
// channel the budget never let us try has earned a retry exactly as a failed one has —
// only an egress REFUSAL is terminal — so a plan that ends with nothing delivered and
// something unattempted must not be acked away as "every destination was refused".
func TestUnattemptedChannelsAreOwedARedelivery(t *testing.T) {
	cases := []struct {
		name  string
		tally dispatchTally
		want  int
	}{
		{"all refused is terminal", dispatchTally{refused: 2}, 0},
		{"a failure is owed a retry", dispatchTally{refused: 1, failed: 1}, 1},
		{"an unattempted channel is owed a retry", dispatchTally{refused: 1, unattempted: 1}, 1},
		{"both count", dispatchTally{failed: 1, unattempted: 2}, 3},
	}
	for _, c := range cases {
		if got := c.tally.retryable(); got != c.want {
			t.Fatalf("%s: retryable() = %d, want %d", c.name, got, c.want)
		}
	}
}

// deadlineRecordingStore records the deadline on the context its Resolve was handed.
type deadlineRecordingStore struct {
	fakeSecretStore
	hadDeadline bool
	remaining   time.Duration
}

func (d *deadlineRecordingStore) Resolve(ctx context.Context, _ secrets.SecretRef) ([]byte, error) {
	if dl, ok := ctx.Deadline(); ok {
		d.hadDeadline = true
		d.remaining = time.Until(dl)
	}
	return nil, secrets.ErrSecretNotFound
}

// TestResolveChannelSecretIsBounded pins the deadline on the secret lookup — the one step
// of a delivery that had none.
//
// The caller's context is context.Background(), on purpose: that is exactly what a
// dispatch worker holds (the pool runs on a background context so it drains on shutdown),
// so a lookup that only honours its caller's deadline has no deadline at all, and a hung
// secret store stalls one of five workers forever with graceful shutdown behind it.
//
// The instrument is the deadline the store OBSERVES rather than wall-clock elapsed time.
// A timing test would have to actually wait out secretResolveTimeout to distinguish
// bounded from unbounded, which buys nothing and costs five seconds a run; what the fix
// changes is whether the store is handed a deadline at all.
func TestResolveChannelSecretIsBounded(t *testing.T) {
	store := &deadlineRecordingStore{}
	n := testNotifier(nil)
	n.store = store

	if _, err := n.resolveChannelSecret(tenantScoped(), 1); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !store.hadDeadline {
		t.Fatal("the secret store was handed a context with NO deadline: on the dispatch path " +
			"the caller's context is the worker's context.Background(), which never fires, so a " +
			"hung store stalls the worker indefinitely")
	}
	if store.remaining > secretResolveTimeout || store.remaining < secretResolveTimeout-time.Second {
		t.Fatalf("secret resolve deadline was %v away, want ~%v (secretResolveTimeout)",
			store.remaining, secretResolveTimeout)
	}
}

// TestResolveChannelSecretHonoursATighterCaller is the counterweight: the secret lookup's
// own timeout must NARROW the caller's budget, never replace it. If it detached from the
// caller, a secret resolve would be able to outlive the whole-dispatch budget it is
// supposed to be spent inside.
func TestResolveChannelSecretHonoursATighterCaller(t *testing.T) {
	store := &deadlineRecordingStore{}
	n := testNotifier(nil)
	n.store = store

	ctx, cancel := context.WithTimeout(tenantScoped(), 100*time.Millisecond)
	defer cancel()
	if _, err := n.resolveChannelSecret(ctx, 1); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !store.hadDeadline || store.remaining > time.Second {
		t.Fatalf("secret resolve deadline was %v away (hadDeadline=%v), want the caller's tighter "+
			"~100ms: the lookup's own timeout must narrow the dispatch budget, not escape it",
			store.remaining, store.hadDeadline)
	}
}

// deadlineRecordingAdapter records the deadline on the context a delivery is handed, so a
// test can assert WHICH budget the dispatch is spending.
type deadlineRecordingAdapter struct {
	calls       int
	hadDeadline bool
	remaining   time.Duration
}

func (d *deadlineRecordingAdapter) Deliver(ctx context.Context, _ *model.NotificationChannel,
	_ string, _ []string, _ *RenderedNotification) error {
	d.calls++
	if dl, ok := ctx.Deadline(); ok {
		d.hadDeadline = true
		d.remaining = time.Until(dl)
	}
	return nil
}

// seedRoutedAlarm gives an api one enabled SMTP channel and one enabled policy routing
// every severity to it, which is the minimum for dispatch to plan a delivery.
func seedRoutedAlarm(t *testing.T, api *model.Api, ctx context.Context) {
	t.Helper()
	if _, err := api.CreateNotificationChannel(ctx, &model.NotificationChannelCreateRequest{
		Token: "smtp-1", ChannelType: model.ChannelTypeSMTP, Enabled: true,
		Config: strPointer(`{"host":"smtp.example.com","from":"alarms@example.com"}`),
	}); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := api.CreateNotificationPolicy(ctx, &model.NotificationPolicyCreateRequest{
		Token: "p1", Enabled: true,
		Rules: []*model.NotificationRuleCreateRequest{{
			Severity: model.SeverityAny, ChannelToken: "smtp-1",
			Recipients: strPointer(`["ops@x.com"]`),
		}},
	}); err != nil {
		t.Fatalf("create policy: %v", err)
	}
}

func strPointer(s string) *string { return &s }

// TestDispatchSpendsTheWholeDispatchBudget pins that the deadline is applied AT THE CALL
// SITE, not merely defined as a constant.
//
// The instrument is the deadline the adapter is handed. The per-attempt timeout is set far
// larger than the budget on purpose: whichever of the two is tighter is what the adapter
// sees, so a dispatch that never wrapped its context would hand over the two-minute
// per-attempt deadline (or none at all) instead of the budget. Nothing else in this file
// can tell those apart.
func TestDispatchSpendsTheWholeDispatchBudget(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedRoutedAlarm(t, api, ctx)

	da := &deadlineRecordingAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: da})
	n.api = api
	n.store = &fakeSecretStore{}
	n.timeout = 2 * time.Minute // far larger than dispatchBudget

	if err := n.dispatch(ctx, raisedEvent("CRITICAL")); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if da.calls != 1 {
		t.Fatalf("adapter called %d time(s), want 1: the fixture must reach a delivery or this "+
			"test measures nothing", da.calls)
	}
	if !da.hadDeadline {
		t.Fatal("the delivery ran with NO deadline: dispatch did not apply the whole-dispatch " +
			"budget, so a slow plan can outlive AckWait and be redelivered underneath the worker")
	}
	if da.remaining > dispatchBudget || da.remaining < dispatchBudget-2*time.Second {
		t.Fatalf("the delivery's deadline was %v away, want ~%v (dispatchBudget); a longer one "+
			"means the dispatch is bounded by the per-attempt timeout instead of by the budget",
			da.remaining, dispatchBudget)
	}
}

// TestEscalateSpendsTheWholeDispatchBudget is the same assertion on the scheduler's path.
// It is asserted separately rather than assumed from the dispatch test because the two
// derive their contexts independently — and an unbounded escalation stalls a loop that
// walks EVERY tenant on one goroutine, so it is an outage for the tenants behind it.
func TestEscalateSpendsTheWholeDispatchBudget(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	state, policies, now := escalationFixture()
	if err := api.RecordNotification(ctx, state.AlarmToken, state.AlarmKey, state.Severity,
		now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	da := &deadlineRecordingAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: da})
	n.api = api
	n.store = &fakeSecretStore{}
	n.timeout = 2 * time.Minute

	if err := n.Escalate(ctx, state, policies, now, 5); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if da.calls != 1 {
		t.Fatalf("adapter called %d time(s), want 1", da.calls)
	}
	if !da.hadDeadline || da.remaining > dispatchBudget || da.remaining < dispatchBudget-2*time.Second {
		t.Fatalf("the escalation delivery's deadline was %v away (hadDeadline=%v), want ~%v "+
			"(dispatchBudget)", da.remaining, da.hadDeadline, dispatchBudget)
	}
}

// slowSucceedingAdapter delivers successfully, but only after taking longer than the
// notifier's whole-dispatch budget — the state the bookkeeping write has to survive.
type slowSucceedingAdapter struct {
	calls int
	delay time.Duration
}

func (s *slowSucceedingAdapter) Deliver(_ context.Context, _ *model.NotificationChannel,
	_ string, _ []string, _ *RenderedNotification) error {
	s.calls++
	time.Sleep(s.delay)
	return nil
}

// TestTheBookkeepingWriteOutlivesASpentBudget pins the one thing that must NOT be inside
// the budget. RecordNotification is what puts a paged alarm into the escalation substrate;
// running it on the exhausted dispatch context would mean a dispatch that spent its whole
// budget delivering could never record that it delivered, and the page it just sent would
// never be followed up.
//
// 🔑 THE BUDGET IS SHRUNK TO 50ms HERE, AND THAT IS THE WHOLE TEST. A mutation run found
// this assertion surviving the write being moved back onto the spent dispatch context —
// not because the assertion was weak, but because at the shipped 40-second budget the
// input class it is about ("the budget is gone by the time the sends finish") is
// unreachable from a unit test. The missing thing was an INPUT, not an assertion, which is
// why PolicyNotifier carries the budget as a field.
func TestTheBookkeepingWriteOutlivesASpentBudget(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedRoutedAlarm(t, api, ctx)

	sa := &slowSucceedingAdapter{delay: 120 * time.Millisecond}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: sa})
	n.api = api
	n.store = &fakeSecretStore{}
	n.budget = 50 * time.Millisecond

	event := raisedEvent("CRITICAL")
	if err := n.dispatch(ctx, event); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if sa.calls != 1 {
		t.Fatalf("adapter called %d time(s), want 1: the send must have happened, or there is "+
			"nothing for the bookkeeping to record", sa.calls)
	}
	states, err := api.NotificationStatesByAlarmToken(ctx, []string{event.AlarmToken})
	if err != nil || len(states) != 1 {
		t.Fatalf("the delivery was not recorded (%d state row(s), err %v): a page went out and "+
			"the alarm never entered the escalation substrate, so nothing will follow it up",
			len(states), err)
	}
	if states[0].NotifyCount != 1 || !states[0].LastNotifiedAt.Valid {
		t.Fatalf("state row does not record the notification: %+v", states[0])
	}
}

// TestASpentBudgetStillReportsTheDeliveryItMade is the counterweight: the shrunk budget
// must not have turned the dispatch into a failure. It delivered, so it must ACK — a
// redelivery here would re-send the channel that already succeeded.
func TestASpentBudgetStillAcks(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedRoutedAlarm(t, api, ctx)

	sa := &slowSucceedingAdapter{delay: 120 * time.Millisecond}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: sa})
	n.api = api
	n.store = &fakeSecretStore{}
	n.budget = 50 * time.Millisecond

	if err := n.dispatch(ctx, raisedEvent("CRITICAL")); err != nil {
		t.Fatalf("a dispatch that delivered must return nil so the consumer acks; got %v", err)
	}
}

// TestTheConstructorSetsTheBudgetFromTheConstant covers the plumbing every test above
// skips. They all build a PolicyNotifier by struct literal and set budget directly, so
// they measure the FIELD; the shipped binary reaches it only through NewPolicyNotifier,
// and with nothing covering that, dropping the assignment would leave production on
// whatever the fallback happened to be.
func TestTheConstructorSetsTheBudgetFromTheConstant(t *testing.T) {
	n := NewPolicyNotifier(nil, nil, 3, 10*time.Second, nil, nil)
	if n.budget != dispatchBudget {
		t.Fatalf("constructor set budget = %v, want dispatchBudget (%v)", n.budget, dispatchBudget)
	}
	// And a literal-built notifier still gets the platform budget rather than no budget.
	if (&PolicyNotifier{}).wholeDispatchBudget() != dispatchBudget {
		t.Fatal("an unset budget must fall back to dispatchBudget, not to zero (no deadline at all)")
	}
}

// captureLogs redirects the package-level zerolog writer for the duration of one test and
// returns a reader for what was written. The log line is the ONLY runtime signal a
// budget-cut channel produces, so it is the thing under test here, not a side effect of it.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = prev })
	return &buf
}

// lineContaining returns the single captured log line whose message contains want, failing
// the test if there is not exactly one.
//
// 🔑 IT EXISTS BECAUSE ASSERTING ON THE WHOLE BUFFER MEASURED THE WRONG LINE. A delivery
// emits a per-attempt warn that carries its own "attempt" field, so a substring check for
// `"attempt":1` across the buffer passed whether or not the budget line carried the field
// at all — a mutation dropping it from the budget line survived. Fields belong to a LINE,
// so the line has to be selected before its fields are read.
func lineContaining(t *testing.T, buf *bytes.Buffer, want string) string {
	t.Helper()
	var found []string
	for _, ln := range strings.Split(buf.String(), "\n") {
		if strings.Contains(ln, want) {
			found = append(found, ln)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one log line containing %q, got %d:\n%s",
			want, len(found), buf.String())
	}
	return found[0]
}

// alwaysFailingAdapter fails every attempt with an ordinary transient error.
type alwaysFailingAdapter struct{ calls int }

func (a *alwaysFailingAdapter) Deliver(context.Context, *model.NotificationChannel, string,
	[]string, *RenderedNotification) error {
	a.calls++
	return errors.New("connection refused")
}

// 🔴 A BUDGET CUT AND AN EXHAUSTED RETRY POLICY ARE DIFFERENT FACTS AND MUST NOT READ ALIKE.
//
// Both surface from the adapter as the same error — a per-attempt timeout and the whole
// dispatch budget are both context.DeadlineExceeded — so the error text cannot separate
// them and only ctx.Err() can. Reporting a budget cut as "after exhausting attempts" tells
// an operator at 3am that their endpoint is slow, when the truth is that the platform
// stopped the dispatch before their configured retries were spent. The startup warning
// does not cover it: that fires only for a single channel that statically cannot fit, so a
// multi-channel plan cut at runtime has no other signal at all.
func TestABudgetCutIsNotReportedAsExhaustedAttempts(t *testing.T) {
	buf := captureLogs(t)
	fa := &alwaysFailingAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
	n.attempts = 1
	n.timeout = time.Minute
	d := delivery{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP), recipients: []string{"x@x.com"}}

	// An already-spent budget, which is exactly what the second channel of a slow plan sees.
	ctx, cancel := context.WithTimeout(tenantScoped(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()

	if got := n.deliverWithRetry(ctx, d, &RenderedNotification{}); got != deliveryFailed {
		t.Fatalf("deliverWithRetry = %v, want deliveryFailed", got)
	}
	out := buf.String()
	if !strings.Contains(out, "whole-dispatch budget was spent") {
		t.Fatalf("a channel cut by the budget produced no line saying so; the only signal an "+
			"operator gets is:\n%s", out)
	}
	if strings.Contains(out, "after exhausting attempts") {
		t.Fatalf("a budget cut was reported as an exhausted retry policy, which sends an "+
			"operator to look at their endpoint instead of at the dispatch:\n%s", out)
	}
}

// The counterweight, and the one that stops the fix from relabelling every failure as a
// budget cut: a channel that genuinely spent its own attempts, with budget to spare, must
// still say so. Without this, "always log the budget line" would pass the test above while
// hiding a real endpoint outage.
func TestAnExhaustedRetryPolicyStillSaysSo(t *testing.T) {
	buf := captureLogs(t)
	fa := &alwaysFailingAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
	n.attempts = 2
	n.timeout = 50 * time.Millisecond
	d := delivery{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP), recipients: []string{"x@x.com"}}

	if got := n.deliverWithRetry(tenantScoped(), d, &RenderedNotification{}); got != deliveryFailed {
		t.Fatalf("deliverWithRetry = %v, want deliveryFailed", got)
	}
	if fa.calls != 2 {
		t.Fatalf("adapter called %d time(s), want both attempts", fa.calls)
	}
	out := buf.String()
	if !strings.Contains(out, "after exhausting attempts") {
		t.Fatalf("a genuinely exhausted retry policy did not say so:\n%s", out)
	}
	if strings.Contains(out, "whole-dispatch budget was spent") {
		t.Fatalf("an endpoint outage was blamed on the dispatch budget, which hides a real "+
			"failure an operator needs to see:\n%s", out)
	}
}

// The mid-backoff exit is the same fact through a different door, and it used to be the
// only exit in deliverWithRetry that logged NOTHING — a channel dropped there left no
// trace at all. It also carries the attempt it reached, which is the number that separates
// "the platform cut a 3-attempt policy at 1" from "this channel spent the whole budget".
func TestABudgetCutMidBackoffIsReported(t *testing.T) {
	buf := captureLogs(t)
	fa := &alwaysFailingAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
	n.attempts = 3
	n.timeout = time.Minute
	d := delivery{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP), recipients: []string{"x@x.com"}}

	// Enough budget for the first attempt to run, not enough to survive the backoff before
	// the second — so the loop exits through the select, not through its own condition.
	ctx, cancel := context.WithTimeout(tenantScoped(), 50*time.Millisecond)
	defer cancel()

	if got := n.deliverWithRetry(ctx, d, &RenderedNotification{}); got != deliveryFailed {
		t.Fatalf("deliverWithRetry = %v, want deliveryFailed", got)
	}
	if fa.calls != 1 {
		t.Fatalf("adapter called %d time(s), want 1: the budget must have cut it during the "+
			"backoff, or this test is exercising the loop-condition exit instead", fa.calls)
	}
	if !strings.Contains(buf.String(), "whole-dispatch budget was spent") {
		t.Fatalf("a channel cut mid-backoff was dropped with no log line at all:\n%s", buf.String())
	}
	// Read the fields OFF THE BUDGET LINE, not off the buffer: the per-attempt warn carries
	// an "attempt" field of its own and would satisfy a buffer-wide check by itself.
	line := lineContaining(t, buf, "whole-dispatch budget was spent")
	if !strings.Contains(line, `"attempt":1`) || !strings.Contains(line, `"attempts":3`) {
		t.Fatalf("the budget line must name the attempt reached out of those configured, which "+
			"is what tells an operator the platform cut a 3-attempt policy at 1:\n%s", line)
	}
}

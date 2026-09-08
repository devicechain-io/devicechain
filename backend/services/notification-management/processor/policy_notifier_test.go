// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-notification-management/model"
	"gorm.io/datatypes"
)

// fakeAdapter fails its first failTimes calls, then succeeds; it records the last
// recipients it was asked to deliver to.
type fakeAdapter struct {
	failTimes int
	calls     int
}

func (f *fakeAdapter) Deliver(_ context.Context, _ *model.NotificationChannel, _ string, _ []string, _ *RenderedNotification) error {
	f.calls++
	if f.calls <= f.failTimes {
		return errors.New("boom")
	}
	return nil
}

func testNotifier(adapters map[string]ChannelAdapter) *PolicyNotifier {
	return &PolicyNotifier{adapters: adapters, attempts: 3, timeout: time.Second}
}

func enabledChannel(token, ctype string) *model.NotificationChannel {
	c := channelWith(token, ctype, "")
	c.Enabled = true
	return c
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func rule(severity string, channel *model.NotificationChannel, recipients ...string) model.NotificationRule {
	r := model.NotificationRule{Severity: severity, Channel: channel}
	if len(recipients) > 0 {
		j := datatypes.JSON(mustJSON(recipients))
		r.Recipients = &j
	}
	return r
}

func policy(token string, rules ...model.NotificationRule) *model.NotificationPolicy {
	p := &model.NotificationPolicy{Enabled: true, Rules: rules}
	p.Token = token
	return p
}

func raisedEvent(severity string) *dmmodel.AlarmStateChangeEvent {
	return &dmmodel.AlarmStateChangeEvent{
		EventType: dmmodel.AlarmEventRaised, AlarmToken: "a1", AlarmKey: "k",
		Severity: severity, State: "ACTIVE", OccurredTime: time.Now().UTC(),
	}
}

func TestSeverityMatches(t *testing.T) {
	if !severityMatches(model.SeverityAny, "CRITICAL") {
		t.Fatal("wildcard should match")
	}
	if !severityMatches("CRITICAL", "CRITICAL") {
		t.Fatal("exact should match")
	}
	if severityMatches("MAJOR", "CRITICAL") {
		t.Fatal("mismatch should not match")
	}
}

func TestPlanDedupesAndFilters(t *testing.T) {
	adapters := map[string]ChannelAdapter{model.ChannelTypeSMTP: &fakeAdapter{}}
	n := testNotifier(adapters)
	smtp := enabledChannel("smtp-1", model.ChannelTypeSMTP)

	// Two policies both routing a CRITICAL to the same channel + recipients → one send.
	p1 := policy("p1", rule(model.SeverityAny, smtp, "ops@x.com"))
	p2 := policy("p2", rule("CRITICAL", smtp, "ops@x.com"))
	got := n.plan(raisedEvent("CRITICAL"), []*model.NotificationPolicy{p1, p2}, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 deduped delivery, got %d", len(got))
	}

	// Different recipients → two distinct deliveries.
	p3 := policy("p3", rule("CRITICAL", smtp, "pager@x.com"))
	got = n.plan(raisedEvent("CRITICAL"), []*model.NotificationPolicy{p2, p3}, nil)
	if len(got) != 2 {
		t.Fatalf("expected 2 deliveries, got %d", len(got))
	}

	// Non-matching severity → nothing.
	got = n.plan(raisedEvent("MINOR"), []*model.NotificationPolicy{p2}, nil)
	if len(got) != 0 {
		t.Fatalf("expected 0 for non-matching severity, got %d", len(got))
	}
}

func TestPlanSkipsDisabledMissingAndScoped(t *testing.T) {
	adapters := map[string]ChannelAdapter{model.ChannelTypeSMTP: &fakeAdapter{}}
	n := testNotifier(adapters)

	// Disabled channel → skipped.
	disabled := channelWith("smtp-off", model.ChannelTypeSMTP, "")
	if got := n.plan(raisedEvent("CRITICAL"), []*model.NotificationPolicy{
		policy("p", rule("CRITICAL", disabled, "x@x.com")),
	}, nil); len(got) != 0 {
		t.Fatalf("disabled channel should be skipped, got %d", len(got))
	}

	// Channel type with no adapter → skipped.
	noAdapter := enabledChannel("sms-1", "sms")
	if got := n.plan(raisedEvent("CRITICAL"), []*model.NotificationPolicy{
		policy("p", rule("CRITICAL", noAdapter, "x@x.com")),
	}, nil); len(got) != 0 {
		t.Fatalf("no-adapter channel should be skipped, got %d", len(got))
	}

	// Device-type-scoped policy → skipped (scoping deferred).
	scoped := policy("scoped", rule("CRITICAL", enabledChannel("smtp-2", model.ChannelTypeSMTP), "x@x.com"))
	scoped.DeviceTypeToken = sql.NullString{String: "thermostat", Valid: true}
	if got := n.plan(raisedEvent("CRITICAL"), []*model.NotificationPolicy{scoped}, nil); len(got) != 0 {
		t.Fatalf("device-type-scoped policy should be skipped, got %d", len(got))
	}

	// Nil channel (dangling rule after channel delete) → skipped, no panic.
	if got := n.plan(raisedEvent("CRITICAL"), []*model.NotificationPolicy{
		policy("p", model.NotificationRule{Severity: "CRITICAL", Channel: nil}),
	}, nil); len(got) != 0 {
		t.Fatalf("nil channel should be skipped, got %d", len(got))
	}
}

func TestThrottled(t *testing.T) {
	n := testNotifier(nil)
	throttle := int64(300)
	p := &model.NotificationPolicy{ThrottleSeconds: sql.NullInt64{Int64: throttle, Valid: true}}

	// No state → never throttled.
	if n.throttled(raisedEvent("CRITICAL"), p, nil) {
		t.Fatal("no state should not throttle")
	}

	now := time.Now().UTC()
	recent := &model.NotificationState{LastNotifiedAt: sql.NullTime{Time: now.Add(-time.Minute), Valid: true}}
	raised := raisedEvent("CRITICAL")
	raised.OccurredTime = now
	if !n.throttled(raised, p, recent) {
		t.Fatal("RAISED within window should throttle")
	}

	// ESCALATED bypasses the throttle (worsening is a new fact).
	esc := raisedEvent("CRITICAL")
	esc.EventType = dmmodel.AlarmEventEscalated
	esc.OccurredTime = now
	if n.throttled(esc, p, recent) {
		t.Fatal("ESCALATED should bypass throttle")
	}

	// Outside the window → not throttled.
	old := &model.NotificationState{LastNotifiedAt: sql.NullTime{Time: now.Add(-time.Hour), Valid: true}}
	if n.throttled(raised, p, old) {
		t.Fatal("outside window should not throttle")
	}
}

func TestDeliverWithRetry(t *testing.T) {
	// Fails twice then succeeds within the 3-attempt budget.
	fa := &fakeAdapter{failTimes: 2}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
	n.timeout = 50 * time.Millisecond
	d := delivery{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP), recipients: []string{"x@x.com"}}
	if n.deliverWithRetry(context.Background(), d, &RenderedNotification{}) != deliveryOK {
		t.Fatalf("expected eventual success, calls=%d", fa.calls)
	}
	if fa.calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", fa.calls)
	}

	// Always fails → false after exhausting attempts.
	fa2 := &fakeAdapter{failTimes: 99}
	n2 := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa2})
	n2.timeout = 50 * time.Millisecond
	if n2.deliverWithRetry(context.Background(), d, &RenderedNotification{}) == deliveryOK {
		t.Fatal("expected failure")
	}
	if fa2.calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", fa2.calls)
	}
}

// escalatingPolicy builds an enabled policy with escalation configured.
func escalatingPolicy(token string, afterSeconds int64, maxEsc int64, rules ...model.NotificationRule) *model.NotificationPolicy {
	p := policy(token, rules...)
	p.EscalateAfterSeconds = sql.NullInt64{Int64: afterSeconds, Valid: true}
	if maxEsc > 0 {
		p.MaxEscalations = sql.NullInt64{Int64: maxEsc, Valid: true}
	}
	return p
}

func openState(severity string, lastNotified time.Time, level int) *model.NotificationState {
	return &model.NotificationState{
		AlarmToken: "a1", AlarmKey: "k", Severity: severity,
		LastNotifiedAt:  sql.NullTime{Time: lastNotified, Valid: true},
		EscalationLevel: level,
	}
}

func TestPlanEscalation(t *testing.T) {
	adapters := map[string]ChannelAdapter{model.ChannelTypeSMTP: &fakeAdapter{}}
	n := testNotifier(adapters)
	smtp := enabledChannel("smtp-1", model.ChannelTypeSMTP)
	now := time.Now().UTC()
	defaultMax := 5

	// Window elapsed, under cap → one delivery.
	p := escalatingPolicy("p", 300, 0, rule("CRITICAL", smtp, "ops@x.com"))
	state := openState("CRITICAL", now.Add(-10*time.Minute), 1)
	if got := n.planEscalation(state, []*model.NotificationPolicy{p}, now, defaultMax); len(got) != 1 {
		t.Fatalf("due escalation: expected 1 delivery, got %d", len(got))
	}

	// Window not yet elapsed → nothing.
	fresh := openState("CRITICAL", now.Add(-1*time.Minute), 1)
	if got := n.planEscalation(fresh, []*model.NotificationPolicy{p}, now, defaultMax); len(got) != 0 {
		t.Fatalf("not-due escalation: expected 0, got %d", len(got))
	}

	// Escalation disabled (no EscalateAfterSeconds) → nothing.
	noEsc := policy("no-esc", rule("CRITICAL", smtp, "ops@x.com"))
	if got := n.planEscalation(state, []*model.NotificationPolicy{noEsc}, now, defaultMax); len(got) != 0 {
		t.Fatalf("disabled escalation: expected 0, got %d", len(got))
	}

	// Cap reached (policy MaxEscalations = 2, level = 2) → nothing.
	capped := escalatingPolicy("capped", 300, 2, rule("CRITICAL", smtp, "ops@x.com"))
	atCap := openState("CRITICAL", now.Add(-10*time.Minute), 2)
	if got := n.planEscalation(atCap, []*model.NotificationPolicy{capped}, now, defaultMax); len(got) != 0 {
		t.Fatalf("capped escalation: expected 0, got %d", len(got))
	}

	// Default cap applies when policy sets none (level = defaultMax) → nothing.
	atDefaultCap := openState("CRITICAL", now.Add(-10*time.Minute), defaultMax)
	if got := n.planEscalation(atDefaultCap, []*model.NotificationPolicy{p}, now, defaultMax); len(got) != 0 {
		t.Fatalf("default-cap escalation: expected 0, got %d", len(got))
	}

	// Severity mismatch → nothing.
	if got := n.planEscalation(openState("MINOR", now.Add(-10*time.Minute), 0),
		[]*model.NotificationPolicy{escalatingPolicy("q", 300, 0, rule("CRITICAL", smtp, "ops@x.com"))},
		now, defaultMax); len(got) != 0 {
		t.Fatalf("severity mismatch: expected 0, got %d", len(got))
	}

	// Two escalating policies to the same channel+recipients → deduped to one.
	p2 := escalatingPolicy("p2", 300, 0, rule(model.SeverityAny, smtp, "ops@x.com"))
	if got := n.planEscalation(state, []*model.NotificationPolicy{p, p2}, now, defaultMax); len(got) != 1 {
		t.Fatalf("dedup: expected 1 delivery, got %d", len(got))
	}
}

// fakeSecretStore is a minimal SecretStore for exercising the dispatcher's secret
// resolution: Resolve returns value/ErrSecretNotFound/err per its fields.
type fakeSecretStore struct {
	value string
	found bool
	err   error
}

func (f *fakeSecretStore) Put(context.Context, secrets.SecretRef, []byte) error    { return nil }
func (f *fakeSecretStore) Rotate(context.Context, secrets.SecretRef, []byte) error { return nil }
func (f *fakeSecretStore) Delete(context.Context, secrets.SecretRef) error         { return nil }
func (f *fakeSecretStore) Exists(context.Context, secrets.SecretRef) (bool, error) {
	return f.found, nil
}

func (f *fakeSecretStore) Resolve(context.Context, secrets.SecretRef) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	if !f.found {
		return nil, secrets.ErrSecretNotFound
	}
	return []byte(f.value), nil
}

// resolveChannelSecret returns the stored value, maps "no secret" to an empty string
// (so a secretless channel still delivers), propagates a real store error (so the
// caller can treat it as a transient delivery failure), and fails closed with no
// tenant in context.
func TestResolveChannelSecret(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "A")

	// A stored secret round-trips.
	n := &PolicyNotifier{store: &fakeSecretStore{value: "sekret", found: true}}
	if v, err := n.resolveChannelSecret(ctx, 7); err != nil || v != "sekret" {
		t.Fatalf("resolve found: v=%q err=%v", v, err)
	}

	// No secret stored → empty string, not an error.
	n = &PolicyNotifier{store: &fakeSecretStore{}}
	if v, err := n.resolveChannelSecret(ctx, 7); err != nil || v != "" {
		t.Fatalf("resolve not-found: v=%q err=%v", v, err)
	}

	// A genuine store error is propagated (caller treats it as transient).
	n = &PolicyNotifier{store: &fakeSecretStore{err: errors.New("boom")}}
	if _, err := n.resolveChannelSecret(ctx, 7); err == nil {
		t.Fatal("expected store error to propagate")
	}

	// No tenant in context fails closed (never a cross-tenant resolve).
	n = &PolicyNotifier{store: &fakeSecretStore{value: "x", found: true}}
	if _, err := n.resolveChannelSecret(context.Background(), 7); err == nil {
		t.Fatal("expected fail-closed with no tenant")
	}
}

func TestParseRecipients(t *testing.T) {
	if parseRecipients(nil) != nil {
		t.Fatal("nil → nil")
	}
	j := datatypes.JSON(mustJSON([]string{"a@x.com", "b@x.com"}))
	got := parseRecipients(&j)
	if len(got) != 2 || got[0] != "a@x.com" {
		t.Fatalf("recipients = %v", got)
	}
	bad := datatypes.JSON([]byte(`{"not":"an array"}`))
	if r := parseRecipients(&bad); r != nil {
		t.Fatalf("garbage → nil, got %v", r)
	}
}

// ---------------------------------------------------------------------------
// ADR-077: a deleted tenant is never paged.
//
// This service EMITS — email and webhook POSTs — so a refusal that arrives late is
// irreversible in a way a late refusal on an ingest path is not. The tests below cover
// all three places the gate is consulted, and each is consulted for a different reason:
// dispatch so the durable consumer acks, Escalate so nothing is written before the
// refusal, and deliverWithRetry as the funnel a future caller inherits.

// gatedNotifier builds a notifier whose lifecycle gate answers `deleted` for every tenant.
//
// 🔑 ITS api IS NIL, AND THAT IS THE INSTRUMENT, not an oversight. dispatch and Escalate
// both call the API immediately after the point the gate sits at, so "did the refusal
// happen before the work?" is answerable by whether the call survives a nil api. A test
// that only asserted "no notification was delivered" would pass just as well with the
// gate placed at the very bottom, after every query and every write.
func gatedNotifier(adapters map[string]ChannelAdapter, deleted bool) *PolicyNotifier {
	n := testNotifier(adapters)
	n.TenantDeleted = func(string) bool { return deleted }
	return n
}

// deletedTenantCtx scopes a context to a tenant, as both real entry points do before
// calling into the notifier.
func deletedTenantCtx() context.Context {
	return core.WithTenant(context.Background(), "acme")
}

// panicked reports whether fn panicked, so a test can assert that the code path it is
// pinning really does reach the nil api when the gate does NOT refuse.
func panicked(fn func()) (didPanic bool) {
	defer func() {
		if recover() != nil {
			didPanic = true
		}
	}()
	fn()
	return false
}

// TestDispatchRefusesADeletedTenantBeforeQuerying pins that a deleted tenant's alarm is
// refused above the policy query, and that the refusal returns nil — which is what makes
// the durable consumer ACK the event instead of leaving it to churn the redelivery cap
// for a tenant that will never be notified.
func TestDispatchRefusesADeletedTenantBeforeQuerying(t *testing.T) {
	fa := &fakeAdapter{}
	n := gatedNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa}, true)

	var err error
	if panicked(func() { err = n.dispatch(deletedTenantCtx(), raisedEvent("CRITICAL")) }) {
		t.Fatal("the refusal must come before the policy query; it reached the (nil) api instead")
	}
	if err != nil {
		t.Fatalf("a refusal must return nil so the consumer acks and drops the event, got %v", err)
	}
	if fa.calls != 0 {
		t.Fatalf("a deleted tenant must not be paged; the adapter was called %d time(s)", fa.calls)
	}
}

// TestDispatchForALiveTenantStillReachesTheApi is the counterweight to the test above and
// the proof that its instrument works: with the gate answering "not deleted", the same
// call must get past the gate and reach the nil api. Without this, the test above would
// pass with a dispatch that returned nil unconditionally.
func TestDispatchForALiveTenantStillReachesTheApi(t *testing.T) {
	n := gatedNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: &fakeAdapter{}}, false)

	if !panicked(func() { _ = n.dispatch(deletedTenantCtx(), raisedEvent("CRITICAL")) }) {
		t.Fatal("a live tenant's alarm must get past the gate and query policies; it did not reach the api")
	}
}

// TestEscalateRefusesADeletedTenantBeforeClaimingATier pins the ordering that matters most
// in this file: the refusal sits above ClaimEscalation, which WRITES.
//
// A refusal below it would still advance the alarm's escalation tier for a tenant being
// reclaimed. That dirties a table the purge is sweeping, and a store that erases rows
// loses its clean-since — restarting the settle window the purge cannot complete without.
// A deleted tenant with one open alarm would hold its own purge open on every tick.
func TestEscalateRefusesADeletedTenantBeforeClaimingATier(t *testing.T) {
	fa := &fakeAdapter{}
	n := gatedNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa}, true)
	smtp := enabledChannel("smtp-1", model.ChannelTypeSMTP)
	now := time.Now().UTC()
	p := escalatingPolicy("p", 300, 0, rule("CRITICAL", smtp, "ops@x.com"))
	state := openState("CRITICAL", now.Add(-10*time.Minute), 1)

	var err error
	if panicked(func() { err = n.Escalate(deletedTenantCtx(), state, []*model.NotificationPolicy{p}, now, 5) }) {
		t.Fatal("the refusal must come before ClaimEscalation; it reached the (nil) api instead")
	}
	if err != nil {
		t.Fatalf("a refused escalation must return nil so the scheduler moves on, got %v", err)
	}
	if fa.calls != 0 {
		t.Fatalf("a deleted tenant must not be escalated; the adapter was called %d time(s)", fa.calls)
	}
}

// TestEscalateForALiveTenantStillClaimsATier is the counterweight: the same fixture must
// plan a real delivery and reach ClaimEscalation when the tenant is live. Without it, the
// test above would pass with a fixture that planned zero deliveries and never went near
// the write it claims to be protecting.
func TestEscalateForALiveTenantStillClaimsATier(t *testing.T) {
	n := gatedNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: &fakeAdapter{}}, false)
	smtp := enabledChannel("smtp-1", model.ChannelTypeSMTP)
	now := time.Now().UTC()
	p := escalatingPolicy("p", 300, 0, rule("CRITICAL", smtp, "ops@x.com"))
	state := openState("CRITICAL", now.Add(-10*time.Minute), 1)

	if !panicked(func() { _ = n.Escalate(deletedTenantCtx(), state, []*model.NotificationPolicy{p}, now, 5) }) {
		t.Fatal("a live tenant's escalation must reach ClaimEscalation; the fixture planned no deliveries, " +
			"so the test above proves nothing about ordering")
	}
}

// TestDeliverWithRetryRefusesADeletedTenant drives the funnel DIRECTLY.
//
// It has to. dispatch and Escalate both refuse above it, so on every path that exists
// today this check is unreachable — and a backstop reached only through callers that
// already refuse cannot be observed failing. Driving it directly is the only way this
// assertion measures the funnel rather than the callers.
func TestDeliverWithRetryRefusesADeletedTenant(t *testing.T) {
	fa := &fakeAdapter{}
	n := gatedNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa}, true)
	d := delivery{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP), recipients: []string{"ops@x.com"}}

	if n.deliverWithRetry(deletedTenantCtx(), d, &RenderedNotification{}) == deliveryOK {
		t.Fatal("the funnel must refuse a deleted tenant")
	}
	if fa.calls != 0 {
		t.Fatalf("nothing may reach the adapter for a deleted tenant; it was called %d time(s)", fa.calls)
	}
}

// TestDeliverWithRetryDeliversForALiveTenant is the funnel's counterweight: without it,
// a funnel that refused unconditionally would be a total notification outage passing as a
// working gate.
func TestDeliverWithRetryDeliversForALiveTenant(t *testing.T) {
	fa := &fakeAdapter{}
	n := gatedNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa}, false)
	d := delivery{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP), recipients: []string{"ops@x.com"}}

	if n.deliverWithRetry(deletedTenantCtx(), d, &RenderedNotification{}) != deliveryOK {
		t.Fatal("a live tenant's notification must be delivered")
	}
	if fa.calls != 1 {
		t.Fatalf("expected exactly one delivery, got %d", fa.calls)
	}
}

// TestAnUnconfiguredGateDelivers pins the fail-open default: NewTenantLifecycleGate returns
// nil on an instance with no user-management configured, and this service must then behave
// exactly as it did before the gate existed rather than panic or refuse everything.
func TestAnUnconfiguredGateDelivers(t *testing.T) {
	fa := &fakeAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa}) // TenantDeleted nil
	d := delivery{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP), recipients: []string{"ops@x.com"}}

	if n.deliverWithRetry(deletedTenantCtx(), d, &RenderedNotification{}) != deliveryOK {
		t.Fatal("an unconfigured gate must admit (fail open)")
	}
	if fa.calls != 1 {
		t.Fatalf("expected exactly one delivery, got %d", fa.calls)
	}
}

// TestTheConstructorWiresTheLifecycleGate covers the plumbing every test above skips.
//
// They all build a PolicyNotifier by struct literal and assign TenantDeleted directly, so
// they measure the FIELD. The shipped binary reaches it only through NewPolicyNotifier —
// and with nothing covering that, deleting `TenantDeleted: tenantDeleted` from the
// constructor would disable the gate in production with this whole file still green.
func TestTheConstructorWiresTheLifecycleGate(t *testing.T) {
	n := NewPolicyNotifier(nil, nil, 3, time.Second, func(tenant string) bool { return tenant == "acme" }, nil)
	fa := &fakeAdapter{}
	n.adapters = map[string]ChannelAdapter{model.ChannelTypeSMTP: fa}
	d := delivery{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP), recipients: []string{"ops@x.com"}}

	if n.deliverWithRetry(deletedTenantCtx(), d, &RenderedNotification{}) == deliveryOK {
		t.Fatal("the gate passed to the constructor must reach the delivery path")
	}
	if fa.calls != 0 {
		t.Fatalf("nothing may reach the adapter; it was called %d time(s)", fa.calls)
	}
}

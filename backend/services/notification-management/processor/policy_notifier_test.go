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
	"github.com/devicechain-io/dc-notification-management/model"
	"gorm.io/datatypes"
)

// fakeAdapter fails its first failTimes calls, then succeeds; it records the last
// recipients it was asked to deliver to.
type fakeAdapter struct {
	failTimes int
	calls     int
}

func (f *fakeAdapter) Deliver(_ context.Context, _ *model.NotificationChannel, _ []string, _ *RenderedNotification) error {
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
	c := channelWith(token, ctype, "", "")
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
	disabled := channelWith("smtp-off", model.ChannelTypeSMTP, "", "")
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
	if !n.deliverWithRetry(context.Background(), d, &RenderedNotification{}) {
		t.Fatalf("expected eventual success, calls=%d", fa.calls)
	}
	if fa.calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", fa.calls)
	}

	// Always fails → false after exhausting attempts.
	fa2 := &fakeAdapter{failTimes: 99}
	n2 := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa2})
	n2.timeout = 50 * time.Millisecond
	if n2.deliverWithRetry(context.Background(), d, &RenderedNotification{}) {
		t.Fatal("expected failure")
	}
	if fa2.calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", fa2.calls)
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

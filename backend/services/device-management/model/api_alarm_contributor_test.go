// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// captureAlarmPublisher records emitted alarm state-change events for assertion.
type captureAlarmPublisher struct{ events []*AlarmStateChangeEvent }

func (c *captureAlarmPublisher) PublishAlarmEvent(_ context.Context, e *AlarmStateChangeEvent) {
	c.events = append(c.events, e)
}

func newContributorTestApi(t *testing.T) (*Api, *captureAlarmPublisher) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&Alarm{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	api := NewApi(&rdb.RdbManager{Database: db})
	cap := &captureAlarmPublisher{}
	api.AlarmPublisher = cap
	return api, cap
}

const dev = uint(42)

func raiseEdge(t *testing.T, api *Api, ctx context.Context, key, rule, sev string, sec int) {
	t.Helper()
	v := 1.0
	if err := api.ApplyAlarmContributorEdge(ctx, dev, key, "temperature", rule, AlarmEdgeRaised, sev, &v, tsec(sec)); err != nil {
		t.Fatalf("raise %s@%d: %v", rule, sec, err)
	}
}

func resolveEdge(t *testing.T, api *Api, ctx context.Context, key, rule string, sec int) {
	t.Helper()
	if err := api.ApplyAlarmContributorEdge(ctx, dev, key, "temperature", rule, AlarmEdgeResolved, "", nil, tsec(sec)); err != nil {
		t.Fatalf("resolve %s@%d: %v", rule, sec, err)
	}
}

func tsec(sec int) time.Time { return time.Unix(int64(sec), 0).UTC() }

func loadAlarm(t *testing.T, api *Api, ctx context.Context, key string) *Alarm {
	t.Helper()
	a, err := api.alarmByOriginatorKey(ctx, string(entity.TypeDevice), dev, key)
	if err != nil {
		t.Fatalf("load alarm: %v", err)
	}
	return a
}

// TestIntegratorRaiseClearLifecycle proves the core ADR-057 lifecycle end-to-end through the DB: a
// rule's raise creates an ACTIVE alarm (one RAISED event); the same rule's resolve clears it (one
// CLEARED event). No alarm exists before, and after the clear the row is CLEARED.
func TestIntegratorRaiseClearLifecycle(t *testing.T) {
	api, cap := newContributorTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	raiseEdge(t, api, ctx, "over-temp", "r1", "MAJOR", 10)
	a := loadAlarm(t, api, ctx, "over-temp")
	if a == nil || a.State != string(AlarmStateActive) || a.Severity != "MAJOR" {
		t.Fatalf("raise must create an ACTIVE MAJOR alarm; got %+v", a)
	}
	if len(cap.events) != 1 || cap.events[0].EventType != AlarmEventRaised {
		t.Fatalf("one RAISED event expected; got %+v", cap.events)
	}

	resolveEdge(t, api, ctx, "over-temp", "r1", 20)
	a = loadAlarm(t, api, ctx, "over-temp")
	if a.State != string(AlarmStateCleared) || !a.ClearedTime.Valid {
		t.Fatalf("resolve must clear the alarm; got %+v", a)
	}
	if len(cap.events) != 2 || cap.events[1].EventType != AlarmEventCleared {
		t.Fatalf("a CLEARED event expected; got %+v", cap.events)
	}
}

// TestIntegratorMultiTierEscalation proves the emergent tiered "over-temp" UX from two rules sharing an
// alarm key: MAJOR raises, CRITICAL escalates in place (one ESCALATED event), CRITICAL resolves
// de-escalates back to MAJOR (DEESCALATED), MAJOR resolves clears (CLEARED) — one alarm throughout.
func TestIntegratorMultiTierEscalation(t *testing.T) {
	api, cap := newContributorTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	raiseEdge(t, api, ctx, "over-temp", "major", "MAJOR", 10)
	raiseEdge(t, api, ctx, "over-temp", "crit", "CRITICAL", 11)
	if a := loadAlarm(t, api, ctx, "over-temp"); a.Severity != "CRITICAL" || a.State != string(AlarmStateActive) {
		t.Fatalf("a CRITICAL contributor must escalate the alarm; got %+v", a)
	}
	resolveEdge(t, api, ctx, "over-temp", "crit", 12)
	if a := loadAlarm(t, api, ctx, "over-temp"); a.Severity != "MAJOR" || a.State != string(AlarmStateActive) {
		t.Fatalf("with CRITICAL gone, the alarm de-escalates to MAJOR and stays active; got %+v", a)
	}
	resolveEdge(t, api, ctx, "over-temp", "major", 13)
	if a := loadAlarm(t, api, ctx, "over-temp"); a.State != string(AlarmStateCleared) {
		t.Fatalf("both rules resolved → cleared; got %+v", a)
	}
	// Exactly one alarm row exists for the key (escalation in place, not a second row).
	var count int64
	api.RDB.DB(ctx).Model(&Alarm{}).Where("alarm_key = ?", "over-temp").Count(&count)
	if count != 1 {
		t.Fatalf("escalation must reuse ONE alarm row; got %d rows", count)
	}
	types := eventTypes(cap.events)
	want := []AlarmEventType{AlarmEventRaised, AlarmEventEscalated, AlarmEventDeescalated, AlarmEventCleared}
	if !sameEvents(types, want) {
		t.Fatalf("event sequence wrong: got %v want %v", types, want)
	}
}

// TestIntegratorReactivationResetsAck proves a cleared alarm reactivates in place on a new raise,
// resetting the acknowledgment (a fresh cycle has not been seen), and emits RAISED.
func TestIntegratorReactivationResetsAck(t *testing.T) {
	api, cap := newContributorTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	raiseEdge(t, api, ctx, "k", "r1", "MAJOR", 10)
	resolveEdge(t, api, ctx, "k", "r1", 20)
	// Operator acknowledges the cleared alarm.
	a := loadAlarm(t, api, ctx, "k")
	api.RDB.DB(ctx).Model(a).Update("acknowledged", true)

	raiseEdge(t, api, ctx, "k", "r1", "MAJOR", 30)
	a = loadAlarm(t, api, ctx, "k")
	if a.State != string(AlarmStateActive) || a.Acknowledged {
		t.Fatalf("reactivation must go ACTIVE and reset ack; got %+v", a)
	}
	if !a.RaisedTime.Equal(tsec(30)) {
		t.Fatalf("reactivation must stamp the new raised time; got %v", a.RaisedTime)
	}
	if last := cap.events[len(cap.events)-1]; last.EventType != AlarmEventRaised {
		t.Fatalf("reactivation must emit RAISED; got %v", last.EventType)
	}
}

// TestIntegratorIdempotentReRaise proves a duplicate raise (at-least-once redelivery) neither writes
// again nor emits again — the contributor reduction reports no change.
func TestIntegratorIdempotentReRaise(t *testing.T) {
	api, cap := newContributorTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	raiseEdge(t, api, ctx, "k", "r1", "MAJOR", 10)
	raiseEdge(t, api, ctx, "k", "r1", "MAJOR", 10) // exact duplicate
	if len(cap.events) != 1 {
		t.Fatalf("a duplicate raise must not emit a second event; got %v", eventTypes(cap.events))
	}
}

// TestIntegratorStaleResolveIgnored proves an out-of-order resolve OLDER than the raise it would undo
// is ignored — the alarm stays raised, no spurious CLEARED.
func TestIntegratorStaleResolveIgnored(t *testing.T) {
	api, cap := newContributorTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	raiseEdge(t, api, ctx, "k", "r1", "MAJOR", 10)
	resolveEdge(t, api, ctx, "k", "r1", 5) // stale (older than the raise)
	if a := loadAlarm(t, api, ctx, "k"); a.State != string(AlarmStateActive) {
		t.Fatalf("a stale resolve must not clear the alarm; got %+v", a)
	}
	if len(cap.events) != 1 {
		t.Fatalf("a stale resolve must emit nothing; got %v", eventTypes(cap.events))
	}
}

// TestIntegratorResolveBeforeRaiseStaysCleared proves the reordering guard: a resolve arriving before
// its (event-time-older) raise creates a CLEARED tombstone row, and the later stale raise is rejected —
// the alarm never wrongly (re)raises. No event is emitted (the alarm never became visibly active).
func TestIntegratorResolveBeforeRaiseStaysCleared(t *testing.T) {
	api, cap := newContributorTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	resolveEdge(t, api, ctx, "k", "r1", 25) // resolve first (redelivery reordering), no prior alarm
	a := loadAlarm(t, api, ctx, "k")
	if a == nil || a.State != string(AlarmStateCleared) {
		t.Fatalf("a lone resolve must persist a CLEARED tombstone row; got %+v", a)
	}
	raiseEdge(t, api, ctx, "k", "r1", "MAJOR", 20) // the older raise arrives late → stale
	if a := loadAlarm(t, api, ctx, "k"); a.State != string(AlarmStateCleared) {
		t.Fatalf("a raise older than the resolve must not reactivate; got %+v", a)
	}
	if len(cap.events) != 0 {
		t.Fatalf("a raise/resolve that never nets active must emit nothing; got %v", eventTypes(cap.events))
	}
}

func eventTypes(evs []*AlarmStateChangeEvent) []AlarmEventType {
	out := make([]AlarmEventType, len(evs))
	for i, e := range evs {
		out[i] = e.EventType
	}
	return out
}

func sameEvents(a, b []AlarmEventType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

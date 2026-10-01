// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// The erasure fence on the BATCH path. A batch transaction holds several tenants' writes, so
// a fence standing for one of them must refuse that tenant's events — left unacked for
// redelivery, exactly as the per-message path leaves them — without taking its batch-mates
// down with it, and an unreadable fence must refuse everything.

const otherTenant = "tenant2"

// forTenant is m on tenant's subject.
func forTenant(m messaging.Message, tenant string) messaging.Message {
	return messaging.Message{Subject: "instance1." + tenant + ".resolved-events", Value: m.Value}
}

// assertProjectedAtFor is assertProjectedAt for any tenant.
func assertProjectedAtFor(t *testing.T, sp *StateProcessor, tenant, device string, want time.Time) {
	t.Helper()
	ctx := core.WithTenant(context.Background(), tenant)
	if got := loadState(t, sp, ctx, device).LastActivityTime.Time; !got.Equal(want) {
		t.Errorf("%s/%s LastActivityTime = %v, want %v", tenant, device, got, want)
	}
	for _, name := range metricNames {
		if got := loadProjectedMeasurement(t, sp, ctx, device, name).OccurredTime; !got.Equal(want) {
			t.Errorf("%s/%s latest %s OccurredTime = %v, want %v", tenant, device, name, got, want)
		}
	}
}

// A fenced tenant's events in a mixed batch are refused and left for redelivery; the other
// tenant's events commit — TOGETHER, in one transaction, after exactly one failed attempt —
// and the fenced tenant's projection does not move.
func TestABatchSetsAFencedTenantAsideAndCommitsTheRest(t *testing.T) {
	sp, db, _ := newFencedStateProcessor(t)
	reg := stateRegistry(sp)
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	fenced := []string{"bff-a", "bff-b", "bff-c", "bff-d"}
	live := []string{"bfl-a", "bfl-b", "bfl-c", "bfl-d"}
	for _, d := range fenced {
		seed(t, sp, threeMetrics(t, d, t0))
	}
	seededCommits := float64(len(fenced))
	plantFence(t, db, fenceTenant)

	// Interleaved: fenced, live, fenced, live, ...
	var msgs []messaging.Message
	for i := range fenced {
		msgs = append(msgs, threeMetrics(t, fenced[i], t1), forTenant(threeMetrics(t, live[i], t1), otherTenant))
	}
	acks := drainQueue(sp, msgs)
	for i, n := range ackCounts(acks) {
		want := int32(i % 2) // live messages sit at odd positions
		if n != want {
			t.Errorf("message %d acknowledged %d times; want %d", i, n, want)
		}
	}
	for _, d := range fenced {
		assertProjectedAt(t, sp, d, t0)
	}
	for _, d := range live {
		assertProjectedAtFor(t, sp, otherTenant, d, t1)
	}
	// ONE fallback: the first attempt was refused for tenant1, and the live tenant's four then
	// committed as ONE batch — a histogram observation of 4, not four of 1.
	if v, _, _, _ := gathered(t, reg, "state_batch_fallbacks_total", ""); v != 1 {
		t.Errorf("state_batch_fallbacks_total = %v; want 1", v)
	}
	if _, n, sum, _ := gathered(t, reg, "state_batch_size", ""); n != 1+seededCommits || sum != 4+seededCommits {
		t.Errorf("batch-size histogram holds %v observations summing to %v; want %v summing to %v "+
			"(the seeds' commits of one, then ONE batch of the live tenant's 4)", n, sum, 1+seededCommits, 4+seededCommits)
	}
	if v, _, _, _ := gathered(t, reg, "state_messages_total", core.ResultRetry); v != 4 {
		t.Errorf("state_messages_total{result=retry} = %v; want the fenced tenant's 4", v)
	}

	// Negative control: with the fence lifted, the SAME events land and the SAME reads see
	// them — so the assertions above could have seen a write had one happened.
	liftFence(t, db, fenceTenant)
	var again []messaging.Message
	for _, d := range fenced {
		again = append(again, threeMetrics(t, d, t1))
	}
	for i, n := range ackCounts(drainQueue(sp, again)) {
		if n != 1 {
			t.Errorf("after the lift, message %d acknowledged %d times; want 1", i, n)
		}
	}
	for _, d := range fenced {
		assertProjectedAt(t, sp, d, t1)
	}
}

// An unreadable fence is not an absent one on the batch path either: nothing in the batch is
// acknowledged, and nothing moves.
func TestABatchOverAnUnreadableFenceMergesNothing(t *testing.T) {
	sp, db, _ := newFencedStateProcessor(t)
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	devices := []string{"bu-a", "bu-b", "bu-c", "bu-d"}
	for _, d := range devices {
		seed(t, sp, threeMetrics(t, d, t0))
	}
	if err := db.Migrator().DropTable(&rdb.PurgedTenant{}); err != nil {
		t.Fatalf("drop the fence table: %v", err)
	}
	var msgs []messaging.Message
	for _, d := range devices {
		msgs = append(msgs, threeMetrics(t, d, t1))
	}
	for i, n := range ackCounts(drainQueue(sp, msgs)) {
		if n != 0 {
			t.Errorf("message %d acknowledged %d times over an unreadable fence; want 0", i, n)
		}
	}
	for _, d := range devices {
		assertProjectedAt(t, sp, d, t0)
	}
}

// A batch that carries NO readings and NO positions writes nothing but device rows, so the
// write-back of those rows is the only statement in it that can meet the fence. Presence
// events for devices already seen are exactly that batch: the fenced tenant's are refused and
// left for redelivery, and its rows do not move, while the other tenant's commit.
func TestABatchOfPresenceOnlyEventsForSeenDevicesIsRefusedUnderAFence(t *testing.T) {
	sp, db, _ := newFencedStateProcessor(t)
	reg := stateRegistry(sp)
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	fenced := []string{"bpf-a", "bpf-b", "bpf-c", "bpf-d"}
	live := []string{"bpl-a", "bpl-b", "bpl-c", "bpl-d"}
	for _, d := range fenced {
		seed(t, sp, threeMetrics(t, d, t0))
	}
	for _, d := range live {
		seed(t, sp, forTenant(threeMetrics(t, d, t0), otherTenant))
	}
	plantFence(t, db, fenceTenant)

	connect := func(d string) messaging.Message { return stateChangeMessage(t, d, t1, "CONNECTED", 7) }
	var msgs []messaging.Message
	for i := range fenced {
		msgs = append(msgs, connect(fenced[i]), forTenant(connect(live[i]), otherTenant))
	}
	for i, n := range ackCounts(drainQueue(sp, msgs)) {
		want := int32(i % 2) // live messages sit at odd positions
		if n != want {
			t.Errorf("message %d acknowledged %d times; want %d", i, n, want)
		}
	}
	if v, _, _, _ := gathered(t, reg, "state_messages_total", core.ResultRetry); v != 4 {
		t.Errorf("state_messages_total{result=retry} = %v; want the fenced tenant's 4", v)
	}
	fencedCtx := core.WithTenant(context.Background(), fenceTenant)
	for _, d := range fenced {
		ds := loadState(t, sp, fencedCtx, d)
		if !ds.LastActivityTime.Time.Equal(t0) || ds.PresenceSource != model.PresenceSourceInferred ||
			ds.PresenceTime.Valid || ds.SessionId != 0 {
			t.Errorf("fenced %s moved: activity %v, source %q, presence time %v, session %d; want t0, "+
				"INFERRED, none, 0", d, ds.LastActivityTime.Time, ds.PresenceSource, ds.PresenceTime, ds.SessionId)
		}
	}
	liveCtx := core.WithTenant(context.Background(), otherTenant)
	for _, d := range live {
		ds := loadState(t, sp, liveCtx, d)
		if !ds.LastActivityTime.Time.Equal(t1) || ds.PresenceSource != model.PresenceSourceAsserted || ds.SessionId != 7 {
			t.Errorf("live %s: activity %v, source %q, session %d; want t1, ASSERTED, 7",
				d, ds.LastActivityTime.Time, ds.PresenceSource, ds.SessionId)
		}
	}

	// Negative control: with the fence lifted, the SAME events land and the SAME reads see
	// them — so the assertions above could have seen a write had one happened.
	liftFence(t, db, fenceTenant)
	var again []messaging.Message
	for _, d := range fenced {
		again = append(again, connect(d))
	}
	for i, n := range ackCounts(drainQueue(sp, again)) {
		if n != 1 {
			t.Errorf("after the lift, message %d acknowledged %d times; want 1", i, n)
		}
	}
	for _, d := range fenced {
		ds := loadState(t, sp, fencedCtx, d)
		if !ds.LastActivityTime.Time.Equal(t1) || ds.PresenceSource != model.PresenceSourceAsserted || ds.SessionId != 7 {
			t.Errorf("after the lift, %s: activity %v, source %q, session %d; want t1, ASSERTED, 7",
				d, ds.LastActivityTime.Time, ds.PresenceSource, ds.SessionId)
		}
	}
}

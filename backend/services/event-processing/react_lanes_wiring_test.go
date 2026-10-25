// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/devicechain-io/dc-event-processing/config"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-event-processing/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/streams"
)

// These tests drive the REACT dispatcher main actually wires (wireReactDispatcher) end to end:
// the real reader options, the real rule resolver over a real rule store, the real raise-alarm
// client, and a real broker. What they measure is taken at the rule read the dispatcher makes
// for every derived event, through a callback on the store's database, so nothing in the
// dispatcher is replaced to observe it.

const lanesRuleID = "acme/p@1/r1"

// ruleReads is the instrument: it counts the rule reads in flight, remembers the most at once,
// and parks each read until hold lets it through.
type ruleReads struct {
	inFlight, maxInFlight, total atomic.Int64
	// releaseAt is how many reads must be inside the callback at once to let them all through
	// (0: never, only an explicit open does).
	releaseAt int64
	hold      chan struct{}
	opened    atomic.Bool
	// giveUp lets a parked read through after this long even if nothing opened it, so a
	// dispatcher that never reaches releaseAt fails its assertion rather than hanging the test.
	giveUp time.Duration
}

func newRuleReads(releaseAt int64, giveUp time.Duration) *ruleReads {
	return &ruleReads{releaseAt: releaseAt, hold: make(chan struct{}), giveUp: giveUp}
}

func (r *ruleReads) open() {
	if r.opened.CompareAndSwap(false, true) {
		close(r.hold)
	}
}

func (r *ruleReads) callback(tx *gorm.DB) {
	if tx.Statement.Table != "detect_rules" {
		return
	}
	n := r.inFlight.Add(1)
	r.total.Add(1)
	for {
		m := r.maxInFlight.Load()
		if n <= m || r.maxInFlight.CompareAndSwap(m, n) {
			break
		}
	}
	if r.releaseAt > 0 && n >= r.releaseAt {
		r.open()
	}
	select {
	case <-r.hold:
	case <-time.After(r.giveUp):
	}
	r.inFlight.Add(-1)
}

// installRuleStore gives the REACT wiring a real rule store over sqlite, holding the one rule the
// derived events name, whose reads go through reads. The fixture restores DetectRuleStore.
func installRuleStore(t *testing.T, reads *ruleReads) {
	t.Helper()
	db := sqlitetest.OpenMemory(t, &gorm.Config{Logger: logger.Discard})
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.DetectRule{}))
	mgr := &rdb.RdbManager{Database: db}
	store := model.NewDetectRuleStore(mgr)
	def, err := json.Marshal(rules.Rule{ID: lanesRuleID, Name: "r", Type: rules.TypeThreshold, Severity: rules.SeverityMajor,
		Actions: []rules.Action{{Type: rules.ActionRaiseAlarm, RaiseAlarm: &rules.RaiseAlarmAction{AlarmKey: "overheat"}}}})
	require.NoError(t, err)
	require.NoError(t, store.Upsert(core.WithTenant(context.Background(), "acme"), []model.DetectRule{{
		RuleId: lanesRuleID, Tenant: "acme", ProfileVersionToken: "p@1", RuleToken: "r1", Definition: string(def),
	}}))
	// Registered after the seed, so the seed's own write is not counted.
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:rule_reads", reads.callback))
	DetectRuleStore = store
}

// publishEvents writes one raised derived event per series, in order.
func (f *reactFixture) publishEvents(t *testing.T, nmgr *messaging.NatsManager, series ...string) {
	t.Helper()
	w, err := nmgr.NewWriter(streams.DerivedEvents)
	require.NoError(t, err)
	ctx := core.WithTenant(context.Background(), "acme")
	subject := messaging.ScopedSubject(f.area, "acme", streams.DerivedEvents)
	for i, s := range series {
		body, err := json.Marshal(runtime.DerivedEvent{
			RuleID: lanesRuleID, Tenant: "acme", Kind: "threshold", Series: s, Edge: runtime.EdgeRaised,
			OccurredTime: time.Date(2026, 10, 25, 12, 0, i, 0, time.UTC),
		})
		require.NoError(t, err)
		require.NoError(t, w.WriteMessages(ctx, messaging.Message{Subject: subject, Value: body}))
	}
}

// devicesOnDistinctLanes returns n device tokens that the dispatcher puts on n different lanes of
// lanes, on this fixture's subject.
func (f *reactFixture) devicesOnDistinctLanes(t *testing.T, n, lanes int) []string {
	t.Helper()
	subject := messaging.ScopedSubject(f.area, "acme", streams.DerivedEvents)
	seen := map[int]bool{}
	var out []string
	for d := 0; d < 10000 && len(out) < n; d++ {
		dev := fmt.Sprintf("device-%d", d)
		if l := processor.LaneOf(subject, dev, lanes); !seen[l] {
			seen[l] = true
			out = append(out, dev)
		}
	}
	require.Len(t, out, n, "no %d devices on distinct lanes", n)
	return out
}

// 🔑 THE CALLER OF THE READER'S CAPACITY. The lanes can overlap only as far as the reader hands
// them deliveries, and the reader's slots come from the one value main passes it. Eight events for
// eight devices on eight different lanes are read; the rule read each dispatch makes waits until
// all eight are inside it at once. The dispatcher main wires must reach DefaultReactWorkers
// dispatches at once. With a reader of one slot (main passing the wrong capacity) it stays at one.
func TestWiredReactDispatcherOverlapsAsManyDevicesAsItHasWorkers(t *testing.T) {
	f := startReactFixture(t)
	reads := newRuleReads(int64(config.DefaultReactWorkers), time.Second)
	installRuleStore(t, reads)
	nmgr := f.newManager(t)
	gate := processor.NewTermGate()
	gate.Enter(func() bool { return true })
	rd := f.wire(t, nmgr, gate)
	require.Equal(t, config.DefaultReactWorkers, rd.Workers())
	require.NoError(t, rd.Start(context.Background()))
	defer stopDispatcher(t, rd)

	f.publishEvents(t, nmgr, f.devicesOnDistinctLanes(t, config.DefaultReactWorkers, rd.Workers())...)

	require.Eventually(t, func() bool { return f.consumer(t).AckFloor.Consumer == uint64(config.DefaultReactWorkers) },
		20*time.Second, 20*time.Millisecond, "the derived events were not all dispatched and acked")
	require.Equal(t, int64(config.DefaultReactWorkers), reads.maxInFlight.Load(),
		"the wired dispatcher did not overlap one dispatch per worker: the reader hands the lanes too few deliveries")
}

// 🔴 THE TERM GATE REACHES THE LANES. The REACT reader stops fetching when the DETECT term is
// lost, but the deliveries it had already handed to a lane are the dispatcher's. Four events of
// one device go to one lane: the first is parked inside its rule read, the other three are queued
// behind it. The term is lost, the first is let finish. Only the dispatch that was already
// running completes; the queued three are released unstarted, and unacked, for the replica that
// holds the term now. They are not read: three more rule reads would be an old leader dispatching
// (and charging its outbound ceiling) beside the new one.
func TestWiredReactDispatcherStartsNothingQueuedOnceTheTermIsLost(t *testing.T) {
	f := startReactFixture(t)
	reads := newRuleReads(0, 30*time.Second)
	installRuleStore(t, reads)
	nmgr := f.newManager(t)
	var held atomic.Bool
	held.Store(true)
	gate := processor.NewTermGate()
	gate.Enter(held.Load)
	rd := f.wire(t, nmgr, gate)
	require.NoError(t, rd.Start(context.Background()))
	defer stopDispatcher(t, rd)

	f.publishEvents(t, nmgr, "device-0", "device-0", "device-0", "device-0")
	// One parked in its rule read and three delivered behind it: what makes "not started" a
	// finding about the gate, not about there having been nothing to start.
	require.Eventually(t, func() bool {
		return reads.total.Load() == 1 && f.consumer(t).NumAckPending == 4
	}, 10*time.Second, 10*time.Millisecond, "one event should be parked in its rule read and three queued behind it")

	held.Store(false)
	reads.open()
	require.Eventually(t, func() bool { return f.consumer(t).AckFloor.Consumer >= 1 }, 10*time.Second, 10*time.Millisecond,
		"the dispatch that was already running did not complete")
	// Far longer than the lane needs to start the next delivery, were it going to.
	time.Sleep(time.Second)
	require.Equal(t, int64(1), reads.total.Load(), "a queued delivery started after the term was lost")
	info := f.consumer(t)
	require.Equal(t, uint64(1), info.AckFloor.Consumer)
	require.Equal(t, 3, info.NumAckPending, "the queued deliveries must be left unacked for the next holder of the term")
}

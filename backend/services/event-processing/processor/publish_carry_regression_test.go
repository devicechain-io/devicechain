// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"testing"
	"time"

	dc "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	rules "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
)

func handoffRule(t *testing.T, version string, kind rules.RuleType, dynamic bool) runtime.ScopedRule {
	t.Helper()
	threshold := 80.0
	r := rules.Rule{ID: "acme/p1@" + version + "/r1", Name: "heat", Type: kind,
		When: rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: &threshold}}
	if dynamic {
		r.When.Threshold = nil
		r.When.ThresholdAttr = "limit"
	}
	switch kind {
	case rules.TypeRepeating:
		r.Count = 3
		r.Window = rules.Duration(time.Minute)
	case rules.TypeCorrelation:
		r.When = rules.Condition{}
		r.Count = 3
		r.Window = rules.Duration(time.Minute)
		r.AnchorType = "area"
	}
	cr, err := rules.Compile(r, rules.Limits{})
	require.NoError(t, err)
	return runtime.ScopedRule{Tenant: "acme", ProfileVersionToken: "p1@" + version, Compiled: cr}
}

func handoffProcessor(t *testing.T, k int, scoped ...runtime.ScopedRule) (*ResolvedEventsProcessor, *derivedCapture) {
	t.Helper()
	w := &derivedCapture{}
	rp := newTestProcessor(newTestStore(t), nil, 1000)
	rp.cfg.Shards = k
	rp.registry = runtime.NewRuleRegistry(scoped)
	rp.publisher = runtime.NewPublisher(w, rp.registry, (*DetectMetrics)(nil))
	require.NoError(t, rp.restore(context.Background()))
	t.Cleanup(rp.closeDetector)
	rp.RosterStore = newProcRosterStore(t)
	rp.ProfileActiveStore = newProcActiveStore(t)
	require.NoError(t, rp.RosterStore.Upsert(context.Background(), &model.DeviceRoster{
		Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase}))
	require.NoError(t, rp.ProfileActiveStore.Upsert(context.Background(), &model.ProfileActive{
		Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: testBase}))
	require.NoError(t, rp.startDeadmanGate(context.Background()))
	rp.attrView = runtime.NewDeviceAttributeView()
	rp.attrView.ReplaceDevice("acme", "dev1", []runtime.AttrEntry{{Scope: runtime.AttrScopeServer, Key: "limit", Value: 80}})
	return rp, w
}

func handoffActivate(t *testing.T, rp *ResolvedEventsProcessor, sr runtime.ScopedRule) {
	t.Helper()
	a := runtime.ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: sr.ProfileVersionToken, PublishedAt: testBase.Add(10 * time.Second)}
	require.NoError(t, rp.ProfileActiveStore.Upsert(context.Background(), &model.ProfileActive{
		Tenant: a.Tenant, ProfileToken: a.ProfileToken, ActiveVersionToken: a.ActiveVersionToken, PublishedAt: a.PublishedAt}))
	rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{sr}, active: &a})
}

func handoffMessage(t *testing.T, seq uint64, version, device, value, anchor string) messaging.Message {
	t.Helper()
	at := testBase.Add(time.Duration(seq) * time.Second)
	m := measuredMsgAt(t, seq, at, "acme", device, "p1@"+version, "temperature", value, &fakeAck{})
	if anchor != "" {
		ev, err := resolvedevent.UnmarshalResolvedEvent(m.Value)
		require.NoError(t, err)
		ev.Anchors = []resolvedevent.ResolvedAnchor{{AnchorType: "area", AnchorToken: anchor}}
		m.Value, err = resolvedevent.MarshalResolvedEvent(ev)
		require.NoError(t, err)
	}
	m.AppendTime = at
	return m
}

func handoffApply(t *testing.T, rp *ResolvedEventsProcessor, m messaging.Message) {
	t.Helper()
	require.True(t, rp.applyResolved(m))
	rp.syncDetector()
}

func TestPublishPendingDynamicThresholdKeepsRaiseAndClear(t *testing.T) {
	for _, k := range []int{1, 4} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			rp, w := handoffProcessor(t, k, handoffRule(t, "v1", rules.TypeThreshold, true))
			handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
			require.Len(t, rp.pendingDets, 1)
			require.False(t, rp.pendingDets[0].early, "attribute-reading thresholds must wait for checkpoint")
			rp.flushDetections(context.Background())
			require.Empty(t, w.events)
			handoffActivate(t, rp, handoffRule(t, "v2", rules.TypeThreshold, true))
			require.False(t, rp.engine.IsRaised(dc.SeriesKey{Rule: "acme/p1@v2/r1", Series: "dev1"}), "unpublished edge must prevent carry")
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 1, "pending v1 raise must be published exactly once")
			require.Equal(t, "acme/p1@v1/r1", w.events[0].RuleID)
			require.Equal(t, runtime.EdgeRaised, w.events[0].Edge)
			handoffApply(t, rp, handoffMessage(t, 2, "v2", "dev1", "20", ""))
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 2, "healthy v2 must clear the published alarm")
			require.Equal(t, runtime.EdgeResolved, w.events[1].Edge)
		})
	}
}

func TestPublishCrashReplayPreservesThresholdLatch(t *testing.T) {
	for _, k := range []int{1, 4} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			v1 := handoffRule(t, "v1", rules.TypeThreshold, false)
			v2 := handoffRule(t, "v2", rules.TypeThreshold, false)
			rp, w := handoffProcessor(t, k, v1)
			msg := handoffMessage(t, 1, "v1", "dev1", "90", "")
			handoffApply(t, rp, msg)
			rp.flushDetections(context.Background())
			require.Len(t, w.events, 1, "raise published before checkpoint")
			handoffActivate(t, rp, v2)
			_, found, err := rp.Store.Load(context.Background(), "singleton")
			require.NoError(t, err)
			require.False(t, found, "crash must precede the checkpoint")

			restarted, replayed := handoffProcessor(t, k, v1, v2)
			restarted.closeDetector()
			restarted.Store = rp.Store
			restarted.RosterStore = rp.RosterStore
			restarted.ProfileActiveStore = rp.ProfileActiveStore
			require.NoError(t, restarted.restore(context.Background()))
			require.NoError(t, restarted.startDeadmanGate(context.Background()))
			replay := &fakeReplayOpener{msgs: []messaging.Message{msg}, head: 1}
			restarted.Replay = replay
			require.NoError(t, restarted.replayToHead())
			require.Equal(t, uint64(1), replay.lastStart)
			require.False(t, restarted.replaying)
			require.Len(t, replayed.events, 1, "actual replay must republish the old raise idempotently")
			require.Equal(t, w.ids, replayed.ids, "replay dedup ids must be stable")
			require.NoError(t, restarted.reconcileDeadmanArming(context.Background()))
			require.True(t, restarted.engine.IsRaised(dc.SeriesKey{Rule: v2.Compiled.ID, Series: "dev1"}))
			handoffApply(t, restarted, handoffMessage(t, 2, "v2", "dev1", "20", ""))
			require.True(t, restarted.checkpoint(context.Background()))
			require.Len(t, replayed.events, 2)
			require.Equal(t, runtime.EdgeResolved, replayed.events[1].Edge)
		})
	}
}

func TestPublishRepeatingDoesNotCarryAnEmptyWindow(t *testing.T) {
	for _, k := range []int{1, 4} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			rp, w := handoffProcessor(t, k, handoffRule(t, "v1", rules.TypeRepeating, false))
			for seq := uint64(1); seq <= 3; seq++ {
				handoffApply(t, rp, handoffMessage(t, seq, "v1", "dev1", "90", ""))
			}
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 1)
			handoffActivate(t, rp, handoffRule(t, "v2", rules.TypeRepeating, false))
			handoffApply(t, rp, handoffMessage(t, 4, "v2", "dev1", "90", ""))
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 1, "first v2 match must not falsely resolve an empty successor window")
			for seq := uint64(5); seq <= 6; seq++ {
				handoffApply(t, rp, handoffMessage(t, seq, "v2", "dev1", "90", ""))
			}
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 2)
			require.Equal(t, runtime.EdgeRaised, w.events[1].Edge, "main's repeat action on a publish is preserved")
		})
	}
}

func TestPublishCorrelationKeepsLateAnchorsAndDoesNotFalseClear(t *testing.T) {
	for _, k := range []int{1, 4} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			rp, w := handoffProcessor(t, k, handoffRule(t, "v1", rules.TypeCorrelation, false))
			for seq := uint64(1); seq <= 3; seq++ {
				handoffApply(t, rp, handoffMessage(t, seq, "v1", fmt.Sprint(seq), "90", "zone"))
			}
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 1)
			handoffActivate(t, rp, handoffRule(t, "v2", rules.TypeCorrelation, false))
			handoffApply(t, rp, handoffMessage(t, 4, "v2", "1", "90", "zone"))
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 1, "an empty v2 member map must not falsely resolve the anchor")
			// Anchor and device tokens occupy different namespaces. Even a collision
			// with a rostered device must never make correlation eligible for this gate.
			for seq := uint64(5); seq <= 7; seq++ {
				handoffApply(t, rp, handoffMessage(t, seq, "v2", fmt.Sprint(seq), "90", "dev1"))
			}
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 2)
			for seq := uint64(8); seq <= 10; seq++ {
				handoffApply(t, rp, handoffMessage(t, seq, "v1", fmt.Sprint(seq), "90", "dev1"))
			}
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 3, "late v1 anchor raise must pass even with a raised successor anchor")
			require.Equal(t, "dev1", w.events[2].Series)
			require.Equal(t, runtime.EdgeRaised, w.events[2].Edge)
			require.False(t, rp.armer.CarriedForward(dc.SeriesKey{Rule: "acme/p1@v1/r1", Series: "dev1"}))
		})
	}
}

func TestPublishLateThresholdRaiseAfterSuccessorClearsIsKept(t *testing.T) {
	rp, w := handoffProcessor(t, 4, handoffRule(t, "v1", rules.TypeThreshold, false))
	handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
	require.True(t, rp.checkpoint(context.Background()))
	handoffActivate(t, rp, handoffRule(t, "v2", rules.TypeThreshold, false))
	handoffApply(t, rp, handoffMessage(t, 2, "v2", "dev1", "20", ""))
	require.True(t, rp.checkpoint(context.Background()))
	require.Len(t, w.events, 2)
	handoffApply(t, rp, handoffMessage(t, 3, "v1", "dev1", "90", ""))
	require.True(t, rp.checkpoint(context.Background()))
	require.Len(t, w.events, 3, "pairing alone cannot justify dropping a new old-version raise")
	require.Equal(t, runtime.EdgeRaised, w.events[2].Edge)
}

// Even when a restored successor is already raised, replayed old-version raises
// are republished with stable identities rather than losing their replay latch.
func TestReplayThresholdRaisePassesWithRaisedSuccessor(t *testing.T) {
	v1 := handoffRule(t, "v1", rules.TypeThreshold, false)
	v2 := handoffRule(t, "v2", rules.TypeThreshold, false)
	rp, w := handoffProcessor(t, 4, v1)
	handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
	require.True(t, rp.checkpoint(context.Background()))
	handoffActivate(t, rp, v2)
	require.True(t, rp.checkpoint(context.Background()))
	// Restart from the v2-latched checkpoint and replay an old-version reading.
	rp.closeDetector()
	require.NoError(t, rp.restore(context.Background()))
	require.NoError(t, rp.startDeadmanGate(context.Background()))
	rp.Replay = &fakeReplayOpener{msgs: []messaging.Message{handoffMessage(t, 2, "v1", "dev1", "90", "")}, head: 2}
	require.NoError(t, rp.replayToHead())
	require.Len(t, w.events, 2, "replay must bypass the live redundant-raise gate")
	require.Equal(t, "acme/p1@v1/r1", w.events[1].RuleID)
	require.True(t, rp.engine.IsRaised(dc.SeriesKey{Rule: v1.Compiled.ID, Series: "dev1"}))
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type movingRuleRouter struct{ other atomic.Int32 }

func (r *movingRuleRouter) ShardGroup(tenant string) (int, error) {
	if tenant == "other" {
		return int(r.other.Load()), nil
	}
	return 0, nil
}

func (*movingRuleRouter) Ready() error { return nil }

func routedRuleFixture(t *testing.T, r *groupRig) (*ResolvedEventsProcessor, *ResolvedEventsProcessor) {
	t.Helper()
	p0, p1 := r.worker(t, 0), r.worker(t, 1)
	p0.Lease, p0.Gate = &messaging.DistributedLease{}, r.gate
	p0.RuleStore = newTestRuleStore(t)
	p1.RuleStore = p0.RuleStore
	ctx, cancel := context.WithCancel(context.Background())
	p0.procCtx, p0.procCancel = ctx, cancel
	p1.procCtx, p1.procCancel = ctx, cancel
	t.Cleanup(cancel)
	p0.ruleUpdates, p1.ruleUpdates = make(chan ruleUpdate, 16), make(chan ruleUpdate, 16)
	require.NoError(t, p0.attachRoutedRuleWorkers(r.nm, p1))
	return p0, p1
}

func receiveRuleUpdate(t *testing.T, p *ResolvedEventsProcessor) ruleUpdate {
	t.Helper()
	select {
	case u := <-p.ruleUpdates:
		return u
	case <-time.After(5 * time.Second):
		t.Fatal("fact did not reach owning worker")
		return ruleUpdate{}
	}
}

// Real fact transport and SQLite projection, with synchronous loop steps so the
// assertions never read a registry concurrently with its writer.
func TestGroupRuleFacts(t *testing.T) {
	r := newGroupRig(t)
	p0, p1 := routedRuleFixture(t, r)
	var reader messaging.MessageReader
	r.b.brokerManager(t, func(nm *messaging.NatsManager) error {
		var err error
		reader, err = nm.NewReader(streams.DetectionRulesPublished)
		return err
	})
	p0.RuleUpdatesReader = reader
	js, err := r.b.nc.JetStream()
	require.NoError(t, err)
	publish := func(tenant, token, def string) {
		m := factMsg(t, 1, tenant, "p@1", &fakeAck{}, dmmodel.PublishedDetectionRule{Token: token, Definition: def})
		_, err := js.Publish(messaging.ScopedSubject(r.b.instance, tenant, streams.DetectionRulesPublished), m.Value)
		require.NoError(t, err)
	}
	publish("other", "g1", deadRule)
	publish("acme", "g0", validFactRule)
	// One process catch-up persists both tenants without touching either registry.
	require.NoError(t, p0.catchUpFactProjections())
	for g, p := range []*ResolvedEventsProcessor{p0, p1} {
		require.NoError(t, p.prepareGroupRules(context.Background()))
		require.NoError(t, p.restore(context.Background()))
		require.NoError(t, p.reconcileRegistry(context.Background()))
		require.Equal(t, 1, p.registry.Count(), "group %d loaded another tenant", g)
	}
	_, ok := p1.registry.Lookup("other/p@1/g1")
	require.True(t, ok)
	_, ok = p0.registry.Lookup("other/p@1/g1")
	require.False(t, ok)
	// Exactly one consumer hands live updates to the owner; applying a queued update
	// is the same single-writer operation used by run().
	p0.readerWG.Add(1)
	go p0.runRuleConsumer()
	t.Cleanup(func() { p0.pcancel(); p0.readerWG.Wait() })
	publish("other", "live1", deadRule)
	u := receiveRuleUpdate(t, p1)
	require.Empty(t, p0.ruleUpdates)
	p1.applyRuleUpdate(u)
	_, ok = p1.registry.Lookup("other/p@1/live1")
	require.True(t, ok)
	publish("acme", "live0", validFactRule)
	p0.applyRuleUpdate(receiveRuleUpdate(t, p0))
	require.Empty(t, p1.ruleUpdates)
	_, ok = p0.registry.Lookup("acme/p@1/live0")
	require.True(t, ok)
	// The repair/removal door routes by tenant too, including a removal-only batch.
	id := "other/p@1/live1"
	p1.engine.SetExpected(detectcore.SeriesKey{Rule: id, Series: "device"}, testBase)
	require.Len(t, p1.engine.ExpectedKeys(), 1)
	require.Empty(t, p0.engine.ExpectedKeys())
	require.NoError(t, p0.signalRuleRepair(context.Background(), "other", "p", "p@1", nil, []string{id}))
	require.Empty(t, p0.ruleUpdates)
	p1.applyRuleUpdate(receiveRuleUpdate(t, p1))
	require.Empty(t, p1.engine.ExpectedKeys(), "removal must disarm the owning engine")
	_, ok = p1.registry.Lookup(id)
	require.False(t, ok)
	require.Equal(t, 2, p0.registry.Count())
	// An unsharded processor retains the original all-tenant path.
	legacy := newTestProcessor(r.store, nil, 10)
	legacy.ruleUpdates = make(chan ruleUpdate, 4)
	for _, tenant := range []string{"other", "acme"} {
		require.True(t, legacy.handleRuleFact(factMsg(t, 1, tenant, "p@1", &fakeAck{}, dmmodel.PublishedDetectionRule{Token: "r", Definition: validFactRule}), true))
		legacy.engine = detectcore.NewEngine(nil, 0)
		legacy.applyRuleUpdate(receiveRuleUpdate(t, legacy))
	}
	require.Equal(t, 2, legacy.registry.Count())
}

func TestGroupRulePlacementRefuses(t *testing.T) {
	router := &movingRuleRouter{}
	router.other.Store(1)
	r := newGroupRig(t, router)
	p0, p1 := routedRuleFixture(t, r)
	compiled, failures := runtime.CompilePublishedRules("other", "p@1", []dmmodel.PublishedDetectionRule{{Token: "r", Definition: deadRule}})
	require.Zero(t, failures)
	for _, p := range []*ResolvedEventsProcessor{p0, p1} {
		require.NoError(t, p.restore(context.Background()))
	}
	p1.applyRuleUpdate(ruleUpdate{upserts: compiled})
	p1.engine.SetExpected(detectcore.SeriesKey{Rule: "other/p@1/r", Series: "device"}, testBase)
	require.Len(t, p1.engine.ExpectedKeys(), 1)
	payload, err := p1.engine.Snapshot()
	require.NoError(t, err)
	require.NoError(t, p1.admitGroupSnapshot(payload))
	require.Error(t, p0.admitGroupSnapshot(payload), "g0 must refuse g1 state")
	// Even an idle worker with an armed tenant must fail before publishing state.
	router.other.Store(0)
	require.False(t, p1.checkpoint(context.Background()))
	require.Error(t, p0.pctx().Err(), "placement failure must cancel the shared term")
	require.ErrorContains(t, p1.prepareGroupRules(context.Background()), "frozen placement")
	a := &fakeAck{}
	require.False(t, p0.handleRuleFact(factMsg(t, 2, "other", "p@2", a, dmmodel.PublishedDetectionRule{Token: "r", Definition: validFactRule}), true))
	require.Zero(t, a.acks, "an ownership refusal must leave the fact unacked")
	// Mutable views may share only the same durable projection set.
	p0.ruleRouting, p1.ruleRouting = nil, nil
	p0.groupWorkers, p1.supervised = nil, false
	p1.AttributeStore = &model.DeviceAttributeStore{}
	require.ErrorContains(t, p0.attachRoutedRuleWorkers(r.nm, p1), "one durable projection set")
}

func TestGroupRuleSupervisorLiveFacts(t *testing.T) {
	r := newGroupRig(t)
	p0, p1 := routedRuleFixture(t, r)
	r.b.brokerManager(t, func(nm *messaging.NatsManager) error {
		var err error
		p0.RuleUpdatesReader, err = nm.NewReader(streams.DetectionRulesPublished, messaging.ReaderWithTermGate(r.gate.Held))
		return err
	})
	dl, err := r.nm.NewDistributedLease(messaging.DefaultLeaseTTL)
	require.NoError(t, err)
	p0.Lease = dl
	require.NoError(t, p0.ExecuteInitialize(context.Background()))
	prior, err := dl.Acquire(singletonPartition)
	require.NoError(t, err)
	require.NoError(t, prior.ReleaseWithin(context.Background()))
	js, err := r.b.nc.JetStream()
	require.NoError(t, err)
	// Downtime facts must be installed before replay in BOTH groups, including g0.
	for g, tenant := range []string{"acme", "other"} {
		m := factMsg(t, 1, tenant, "p@1", &fakeAck{}, dmmodel.PublishedDetectionRule{Token: "startup", Definition: deadRule})
		_, err := js.Publish(messaging.ScopedSubject(r.b.instance, tenant, streams.DetectionRulesPublished), m.Value)
		require.NoError(t, err)
		ev, err := resolvedevent.UnmarshalResolvedEvent(resolvedBytes(t, testBase))
		require.NoError(t, err)
		ev.ProfileVersionToken = "p@1"
		payload, err := resolvedevent.MarshalResolvedEvent(ev)
		require.NoError(t, err)
		_, err = js.Publish(messaging.ScopedSubject(r.b.instance, tenant, r.ids[g].physical), payload)
		require.NoError(t, err)
	}
	for term := 1; term <= 2; term++ {
		h, err := p0.beginTerm()
		require.NoError(t, err)
		func() {
			defer p0.endTerm(h)
			if term == 1 {
				for g, tenant := range []string{"acme", "other"} {
					require.Contains(t, string(r.row(t, g, 1).Payload), tenant+"/p@1/startup", "startup catch-up must precede replay")
				}
			}
			for _, tenant := range []string{"other", "acme"} {
				m := factMsg(t, 1, tenant, "p@1", &fakeAck{}, dmmodel.PublishedDetectionRule{Token: string(rune('a' + term)), Definition: deadRule})
				_, err := js.Publish(messaging.ScopedSubject(r.b.instance, tenant, streams.DetectionRulesPublished), m.Value)
				require.NoError(t, err)
			}
			require.Eventually(t, func() bool {
				return testutil.ToFloat64(p0.metrics.rulesActive) == float64(term+1) && testutil.ToFloat64(p1.metrics.rulesActive) == float64(term+1)
			}, 5*time.Second, 10*time.Millisecond, "shared consumer did not reach every live worker")
		}()
		// All loops have joined, so direct registry inspection is safe.
		for g, p := range []*ResolvedEventsProcessor{p0, p1} {
			tenant := "acme"
			if g == 1 {
				tenant = "other"
			}
			for _, id := range p.registry.IDs() {
				got, ok := runtime.RuleTenant(id)
				require.True(t, ok)
				require.Equal(t, tenant, got)
			}
		}
	}
}

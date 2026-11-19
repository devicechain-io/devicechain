// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dmcontract "github.com/devicechain-io/dc-device-management/contract"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/entityevent"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func membershipFixture(t *testing.T, r *groupRig) (*ResolvedEventsProcessor, *ResolvedEventsProcessor) {
	t.Helper()
	p0, p1 := routedRuleFixture(t, r)
	for _, p := range []*ResolvedEventsProcessor{p0, p1} {
		p.armUpdates = make(chan armUpdate, 16)
		p.seriesDrops = make(chan seriesDrop, 16)
	}
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "membership.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.DeviceRoster{}, &model.ProfileActive{}, &model.DetectRule{}))
	rm := &rdb.RdbManager{Database: db}
	p0.RuleStore = model.NewDetectRuleStore(rm)
	p1.RuleStore = p0.RuleStore
	p0.RosterStore, p0.ProfileActiveStore = model.NewDeviceRosterStore(rm), model.NewProfileActiveStore(rm)
	p1.RosterStore, p1.ProfileActiveStore = p0.RosterStore, p0.ProfileActiveStore
	p0.ruleRouting, p1.ruleRouting, p0.groupWorkers, p1.supervised = nil, nil, nil, false
	require.NoError(t, p0.attachRoutedRuleWorkers(r.nm, p1))
	return p0, p1
}

func receiveArmUpdate(t *testing.T, p *ResolvedEventsProcessor) armUpdate {
	t.Helper()
	select {
	case u := <-p.armUpdates:
		return u
	case <-time.After(5 * time.Second):
		t.Fatal("membership did not reach owning worker")
		return armUpdate{}
	}
}

// The real broker feeds the singleton consumers and SQLite is the restart source.
// Loop operations are driven synchronously to inspect views without a data race.
func TestGroupMembershipFacts(t *testing.T) {
	r := newGroupRig(t)
	p0, p1 := membershipFixture(t, r)
	r.b.brokerManager(t, func(nm *messaging.NatsManager) error {
		var err error
		p0.RuleUpdatesReader, err = nm.NewReader(streams.DetectionRulesPublished)
		if err != nil {
			return err
		}
		p0.RosterReader, err = nm.NewReader(streams.DeviceRoster)
		if err != nil {
			return err
		}
		p0.EntityDeletedReader, err = nm.NewReader(streams.EntityDeleted)
		return err
	})
	js, err := r.b.nc.JetStream()
	require.NoError(t, err)
	publish := func(tenant, suffix string, m messaging.Message) {
		_, err := js.Publish(messaging.ScopedSubject(r.b.instance, tenant, suffix), m.Value)
		require.NoError(t, err)
	}
	for _, tenant := range []string{"other", "acme"} {
		publish(tenant, streams.DetectionRulesPublished, membershipRuleMessage(t, tenant))
		publish(tenant, streams.DeviceRoster, rosterMsg(t, tenant, "never", "p", testBase, &fakeAck{}))
		publish(tenant, streams.DeviceRoster, rosterMsg(t, tenant, "gone", "p", testBase, &fakeAck{}))
		publish(tenant, streams.EntityDeleted, entityDeletedMsg(t, tenant, entity.TypeDevice, "gone", &fakeAck{}))
	}
	require.NoError(t, p0.catchUpFactProjections())
	for g, p := range []*ResolvedEventsProcessor{p0, p1} {
		require.NoError(t, p.prepareGroupRules(context.Background()))
		require.NoError(t, p.restore(context.Background()))
		require.NoError(t, p.startDeadmanGate(context.Background()))
		// Inspect the loaded gate's membership independently of the filtered registry.
		// A temporary foreign rule is only a read probe, never installed in the engine.
		foreign := []string{"other", "acme"}[g]
		probe := absScopedProc(foreign, "p@1", foreign+"/p@1/probe")
		p.registry.Upsert(probe)
		require.False(t, p.armer.AbsenceLive(detectcore.SeriesKey{Rule: probe.Compiled.ID, Series: "never"}), "foreign roster leaked into gate")
		p.registry.Remove(probe.Compiled.ID)
		require.Empty(t, p.engine.ExpectedKeys(), "pre-replay gate must not arm")
		require.NoError(t, p.reconcileDeadmanArming(context.Background()))
		// Inspect the loaded gate's membership independently of the filtered registry.
		// A temporary foreign rule is only a read probe, never installed in the engine.
		foreign = []string{"other", "acme"}[g]
		probe = absScopedProc(foreign, "p@1", foreign+"/p@1/probe")
		p.registry.Upsert(probe)
		require.False(t, p.armer.AbsenceLive(detectcore.SeriesKey{Rule: probe.Compiled.ID, Series: "never"}), "foreign roster leaked into gate")
		p.registry.Remove(probe.Compiled.ID)
		tenant := []string{"acme", "other"}[g]
		require.Equal(t, []detectcore.SeriesKey{{Rule: tenant + "/p@1/dead", Series: "never"}}, p.engine.ExpectedKeys())

	}
	p0.readerWG.Add(3)
	go p0.runRuleConsumer()
	go p0.runRosterConsumer()
	go p0.runEntityDeletedConsumer()
	t.Cleanup(func() { p0.pcancel(); p0.readerWG.Wait() })
	for g, p := range []*ResolvedEventsProcessor{p0, p1} {
		tenant := []string{"acme", "other"}[g]
		other := []*ResolvedEventsProcessor{p1, p0}[g]
		publish(tenant, streams.DeviceRoster, rosterMsg(t, tenant, "live", "p", testBase, &fakeAck{}))
		p.applyArmRecheck(receiveArmUpdate(t, p))
		require.Empty(t, other.armUpdates)
		require.Len(t, p.engine.ExpectedKeys(), 2)
		publish(tenant, streams.EntityDeleted, entityDeletedMsg(t, tenant, entity.TypeDevice, "live", &fakeAck{}))
		p.applyArmRecheck(receiveArmUpdate(t, p))
		select {
		case sd := <-p.seriesDrops:
			p.applySeriesDrop(sd)
		case <-time.After(5 * time.Second):
			t.Fatal("deletion did not reach owner")
		}
		require.Empty(t, other.armUpdates)
		require.Empty(t, other.seriesDrops)
		require.Len(t, p.engine.ExpectedKeys(), 1, "deletion must disarm only its owner")
		// Re-typing to no profile is a membership descope on the same routed door.
		publish(tenant, streams.DeviceRoster, rosterMsg(t, tenant, "never", "", testBase.Add(2*time.Hour), &fakeAck{}))
		p.applyArmRecheck(receiveArmUpdate(t, p))
		require.Empty(t, p.engine.ExpectedKeys())
		// A profile publish changes the active version on the owning loop only.
		publish(tenant, streams.DeviceRoster, rosterMsg(t, tenant, "late", "p", testBase, &fakeAck{}))
		p.applyArmRecheck(receiveArmUpdate(t, p))
		require.Equal(t, []detectcore.SeriesKey{{Rule: tenant + "/p@1/dead", Series: "late"}}, p.engine.ExpectedKeys())
		b, err := dmproto.MarshalDetectionRulesPublishedEvent(&dmcontract.DetectionRulesPublishedEvent{
			ProfileVersionToken: "p@2", PublishedAt: testBase.Add(time.Second), Rules: []dmcontract.PublishedDetectionRule{{Token: "dead", Definition: deadRule}},
		})
		require.NoError(t, err)
		publish(tenant, streams.DetectionRulesPublished, messaging.NewConsumedMessage("", b, 0, nil, &fakeAck{}))
		p.applyRuleUpdate(receiveRuleUpdate(t, p))
		require.Empty(t, other.ruleUpdates)
		require.Equal(t, []detectcore.SeriesKey{{Rule: tenant + "/p@2/dead", Series: "late"}}, p.engine.ExpectedKeys())
	}
	// Both startup loads and post-replay reconciliation must keep foreign versions out.
	rosters, err := p0.RosterStore.LoadAll(context.Background())
	require.NoError(t, err)
	actives, err := p0.ProfileActiveStore.LoadAll(context.Background())
	require.NoError(t, err)
	for g, p := range []*ResolvedEventsProcessor{p0, p1} {
		rr, aa, err := p.ownedMembership(rosters, actives)
		require.NoError(t, err)
		require.Len(t, rr, 2)
		require.Len(t, aa, 1)
		require.Equal(t, []string{"acme", "other"}[g], rr[0].Tenant)
		require.Equal(t, rr[0].Tenant, aa[0].Tenant)
	}
}

func TestGroupMembershipPlacementRefuses(t *testing.T) {
	router := &movingRuleRouter{}
	router.other.Store(1)
	r := newGroupRig(t, router)
	p0, p1 := membershipFixture(t, r)
	require.True(t, p0.handleRosterFact(rosterMsg(t, "other", "never", "p", testBase, &fakeAck{}), false))
	router.other.Store(0)
	ack := &fakeAck{}
	require.False(t, p0.handleEntityDeletedFact(entityDeletedMsg(t, "other", entity.TypeDevice, "never", ack), false))
	require.Zero(t, ack.acks)
	require.Error(t, p1.pctx().Err())
	row, live, err := p0.RosterStore.Load(context.Background(), "other", "never")
	require.NoError(t, err)
	require.True(t, live)
	require.False(t, row.Deleted)
	require.Error(t, p1.startDeadmanGate(context.Background()))
	// A mismatched attribute projection is refused at attachment.
	p0.ruleRouting, p1.ruleRouting, p0.groupWorkers, p1.supervised = nil, nil, nil, false
	p0.AttributeStore = &model.DeviceAttributeStore{}
	require.ErrorContains(t, p0.attachRoutedRuleWorkers(r.nm, p1), "one durable projection set")
}

func membershipRuleMessage(t *testing.T, tenant string) messaging.Message {
	t.Helper()
	b, err := dmproto.MarshalDetectionRulesPublishedEvent(&dmcontract.DetectionRulesPublishedEvent{
		ProfileVersionToken: "p@1", PublishedAt: testBase,
		Rules: []dmcontract.PublishedDetectionRule{{Token: "dead", Definition: deadRule}},
	})
	require.NoError(t, err)
	return messaging.NewConsumedMessage("dc."+tenant+".detection-rules-published", b, 0, nil, &fakeAck{})
}

// This exercises the supervisor's real all-worker build and delayed singleton
// consumer launch, over two terms. Assertions read committed SQLite snapshots.
func TestGroupMembershipSupervisor(t *testing.T) {
	r := newGroupRig(t)
	p0, p1 := membershipFixture(t, r)
	r.b.brokerManager(t, func(nm *messaging.NatsManager) error {
		var err error
		p0.RuleUpdatesReader, err = nm.NewReader(streams.DetectionRulesPublished, messaging.ReaderWithTermGate(r.gate.Held))
		if err != nil {
			return err
		}
		p0.RosterReader, err = nm.NewReader(streams.DeviceRoster, messaging.ReaderWithTermGate(r.gate.Held))
		if err != nil {
			return err
		}
		p0.EntityDeletedReader, err = nm.NewReader(streams.EntityDeleted, messaging.ReaderWithTermGate(r.gate.Held))
		return err
	})
	dl, err := r.nm.NewDistributedLease(messaging.DefaultLeaseTTL)
	require.NoError(t, err)
	p0.Lease = dl
	for _, p := range []*ResolvedEventsProcessor{p0, p1} {
		p.cfg.CheckpointEvents = 1
	}
	require.NoError(t, p0.ExecuteInitialize(context.Background()))
	prior, err := dl.Acquire(singletonPartition)
	require.NoError(t, err)
	require.NoError(t, prior.ReleaseWithin(context.Background()))
	js, err := r.b.nc.JetStream()
	require.NoError(t, err)
	publish := func(tenant, suffix string, m messaging.Message) {
		_, err := js.Publish(messaging.ScopedSubject(r.b.instance, tenant, suffix), m.Value)
		require.NoError(t, err)
	}
	for _, tenant := range []string{"acme", "other"} {
		publish(tenant, streams.DetectionRulesPublished, membershipRuleMessage(t, tenant))
		publish(tenant, streams.DeviceRoster, rosterMsg(t, tenant, "never", "p", testBase, &fakeAck{}))
	}
	waitSnapshot := func(g int, seq int64, hasLive bool) {
		require.Eventually(t, func() bool {
			row, ok, err := r.store.Load(context.Background(), groupPartition(g))
			if err != nil || !ok || row.StreamSeq != seq {
				return false
			}
			var snap struct {
				Expected []struct{ Rule, Series string }
			}
			if json.Unmarshal(row.Payload, &snap) != nil {
				return false
			}
			never, live := false, false
			for _, key := range snap.Expected {
				if !strings.HasPrefix(key.Rule, []string{"acme/", "other/"}[g]) {
					return false
				}
				never = never || key.Series == "never"
				live = live || key.Series == "live"
			}
			if never && live == hasLive {
				return true
			}
			return false
		}, 5*time.Second, 10*time.Millisecond, "group %d seq %d live %v did not checkpoint its own membership", g, seq, hasLive)
	}
	settled := func(reader messaging.MessageReader) {
		require.Eventually(t, func() bool {
			pending, acks, err := reader.(consumerBacklog).Backlog(context.Background())
			return err == nil && pending == 0 && acks == 0 && len(p0.armUpdates) == 0 && len(p1.armUpdates) == 0 && len(p0.seriesDrops) == 0 && len(p1.seriesDrops) == 0
		}, 5*time.Second, 10*time.Millisecond, "fact handoffs did not settle on owner loops")
	}
	for term := 1; term <= 2; term++ {
		h, err := p0.beginTerm()
		require.NoError(t, err)
		func() {
			defer p0.endTerm(h)
			for _, tenant := range []string{"acme", "other"} {
				publish(tenant, streams.DeviceRoster, rosterMsg(t, tenant, "live", "p", testBase.Add(time.Duration(term)*2*time.Hour), &fakeAck{}))
			}
			// Wait for projection writes before resolved input prompts a checkpoint.
			// The committed snapshot proves the loop applied the membership recheck.
			require.Eventually(t, func() bool {
				for _, tenant := range []string{"acme", "other"} {
					row, ok, err := p0.RosterStore.Load(context.Background(), tenant, "live")
					if err != nil || !ok || !row.ExpectedSince.Equal(testBase.Add(time.Duration(term)*2*time.Hour)) {
						return false
					}
				}
				return true
			}, 5*time.Second, 10*time.Millisecond)
			settled(p0.RosterReader)
			for g := 0; g < 2; g++ {
				seq := 2*term - 1
				r.publish(t, g, seq, seq)
				waitSnapshot(g, int64(seq), true)
			}
			b, err := entityevent.MarshalEntityDeletedEvent(&entityevent.EntityDeletedEvent{
				EntityType: entity.TypeDevice, EntityId: 1, EntityToken: "live", DeletedTime: testBase.Add(time.Duration(term)*2*time.Hour + time.Hour),
			})
			require.NoError(t, err)
			for _, tenant := range []string{"acme", "other"} {
				publish(tenant, streams.EntityDeleted, messaging.NewConsumedMessage("", b, 0, nil, &fakeAck{}))
			}
			require.Eventually(t, func() bool {
				for _, tenant := range []string{"acme", "other"} {
					row, live, err := p0.RosterStore.Load(context.Background(), tenant, "live")
					if err != nil || live || !row.Deleted {
						return false
					}
				}
				return true
			}, 5*time.Second, 10*time.Millisecond)
			settled(p0.EntityDeletedReader)
			for g := 0; g < 2; g++ {
				seq := 2 * term
				r.publish(t, g, seq, seq)
				waitSnapshot(g, int64(seq), false)
			}
		}()
		for g, p := range []*ResolvedEventsProcessor{p0, p1} {
			require.Len(t, p.engine.ExpectedKeys(), 1)
			for _, k := range p.engine.ExpectedKeys() {
				require.True(t, strings.HasPrefix(k.Rule, []string{"acme/", "other/"}[g]))
			}
		}
	}
}

// A source repair uses the same owner door as a delivered fact, including the
// two-sweep confirmation of removal. No mutable armer is touched off-loop.
type membershipRosterSource struct {
	DeviceManagementFacts
	rows []dmcontract.DeviceRosterEntry
}

func (s *membershipRosterSource) Roster(context.Context, string) ([]dmcontract.DeviceRosterEntry, error) {
	return s.rows, nil
}

func TestGroupMembershipReconcile(t *testing.T) {
	r := newGroupRig(t)
	p0, p1 := membershipFixture(t, r)
	source := &membershipRosterSource{rows: []dmcontract.DeviceRosterEntry{{DeviceToken: "lost", ProfileToken: "p", ExpectedSince: testBase}}}
	p0.DeviceManagement = source
	for _, tenant := range []string{"acme", "other"} {
		require.True(t, p0.handleRuleFact(membershipRuleMessage(t, tenant), false))
	}
	for _, p := range []*ResolvedEventsProcessor{p0, p1} {
		require.NoError(t, p.prepareGroupRules(context.Background()))
		require.NoError(t, p.restore(context.Background()))
		require.NoError(t, p.startDeadmanGate(context.Background()))
	}
	p0.factAbsent = newAbsentSeen()
	for g, p := range []*ResolvedEventsProcessor{p0, p1} {
		tenant := []string{"acme", "other"}[g]
		counts, err := p0.reconcileTenantRoster(context.Background(), tenant, testBase.Add(time.Hour))
		require.NoError(t, err)
		require.Equal(t, 1, counts[projectionRoster])
		p.applyArmRecheck(receiveArmUpdate(t, p))
		require.Len(t, p.engine.ExpectedKeys(), 1)
		require.Empty(t, []*ResolvedEventsProcessor{p1, p0}[g].armUpdates)
	}
	source.rows = nil
	for g, p := range []*ResolvedEventsProcessor{p0, p1} {
		tenant := []string{"acme", "other"}[g]
		for sweep := 0; sweep < 2; sweep++ {
			_, err := p0.reconcileTenantRoster(context.Background(), tenant, testBase.Add(time.Hour))
			require.NoError(t, err)
		}
		p.applyArmRecheck(receiveArmUpdate(t, p))
		require.Empty(t, p.engine.ExpectedKeys())
	}
}

// Consumer accounting may briefly report zero while the fact is already readable.
// A group build must still perform its initial bounded catch-up read.
type groupZeroBacklogReader struct{ *fakeReader }

func (*groupZeroBacklogReader) Backlog(context.Context) (uint64, uint64, error) { return 0, 0, nil }

func TestGroupMembershipZeroBacklogCatchup(t *testing.T) {
	r := newGroupRig(t)
	p0, p1 := membershipFixture(t, r)
	require.True(t, p0.handleRuleFact(membershipRuleMessage(t, "other"), false))
	p0.RosterReader = &groupZeroBacklogReader{&fakeReader{results: []readResult{{msg: rosterMsg(t, "other", "never", "p", testBase, &fakeAck{})}}}}
	require.NoError(t, p0.catchUpFactProjections())
	for g, p := range []*ResolvedEventsProcessor{p0, p1} {
		require.NoError(t, p.prepareGroupRules(context.Background()))
		require.NoError(t, p.restore(context.Background()))
		require.NoError(t, p.startDeadmanGate(context.Background()))
		require.NoError(t, p.reconcileDeadmanArming(context.Background()))
		require.Len(t, p.engine.ExpectedKeys(), g)
	}
}

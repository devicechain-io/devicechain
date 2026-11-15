// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmprocessor "github.com/devicechain-io/dc-device-management/processor"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/entityevent"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func groupViewsFixture(t *testing.T, r *groupRig) (*ResolvedEventsProcessor, *ResolvedEventsProcessor, *dmmodel.Api, *schemaFenceSource) {
	t.Helper()
	p0, p1 := membershipFixture(t, r)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "attributes.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.DeviceAttribute{}, &model.DeviceAttributeDeletion{}))
	store := model.NewDeviceAttributeStore(&rdb.RdbManager{Database: db})
	api := newFenceDmApi(t)
	src := newSchemaFenceSource(t, api)
	for _, p := range []*ResolvedEventsProcessor{p0, p1} {
		p.AttributeStore = store
		p.FenceSets, p.VersionedFenceSets, p.FenceManifests = src, src, src
		p.attrUpdates, p.fenceUpdates = make(chan attrUpdate, 32), make(chan fenceUpdate, 32)
	}
	p0.ruleRouting, p1.ruleRouting, p0.groupWorkers, p1.supervised = nil, nil, nil, false
	require.NoError(t, p0.attachRoutedRuleWorkers(r.nm, p1))
	return p0, p1, api, src
}

func receiveAttrUpdate(t *testing.T, p *ResolvedEventsProcessor) attrUpdate {
	t.Helper()
	select {
	case u := <-p.attrUpdates:
		return u
	case <-time.After(5 * time.Second):
		t.Fatal("attribute did not reach owning worker")
		return attrUpdate{}
	}
}

func receiveFenceUpdate(t *testing.T, p *ResolvedEventsProcessor) fenceUpdate {
	t.Helper()
	select {
	case u := <-p.fenceUpdates:
		return u
	case <-time.After(5 * time.Second):
		t.Fatal("fence did not reach owning worker")
		return fenceUpdate{}
	}
}

// Real broker consumers, SQL projection and the real device-management fence
// archive/client. Loop mutations are stepped synchronously to inspect views safely.
func TestGroupAttributeFenceFacts(t *testing.T) {
	r := newGroupRig(t)
	p0, p1, api, _ := groupViewsFixture(t, r)
	r.b.brokerManager(t, func(nm *messaging.NatsManager) error {
		var err error
		p0.AttributeReader, err = nm.NewReader(streams.DeviceAttribute)
		if err != nil {
			return err
		}
		p0.FenceSetReader, err = nm.NewReader(streams.GeoFenceSetManifest)
		return err
	})
	js, err := r.b.nc.JetStream()
	require.NoError(t, err)
	publish := func(tenant, suffix string, value []byte) {
		_, err := js.Publish(messaging.ScopedSubject(r.b.instance, tenant, suffix), value)
		require.NoError(t, err)
	}
	attribute := func(tenant string, value float64, removed bool, at time.Time) messaging.Message {
		return attrMsg(t, tenant, &dmmodel.DeviceAttributeEvent{DeviceToken: "d", Scope: "SHARED", AttrKey: "limit", Value: value, Removed: removed, UpdatedAt: at}, &fakeAck{})
	}
	for g, p := range []*ResolvedEventsProcessor{p0, p1} {
		tenant := []string{"acme", "other"}[g]
		publish(tenant, streams.DeviceAttribute, attribute(tenant, float64(g+10), false, testBase).Value)
		_, err := api.CreateGeoFence(core.WithTenant(context.Background(), tenant), &dmmodel.GeoFenceCreateRequest{Token: "yard", Geometry: fenceBox(0, 0, 1, 1)})
		require.NoError(t, err)
		for _, sr := range fenceScopedRules(t, tenant, "p@1", "yard") {
			p.registry.Upsert(sr)
		}
	}
	// Catch-up must not signal stale channels from an earlier term, even if views exist.
	for _, p := range []*ResolvedEventsProcessor{p0, p1} {
		require.NoError(t, p.startAttributeView(context.Background()))
	}
	require.NoError(t, p0.catchUpFactProjections())
	for g, p := range []*ResolvedEventsProcessor{p0, p1} {
		tenant, foreign := []string{"acme", "other"}[g], []string{"other", "acme"}[g]
		require.Empty(t, p.attrUpdates)
		require.NoError(t, p.startAttributeView(context.Background()))
		require.Equal(t, float64(g+10), p.attrView.For(tenant, "d")["limit"])
		require.Empty(t, p.attrView.For(foreign, "d"))
		require.NoError(t, p.startFenceView(context.Background()))
		require.Equal(t, []int32{1}, p.fenceView.RetainedVersions(tenant))
		require.Empty(t, p.fenceView.RetainedVersions(foreign))
		require.NoError(t, p.reconcileAttributeView(context.Background()))
		require.NoError(t, p.reconcileFenceView(context.Background()))
		require.Empty(t, p.attrView.For(foreign, "d"))
		require.Empty(t, p.fenceView.RetainedVersions(foreign))
	}
	p0.readerWG.Add(2)
	go p0.runAttributeConsumer()
	go p0.runFenceSetConsumer()
	t.Cleanup(func() { p0.pcancel(); p0.readerWG.Wait() })
	for g, p := range []*ResolvedEventsProcessor{p0, p1} {
		tenant, other := []string{"acme", "other"}[g], []*ResolvedEventsProcessor{p1, p0}[g]
		publish(tenant, streams.DeviceAttribute, attribute(tenant, 42, false, testBase.Add(time.Hour)).Value)
		p.applyAttrRecheck(receiveAttrUpdate(t, p))
		require.Empty(t, other.attrUpdates)
		require.Equal(t, float64(42), p.attrView.For(tenant, "d")["limit"])
		require.Empty(t, other.attrView.For(tenant, "d"))
		publish(tenant, streams.DeviceAttribute, attribute(tenant, 0, true, testBase.Add(2*time.Hour)).Value)
		p.applyAttrRecheck(receiveAttrUpdate(t, p))
		require.Empty(t, p.attrView.For(tenant, "d"), "attribute descope must remove the value")
		publish(tenant, streams.DeviceAttribute, attribute(tenant, 55, false, testBase.Add(3*time.Hour)).Value)
		p.applyAttrRecheck(receiveAttrUpdate(t, p))
		deleted, err := entityevent.MarshalEntityDeletedEvent(&entityevent.EntityDeletedEvent{EntityType: entity.TypeDevice, EntityId: 1, EntityToken: "d", DeletedTime: testBase.Add(4 * time.Hour)})
		require.NoError(t, err)
		require.True(t, p0.handleEntityDeletedFact(messaging.NewConsumedMessage("dc."+tenant+".entity-deleted", deleted, 0, nil, &fakeAck{}), true))
		p.applyAttrRecheck(receiveAttrUpdate(t, p))
		require.Empty(t, p.attrView.For(tenant, "d"))
		require.Empty(t, other.attrUpdates)
		// Use the producer's real archive mint and manifest emission for update and deletion.
		facts := &fenceFactWriter{}
		api.GeoFenceSetPublisher = dmprocessor.NewGeoFenceSetWriter(facts, 0, nil)
		ctx := core.WithTenant(context.Background(), tenant)
		_, err = api.CreateGeoFence(ctx, &dmmodel.GeoFenceCreateRequest{Token: "second", Geometry: fenceBox(2, 2, 3, 3)})
		require.NoError(t, err)
		raw, _ := lastFact(t, facts)
		publish(tenant, streams.GeoFenceSetManifest, raw)
		p.applyFenceSet(receiveFenceUpdate(t, p))
		require.Empty(t, other.fenceUpdates)
		require.Equal(t, []int32{1, 2}, p.fenceView.RetainedVersions(tenant))
		require.Empty(t, other.fenceView.RetainedVersions(tenant))
		deletedFence, err := api.DeleteGeoFence(ctx, "yard")
		require.NoError(t, err)
		require.True(t, deletedFence)
		raw, _ = lastFact(t, facts)
		publish(tenant, streams.GeoFenceSetManifest, raw)
		p.applyFenceSet(receiveFenceUpdate(t, p))
		require.Equal(t, []int32{1, 2, 3}, p.fenceView.RetainedVersions(tenant))
		require.Nil(t, p.fenceView.For(tenant, 3).Fence("yard"))
		require.NotNil(t, p.fenceView.For(tenant, 1).Fence("yard"), "older immutable versions remain evaluable")
		require.Empty(t, other.fenceUpdates)
		// A lost fact is repaired off-loop from only this worker's registry tenants.
		_, err = api.DeleteGeoFence(ctx, "second")
		require.NoError(t, err)
		p.readerWG.Add(1)
		p.runFenceReconcile([]string{tenant})
		p.applyFenceSet(receiveFenceUpdate(t, p))
		require.Equal(t, []int32{1, 2, 3, 4}, p.fenceView.RetainedVersions(tenant))
		require.Zero(t, p.fenceView.For(tenant, 4).Len(), "repair must carry the empty set after deletion")
		require.Empty(t, other.fenceUpdates)
	}
}

type groupAttributeSource struct {
	DeviceManagementFacts
	rows []dmmodel.DeviceThresholdAttribute
}

func (s *groupAttributeSource) ThresholdAttributes(context.Context, string) ([]dmmodel.DeviceThresholdAttribute, error) {
	return s.rows, nil
}

func TestGroupAttributeRepair(t *testing.T) {
	r := newGroupRig(t)
	p0, p1, _, _ := groupViewsFixture(t, r)
	source := &groupAttributeSource{rows: []dmmodel.DeviceThresholdAttribute{{DeviceToken: "lost", Scope: "SHARED", AttrKey: "limit", Value: 27, UpdatedAt: testBase}}}
	p0.DeviceManagement = source
	p0.factAbsent = newAbsentSeen()
	for _, p := range []*ResolvedEventsProcessor{p0, p1} {
		require.NoError(t, p.startAttributeView(context.Background()))
	}
	for g, p := range []*ResolvedEventsProcessor{p0, p1} {
		tenant := []string{"acme", "other"}[g]
		counts, err := p0.reconcileTenantAttributes(context.Background(), tenant, testBase.Add(time.Hour))
		require.NoError(t, err)
		require.Equal(t, 1, counts[projectionAttributes])
		p.applyAttrRecheck(receiveAttrUpdate(t, p))
		require.Equal(t, float64(27), p.attrView.For(tenant, "lost")["limit"])
		require.Empty(t, []*ResolvedEventsProcessor{p1, p0}[g].attrUpdates)
	}
	source.rows = nil
	for g, p := range []*ResolvedEventsProcessor{p0, p1} {
		tenant := []string{"acme", "other"}[g]
		for sweep := 0; sweep < 2; sweep++ {
			_, err := p0.reconcileTenantAttributes(context.Background(), tenant, testBase.Add(time.Hour))
			require.NoError(t, err)
		}
		p.applyAttrRecheck(receiveAttrUpdate(t, p))
		require.Empty(t, p.attrView.For(tenant, "lost"))
		require.Empty(t, []*ResolvedEventsProcessor{p1, p0}[g].attrUpdates)
	}
}

func TestGroupViewPlacementRefuses(t *testing.T) {
	for _, kind := range []string{"attribute", "fence", "repair", "attribute-loop", "fence-loop"} {
		t.Run(kind, func(t *testing.T) {
			router := &movingRuleRouter{}
			router.other.Store(1)
			r := newGroupRig(t, router)
			p0, p1, _, _ := groupViewsFixture(t, r)
			_, err := p0.ruleWorker("other")
			require.NoError(t, err)
			router.other.Store(0)
			ack := &fakeAck{}
			switch kind {
			case "attribute":
				require.False(t, p0.handleAttributeFact(attrMsg(t, "other", &dmmodel.DeviceAttributeEvent{DeviceToken: "d", Scope: "SHARED", AttrKey: "limit", Value: 27, UpdatedAt: testBase}, ack)))
			case "fence":
				raw, err := dmmodel.MarshalGeoFenceSetManifest(&dmmodel.GeoFenceSetManifest{Version: 1})
				require.NoError(t, err)
				require.False(t, p0.handleFenceSetFact(fenceFactMsg("other", raw, ack)))
			case "repair":
				_, err := p0.reconcileTenantAttributes(context.Background(), "other", testBase)
				require.Error(t, err)
			case "attribute-loop":
				p1.applyAttrRecheck(attrUpdate{tenant: "other", deviceToken: "d"})
			case "fence-loop":
				p1.applyFenceSet(fenceUpdate{tenant: "other", set: boxFenceSetFor(1, "yard", 0, 0, 1, 1)})
			}
			require.Zero(t, ack.acks)
			require.Error(t, p1.pctx().Err(), "placement move must end the common term")
			rows, err := p0.AttributeStore.LoadAll(context.Background())
			require.NoError(t, err)
			require.Empty(t, rows)
		})
	}
}

type groupBuildFactProbe struct {
	DeviceManagementFacts
	called chan struct{}
}

func (s *groupBuildFactProbe) Tenants(context.Context) ([]string, error) {
	select {
	case s.called <- struct{}{}:
	default:
	}
	return nil, nil
}

// A slow sibling restore must not race an already-live supervisor's periodic
// source repair against that sibling's view/channel initialization.
func TestGroupBuildDefersFactReconcile(t *testing.T) {
	r := newGroupRig(t)
	p0, _, _, _ := groupViewsFixture(t, r)
	source := &groupBuildFactProbe{called: make(chan struct{}, 1)}
	p0.DeviceManagement = source
	p0.cfg.TickInterval = time.Millisecond
	r.gate.Enter(func() bool { return true })
	t.Cleanup(func() { p0.pcancel(); p0.readerWG.Wait() })
	require.NoError(t, p0.buildTerm(context.Background()))
	// The supervisor has built only group zero. No loop, and therefore no periodic
	// fact repair, may run until buildWorkers finishes every sibling.
	select {
	case <-source.called:
		t.Fatal("fact reconcile ran before sibling workers were built")
	case <-time.After(250 * time.Millisecond):
	}
}

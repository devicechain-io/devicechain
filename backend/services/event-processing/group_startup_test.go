// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	dmcontract "github.com/devicechain-io/dc-device-management/contract"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-event-processing/config"
	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	"github.com/devicechain-io/dc-event-processing/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/entityevent"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestResolvedGroupStartupRefusesUntilSupportGuardLift(t *testing.T) {
	ms := &core.Microservice{InstanceId: "group-refusal", FunctionalArea: "event-processing"}
	ms.InstanceConfiguration.Infrastructure.Nats.ShardGroups = &mscfg.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{streams.ResolvedEvents}}
	nm := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), nil)
	err := createNatsComponents(nm, nil)
	require.ErrorContains(t, err, "S7 support-guard lift")
	require.ErrorContains(t, err, "per-partition purge erasure")
	require.ErrorContains(t, err, "N x K ceiling")
}

// The archive transport is a fixture; main's source wiring, consumers, worker
// lifecycle, evaluation, and SQL checkpoints are real. The processor tests also
// exercise these sources through device-management's real schema and archive.
type mainGroupFences struct{ sets map[int32]*geofence.FenceSet }

func (s *mainGroupFences) CurrentFenceSet(context.Context, string) (*geofence.FenceSet, error) {
	return s.sets[1], nil
}
func (s *mainGroupFences) FenceSetAt(_ context.Context, _ string, version int32) (*geofence.FenceSet, error) {
	return s.sets[version], nil
}
func (s *mainGroupFences) ResolveManifest(_ context.Context, _ string, m *dmcontract.GeoFenceSetManifest) (*geofence.FenceSet, error) {
	return s.sets[m.Version], nil
}

func TestMainResolvedGroupWorkersRouteEveryFact(t *testing.T) {
	host, port := startEmbeddedNats(t)
	prevMs, prevCfg, prevSvc := Microservice, Configuration, Svc
	prevDetect, prevReact, prevReader := ResolvedEventsProcessor, ReactDispatcher, ResolvedEventsReader
	prevSnap, prevRules, prevStats, prevRoster, prevActive, prevAttr, prevReg := SnapshotStore, DetectRuleStore, RuleStatStore, DeviceRosterStore, ProfileActiveStore, DeviceAttributeStore, RuleRegistry
	prevDM, prevFS, prevCFS, prevFM := DeviceManagementFacts, FenceSets, CurrentFenceSets, FenceManifests
	prevDetM, prevReM, prevEU, prevDL, prevGate := DetectMetrics, ReactMetrics, EgressUnresolved, DeadLetters, DetectTermGate
	prevStop, prevRefusal := stopStreamSharding, refuseResolvedEventShards
	t.Cleanup(func() {
		Microservice, Configuration, Svc = prevMs, prevCfg, prevSvc
		ResolvedEventsProcessor, ReactDispatcher, ResolvedEventsReader = prevDetect, prevReact, prevReader
		SnapshotStore, DetectRuleStore, RuleStatStore, DeviceRosterStore, ProfileActiveStore, DeviceAttributeStore, RuleRegistry = prevSnap, prevRules, prevStats, prevRoster, prevActive, prevAttr, prevReg
		DeviceManagementFacts, FenceSets, CurrentFenceSets, FenceManifests = prevDM, prevFS, prevCFS, prevFM
		DetectMetrics, ReactMetrics, EgressUnresolved, DeadLetters, DetectTermGate = prevDetM, prevReM, prevEU, prevDL, prevGate
		stopStreamSharding, refuseResolvedEventShards = prevStop, prevRefusal
	})
	refuseResolvedEventShards = false
	instance := fmt.Sprintf("maingroups%d", time.Now().UnixNano())
	Microservice = &core.Microservice{InstanceId: instance, FunctionalArea: "event-processing"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port, ShardGroups: &mscfg.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{streams.ResolvedEvents}}}
	Configuration = &config.EventProcessingConfiguration{}
	Configuration.ApplyDefaults()
	Configuration.CheckpointEvents, Configuration.CheckpointIntervalSeconds = 1, 1
	Configuration.WatermarkLatenessSeconds, Configuration.IdleAdvanceGuardSeconds = -1, -1
	db := sqlitetest.OpenMemory(t, &gorm.Config{Logger: logger.Discard})
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.DetectSnapshot{}, &model.DetectRule{}, &model.RuleStat{}, &model.DeviceRoster{}, &model.ProfileActive{}, &model.DeviceAttribute{}, &model.DeviceAttributeDeletion{}))
	rm := &rdb.RdbManager{Database: db}
	SnapshotStore, DetectRuleStore, RuleStatStore = model.NewSnapshotStore(rm), model.NewDetectRuleStore(rm), model.NewRuleStatStore(rm)
	DeviceRosterStore, ProfileActiveStore, DeviceAttributeStore = model.NewDeviceRosterStore(rm), model.NewProfileActiveStore(rm), model.NewDeviceAttributeStore(rm)
	empty := geofence.NewFenceSet(1, nil)
	geometry := `{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1],[0,0]]]}}`
	// Compile the immutable geometry once, as an archive client would.
	box := geofence.NewFenceSet(2, []geofence.SnapshotFence{{Token: "yard", Geometry: json.RawMessage(geometry)}})
	source := &mainGroupFences{sets: map[int32]*geofence.FenceSet{1: empty, 2: box}}
	FenceSets, CurrentFenceSets, FenceManifests, DeviceManagementFacts = source, source, source, nil
	Svc = service.New(Microservice, service.Spec{Nats: &service.NatsSpec{}})
	buildMetrics()
	nm := messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), func(nm *messaging.NatsManager) error {
		kv, err := nm.PartitionPlacementStore()
		if err != nil {
			return err
		}
		table := partition.NewTable(2)
		table.Groups, table.Overrides = []int{0, 1}, map[string]int{"g0": 0, "g1": 1}
		raw, err := partition.Encode(table)
		if err != nil {
			return err
		}
		if _, err := kv.Put(partition.TableKey, raw); err != nil {
			return err
		}
		return createNatsComponents(nm, svcclient.NewPeers(Microservice.InstanceConfiguration.Infrastructure, Microservice.FunctionalArea))
	})
	nm.RecordMaxDeliveries(deadletter.MaxDeliveryRecorder(DeadLetters))
	require.NoError(t, nm.Initialize(context.Background()))
	t.Cleanup(func() {
		if stopStreamSharding != nil {
			stopStreamSharding()
		}
		if c := nm.Conn(); c != nil {
			c.Close()
		}
	})
	require.NoError(t, nm.Start(context.Background()), "the real main N-worker construction")
	js, err := nm.Conn().JetStream()
	require.NoError(t, err)
	publish := func(tenant, suffix string, raw []byte) {
		_, err := js.Publish(messaging.ScopedSubject(instance, tenant, suffix), raw)
		require.NoError(t, err)
	}
	base := time.Now().UTC().Add(-time.Minute)
	for _, tenant := range []string{"g0", "g1"} {
		raw, err := dmproto.MarshalDetectionRulesPublishedEvent(&dmcontract.DetectionRulesPublishedEvent{
			ProfileVersionToken: "p@1", PublishedAt: base, Rules: []dmcontract.PublishedDetectionRule{
				{Token: "dead", Definition: `{"name":"dead","type":"absence","timeout":"1h"}`},
				{Token: "views", Definition: `{"name":"views","type":"threshold","when":{"cel":"has(attr.limit) && attr.limit > 10.0 && geo.inFence(\"yard\")"}}`},
			},
		})
		require.NoError(t, err)
		publish(tenant, streams.DetectionRulesPublished, raw)
		for _, device := range []string{"never", "live"} {
			raw, err := dmproto.MarshalDeviceRosterEvent(&dmcontract.DeviceRosterEvent{DeviceToken: device, ProfileToken: "p", ExpectedSince: base})
			require.NoError(t, err)
			publish(tenant, streams.DeviceRoster, raw)
		}
	}
	// Establish a clean predecessor so this test need not wait the fresh-install slack.
	dl, err := nm.NewDistributedLease(messaging.DefaultLeaseTTL)
	require.NoError(t, err)
	prior, err := dl.Acquire(singletonPartition)
	require.NoError(t, err)
	require.NoError(t, prior.ReleaseWithin(context.Background()))
	require.NoError(t, ResolvedEventsProcessor.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, ResolvedEventsProcessor.Stop(context.Background())) })
	partitions := []string{singletonPartition, streams.ResolvedEvents + "-g1"}
	type key struct{ Rule, Series string }
	type snapshot struct{ Expected, Raised []key }
	readSnapshot := func(g int) (snapshot, bool) {
		var snap snapshot
		row, ok, err := SnapshotStore.Load(context.Background(), partitions[g])
		if err != nil || !ok || json.Unmarshal(row.Payload, &snap) != nil {
			return snap, false
		}
		// Check ALL serialized rule references, including retained timer generations.
		if strings.Contains(string(row.Payload), fmt.Sprintf("g%d/", 1-g)) {
			return snap, false
		}
		return snap, true
	}
	for g := 0; g < 2; g++ {
		require.Eventually(t, func() bool {
			// A profile-free probe advances the checkpoint without feeding any rule.
			// This observes the armer AFTER startup's replay/reconcile has completed.
			raw, err := resolvedevent.MarshalResolvedEvent(&resolvedevent.ResolvedEvent{Source: "mqtt", SourceDeviceToken: "probe", OccurredTime: base, ProcessedTime: base, EventType: eventtype.Measurement, Payload: &resolvedevent.ResolvedMeasurementsPayload{}})
			require.NoError(t, err)
			publish(fmt.Sprintf("g%d", g), messaging.GroupSuffix(streams.ResolvedEvents, g), raw)
			s, ok := readSnapshot(g)
			return ok && len(s.Expected) == 2
		}, 30*time.Second, 100*time.Millisecond, "worker %d must arm its own never-reported devices", g)
	}
	// Live facts enter through main's singleton consumers and wake their owners.
	for g, tenant := range []string{"g0", "g1"} {
		raw, err := dmproto.MarshalDeviceAttributeEvent(&dmcontract.DeviceAttributeEvent{DeviceToken: "live", Scope: "SHARED", AttrKey: "limit", Value: 42, UpdatedAt: base.Add(time.Second)})
		require.NoError(t, err)
		publish(tenant, streams.DeviceAttribute, raw)
		raw, err = dmcontract.MarshalGeoFenceSetManifest(&dmcontract.GeoFenceSetManifest{Version: 2})
		require.NoError(t, err)
		publish(tenant, streams.GeoFenceSetManifest, raw)
		// Repeated event probes avoid assuming channel selection order across facts.
		lon, lat := "0.5", "0.5"
		seq := 0
		require.Eventually(t, func() bool {
			seq++
			at := base.Add(time.Duration(seq+1) * time.Second)
			raw, err := resolvedevent.MarshalResolvedEvent(&resolvedevent.ResolvedEvent{Source: "mqtt", SourceDeviceToken: "live", ProfileVersionToken: "p@1", OccurredTime: at, ProcessedTime: at, EventType: eventtype.Location, FenceSetVersion: 2, Payload: &resolvedevent.ResolvedLocationsPayload{Entries: []resolvedevent.ResolvedLocationEntry{{OccurredTime: at, Longitude: &lon, Latitude: &lat}}}})
			require.NoError(t, err)
			publish(tenant, messaging.GroupSuffix(streams.ResolvedEvents, g), raw)
			s, ok := readSnapshot(g)
			for _, k := range s.Raised {
				if k.Rule == tenant+"/p@1/views" && k.Series == "live" {
					return ok
				}
			}
			return false
		}, 10*time.Second, 100*time.Millisecond, "worker %d did not evaluate its routed attributes AND fences", g)
		// The final probe may still be in flight when a preceding checkpoint proves
		// the match. Settle that stream before introducing a cross-stream deletion.
		physical := messaging.GroupSuffix(streams.ResolvedEvents, g)
		require.Eventually(t, func() bool {
			ci, err := js.ConsumerInfo(messaging.StreamName(instance, physical), messaging.AckFloorDurableName(instance, "event-processing", physical))
			return err == nil && ci.NumPending == 0 && ci.NumAckPending == 0
		}, 10*time.Second, 20*time.Millisecond, "worker %d resolved probes did not settle", g)
		raw, err = entityevent.MarshalEntityDeletedEvent(&entityevent.EntityDeletedEvent{EntityType: entity.TypeDevice, EntityId: 1, EntityToken: "live", DeletedTime: base.Add(time.Hour)})
		require.NoError(t, err)
		publish(tenant, streams.EntityDeleted, raw)
		require.Eventually(t, func() bool {
			// The teardown changes engine state on the control plane, which no resolved event
			// accompanies; a profile-free probe gives the worker a checkpoint to commit it in.
			raw, err := resolvedevent.MarshalResolvedEvent(&resolvedevent.ResolvedEvent{Source: "mqtt", SourceDeviceToken: "probe", OccurredTime: base, ProcessedTime: base, EventType: eventtype.Measurement, Payload: &resolvedevent.ResolvedMeasurementsPayload{}})
			require.NoError(t, err)
			publish(tenant, physical, raw)
			s, ok := readSnapshot(g)
			if !ok || len(s.Raised) != 0 || len(s.Expected) != 1 {
				return false
			}
			return s.Expected[0].Rule == tenant+"/p@1/dead" && s.Expected[0].Series == "never"
		}, 10*time.Second, 100*time.Millisecond, "worker %d did not erase its deleted device", g)
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/devicechain-io/dc-event-processing/config"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
)

// TestStartsUnderItsDeclaredCatalog starts this service's real broker wiring with Deployed
// set, the one condition under which core/messaging holds the area to what core/streams
// and core/kv declare for it. Every other test builds a Microservice literal with
// Deployed=false, so a stream, reader mode, consumer or bucket this service uses but the
// catalog does not declare would pass CI and crash-loop the pod.
//
// Covered by the real start (createNatsComponents, the processors' Start, the REACT
// dispatcher, the tenant-purge responder, and Svc's real max-delivery recorder): the
// resolved-events, detection-rules-published, device-roster, entity-deleted,
// device-attribute and geo-fence-set-manifest readers, the derived-events reader (REACT)
// and writer, the raise-alarm, connector-dispatch and dead-letters writers, and the
// DETECT partition lease. The lazy paths are opened directly below, in the production mode.
func TestStartsUnderItsDeclaredCatalog(t *testing.T) {
	instance := fmt.Sprintf("catalogboot%d", time.Now().UnixNano())
	// The broker holds the area's login to exactly the permissions natsauth generates for it, so this
	// is also the area's permission test: the real wiring runs against it and the broker's log of
	// refusals is asserted empty at the end.
	broker := permtest.Start(t, instance)

	prevMs, prevCfg, prevNats, prevRdb, prevSvc := Microservice, Configuration, NatsManager, RdbManager, Svc
	prevDetect, prevReact, prevPurge, prevReader := ResolvedEventsProcessor, ReactDispatcher, TenantPurgeResponder, ResolvedEventsReader
	prevSnap, prevRules, prevStats, prevRoster, prevActive, prevAttr, prevReg :=
		SnapshotStore, DetectRuleStore, RuleStatStore, DeviceRosterStore, ProfileActiveStore, DeviceAttributeStore, RuleRegistry
	prevDM, prevFS, prevCFS, prevFM := DeviceManagementFacts, FenceSets, CurrentFenceSets, FenceManifests
	prevDetM, prevReM, prevEU, prevDL, prevGate := DetectMetrics, ReactMetrics, EgressUnresolved, DeadLetters, DetectTermGate
	t.Cleanup(func() {
		Microservice, Configuration, NatsManager, RdbManager, Svc = prevMs, prevCfg, prevNats, prevRdb, prevSvc
		ResolvedEventsProcessor, ReactDispatcher, TenantPurgeResponder, ResolvedEventsReader = prevDetect, prevReact, prevPurge, prevReader
		SnapshotStore, DetectRuleStore, RuleStatStore, DeviceRosterStore, ProfileActiveStore, DeviceAttributeStore, RuleRegistry =
			prevSnap, prevRules, prevStats, prevRoster, prevActive, prevAttr, prevReg
		DeviceManagementFacts, FenceSets, CurrentFenceSets, FenceManifests = prevDM, prevFS, prevCFS, prevFM
		DetectMetrics, ReactMetrics, EgressUnresolved, DeadLetters, DetectTermGate = prevDetM, prevReM, prevEU, prevDL, prevGate
	})

	Microservice = &core.Microservice{
		InstanceId:     instance,
		FunctionalArea: "event-processing",
		Deployed:       true,
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats = broker.NatsConfiguration("event-processing")
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	Configuration = &config.EventProcessingConfiguration{}
	Configuration.ApplyDefaults()

	// The stores over an in-memory sqlite carrying the production callback chain, in place
	// of the Postgres the real AfterRdb builds them over.
	db := sqlitetest.OpenMemory(t, &gorm.Config{Logger: logger.Discard})
	for _, register := range []func(*gorm.DB) error{
		rdb.RegisterTenantScoping, rdb.RegisterTokenGrammar, rdb.RegisterAuditJournal, rdb.RegisterTenantFence,
	} {
		require.NoError(t, register(db))
	}
	require.NoError(t, db.AutoMigrate(&rdb.PurgedTenant{}, &rdb.AuditEvent{}, &model.DetectSnapshot{},
		&model.DetectRule{}, &model.DeviceRoster{}, &model.ProfileActive{}, &model.DeviceAttribute{},
		&model.DeviceAttributeDeletion{}, &model.RuleStat{}))
	RdbManager = &rdb.RdbManager{Database: db}
	SnapshotStore = model.NewSnapshotStore(RdbManager)
	DetectRuleStore = model.NewDetectRuleStore(RdbManager)
	RuleStatStore = model.NewRuleStatStore(RdbManager)
	DeviceRosterStore = model.NewDeviceRosterStore(RdbManager)
	ProfileActiveStore = model.NewProfileActiveStore(RdbManager)
	DeviceAttributeStore = model.NewDeviceAttributeStore(RdbManager)
	FenceSets, CurrentFenceSets, FenceManifests = buildFenceSetSeam()
	DeviceManagementFacts = buildFactReconcileSeam()

	Svc = service.New(Microservice, service.Spec{Nats: &service.NatsSpec{OnCreate: createNatsComponents}})
	buildMetrics()

	ctx := context.Background()
	require.NoError(t, Svc.Initialize(ctx), "initialize under the catalog")
	NatsManager = Svc.Nats
	t.Cleanup(func() {
		_ = beforeMicroserviceStopped(ctx)
		if c := NatsManager.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	// main.go afterMicroserviceStarted, verbatim: Svc.Start (createNatsComponents), then the
	// processors, the purge responder and the REACT dispatcher.
	require.NoError(t, afterMicroserviceStarted(ctx),
		"start under the catalog: the DETECT and REACT readers, writers, partition lease and max-delivery recorder")

	// A term is acquired once the lease is held; that is the point where the processor goes
	// on to its replay (ResolvedEventsProcessor.replayToHead) and gap fill (live_gap.go).
	require.Eventually(t, DetectTermGate.Held, 20*time.Second, 100*time.Millisecond,
		"the DETECT partition lease was never acquired under the catalog")

	// Opened lazily in production, so opened directly here in the production mode:
	//
	// replayToHead, processor/ResolvedEventsProcessor.go:1520
	replay, _, err := NatsManager.NewReplayReader(streams.ResolvedEvents, 1)
	require.NoError(t, err, "replay reader on %s", streams.ResolvedEvents)
	require.NoError(t, replay.Close())
	// the live gap fill, processor/live_gap.go:189. A range read refuses a range past the
	// stream's last sequence, so put one message on the stream first, as its writer
	// (device-management): event-processing is rightly not a declared writer of resolved-events.
	js, err := broker.Connect(t, "device-management").JetStream()
	require.NoError(t, err)
	_, err = js.Publish(messaging.ConcreteSubjectFor(Microservice.InstanceId, "acme", streams.ResolvedEvents, "dev1"), []byte(`{}`))
	require.NoError(t, err)
	rng, err := NatsManager.NewRangeReader(streams.ResolvedEvents, 1, 1)
	require.NoError(t, err, "range reader on %s", streams.ResolvedEvents)
	require.NoError(t, rng.Close())
	// the rule preview, internal/preview/preview.go:206 (via graphql/resolvers_preview_rule.go:326)
	prev, _, err := NatsManager.NewReplayReaderFromTime(streams.ResolvedEvents, time.Now().Add(-time.Hour))
	require.NoError(t, err, "time-started replay reader on %s", streams.ResolvedEvents)
	require.NoError(t, prev.Close())
	// the detections subscription, graphql/subscriptions_detections.go:52
	live, err := NatsManager.SubscribeLive(ctx, "acme", streams.DerivedEvents)
	require.NoError(t, err, "live subscription on %s", streams.DerivedEvents)
	_ = live
	broker.RequireNoViolations(t)
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-event-management/config"
	"github.com/devicechain-io/dc-event-management/graphql"
	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-event-management/processor"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/rs/zerolog/log"
)

var (
	Microservice  *core.Microservice
	Configuration *config.EventManagementConfiguration

	// Svc owns the three managers below and the order of all four of their lifecycle
	// phases. They stay named here because the rest of this service refers to them
	// directly; Svc is what decides when each one runs.
	Svc *service.Service

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager

	Api *model.Api

	ResolvedEventsReader      messaging.MessageReader
	EventPersistenceProcessor *processor.EventPersistenceProcessor
	FailedEventsWriter        messaging.MessageWriter

	// PersistMetrics is built ONCE, in the initialize phase, and shared by every
	// EventPersistenceProcessor the NATS manager's oncreate callback builds. See
	// buildMetrics.
	PersistMetrics *core.ProcessorMetrics

	EntityDeletedReader    messaging.MessageReader
	EntityAnchorReconciler *processor.EntityAnchorReconciler

	AnchorSweep *processor.AnchorReconciliationSweep
)

func main() {
	callbacks := core.LifecycleCallbacks{
		Initializer: core.LifecycleCallback{
			Preprocess:  func(context.Context) error { return nil },
			Postprocess: afterMicroserviceInitialized,
		},
		Starter: core.LifecycleCallback{
			Preprocess:  func(context.Context) error { return nil },
			Postprocess: afterMicroserviceStarted,
		},
		Stopper: core.LifecycleCallback{
			Preprocess:  beforeMicroserviceStopped,
			Postprocess: func(context.Context) error { return nil },
		},
		Terminator: core.LifecycleCallback{
			Preprocess:  beforeMicroserviceTerminated,
			Postprocess: func(context.Context) error { return nil },
		},
	}
	Microservice = core.NewMicroservice(callbacks)
	Microservice.Run()
}

// Parses the configuration from raw bytes.
func parseConfiguration() error {
	config := &config.EventManagementConfiguration{}
	err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, config)
	if err != nil {
		return err
	}
	Configuration = config
	return nil
}

// buildMetrics creates this service's Prometheus instruments exactly once.
//
// 🔴 IT IS CALLED FROM THE INITIALIZE PHASE, NOT FROM WHERE THE PROCESSOR IS BUILT.
// The processor is built in createNatsComponents, which the NATS manager invokes on
// EVERY start, and a collector belongs to the PROCESS whereas everything that callback
// builds belongs to the CONNECTION — registering one twice on this microservice's
// registry panics. Initialize is where the process's own singletons are made, which is
// what makes this the safe half; messaging.NewNatsManager carries the reasoning.
func buildMetrics() {
	PersistMetrics = processor.NewPersistMetrics(Microservice)
}

// Create messaging components used by this microservice.
func createNatsComponents(nmgr *messaging.NatsManager) error {
	// Create reader for resolved events (wildcard across tenants).
	revents, err := nmgr.NewReader(streams.ResolvedEvents)
	if err != nil {
		return err
	}
	ResolvedEventsReader = revents

	// Add and initialize failed events writer.
	fevents, err := nmgr.NewWriter(streams.FailedEvents)
	if err != nil {
		return err
	}
	FailedEventsWriter = fevents

	// Add and initialize inbound events processor.
	// Its instruments were built once in afterMicroserviceInitialized and are handed
	// in, because a collector belongs to the process while everything this callback
	// builds belongs to the connection, and a second registration panics.
	EventPersistenceProcessor = processor.NewEventPersistenceProcessor(Microservice, ResolvedEventsReader,
		FailedEventsWriter, core.NewNoOpLifecycleCallbacks(), Api, PersistMetrics)
	err = EventPersistenceProcessor.Initialize(context.Background())
	if err != nil {
		return err
	}

	// Reader + reconciler for entity-deletion events (ADR-044): drops event_anchors
	// rows referencing an entity deleted in device-management. Durable, idempotent.
	dentity, err := nmgr.NewReader(streams.EntityDeleted)
	if err != nil {
		return err
	}
	EntityDeletedReader = dentity
	EntityAnchorReconciler = processor.NewEntityAnchorReconciler(Microservice, EntityDeletedReader,
		Api, core.NewNoOpLifecycleCallbacks())
	if err := EntityAnchorReconciler.Initialize(context.Background()); err != nil {
		return err
	}
	return nil
}

// wireAnchorSweep builds the reconciliation sweep (ADR-044 decision 3) when it is
// configured — a positive interval plus the shared service secret and the
// device-management endpoint it queries. It is disabled (with a log line) otherwise:
// the entity.deleted consumer remains the primary path, so a missing sweep degrades
// gracefully rather than failing startup.
func wireAnchorSweep(ctx context.Context) error {
	interval := Configuration.AnchorSweepIntervalSeconds
	if interval <= 0 {
		log.Info().Msg("Anchor reconciliation sweep disabled (interval <= 0).")
		return nil
	}
	infra := Microservice.InstanceConfiguration.Infrastructure
	if infra.ServiceAuth.Secret == "" || infra.DeviceManagement.Hostname == "" || infra.DeviceManagement.Port == 0 ||
		infra.UserManagement.Hostname == "" || infra.UserManagement.Port == 0 {
		log.Warn().Msg("Service secret, device-management, or user-management endpoint not configured — anchor reconciliation sweep disabled.")
		return nil
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "event-management", []string{string(auth.DeviceRead)})
	dmURL := fmt.Sprintf("http://%s:%d/graphql", infra.DeviceManagement.Hostname, infra.DeviceManagement.Port)
	AnchorSweep = processor.NewAnchorReconciliationSweep(Microservice, Api, client, dmURL,
		time.Duration(interval)*time.Second, core.NewNoOpLifecycleCallbacks())
	if err := AnchorSweep.Initialize(ctx); err != nil {
		return err
	}
	log.Info().Str("deviceManagement", dmURL).Int("intervalSeconds", interval).
		Msg("Anchor reconciliation sweep enabled (ADR-044 decision 3).")
	return nil
}

// Called after microservice has been initialized.
func afterMicroserviceInitialized(ctx context.Context) error {
	// Parse configuration.
	err := parseConfiguration()
	if err != nil {
		return err
	}

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the
	// validator in the background and gate the data plane on readiness rather
	// than exiting when user-management is briefly unreachable (amends ADR-008).
	//
	// Left here rather than handed to core/service: services do not agree on how the gate
	// opens — most fetch it in the background like this, user-management has its own
	// validator and marks ready outright, and the ingest services open it with no auth
	// surface at all. Three answers is not a default.
	Microservice.StartInstanceAuthGate(ctx)

	// The three managers, their construction and the order of all four of their lifecycle
	// phases now live in core/service. What stays here is what is genuinely this service's:
	// which store, which migrations, which oncreate callback, which schema, and the wiring
	// in AfterRdb that has to happen between two of the constructions.
	Svc = service.New(Microservice, service.Spec{
		Rdb: &service.RdbSpec{
			// 🔴 THE EVENT STORE, NOT THE RELATIONAL ONE. This service's tables are
			// hypertables and live in the instance's TimescaleDB cluster, which is a
			// different database from the one every other area opens. It is named here
			// rather than defaulted because core/service must not guess: pointing this at
			// Persistence.Rdb would create this schema in the wrong cluster, and would
			// also make dcctl count this area against the relational connection budget it
			// never draws on.
			Instance:   Microservice.InstanceConfiguration.Persistence.Tsdb,
			Migrations: model.Migrations,
			Config:     Configuration.TsdbConfiguration,
		},
		// Runs once the rdb manager is initialized and before the other two are built.
		AfterRdb: func(ctx context.Context, m *service.Managers) error {
			RdbManager = m.Rdb

			// Create RDB caches.
			model.InitializeCaches(RdbManager)

			// Wrap api around rdb manager. The rollup read-path kill-switch (ADR-026) is set
			// from config: bucketed reads use the measurement_rollups continuous aggregate
			// unless an operator disables it.
			Api = model.NewApi(RdbManager)
			Api.RollupReadsDisabled = Configuration.Lifecycle.DisableRollupReads

			// Reconcile the TimescaleDB data-lifecycle policies (ADR-026) now that the
			// hypertables exist — the migrations ran in the manager's initialize, which has
			// just returned. Best-effort with loud logging inside; a returned error means
			// the reconcile could not be attempted.
			//
			// 🔑 THIS RUNS IN THE INITIALIZE PHASE, WHERE IT USED TO RUN IN THE START PHASE
			// BETWEEN TWO MANAGER STARTS. Nothing was lost by moving it: RdbManager's start
			// is literally `return nil`, so there is no "started" state a database
			// operation could be waiting for — the connection and the hypertables both
			// exist the moment initialize returns. What it still must not do is happen
			// after the GraphQL server begins accepting, and running a phase earlier keeps
			// that with room to spare.
			lc := Configuration.Lifecycle
			compressAfterDays := config.DefaultCompressAfterDays
			if lc.CompressAfterDays != nil { // normally set by ApplyDefaults; guard against a nil deref
				compressAfterDays = *lc.CompressAfterDays
			}
			if err := model.ApplyDataLifecyclePolicies(ctx, RdbManager, model.DataLifecyclePolicy{
				ChunkIntervalHours: lc.ChunkIntervalHours,
				CompressAfterDays:  compressAfterDays,
				RetentionDays:      lc.RetentionDays,
				// Passed through as a pointer: nil means "no override", so location inherits
				// the uniform window. Collapsing it to an int here would lose the distinction
				// between "unset" and an explicit 0 (retention off for location only).
				LocationRetentionDays: lc.LocationRetentionDays,
			}); err != nil {
				return err
			}

			// Converge the read-only SQL/BI surface — its tenant-filtered views and the
			// privileges over them. It runs here rather than only in a migration for two
			// reasons: the reader group role is created by the database cluster
			// asynchronously, so a pod can reach the database before the role exists (a
			// migration would record that one failure as done and leave a surface nobody can
			// read); and the views themselves carry the tenant boundary, which should not be
			// something a database can lose permanently between schema changes. See
			// model/analytics.go. A returned error means the boundary could not be put back,
			// which is not a state to serve from — and returning it here means the GraphQL
			// server is never built, let alone started.
			if err := model.ReconcileAnalyticsSurface(ctx, RdbManager); err != nil {
				return err
			}

			// Build every Prometheus instrument this service exports, before the NATS manager
			// that consumes them.
			buildMetrics()

			// Build the reconciliation sweep (ADR-044 decision 3), the backstop for
			// entity-deletion events missed by the primary consumer. It needs the Api and
			// nothing else, which is what makes this the right side of the sequence for it.
			return wireAnchorSweep(ctx)
		},
		Nats: &service.NatsSpec{OnCreate: createNatsComponents},
		GraphQL: &service.GraphQLSpec{
			Schema:   graphql.SchemaContent,
			Resolver: func() interface{} { return &graphql.SchemaResolver{} },
			// Evaluated when the GraphQL manager is built, which is after the broker
			// manager — so the live subscription resolvers (SubscribeLive) get a NATS
			// manager that is already connected, before the subscription server accepts a
			// client.
			//
			// 🔴 IT READS Svc.Nats RATHER THAN THE PACKAGE VARIABLE, which is still nil at
			// this moment: NatsManager is published below, after Initialize returns, and
			// this runs inside it. Capturing the variable would hand every subscription
			// resolver a nil manager.
			Providers: func() map[gqlcore.ContextKey]interface{} {
				return map[gqlcore.ContextKey]interface{}{
					gqlcore.ContextRdbKey:  RdbManager,
					gqlcore.ContextApiKey:  Api,
					gqlcore.ContextNatsKey: Svc.Nats,
				}
			},
		},
	})
	if err := Svc.Initialize(ctx); err != nil {
		return err
	}
	// Published for the rest of the service, which names these managers directly.
	NatsManager, GraphQLManager = Svc.Nats, Svc.GraphQL
	return nil
}

// Called after microservice has been started.
func afterMicroserviceStarted(ctx context.Context) error {
	// Rdb, then NATS, then the GraphQL server. That order, and the reason the broker has to
	// be up before the HTTP server accepts traffic, now live in core/service.
	if err := Svc.Start(ctx); err != nil {
		return err
	}

	// Start event persistence processor.
	err := EventPersistenceProcessor.Start(ctx)
	if err != nil {
		return err
	}

	// Start entity-anchor reconciler.
	err = EntityAnchorReconciler.Start(ctx)
	if err != nil {
		return err
	}

	// Start the reconciliation sweep, if configured.
	if AnchorSweep != nil {
		if err := AnchorSweep.Start(ctx); err != nil {
			return err
		}
	}

	return nil
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	// Stop the reconciliation sweep, if running.
	if AnchorSweep != nil {
		if err := AnchorSweep.Stop(ctx); err != nil {
			return err
		}
	}

	// Stop entity-anchor reconciler.
	if err := EntityAnchorReconciler.Stop(ctx); err != nil {
		return err
	}

	// Stop event persistence processor.
	if err := EventPersistenceProcessor.Stop(ctx); err != nil {
		return err
	}

	// GraphQL, then NATS, then Rdb. The GraphQL plane here holds live broker state, not
	// just database reads: the events subscription reads the tenant's resolved-event stream
	// over this connection for as long as a socket is open, and GraphQLManager's stop is
	// what closes those sockets. Doing that first cancels each subscription's context while
	// its broker subscription is still live, so the feed unwinds from the top and every
	// subscriber is closed by the server it asked.
	//
	// 🔴 THE REVERSE ORDER REPORTS NOTHING AT ALL — IT DOES NOT SURFACE AS A BROKER FAULT,
	// AND THAT IS WHY IT IS WORTH ORDERING RATHER THAN LEAVING TO CHANCE. Draining the
	// connection first removes the subscription, and nats.go's removeSub closes a
	// subscription's delivery channel only for a SyncSubscription; SubscribeLive is built on
	// ChanSubscribe, so the channel is set to nil and never closed. Its forwarding goroutine
	// stays parked on its select, the channel it feeds is never closed, and the resolver's
	// `for msg := range live` simply blocks. Nor does anything else complain: the manager
	// sets its shutting-down flag before Drain, so the connection's ClosedHandler logs an
	// expected shutdown rather than the permanent-failure error. The subscriber is left with
	// neither data nor a close until the GraphQL stop finally drops its socket — a silent
	// stall for the length of the teardown, indistinguishable at the client from an idle feed.
	//
	// core/service stops them in exactly that order, for every service. Pinned next door in
	// graphql_shutdown_order_test.go, which drives this function.
	return Svc.Stop(ctx)
}

// Called before microservice has been terminated.
func beforeMicroserviceTerminated(ctx context.Context) error {
	// GraphQL, then NATS, then Rdb — the same order as the stop above.
	//
	// This service used to terminate NATS first. The change is not observable:
	// GraphQLManager.ExecuteTerminate returns nil and carries no callback, so the only
	// thing that moved is a no-op, and NATS still terminates before Rdb — which is the pair
	// that actually closes handles.
	return Svc.Terminate(ctx)
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"

	dmconfig "github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-notification-management/config"
	"github.com/devicechain-io/dc-notification-management/graphql"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/devicechain-io/dc-notification-management/processor"
	"github.com/devicechain-io/dc-notification-management/schema"
)

var (
	Microservice  *core.Microservice
	Configuration *config.NotificationManagementConfiguration

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager
	Api            *model.Api

	AlarmEventsReader     messaging.MessageReader
	Notifier              *processor.PolicyNotifier
	NotificationProcessor *processor.NotificationProcessor
	RetentionSweeper      *processor.RetentionSweeper
	EscalationScheduler   *processor.EscalationScheduler
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

// parseConfiguration parses the configuration from raw bytes.
func parseConfiguration() error {
	cfg := &config.NotificationManagementConfiguration{}
	err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, cfg)
	if err != nil {
		return err
	}
	Configuration = cfg
	return nil
}

// createNatsComponents creates the messaging components used by this microservice:
// a durable consumer of the alarm-events stream feeding the notification processor.
func createNatsComponents(nmgr *messaging.NatsManager) error {
	// Durable reader over the cross-tenant alarm-events wildcard (ADR-041). The
	// durable name is per instance + area, so replicas share one consumer and each
	// event is delivered to exactly one of them.
	//
	// DeliverNew starts the durable at the stream tail on first creation, so enabling
	// this service on a running fleet does NOT replay up to streamMaxAge (7d) of
	// retained alarm transitions and page humans about days-old alarms. Downtime-safety
	// is unaffected: once the durable exists its ack cursor persists, so a restart
	// resumes from the last ack. (The stream is LimitsPolicy, so an outage longer than
	// streamMaxAge still drops unseen events — "briefly down" is safe, a week is not.)
	// N.B created this durable with the default DeliverAll; the policy change rides a
	// fresh bring-up, per the pre-GA decisive-cutover convention.
	aevents, err := nmgr.NewReader(dmconfig.SUBJECT_ALARM_EVENTS, messaging.ReaderWithDeliverNew())
	if err != nil {
		return err
	}
	AlarmEventsReader = aevents

	// The policy-driven channel dispatcher (N.C, built in afterMicroserviceInitialized so
	// the escalation scheduler can share it) drives the consumer behind the Notifier seam.
	NotificationProcessor = processor.NewNotificationProcessor(Microservice, AlarmEventsReader,
		core.NewNoOpLifecycleCallbacks(), Notifier)
	return NotificationProcessor.Initialize(context.Background())
}

// afterMicroserviceInitialized initializes components after the microservice is up.
func afterMicroserviceInitialized(ctx context.Context) error {
	// Parse configuration.
	if err := parseConfiguration(); err != nil {
		return err
	}

	// Create and initialize rdb manager (runs the notification schema migrations).
	// The per-tenant channels, policies, and per-alarm notification state are
	// served from RDB.
	RdbManager = rdb.NewRdbManager(Microservice, core.NewNoOpLifecycleCallbacks(), schema.Migrations,
		Microservice.InstanceConfiguration.Persistence.Rdb, Configuration.RdbConfiguration)
	if err := RdbManager.Initialize(ctx); err != nil {
		return err
	}
	Api = model.NewApi(RdbManager)

	// The policy-driven channel dispatcher (N.C): evaluate each tenant's notification
	// policies and deliver matching alarms through the configured SMTP/webhook channels,
	// maintaining the per-alarm NotificationState. It replaces the first-slice LogNotifier
	// behind the Notifier seam and is shared by the consumer processor (event-driven
	// dispatch) and the escalation scheduler (timed re-notification), so both deliver
	// through one adapter registry and retry policy.
	Notifier = processor.NewPolicyNotifier(Api, Configuration.DeliveryAttempts, Configuration.DeliveryTimeout())

	// Retention sweep: prune cleared per-alarm state older than the retention window so
	// the notification state stays bounded (ADR-017 N.C). A negative interval disables
	// it (the table then grows unbounded — operator opt-out only).
	if Configuration.RetentionSweepSeconds >= 0 {
		RetentionSweeper = processor.NewRetentionSweeper(Microservice, Api,
			Configuration.StateRetention(), Configuration.RetentionSweepInterval(),
			core.NewNoOpLifecycleCallbacks())
		if err := RetentionSweeper.Initialize(ctx); err != nil {
			return err
		}
	}

	// Escalation scheduler (N.D): re-notify open alarms that stay unacknowledged and
	// uncleared past their policy's escalation window, up to a bounded number of tiers. A
	// negative interval disables escalation (alarms then page only on event transitions).
	if Configuration.EscalationSweepSeconds >= 0 {
		EscalationScheduler = processor.NewEscalationScheduler(Microservice, Api, Notifier,
			Configuration.EscalationSweepInterval(), Configuration.DefaultMaxEscalations,
			core.NewNoOpLifecycleCallbacks())
		if err := EscalationScheduler.Initialize(ctx); err != nil {
			return err
		}
	}

	// Create and initialize nats manager (which invokes createNatsComponents).
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	if err := NatsManager.Initialize(ctx); err != nil {
		return err
	}

	// Create and initialize graphql manager. The schema now serves the notification
	// configuration CRUD (channels/policies) backed by the rdb Api, plus the static
	// channel-type capability list.
	providers := map[gqlcore.ContextKey]interface{}{
		gqlcore.ContextRdbKey: RdbManager,
		gqlcore.ContextApiKey: Api,
	}
	parsed := gqlcore.MustParseSchema(graphql.SchemaContent, &graphql.SchemaResolver{})

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the
	// validator in the background and gate the data plane on readiness rather than
	// exiting when user-management is briefly unreachable (amends ADR-008).
	Microservice.StartInstanceAuthGate(ctx)

	GraphQLManager = gqlcore.NewGraphQLManager(Microservice, core.NewNoOpLifecycleCallbacks(),
		parsed, providers, Microservice.Readiness)
	return GraphQLManager.Initialize(ctx)
}

// afterMicroserviceStarted starts components after the microservice is started.
func afterMicroserviceStarted(ctx context.Context) error {
	if err := RdbManager.Start(ctx); err != nil {
		return err
	}
	if err := GraphQLManager.Start(ctx); err != nil {
		return err
	}
	if err := NatsManager.Start(ctx); err != nil {
		return err
	}
	if err := NotificationProcessor.Start(ctx); err != nil {
		return err
	}
	if RetentionSweeper != nil {
		if err := RetentionSweeper.Start(ctx); err != nil {
			return err
		}
	}
	if EscalationScheduler != nil {
		return EscalationScheduler.Start(ctx)
	}
	return nil
}

// beforeMicroserviceStopped stops components in reverse dependency order.
func beforeMicroserviceStopped(ctx context.Context) error {
	if EscalationScheduler != nil {
		if err := EscalationScheduler.Stop(ctx); err != nil {
			return err
		}
	}
	if RetentionSweeper != nil {
		if err := RetentionSweeper.Stop(ctx); err != nil {
			return err
		}
	}
	if err := NotificationProcessor.Stop(ctx); err != nil {
		return err
	}
	if err := NatsManager.Stop(ctx); err != nil {
		return err
	}
	if err := GraphQLManager.Stop(ctx); err != nil {
		return err
	}
	return RdbManager.Stop(ctx)
}

// beforeMicroserviceTerminated terminates components in reverse dependency order.
func beforeMicroserviceTerminated(ctx context.Context) error {
	if EscalationScheduler != nil {
		if err := EscalationScheduler.Terminate(ctx); err != nil {
			return err
		}
	}
	if RetentionSweeper != nil {
		if err := RetentionSweeper.Terminate(ctx); err != nil {
			return err
		}
	}
	if err := NotificationProcessor.Terminate(ctx); err != nil {
		return err
	}
	if err := NatsManager.Terminate(ctx); err != nil {
		return err
	}
	if err := GraphQLManager.Terminate(ctx); err != nil {
		return err
	}
	return RdbManager.Terminate(ctx)
}

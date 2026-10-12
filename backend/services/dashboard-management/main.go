// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"

	"github.com/devicechain-io/dc-dashboard-management/config"
	"github.com/devicechain-io/dc-dashboard-management/graphql"
	"github.com/devicechain-io/dc-dashboard-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	Microservice  *core.Microservice
	Configuration *config.DashboardManagementConfiguration

	// Svc owns the managers below and the order of all four of their lifecycle phases.
	Svc *service.Service

	RdbManager *rdb.RdbManager

	Api *model.Api
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
	cfg := &config.DashboardManagementConfiguration{}
	err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, cfg)
	if err != nil {
		return err
	}
	Configuration = cfg
	return nil
}

// Called after microservice has been initialized.
func afterMicroserviceInitialized(ctx context.Context) error {
	// Parse configuration.
	err := parseConfiguration()
	if err != nil {
		return err
	}

	// Dashboard-management is CRUD-only: a relational database and a GraphQL plane, no
	// NATS, no subscriptions. core/service builds both and owns the order of all four
	// lifecycle phases.
	Svc = service.New(Microservice, service.Spec{
		Rdb: &service.RdbSpec{
			Migrations: model.Migrations,
			Instance:   Microservice.InstanceConfiguration.Persistence.Rdb,
			Config:     Configuration.RdbConfiguration,
		},
		AfterRdb: afterRdb,
		GraphQL: &service.GraphQLSpec{
			Schema:   graphql.SchemaContent,
			Resolver: func() interface{} { return &graphql.SchemaResolver{} },
			// Map of providers that will be injected into graphql http context.
			Providers: func() map[gqlcore.ContextKey]interface{} {
				return map[gqlcore.ContextKey]interface{}{
					gqlcore.ContextRdbKey: RdbManager,
					gqlcore.ContextApiKey: Api,
				}
			},
		},
	})
	if err := Svc.Initialize(ctx); err != nil {
		return err
	}
	return nil
}

// afterRdb runs once the relational manager is initialized, before the GraphQL manager is
// built: it wraps the Api around the manager the providers carry.
func afterRdb(ctx context.Context, m *service.Managers) error {
	RdbManager = m.Rdb

	// Wrap api around rdb manager.
	Api = model.NewApi(RdbManager)
	// The tenant-lifecycle gate: a deleted tenant's still-valid access token must not
	// re-land dashboard rows behind the purge sweep. Nil (user-management unconfigured)
	// leaves the gate off with a warning; the per-area erasure fence is the guarantee.
	infra := Microservice.InstanceConfiguration.Infrastructure
	Api.TenantDeleted = governance.NewTenantLifecycleGate(infra.UserManagement, infra.ServiceAuth.Secret, "dashboard-management", governance.NewSafetyGates(Microservice))

	// Report the size of this schema's append-only history tables (ADR-023): a dashboard version history only
	// grows, and so does the audit journal the collector adds for every schema. Nothing
	// prunes either, so the storage floor rises with use and nothing else says how fast.
	// Registered directly rather than through Microservice.NewGauge because a failed read
	// must produce NO series rather than a zero. See rdb.StorageGrowthCollector.
	prometheus.MustRegister(rdb.NewStorageGrowthCollector(Microservice, RdbManager.Database,
		&model.DashboardVersion{}))

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the
	// validator in the background and gate the data plane on readiness rather
	// than exiting when user-management is briefly unreachable (amends ADR-008).
	Microservice.StartInstanceAuthGate(ctx)
	return nil
}

// Called after microservice has been started.
func afterMicroserviceStarted(ctx context.Context) error {
	return Svc.Start(ctx)
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	return Svc.Stop(ctx)
}

// Called before microservice has been terminated.
func beforeMicroserviceTerminated(ctx context.Context) error {
	return Svc.Terminate(ctx)
}

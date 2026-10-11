// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// update-management owns over-the-air update artifacts, their assignment to devices and
// the download plane (OTA). This build is the SCAFFOLD: the service starts, migrates its
// (empty) baseline, serves /healthz, /readyz and /metrics, and answers its GraphQL plane
// with an explicit NOT_IMPLEMENTED error. It holds no business logic yet.
//
// Deliberately absent until the first feature lands, each with the reason:
//
//   - No broker manager. Nothing is produced or consumed yet, and a service with a broker
//     manager is enrolled in the dead-letter and max-delivery streams' area lists.
//   - No tenant-lifecycle gate. The gate guards WRITE paths, and this plane has none. The
//     erasure fence is still armed: core/rdb registers it on every relational manager.
//   - No object store. The artifact catalogue that reads and writes blobs is what adds it,
//     together with the chart's blob mount for this area.
package main

import (
	"context"

	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-update-management/config"
	"github.com/devicechain-io/dc-update-management/graphql"
	"github.com/devicechain-io/dc-update-management/schema"
)

var (
	Microservice  *core.Microservice
	Configuration *config.UpdateManagementConfiguration

	// Svc owns the managers below and the order of all four of their lifecycle phases.
	Svc *service.Service

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager
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

// parseConfiguration parses this service's typed configuration from raw bytes.
func parseConfiguration() error {
	cfg := &config.UpdateManagementConfiguration{}
	if err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, cfg); err != nil {
		return err
	}
	Configuration = cfg
	return nil
}

// Called after microservice has been initialized.
func afterMicroserviceInitialized(ctx context.Context) error {
	if err := parseConfiguration(); err != nil {
		return err
	}

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the validator
	// in the background and gate the data plane on readiness.
	Microservice.StartInstanceAuthGate(ctx)

	Svc = service.New(Microservice, service.Spec{
		Rdb: &service.RdbSpec{
			Migrations: schema.Migrations,
			Instance:   Microservice.InstanceConfiguration.Persistence.Rdb,
			Config:     Configuration.RdbConfiguration,
		},
		AfterRdb: func(_ context.Context, m *service.Managers) error {
			RdbManager = m.Rdb
			return nil
		},
		GraphQL: &service.GraphQLSpec{
			Schema:   graphql.SchemaContent,
			Resolver: func() interface{} { return &graphql.SchemaResolver{} },
			Providers: func() map[gqlcore.ContextKey]interface{} {
				return map[gqlcore.ContextKey]interface{}{
					gqlcore.ContextRdbKey: RdbManager,
				}
			},
		},
	})
	if err := Svc.Initialize(ctx); err != nil {
		return err
	}
	GraphQLManager = Svc.GraphQL
	return nil
}

// Called after microservice has been started. Rdb, then the GraphQL server.
func afterMicroserviceStarted(ctx context.Context) error {
	return Svc.Start(ctx)
}

// Called before microservice has been stopped. The GraphQL server, then Rdb.
func beforeMicroserviceStopped(ctx context.Context) error {
	return Svc.Stop(ctx)
}

// Called before microservice has been terminated, in the same order as the stop.
func beforeMicroserviceTerminated(ctx context.Context) error {
	return Svc.Terminate(ctx)
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// update-management owns over-the-air update artifacts, their assignment to devices and
// the download plane (OTA). This build carries the artifact catalogue: a per-tenant,
// immutable record of each firmware, software or configuration image, a GraphQL plane to
// declare and inspect them, and a streaming upload endpoint that stores the bytes and
// verifies them against the declared size and digest. Assignment and download are not
// built yet.
//
// Deliberately absent, with the reason:
//
//   - No broker manager. Nothing is produced or consumed yet, and a service with a broker
//     manager is enrolled in the dead-letter and max-delivery streams' area lists.
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/devicechain-io/dc-update-management/config"
	"github.com/devicechain-io/dc-update-management/graphql"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/devicechain-io/dc-update-management/profilecheck"
	"github.com/devicechain-io/dc-update-management/schema"
	"github.com/devicechain-io/dc-update-management/upload"
	"github.com/rs/zerolog/log"
)

var (
	Microservice  *core.Microservice
	Configuration *config.UpdateManagementConfiguration

	// Svc owns the managers below and the order of all four of their lifecycle phases.
	Svc *service.Service

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager

	// Api is the artifact catalogue: persistence, the object store and the verification
	// the upload endpoint shares with the GraphQL plane.
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
		AfterRdb: func(ctx context.Context, m *service.Managers) error {
			RdbManager = m.Rdb
			var err error
			Api, err = buildApi(ctx)
			return err
		},
		GraphQL: &service.GraphQLSpec{
			Schema: graphql.SchemaContent,
			Resolver: func() interface{} {
				return &graphql.SchemaResolver{Area: string(Microservice.FunctionalArea)}
			},
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
	GraphQLManager = Svc.GraphQL

	// The artifact content endpoint. It authenticates against the validator the readiness
	// gate binds late, so until the auth bootstrap succeeds every upload is refused.
	registerUploadHandler()
	return nil
}

// registerUploadHandler mounts the artifact content endpoint on the microservice's OWN
// mux (never http.DefaultServeMux, which no server here serves).
func registerUploadHandler() {
	upload.Register(Microservice.Mux(), Api, Microservice.Readiness.Validator, Configuration.Updates.MaxConcurrentUploads)
}

// buildApi assembles the artifact catalogue over the object store and the relational
// manager, wiring the optional collaborators it can reach.
//
// Two of them are deliberately asymmetric. The profile check FAILS CLOSED when it cannot
// be wired (an artifact whose compatibility list was never checked is worthless), while
// the tenant-lifecycle gate degrades to the erasure fence with a warning, exactly as every
// other service does when user-management is not configured.
func buildApi(ctx context.Context) (*model.Api, error) {
	store, err := buildBlobStore(ctx)
	if err != nil {
		return nil, err
	}
	backend := Microservice.InstanceConfiguration.Infrastructure.Blob.Backend
	api := model.NewApi(RdbManager, store, model.Limits{
		MaxArtifactBytes:           Configuration.EffectiveMaxArtifactBytes(backend),
		ConfiguredMaxArtifactBytes: Configuration.Updates.MaxArtifactBytes,
		MaxTenantArtifactBytes:     Configuration.Updates.MaxTenantArtifactBytes,
	})
	if store == nil {
		log.Warn().Msg("Object store not configured (infrastructure.blob) - artifacts cannot be declared or uploaded until it is.")
	}
	if backend == blob.BackendS3 && Configuration.Updates.MaxArtifactBytes > config.S3BufferedPutCap {
		log.Warn().Int64("configured", Configuration.Updates.MaxArtifactBytes).Int64("effective", config.S3BufferedPutCap).
			Msg("The S3 object store buffers an upload in memory, so artifacts are limited to the effective ceiling until streaming multipart upload lands.")
	}

	infra := Microservice.InstanceConfiguration.Infrastructure
	api.TenantDeleted = governance.NewTenantLifecycleGate(infra.UserManagement, infra.ServiceAuth.Secret,
		"update-management", governance.NewSafetyGates(Microservice))

	switch {
	case infra.ServiceAuth.Secret == "":
		log.Warn().Msg("Service secret not configured - createArtifact will be REFUSED: compatible profiles cannot be validated against device-management.")
	case infra.DeviceManagement.Hostname == "" || infra.DeviceManagement.Port == 0:
		log.Warn().Msg("device-management endpoint not configured (infrastructure.deviceManagement) - createArtifact will be REFUSED: compatible profiles cannot be validated.")
	default:
		client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "update-management",
			[]string{string(auth.DeviceRead)})
		url := fmt.Sprintf("http://%s:%d/graphql", infra.DeviceManagement.Hostname, infra.DeviceManagement.Port)
		api.Profiles = profilecheck.New(client, url)
		log.Info().Str("deviceManagement", url).Msg("Compatible profiles are validated against device-management.")
	}
	return api, nil
}

// buildBlobStore constructs the object store from the instance infrastructure config. The
// filesystem backend with no directory is "not configured" (nil), so a deployment that has
// not mounted a blob volume still starts; an unknown backend fails closed in blob.New.
func buildBlobStore(ctx context.Context) (blob.Store, error) {
	cfg := Microservice.InstanceConfiguration.Infrastructure.Blob
	if cfg.Backend == blob.BackendFilesystem && strings.TrimSpace(cfg.Directory) == "" {
		return nil, nil
	}
	return blob.New(ctx, blob.Config{
		Backend:      cfg.Backend,
		Directory:    cfg.Directory,
		Bucket:       cfg.Bucket,
		Region:       cfg.Region,
		Endpoint:     cfg.Endpoint,
		UsePathStyle: cfg.UsePathStyle,
	}, Microservice.InstanceId)
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

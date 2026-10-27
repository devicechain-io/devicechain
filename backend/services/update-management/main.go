// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// update-management owns over-the-air update artifacts, their assignment to devices and
// the download plane (OTA). It carries
//
//   - the artifact catalogue: a per-tenant, immutable record of each firmware, software or
//     configuration image, a GraphQL plane to declare and inspect them, and a streaming
//     upload endpoint that stores the bytes and verifies them against the declared size
//     and digest;
//   - assignments: the decision that a device installs an artifact, and the attempt that
//     tracks it through the update state machine in core/ota;
//   - the device channels: update assignments, download grants and cancellations go DOWN a
//     device desired-state subject, and progress reports and download requests come UP its
//     reports subject (device-management reads the same subject for configuration);
//   - the download endpoint a device fetches the bytes from, authorized by a short-lived
//     capability rather than by any credential of the tenant;
//   - a deadline sweep that times out attempts whose device went quiet;
//   - group rollouts: an artifact assigned to the frozen membership of a device group, at most a
//     capped number of devices at a time, with pause, resume, abort and a failure threshold. A
//     driver on every replica dispatches through the same assignment path a single device uses.
//
// What it does not do, so nobody assumes it: it authenticates who produced an artifact only
// through signatures by signers the tenant registered, and a rollout has no waves, schedules or
// maintenance windows.
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/governance"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/devicechain-io/dc-update-management/config"
	"github.com/devicechain-io/dc-update-management/download"
	"github.com/devicechain-io/dc-update-management/graphql"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/devicechain-io/dc-update-management/processor"
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
	NatsManager    *messaging.NatsManager
	GraphQLManager *gqlcore.GraphQLManager

	// Api is the artifact catalogue and the update plane: persistence, the object store, the
	// device channels and the verification the upload and download endpoints share with the
	// GraphQL plane.
	Api *model.Api

	// DeadLetters is this service identity as a dead-letter producer, built by core/service.
	DeadLetters *deadletter.Producer

	// ReportsMetrics is built once in the initialize phase; the consumer built on every NATS
	// start is handed it, because a collector must not be registered twice.
	ReportsMetrics processor.UpdateReportsMetrics
	DownloadStats  *download.Metrics

	// ReportsReader and ReportsConsumer read what devices report on device-reports (update
	// progress, download requests, sync); Sweeper times out attempts whose device went quiet.
	ReportsReader   messaging.MessageReader
	ReportsConsumer *processor.ReportsConsumer

	// RosterReader and RosterConsumer read device-management's device-roster facts, so a device's
	// disable or enable is felt at once and an enable re-offers its assignment.
	RosterReader   messaging.MessageReader
	RosterConsumer *processor.RosterConsumer
	Sweeper        *processor.DeadlineSweeper

	// RolloutStats and Rollouts are the group-rollout driver and its instruments.
	RolloutStats *processor.RolloutMetrics
	Rollouts     *processor.RolloutDriver
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
	buildMetrics()

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
			if Api, err = buildApi(ctx); err != nil {
				return err
			}
			// The envelope-encrypted secret store holds each tenant download-capability
			// key. Startup fails closed without a usable instance root key: a service that
			// cannot form its KEK would otherwise answer every download with a refusal and
			// look healthy.
			return wireSecrets(ctx, Api)
		},
		Nats: &service.NatsSpec{OnCreate: createNatsComponents},
		GraphQL: &service.GraphQLSpec{
			Schema: graphql.SchemaContent,
			Resolver: func() interface{} {
				return newSchemaResolver(Configuration, string(Microservice.FunctionalArea),
					Microservice.InstanceConfiguration.Infrastructure.Blob.Backend)
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
	NatsManager, GraphQLManager = Svc.Nats, Svc.GraphQL
	DeadLetters = Svc.DeadLetters

	// The artifact content endpoint. It authenticates against the validator the readiness
	// gate binds late, so until the auth bootstrap succeeds every upload is refused.
	registerUploadHandler()
	// The download endpoint authenticates by capability, not by any token of the tenant.
	registerDownloadHandler()
	return nil
}

// buildMetrics registers this service instruments, once. It runs in the initialize phase
// because the consumer built on every NATS start must not register a collector again.
func buildMetrics() {
	ReportsMetrics = processor.NewUpdateReportsMetrics(Microservice)
	RolloutStats = processor.NewRolloutMetrics(Microservice)
	DownloadStats = &download.Metrics{
		Requests: Microservice.NewCounterVec("update_downloads_total",
			"Artifact download requests, by outcome: served, aborted (the client went away or the object "+
				"could not be read mid-stream), or why it was refused before any byte was served "+
				"(capability_malformed, capability_invalid, capability_expired, stale, gone, "+
				"range_not_satisfiable, busy). A rising capability_invalid rate is someone guessing.",
			[]string{"result"}),
		Bytes: Microservice.NewCounter("update_download_bytes_total",
			"Artifact bytes streamed to devices."),
	}
	for _, r := range download.Results {
		DownloadStats.Requests.WithLabelValues(r).Add(0)
	}
}

// wireSecrets gives the API its secret store, over the service database. A function of its own so
// a test wires the store exactly as the service does.
func wireSecrets(ctx context.Context, api *model.Api) error {
	store, err := secrets.NewFromConfig(ctx, Microservice.InstanceConfiguration.Infrastructure.Secrets, RdbManager.Database)
	if err != nil {
		return err
	}
	api.Secrets = store
	return nil
}

// startSweeper starts the deadline sweep at the configured cadence.
func startSweeper() {
	Sweeper = processor.NewDeadlineSweeper(Api, time.Duration(Configuration.Updates.SweepIntervalSeconds)*time.Second, &ReportsMetrics)
	Sweeper.Start(context.Background())
}

// startRolloutDriver starts the group-rollout driver.
func startRolloutDriver() {
	Rollouts = processor.NewRolloutDriver(Api, processor.RolloutInterval)
	Rollouts.Start(context.Background())
}

// registerDownloadHandler mounts the artifact download endpoint on the microservice own mux.
func registerDownloadHandler() {
	download.Register(Microservice.Mux(), Api, Configuration.Updates.MaxConcurrentDownloads, DownloadStats)
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

	api.Policy = Configuration.Policy()
	api.AllowUnverified = Configuration.Updates.AllowUnverifiedArtifacts
	api.DownloadBaseURL = Configuration.Updates.DownloadBaseURL
	api.CapabilityTTL = time.Duration(Configuration.Updates.CapabilityTTLSeconds) * time.Second
	api.OnRolloutStep = RolloutStats.Observe
	if api.AllowUnverified {
		log.Warn().Msg("updates.allowUnverifiedArtifacts is set: artifacts with no active-signer signature can be assigned to devices. " +
			"Nothing authenticates who produced them; this is a development setting.")
	}
	if Configuration.Updates.DownloadBaseIsInsecure() {
		log.Warn().Str("downloadBaseUrl", api.DownloadBaseURL).
			Msg("updates.downloadBaseUrl is plain http on a non-loopback host - devices are sent download links that are not encrypted in transit, " +
				"and a device that requires TLS will refuse them. Serve the instance over TLS (a lab with no TLS can ignore this).")
	}
	if api.DownloadBaseURL == "" {
		log.Warn().Msg("updates.downloadBaseUrl is not set - no device can be given a download capability, so no assigned update can be fetched.")
	}

	infra := Microservice.InstanceConfiguration.Infrastructure
	api.TenantDeleted = governance.NewTenantLifecycleGate(infra.UserManagement, infra.ServiceAuth.Secret,
		"update-management", governance.NewSafetyGates(Microservice))

	switch {
	case infra.ServiceAuth.Secret == "":
		log.Warn().Msg("Service secret not configured - createArtifact, assignArtifact and startUpdateRollout will be REFUSED, and every device-facing update message (reports, syncs, download requests, downloads) will be REFUSED and retried: profiles, devices and groups cannot be looked up in device-management.")
	case infra.DeviceManagement.Hostname == "" || infra.DeviceManagement.Port == 0:
		log.Warn().Msg("device-management endpoint not configured (infrastructure.deviceManagement) - createArtifact, assignArtifact and startUpdateRollout will be REFUSED, and every device-facing update message (reports, syncs, download requests, downloads) will be REFUSED and retried: profiles, devices and groups cannot be looked up.")
	default:
		client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "update-management",
			[]string{string(auth.DeviceRead)})
		url := fmt.Sprintf("http://%s:%d/graphql", infra.DeviceManagement.Hostname, infra.DeviceManagement.Port)
		checker := profilecheck.New(client, url)
		api.Profiles = checker
		api.Devices = checker
		api.Groups = checker
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

// createNatsComponents wires the device channels: the writer on device-desired that carries
// assignments, grants and cancels to a device, and the durable reader and consumer of what the
// device reports. It is the NATS manager oncreate callback, so it runs on every start.
func createNatsComponents(nmgr *messaging.NatsManager) error {
	desired, err := nmgr.NewWriter(streams.DeviceDesired)
	if err != nil {
		return err
	}
	Api.Desired = processor.NewDesiredWriter(desired, ReportsMetrics.DesiredFailures)

	dead, err := newDeadLetterSink(nmgr)
	if err != nil {
		return err
	}
	reader, err := nmgr.NewReader(streams.DeviceReports)
	if err != nil {
		return err
	}
	ReportsReader = reader
	ReportsConsumer = newReportsConsumer(reader, dead)
	if err := ReportsConsumer.Initialize(context.Background()); err != nil {
		return err
	}
	rosterReader, err := nmgr.NewReader(streams.DeviceRoster, messaging.ReaderWithDeliverNew())
	if err != nil {
		return err
	}
	RosterReader = rosterReader
	RosterConsumer = processor.NewRosterConsumer(Microservice, rosterReader, core.NewNoOpLifecycleCallbacks(), Api, dead)
	return RosterConsumer.Initialize(context.Background())
}

// newDeadLetterSink builds this service sink over the platform dead-letter stream, stamped by
// DeadLetters (the producer core/service built, which the max-delivery recorder shares).
func newDeadLetterSink(nmgr *messaging.NatsManager) (*deadletter.Sink, error) {
	w, err := nmgr.NewWriter(streams.DeadLetters)
	if err != nil {
		return nil, err
	}
	return DeadLetters.NewSink(w), nil
}

// newReportsConsumer assembles the device-reports consumer from this service globals. It is a
// function of its own so a test builds it exactly as this service does.
func newReportsConsumer(reader messaging.MessageReader, dead *deadletter.Sink) *processor.ReportsConsumer {
	return processor.NewReportsConsumer(Microservice, reader, core.NewNoOpLifecycleCallbacks(), Api, dead, ReportsMetrics)
}

// Called after microservice has been started. Rdb, then NATS (which builds the consumer), then the
// GraphQL server; the consumer and the deadline sweep start once all of those are up.
func afterMicroserviceStarted(ctx context.Context) error {
	if err := Svc.Start(ctx); err != nil {
		return err
	}
	if err := ReportsConsumer.Start(ctx); err != nil {
		return err
	}
	if err := RosterConsumer.Start(ctx); err != nil {
		return err
	}
	startSweeper()
	startRolloutDriver()
	return nil
}

// Called before microservice has been stopped. The sweep and the consumer, then the managers in
// their reverse order.
func beforeMicroserviceStopped(ctx context.Context) error {
	if Rollouts != nil {
		Rollouts.Stop()
	}
	if Sweeper != nil {
		Sweeper.Stop()
	}
	if RosterConsumer != nil {
		if err := RosterConsumer.Stop(ctx); err != nil {
			return err
		}
	}
	if ReportsConsumer != nil {
		if err := ReportsConsumer.Stop(ctx); err != nil {
			return err
		}
	}
	return Svc.Stop(ctx)
}

// Called before microservice has been terminated, in the same order as the stop.
func beforeMicroserviceTerminated(ctx context.Context) error {
	if RosterConsumer != nil {
		if err := RosterConsumer.Terminate(ctx); err != nil {
			return err
		}
	}
	if ReportsConsumer != nil {
		if err := ReportsConsumer.Terminate(ctx); err != nil {
			return err
		}
	}
	return Svc.Terminate(ctx)
}

// newSchemaResolver is the data-plane root resolver for a configuration: what updateManagementInfo
// reports is read from the configuration here, in one place a test can pin.
func newSchemaResolver(cfg *config.UpdateManagementConfiguration, area, blobBackend string) *graphql.SchemaResolver {
	return &graphql.SchemaResolver{
		Area:                     area,
		AllowUnverifiedArtifacts: cfg.Updates.AllowUnverifiedArtifacts,
		DownloadsConfigured:      cfg.Updates.DownloadBaseURL != "",
		MaxArtifactBytes:         cfg.EffectiveMaxArtifactBytes(blobBackend),
	}
}

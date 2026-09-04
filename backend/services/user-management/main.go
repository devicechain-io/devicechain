// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/config"
	"github.com/devicechain-io/dc-user-management/deadletters"
	"github.com/devicechain-io/dc-user-management/graphql"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
	"github.com/devicechain-io/dc-user-management/purge"
	"github.com/devicechain-io/dc-user-management/schema"
	"github.com/devicechain-io/dc-user-management/settings"
	"github.com/devicechain-io/dc-user-management/settingsdefs"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

var (
	Microservice  *core.Microservice
	Configuration *config.UserManagementConfiguration

	RdbManager      *rdb.RdbManager
	NatsManager     *messaging.NatsManager
	GraphQLManager  *gqlcore.GraphQLManager
	IdentityManager *identity.Manager
	SettingsService *settings.Service
	BlobStore       blob.Store

	// PurgeCoordinator reclaims a deleted tenant's data and then releases its token
	// (ADR-077). nil when disabled by a negative interval.
	PurgeCoordinator *purge.Coordinator

	// DeadLetterReader drains the platform's dead-letter stream.
	DeadLetterReader messaging.MessageReader

	// DeadLetterStore is the ADR-024 record of work a consumer accepted and gave up on,
	// with the consumer that fills it and the sweep that bounds it.
	DeadLetterStore    *deadletters.Store
	DeadLetterConsumer *deadletters.Consumer
	DeadLetterSweeper  *deadletters.Sweeper

	// TsdbGuest is the purge's connection to the telemetry cluster, which
	// event-management owns and this service only visits. Built alongside the
	// coordinator, so it is nil whenever the coordinator is.
	//
	// It is deliberately NOT a lifecycle component: it has no start or stop phase, and
	// the one it used to have made shutdown fail. See rdb.Guest.Initialize.
	TsdbGuest *rdb.Guest
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
	cfg := &config.UserManagementConfiguration{}
	if err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, cfg); err != nil {
		return err
	}
	Configuration = cfg
	return nil
}

// Called after microservice has been initialized.
func afterMicroserviceInitialized(ctx context.Context) error {
	// Parse configuration.
	if err := parseConfiguration(); err != nil {
		return err
	}

	// Create and initialize rdb manager (runs migrations, registers tenant scope).
	RdbManager = rdb.NewRdbManager(Microservice, core.NewNoOpLifecycleCallbacks(), schema.Migrations,
		Microservice.InstanceConfiguration.Persistence.Rdb, Configuration.RdbConfiguration)
	if err := RdbManager.Initialize(ctx); err != nil {
		return err
	}

	// Create and initialize nats manager (the refresh-token KV store, and the ADR-024
	// dead-letter reader).
	//
	// 🔴 THE READER IS BUILT IN THE CALLBACK, WHICH IS WHERE STREAM CREATION HAPPENS. A
	// reader created later would ask a manager that has already decided what streams it
	// needs, and a deployment missing the stream would discover it at the first dead
	// letter rather than at startup — the one moment the record has to work.
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(),
		func(nmgr *messaging.NatsManager) error {
			reader, err := nmgr.NewReader(streams.DeadLetters)
			if err != nil {
				return err
			}
			DeadLetterReader = reader
			return nil
		})
	if err := NatsManager.Initialize(ctx); err != nil {
		return err
	}

	// Build the identity manager: load/create the signing key, wire the refresh
	// store, and seed the bootstrap admin (ADR-008).
	accessTTL := time.Duration(Configuration.Auth.AccessTokenTtlSeconds) * time.Second
	refreshTTL := time.Duration(Configuration.Auth.RefreshTokenTtlSeconds) * time.Second
	// These two buckets pass their name twice because KeyValueStore takes the
	// LOGICAL name (its entry in the kv inventory, which selects the disk ceiling)
	// and the CONCRETE bucket name separately. They differ for the caches and the
	// lock, which are prefixed per instance and functional area; these two are
	// already instance-global, so the two names coincide.
	refreshKV, err := NatsManager.KeyValueStore(identity.RefreshBucket, identity.RefreshBucket, refreshTTL)
	if err != nil {
		return err
	}
	// The OAuth authorization-code store is created only when OAuth is enabled
	// (ADR-047); nil otherwise, leaving the token endpoint's code path off.
	var codesKV nats.KeyValue
	if Configuration.OAuthEnabled() {
		codesKV, err = NatsManager.KeyValueStore(identity.AuthCodeBucket, identity.AuthCodeBucket, identity.AuthCodeTTL)
		if err != nil {
			return err
		}
	}
	// Distributed lock (ADR-007, NATS KV) serializing signing-key work and
	// bootstrap seeding across replicas.
	lock, err := NatsManager.NewDistributedLock(5 * time.Second)
	if err != nil {
		return err
	}
	IdentityManager = identity.NewManager(Microservice, RdbManager, lock, accessTTL, refreshTTL, Configuration.Auth.IssuerUrl, identity.BootstrapConfig{
		SuperuserEmail:    Configuration.Auth.SuperuserEmail,
		SuperuserPassword: Configuration.Auth.SuperuserPassword,
		SeedClients:       seedClientsFromConfig(Configuration.Auth.SeedClients),
	})
	if err := IdentityManager.Initialize(ctx, refreshKV, codesKV); err != nil {
		return err
	}

	// Age-based signing-key rotation (ADR-008 follow-up): rotate at startup if the
	// active key is older than the configured max age, retaining the prior key for
	// the configured window so the tokens it signed keep verifying.
	keyMaxAge := time.Duration(Configuration.Auth.SigningKeyMaxAgeDays) * 24 * time.Hour
	keyRetention := time.Duration(Configuration.Auth.SigningKeyRetentionDays) * 24 * time.Hour
	if err := IdentityManager.MaybeRotateOnAge(ctx, keyMaxAge, keyRetention); err != nil {
		return err
	}

	// Serve the platform signing keys so other services can validate tokens.
	registerKeyHandlers()

	// Serve the service-token mint endpoint (ADR-044 amendment) so a caller can
	// exchange the shared service secret for a short-lived machine token.
	registerServiceTokenHandler()

	// Serve the OAuth 2.1 Authorization-Server Metadata (RFC 8414) when an issuer
	// URL is configured (ADR-047). Absent an issuer the OAuth surface stays off,
	// fail-closed — mirroring the service-token secret gate above.
	if Configuration.OAuthEnabled() {
		registerOAuthHandlers()
	}

	// The instance-scoped settings Service (ADR-042 P2). Shared between the
	// data-plane resolver (which reads the branding.default setting as the cascade's
	// default tier, ADR-038) and its own /settings/graphql handler.
	SettingsService = settings.NewService(settings.NewStore(RdbManager), settingsdefs.Registry())

	// The object/asset store (ADR-058) — the branding-logo consumer. Constructed
	// once; nil when not configured (branding-logo upload/read then 503, Tier-0
	// logos still work). A configured-but-unbuilt backend fails startup closed.
	BlobStore, err = buildBlobStore(ctx)
	if err != nil {
		return err
	}

	// Map of providers injected into the graphql http context.
	providers := map[gqlcore.ContextKey]interface{}{
		graphql.ContextIdentityKey: IdentityManager,
		graphql.ContextSettingsKey: SettingsService,
		graphql.ContextBlobKey:     BlobStore,
	}

	// user-management validates its own API requests with the local public key
	// and depends on no other service, so the readiness gate opens immediately
	// (ADR-022 decision 3); login/refresh remain reachable without a token (an
	// absent token is allowed through, see the auth handler).
	Microservice.MarkReady(IdentityManager.Validator())

	parsed := gqlcore.MustParseSchema(graphql.SchemaContent, &graphql.SchemaResolver{})
	GraphQLManager = gqlcore.NewGraphQLManager(Microservice, core.NewNoOpLifecycleCallbacks(), parsed,
		providers, Microservice.Readiness)
	if err := GraphQLManager.Initialize(ctx); err != nil {
		return err
	}

	// Instance-scoped admin API (ADR-033), served on the shared http server at
	// /admin/graphql. It validates identity-tier tokens (not tenant access tokens)
	// and runs in the system context; its resolvers gate each operation on a
	// system authority. Registered here, before the GraphQL server starts in
	// afterMicroserviceStarted.
	registerAdminHandler()

	// Instance-scoped settings API (ADR-042 P2), served at /settings/graphql on the
	// same identity-token / system-context lane as the admin API. Its store is a
	// sealed package so the seam is pre-cut for a future extraction.
	registerSettingsHandler()

	// Tenant branding-logo object-store endpoints (ADR-058): an authorizing read
	// proxy + a branding:write upload, on the shared http server. Data-plane tier
	// (tenant access tokens), self-scoped to the caller's tenant.
	graphql.RegisterBrandingLogoHandler(http.DefaultServeMux, BlobStore, IdentityManager, IdentityManager.Validator())

	// The ADR-077 purge coordinator. Deleting a tenant marks its row `purging` and cuts
	// access; this is what then erases its data across every store and removes the row,
	// which is what releases its token for reuse. It follows the sweeper shape
	// notification-management's retention sweep and event-management's anchor sweep
	// already use — a lifecycle component owning a ticker goroutine, joined on stop. See
	// the package doc for why the erasure is organised by STORE rather than by area.
	if interval := Configuration.TenantPurgeInterval(); interval > 0 {
		// The telemetry cluster is a database this service does not own — event-management
		// does — so it is reached through a guest connection rather than a second
		// RdbManager, which would create a user-management schema inside it. It resolves
		// its configuration here and connects on first use, so a telemetry cluster that is
		// down delays a purge instead of blocking every login in the instance (rdb.Guest).
		TsdbGuest = rdb.NewGuest(Microservice, "tsdb",
			Microservice.InstanceConfiguration.Persistence.Tsdb, Configuration.TsdbConfiguration)
		if err := TsdbGuest.Initialize(ctx); err != nil {
			return err
		}

		PurgeCoordinator = purge.NewCoordinator(Microservice, iam.NewStore(RdbManager), RdbManager,
			purge.DefaultStores(RdbManager, TsdbGuest, NatsManager, Microservice.InstanceId,
				BlobStore, iam.NewStore(RdbManager)), interval, Configuration.TenantPurgeSettle(),
			Configuration.TenantPurgeTokenHold(), core.NewNoOpLifecycleCallbacks())
		if err := PurgeCoordinator.Initialize(ctx); err != nil {
			return err
		}
	} else {
		log.Info().Msg("Tenant purge coordinator disabled by configuration; deleted tenants stay purging")
	}

	// The ADR-024 store, its consumer and its retention sweep. It lives here rather than
	// in one of the four services that WRITE dead letters, because the read surface is the
	// operator plane: a per-entry failure reason at full fidelity on a tenant-readable
	// surface would hand back the status-class oracle the egress boundary closed.
	DeadLetterStore = deadletters.NewStore(RdbManager)
	DeadLetterConsumer = deadletters.NewConsumer(Microservice, DeadLetterReader, DeadLetterStore,
		core.NewNoOpLifecycleCallbacks())
	if err := DeadLetterConsumer.Initialize(ctx); err != nil {
		return err
	}
	if sweep := Configuration.DeadLetterSweepInterval(); sweep > 0 {
		DeadLetterSweeper = deadletters.NewSweeper(Microservice, DeadLetterStore,
			Configuration.DeadLetterRetention(), sweep, core.NewNoOpLifecycleCallbacks())
		if err := DeadLetterSweeper.Initialize(ctx); err != nil {
			return err
		}
	} else {
		log.Info().Msg("Dead-letter retention sweep disabled by configuration; the store grows unbounded")
	}

	return nil
}

// seedClientsFromConfig maps the typed config seed-client entries onto the
// identity-layer struct the manager provisions at startup (ADR-047), keeping the
// config package free of an identity import.
func seedClientsFromConfig(in []config.SeedOAuthClientConfig) []identity.SeedOAuthClient {
	if len(in) == 0 {
		return nil
	}
	out := make([]identity.SeedOAuthClient, 0, len(in))
	for _, c := range in {
		out = append(out, identity.SeedOAuthClient{
			ClientId:     c.ClientId,
			RedirectURIs: c.RedirectURIs,
			Scopes:       c.Scopes,
			SecretHash:   c.SecretHash,
		})
	}
	return out
}

// buildBlobStore constructs the object/asset store (ADR-058) from the instance
// infrastructure config. The filesystem backend with no directory is treated as
// "not configured": the store is nil (the branding-logo endpoints then return 503
// and Tier-0 inline/URL logos still work), rather than failing an otherwise-healthy
// service that has not mounted a blob volume. A configured backend that is unknown
// or not built in this binary fails closed here (blob.New).
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

// registerAdminHandler parses the admin schema and registers its identity-token
// handler on the default mux (ADR-033). The admin Service shares the instance
// RdbManager via its own iam store wrapper.
func registerAdminHandler() {
	adminSvc := admin.NewService(iam.NewStore(RdbManager),
		Configuration.TenantPurgeSettle(), Configuration.TenantPurgeTokenHold(), DeadLetterStore)
	adminProviders := map[gqlcore.ContextKey]interface{}{
		graphql.ContextAdminKey: adminSvc,
	}
	adminSchema := gqlcore.MustParseSchema(graphql.AdminSchemaContent, &graphql.AdminResolver{})
	http.Handle("/admin/graphql", gqlcore.NewAdminHttpHandler(adminSchema, adminProviders, Microservice.Readiness))
}

// registerSettingsHandler parses the settings schema and registers its
// identity-token handler on the default mux (ADR-042 P2). The settings Service
// wraps the instance RdbManager via its own sealed store (no iam dependency).
func registerSettingsHandler() {
	settingsProviders := map[gqlcore.ContextKey]interface{}{
		graphql.ContextSettingsKey: SettingsService,
	}
	settingsSchema := gqlcore.MustParseSchema(graphql.SettingsSchemaContent, &graphql.SettingsResolver{})
	http.Handle("/settings/graphql", gqlcore.NewAdminHttpHandler(settingsSchema, settingsProviders, Microservice.Readiness))
}

// registerKeyHandlers serves the instance signing keys on the shared http server
// so the other services can validate tokens (ADR-008). /auth/jwks is the JWK Set
// of every retained public key — consumers select the right key by the token's
// kid, which lets a signing-key rotation propagate without restarts.
func registerKeyHandlers() {
	http.HandleFunc("/auth/jwks", func(w http.ResponseWriter, r *http.Request) {
		jwks, err := IdentityManager.JWKS()
		if err != nil {
			http.Error(w, "failed to build JWKS", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwks)
	})
}

// registerOAuthHandlers serves the OAuth 2.1 Authorization-Server surface (ADR-047)
// on the shared http server. Called only when an issuer URL is configured, so the
// issuer is a validated absolute origin. Slice A1 registers discovery only (RFC
// 8414 metadata + a public JWKS mirror); the /oauth/authorize and /oauth/token
// endpoints it advertises land in the following slices.
//
// Deployment companion (tracked for the slice that enables OAuth in a cluster):
// external discovery needs two ingress adjustments this in-code slice does not
// make. (1) The advertised jwks_uri is /oauth/jwks precisely so it is NOT caught
// by the ingress rule that 404s external /api/<area>/auth/* — no ingress change
// needed for it. (2) A strict RFC 8414 client of an issuer WITH a path
// (https://host/api/user-management) fetches the metadata at the path-INSERTED
// location (https://host/.well-known/oauth-authorization-server/api/user-management),
// which the current /api/<area> ingress rule does not route; the widely-used
// path-APPENDED form (<issuer>/.well-known/...) does route through it. Supporting
// strict clients needs an added ingress rule (or a dedicated path-less issuer
// host). Inert until an operator sets IssuerUrl.
func registerOAuthHandlers() {
	http.Handle(identity.MetadataPath, identity.AuthorizationServerMetadataHandler(Configuration.Auth.IssuerUrl))

	// The token endpoint (ADR-047 slice B): authorization_code (+ PKCE) and
	// refresh_token grants. AuthenticateClient runs first — public clients (PKCE) and
	// confidential clients (client_secret_basic/post) both flow through it. The
	// authorize endpoint that issues codes lands in slice C.
	http.Handle(identity.TokenPath, identity.TokenHandler(
		IdentityManager.AuthenticateClient,
		IdentityManager.RedeemAuthorizationCode,
		IdentityManager.RefreshOAuth,
	))

	// The authorization endpoint (ADR-047 slice C): the server-rendered
	// login → tenant-select → consent flow that issues the codes the token endpoint
	// redeems.
	http.Handle(identity.AuthorizePath, identity.AuthorizeHandler(IdentityManager))

	// The userinfo endpoint (ADR-047 SSO): a login client (Grafana) that treats the
	// access token as opaque calls it with the token as a Bearer credential to read
	// the subject's identity + the operator-tier `sudo` claim. Validation goes
	// through the access-token validator (signature + type + tenant).
	http.Handle(identity.UserinfoPath, identity.UserinfoHandler(func(t string) (*auth.Claims, error) {
		return IdentityManager.Validator().Validate(t)
	}))

	// Public JWKS mirror for external OAuth token validators (see OAuthJwksPath).
	// Serves the same retained key set as /auth/jwks.
	http.HandleFunc(identity.OAuthJwksPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		jwks, err := IdentityManager.JWKS()
		if err != nil {
			http.Error(w, "failed to build JWKS", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwks)
	})
}

// registerServiceTokenHandler serves the service-token mint endpoint (ADR-044
// amendment). A caller presents the shared service secret (compared constant-time
// against this instance's configured copy) and receives a short-lived service
// token carrying its requested authorities, signed by the instance key so every
// service's JWKS validator accepts it. The secret is the bootstrap trust root, so
// an empty configured secret fails closed (minting disabled). The handler body
// lives in identity so its branches are unit-tested.
func registerServiceTokenHandler() {
	http.Handle(auth.ServiceTokenPath, identity.ServiceTokenHandler(
		func() string { return Microservice.InstanceConfiguration.Infrastructure.ServiceAuth.Secret },
		IdentityManager.IssueServiceToken,
	))
}

// Called after microservice has been started.
func afterMicroserviceStarted(ctx context.Context) error {
	if err := RdbManager.Start(ctx); err != nil {
		return err
	}
	if err := NatsManager.Start(ctx); err != nil {
		return err
	}
	if PurgeCoordinator != nil {
		if err := PurgeCoordinator.Start(ctx); err != nil {
			return err
		}
	}
	if DeadLetterConsumer != nil {
		if err := DeadLetterConsumer.Start(ctx); err != nil {
			return err
		}
	}
	if DeadLetterSweeper != nil {
		if err := DeadLetterSweeper.Start(ctx); err != nil {
			return err
		}
	}
	return GraphQLManager.Start(ctx)
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	if err := GraphQLManager.Stop(ctx); err != nil {
		return err
	}
	// Before the database it sweeps through and the broker, in the reverse of the start
	// order: its pass holds an advisory lock on a pooled connection.
	if PurgeCoordinator != nil {
		if err := PurgeCoordinator.Stop(ctx); err != nil {
			return err
		}
	}
	// Before the broker it reads from and the database it writes to, in the reverse of
	// the start order.
	if DeadLetterSweeper != nil {
		if err := DeadLetterSweeper.Stop(ctx); err != nil {
			return err
		}
	}
	if DeadLetterConsumer != nil {
		if err := DeadLetterConsumer.Stop(ctx); err != nil {
			return err
		}
	}
	if err := NatsManager.Stop(ctx); err != nil {
		return err
	}
	return RdbManager.Stop(ctx)
}

// Called before microservice has been terminated.
func beforeMicroserviceTerminated(ctx context.Context) error {
	if err := GraphQLManager.Terminate(ctx); err != nil {
		return err
	}
	if PurgeCoordinator != nil {
		if err := PurgeCoordinator.Terminate(ctx); err != nil {
			return err
		}
	}
	if DeadLetterSweeper != nil {
		if err := DeadLetterSweeper.Terminate(ctx); err != nil {
			return err
		}
	}
	if DeadLetterConsumer != nil {
		if err := DeadLetterConsumer.Terminate(ctx); err != nil {
			return err
		}
	}
	// After the coordinator, which is its only user.
	if TsdbGuest != nil {
		if err := TsdbGuest.Close(); err != nil {
			return err
		}
	}
	if err := NatsManager.Terminate(ctx); err != nil {
		return err
	}
	return RdbManager.Terminate(ctx)
}

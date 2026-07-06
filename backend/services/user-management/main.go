// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/config"
	"github.com/devicechain-io/dc-user-management/graphql"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
	"github.com/devicechain-io/dc-user-management/schema"
	"github.com/devicechain-io/dc-user-management/settings"
)

var (
	Microservice  *core.Microservice
	Configuration *config.UserManagementConfiguration

	RdbManager      *rdb.RdbManager
	NatsManager     *messaging.NatsManager
	GraphQLManager  *gqlcore.GraphQLManager
	IdentityManager *identity.Manager
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

	// Create and initialize nats manager (used for the refresh-token KV store).
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })
	if err := NatsManager.Initialize(ctx); err != nil {
		return err
	}

	// Build the identity manager: load/create the signing key, wire the refresh
	// store, and seed the bootstrap admin (ADR-008).
	accessTTL := time.Duration(Configuration.Auth.AccessTokenTtlSeconds) * time.Second
	refreshTTL := time.Duration(Configuration.Auth.RefreshTokenTtlSeconds) * time.Second
	refreshKV, err := NatsManager.KeyValueStore(identity.RefreshBucket, refreshTTL)
	if err != nil {
		return err
	}
	// Distributed lock (ADR-007, NATS KV) serializing signing-key work and
	// bootstrap seeding across replicas.
	lock, err := NatsManager.NewDistributedLock(5 * time.Second)
	if err != nil {
		return err
	}
	IdentityManager = identity.NewManager(Microservice, RdbManager, lock, accessTTL, refreshTTL, identity.BootstrapConfig{
		SuperuserEmail:    Configuration.Auth.SuperuserEmail,
		SuperuserPassword: Configuration.Auth.SuperuserPassword,
	})
	if err := IdentityManager.Initialize(ctx, refreshKV); err != nil {
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

	// Map of providers injected into the graphql http context.
	providers := map[gqlcore.ContextKey]interface{}{
		graphql.ContextIdentityKey: IdentityManager,
	}

	// user-management validates its own API requests with the local public key
	// and depends on no other service, so the readiness gate opens immediately
	// (ADR-022 decision 3); login/refresh remain reachable without a token (an
	// absent token is allowed through, see the auth handler).
	Microservice.MarkReady(IdentityManager.Validator())

	parsed := gqlcore.MustParseSchema(graphql.SchemaContent, &graphql.SchemaResolver{})
	GraphQLManager = gqlcore.NewGraphQLManager(Microservice, core.NewNoOpLifecycleCallbacks(), *parsed,
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

	return nil
}

// registerAdminHandler parses the admin schema and registers its identity-token
// handler on the default mux (ADR-033). The admin Service shares the instance
// RdbManager via its own iam store wrapper.
func registerAdminHandler() {
	adminSvc := admin.NewService(iam.NewStore(RdbManager))
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
	settingsSvc := settings.NewService(settings.NewStore(RdbManager))
	settingsProviders := map[gqlcore.ContextKey]interface{}{
		graphql.ContextSettingsKey: settingsSvc,
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

// registerServiceTokenHandler serves the service-token mint endpoint (ADR-044
// amendment). A caller presents the shared service secret (constant-time compared
// against this instance's configured copy) and receives a short-lived service
// token carrying its requested authorities, signed by the instance key so every
// service's JWKS validator accepts it. The secret is the bootstrap trust root, so
// an empty configured secret fails closed (minting disabled).
func registerServiceTokenHandler() {
	http.HandleFunc(auth.ServiceTokenPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		secret := Microservice.InstanceConfiguration.Infrastructure.ServiceAuth.Secret
		if secret == "" {
			http.Error(w, "service-token minting is not configured", http.StatusServiceUnavailable)
			return
		}
		presented := r.Header.Get(auth.ServiceSecretHeader)
		if subtle.ConstantTimeCompare([]byte(presented), []byte(secret)) != 1 {
			http.Error(w, "invalid service secret", http.StatusUnauthorized)
			return
		}
		var req auth.ServiceTokenRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Subject == "" || len(req.Authorities) == 0 {
			http.Error(w, "subject and authorities are required", http.StatusBadRequest)
			return
		}
		tok, err := IdentityManager.IssueServiceToken(req.Subject, req.Authorities)
		if err != nil {
			http.Error(w, "failed to mint service token", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: tok.Token, ExpiresAt: tok.ExpiresAt.Unix()})
	})
}

// Called after microservice has been started.
func afterMicroserviceStarted(ctx context.Context) error {
	if err := RdbManager.Start(ctx); err != nil {
		return err
	}
	if err := NatsManager.Start(ctx); err != nil {
		return err
	}
	return GraphQLManager.Start(ctx)
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	if err := GraphQLManager.Stop(ctx); err != nil {
		return err
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
	if err := NatsManager.Terminate(ctx); err != nil {
		return err
	}
	return RdbManager.Terminate(ctx)
}

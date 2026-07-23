// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command lwm2m-ingest is the DeviceChain LwM2M ingest adapter (ADR-075): a stateful
// service that terminates OMA LwM2M over CoAP/UDP+DTLS from constrained devices and folds
// their registration (presence), telemetry, and commands onto the canonical
// device-management model. Slice L0 stood up the transport (a DTLS-PSK CoAP server, CID
// on by default); L0.5 extracted the shared ingest adapter; L1 (this) adds the /rd
// registration interface and the device-presence it drives — an authenticated device that
// Registers becomes CONNECTED (ASSERTED, ADR-067), an Update keeps it alive, and a
// Deregister or a lapsed lifetime makes it DISCONNECTED.
//
// Tenancy is bound to the AUTHENTICATED DTLS PSK identity, never the device's own
// registration payload (ADR-075 D1): the handler recovers the identity from the
// connection and resolves it to a (tenant, external id) binding from config. Telemetry
// (Observe/Notify) and downlink arrive in L2+.
//
// HA posture (ADR-075 C1, ADR-070): LwM2M devices connect IN to one bound UDP socket, so
// a warm standby would silently receive and drop half the datagrams a Service sprays
// across replicas. GA runs a SINGLE serving replica; a fenced ownership lease + failover
// reconstruction of the registration table (rebuild from device-state, lifetime-timer
// sweep) is L3. L1 is single-replica: a device's presence missed across a pod restart is
// the known gap L3 closes. L1 does read the epoch floor from device-state at startup, so a
// restart across a wall-clock step-back cannot mint stale-rejected presence epochs.
package main

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-lwm2m-ingest/config"
	"github.com/devicechain-io/dc-lwm2m-ingest/decode"
	"github.com/devicechain-io/dc-lwm2m-ingest/observe"
	"github.com/devicechain-io/dc-lwm2m-ingest/registry"
	"github.com/devicechain-io/dc-lwm2m-ingest/server"
	"github.com/devicechain-io/dc-microservice/auth"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

const httpPort = 8080

// reconcileQueryTimeout bounds EACH per-tenant device-state read that floors the epoch
// source at startup. A slow/absent device-state must not stall startup — on timeout that
// tenant's floor is skipped (best-effort; the wall clock still monotonically advances
// across a normal restart, so only a step-back is at risk, and that retries on the next
// restart).
const reconcileQueryTimeout = 5 * time.Second

var (
	Microservice  *core.Microservice
	Configuration *config.Lwm2mConfiguration
	Server        *server.Server
	NatsManager   *messaging.NatsManager
	Registry      *registry.Registry
	httpServer    *http.Server
	// stopping is set the instant a graceful shutdown begins, so the serve goroutine can
	// tell an intentional Stop (Serve returns, ignore) from an unexpected transport death
	// (Serve returns while still meant to be serving — fatal, since GA is a single
	// serving replica and a downed socket is a total outage).
	stopping atomic.Bool
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
			Preprocess:  func(context.Context) error { return nil },
			Postprocess: afterMicroserviceTerminated,
		},
	}
	Microservice = core.NewMicroservice(callbacks)
	Microservice.Run()
}

// parseConfiguration loads the typed config from the mounted document.
func parseConfiguration() error {
	cfg := &config.Lwm2mConfiguration{}
	if err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, cfg); err != nil {
		return err
	}
	Configuration = cfg
	return nil
}

// afterMicroserviceInitialized parses config, resolves the PSK credential map, builds the
// registration/presence layer (device resolution + durable emit) when any credential is
// provisioned, floors the epoch source from device-state, and builds the CoAP/DTLS
// transport with the /rd handlers mounted. It starts no bearer-token auth gate: this
// service authenticates devices at the DTLS layer, not with platform JWTs.
func afterMicroserviceInitialized(ctx context.Context) error {
	if err := parseConfiguration(); err != nil {
		return err
	}

	// Credentials are resolved here (not in the server) so a missing/mis-projected PSK
	// Secret crashes startup rather than degrading into a listener that cannot
	// authenticate the identity it was meant to. An empty identity set is a valid inert
	// posture (bind, authenticate no one).
	creds, err := Configuration.ResolveCredentials()
	if err != nil {
		return err
	}

	// The registration/presence layer is REQUIRED whenever the adapter has any credential
	// to serve — a provisioned device whose presence path is unreachable would look
	// registered but produce no presence. Build it only when there are credentials so an
	// inert (no-identity) deployment does not demand the durable-emit + device-management
	// coordinates it will never use.
	var mounts []server.RouteMount
	if len(Configuration.Security.Identities) > 0 {
		NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(),
			func(*messaging.NatsManager) error { return nil })
		if err := NatsManager.Initialize(ctx); err != nil {
			return err
		}
		// Build the writer synchronously here (right after Initialize sets the JetStream
		// context), not in the oncreate callback which does not run until Start — the same
		// ordering the Sparkplug adapter relies on.
		writer, err := NatsManager.NewWriter(streams.InboundEvents)
		if err != nil {
			return err
		}
		handlers, reg, epoch, reconciler, err := buildRegistry(writer, Configuration.Bindings())
		if err != nil {
			return err
		}
		Registry = reg
		// R2 (ADR-075): floor the epoch from the device-state projection BEFORE the server
		// serves, so no registration processed after startup mints an epoch a wall-clock
		// step-back across the restart would make the projection reject as stale.
		establishEpochFloor(ctx, epoch, reconciler, Configuration.Bindings())
		mounts = append(mounts, handlers.Mount)
	}

	metrics := server.Metrics{
		Handshakes: Microservice.NewCounter("handshakes_total",
			"Completed DTLS handshakes (each is a new authenticated LwM2M session).", nil),
		HandshakeFailures: Microservice.NewCounter("handshake_failures_total",
			"DTLS handshakes refused because the client presented an unprovisioned PSK identity (the fail-closed auth floor).", nil),
		ActiveSessions: Microservice.NewGauge("active_sessions",
			"Live DTLS sessions currently held.", nil),
		SessionsRejected: Microservice.NewCounter("sessions_rejected_total",
			"New sessions refused because the live-session ceiling (maxSessions) was reached.", nil),
		Requests: Microservice.NewCounterVec("coap_requests_total",
			"Transport-level CoAP requests handled by the L0 health probe, by response code. The /rd registration outcomes are metered by the registrations/updates/deregistrations/expiries counters.", []string{"code"}),
	}

	srv, err := server.New(server.Config{
		Addr:             fmt.Sprintf("%s:%d", Configuration.Listen.Host, Configuration.Listen.Port),
		Credentials:      creds,
		CIDLength:        Configuration.ConnectionIdLengthOrDefault(),
		HandshakeTimeout: durationSeconds(Configuration.Security.HandshakeTimeoutSeconds),
		MaxSessions:      Configuration.Security.MaxSessions,
		IdleTimeout:      durationSeconds(Configuration.Security.IdleTimeoutSeconds),
	}, metrics, mounts...)
	if err != nil {
		return err
	}
	Server = srv
	log.Info().Str("addr", srv.Addr().String()).Int("cidLength", Configuration.ConnectionIdLengthOrDefault()).
		Int("credentials", len(creds)).Int("registrationHandlers", len(mounts)).
		Msg("Built LwM2M CoAP/DTLS transport.")

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	http.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if Microservice.Readiness.Ready() && !Microservice.Readiness.Draining() {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	})
	return nil
}

// buildRegistry assembles the registration/presence layer: the cross-service GraphQL
// client (device:read/write to resolve+register devices, state:read to floor the epoch),
// the shared adapter's Registrar + Emitter (LwM2M token/dedup prefixes "lw-"/"lw"), a
// shared EpochSource, the Registry, and its CoAP handlers.
func buildRegistry(writer messaging.MessageWriter, bindings map[string]config.PskBinding) (*registry.Handlers, *registry.Registry, *adapter.EpochSource, *adapter.Reconciler, error) {
	if writer == nil {
		return nil, nil, nil, nil, fmt.Errorf("inbound-events writer is nil — the durable emit path was not wired before the registry was built")
	}
	infra := Microservice.InstanceConfiguration.Infrastructure
	graphqlURL, err := ingestEndpoint(infra)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	deviceStateURL, err := deviceStateEndpoint(infra)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "lwm2m-ingest",
		[]string{string(auth.DeviceRead), string(auth.DeviceWrite), string(auth.StateRead)})

	metrics := registry.Metrics{
		Registrations: Microservice.NewCounter("registrations_total",
			"LwM2M /rd registrations accepted (a CONNECTED presence event was emitted).", nil),
		Updates: Microservice.NewCounter("registration_updates_total",
			"LwM2M registration lifetime refreshes (POST /rd/{id}).", nil),
		Deregistrations: Microservice.NewCounter("deregistrations_total",
			"LwM2M explicit deregistrations (DELETE /rd/{id}).", nil),
		Expiries: Microservice.NewCounter("registration_expiries_total",
			"LwM2M registrations that lapsed with no update (DISCONNECTED by lifetime timer).", nil),
		ActiveRegistrations: Microservice.NewGauge("active_registrations",
			"Live LwM2M registrations currently held.", nil),
		DevicesRegistered: Microservice.NewCounter("devices_registered_total",
			"Devices auto-created on their first LwM2M registration.", nil),
		PresenceEmitted: Microservice.NewCounter("presence_emitted_total",
			"Presence StateChange events (ADR-067) durably written on an LwM2M register/deregister/expiry.", nil),
		Dropped: Microservice.NewCounter("presence_dropped_total",
			"Presence transitions dropped: an unregistered device on a no-auto-register credential, or a durable-emit budget exhausted.", nil),
		AuthErrors: Microservice.NewCounter("auth_errors_total",
			"/rd requests refused because no authenticated PSK identity (or its tenancy binding) could be recovered.", nil),
		BadRequests: Microservice.NewCounter("bad_requests_total",
			"Malformed /rd requests (e.g. a registration-item request with no location).", nil),
		ObservationOverflow: Microservice.NewCounter("observation_overflow_total",
			"Telemetry object instances a registration declared beyond the per-registration observe cap (dropped, not observed).", nil),
	}

	registrar := adapter.NewRegistrar(client, graphqlURL, "lw-")
	emitter := adapter.NewEmitter(writer, time.Now, "lw")
	epoch := adapter.NewEpochSource(time.Now)
	reconciler := adapter.NewReconciler(client, deviceStateURL)

	// L2b telemetry: the shared ingester (REUSING the same registrar + emitter the presence
	// path uses) behind the Observe/Notify manager. A Notify's samples flow through the same
	// durable emit Sparkplug uses. NOTE (ADR-023): this device-facing ingest is not yet behind
	// the per-tenant rate gate — that is L2c (the shared limiter in the adapter package).
	ingester := adapter.NewIngester(registrar, emitter, adapter.IngestMetrics{
		MeasurementsEmitted: Microservice.NewCounter("measurements_emitted_total",
			"Individual measurement samples durably written from LwM2M Observe/Notify.", nil),
		DevicesRegistered: Microservice.NewCounter("telemetry_devices_registered_total",
			"Devices auto-created on a first telemetry sample (rare: LwM2M devices are created at /rd registration).", nil),
		UnknownDropped: Microservice.NewCounter("telemetry_unknown_dropped_total",
			"Telemetry samples dropped for an unregistered device (auto-registration off for the credential).", nil),
	})
	obsMetrics := observe.Metrics{
		NotifiesReceived: Microservice.NewCounter("notifies_received_total",
			"LwM2M Notify messages received on an observed object instance.", nil),
		DecodeFailures: Microservice.NewCounter("notify_decode_failures_total",
			"LwM2M Notify payloads that could not be decoded (malformed or unreadable).", nil),
		UnknownContentFormat: Microservice.NewCounter("notify_unknown_content_format_total",
			"LwM2M Notify payloads in a content format this adapter does not decode (e.g. TLV; SenML-JSON only).", nil),
		ObserveEstablishRefused: Microservice.NewCounter("observe_establish_refused_total",
			"Observe requests refused or failed (dominant cause: a conformant LwM2M 1.0-only client answering the SenML Observe with 4.06).", nil),
		TerminalNotifications: Microservice.NewCounter("observe_terminal_notifications_total",
			"Notifications that terminated an observation (RFC 7641, e.g. 4.04 after the observed instance was deleted).", nil),
		IngestDropped: Microservice.NewCounter("notify_ingest_dropped_total",
			"LwM2M Notify samples dropped on a retryable ingest error (no retry in the notify path; the next Notify supersedes).", nil),
		ActiveObservations: Microservice.NewGauge("active_observations",
			"Live LwM2M observations currently held across all sessions.", nil),
	}
	obsManager := observe.NewManager(ingester, obsMetrics, observe.Options{})

	reg := registry.New(registrar, emitter, epoch, metrics, registry.Options{
		Source: registry.SourceLwM2M,
		// The registry ends a session (Deregister/expiry) → the observe manager cancels that
		// session's observations. This is the ONE hook that keeps the registry a pure presence
		// machine while the manager owns all conn/observation state (L2b).
		OnSessionEnd: obsManager.Cancel,
	})
	handlers := registry.NewHandlers(reg, bindings, metrics, obsManager, decode.DefaultObjectAllowlist)
	log.Info().Str("deviceManagement", graphqlURL).Str("deviceState", deviceStateURL).
		Msg("LwM2M ingest will resolve + emit presence via device-management and floor its epoch via device-state (ADR-044/067).")
	return handlers, reg, epoch, reconciler, nil
}

// establishEpochFloor reads every bound tenant's asserted-active LwM2M devices from the
// device-state projection and raises the shared epoch source's floor above the maximum
// session found (ADR-075 R2 / ADR-067). One EpochSource serves all tenants on the socket,
// so the floor is the global max across tenants. Best-effort: a read failure for a tenant
// is logged and skipped (the floor is retried on the next restart); it never blocks
// startup.
func establishEpochFloor(ctx context.Context, epoch *adapter.EpochSource, reconciler *adapter.Reconciler, bindings map[string]config.PskBinding) {
	tenants := map[string]struct{}{}
	for _, b := range bindings {
		tenants[b.Tenant] = struct{}{}
	}
	var globalMax uint64
	for tenant := range tenants {
		// A PER-TENANT timeout: a slow device-state must not let the first tenant burn a
		// shared budget and leave every later tenant's floor unraised (the at-risk case is
		// precisely a slow restart, which correlates with the step-back the floor guards).
		qctx, cancel := context.WithTimeout(ctx, reconcileQueryTimeout)
		_, maxSession, err := reconciler.AssertedActive(qctx, tenant, registry.SourceLwM2M)
		cancel()
		if err != nil {
			log.Warn().Err(err).Str("tenant", tenant).
				Msg("Could not read device-state to floor the LwM2M epoch for this tenant; skipping (retried on the next restart).")
			continue
		}
		if maxSession > globalMax {
			globalMax = maxSession
		}
	}
	if globalMax > 0 {
		epoch.SetFloor(globalMax + 1)
		log.Info().Uint64("floor", globalMax+1).Msg("Floored the LwM2M epoch source from the device-state projection.")
	}
}

// ingestEndpoint validates the device-management coordinate the registrar resolves
// through and returns its GraphQL URL. Fail closed: a device-facing source that cannot
// resolve devices would refuse every registration.
func ingestEndpoint(infra mscfg.InfrastructureConfiguration) (string, error) {
	if infra.ServiceAuth.Secret == "" {
		return "", fmt.Errorf("service secret not configured (infrastructure.serviceAuth) — lwm2m-ingest cannot resolve devices and would refuse all registrations")
	}
	if infra.DeviceManagement.Hostname == "" || infra.DeviceManagement.Port == 0 {
		return "", fmt.Errorf("device-management endpoint not configured (infrastructure.deviceManagement) — lwm2m-ingest cannot resolve devices and would refuse all registrations")
	}
	return fmt.Sprintf("http://%s:%d/graphql", infra.DeviceManagement.Hostname, infra.DeviceManagement.Port), nil
}

// deviceStateEndpoint validates the device-state coordinate the epoch floor-read needs and
// returns its GraphQL URL. Fail closed: without it a restart across a wall-clock step-back
// could mint stale-rejected presence epochs.
func deviceStateEndpoint(infra mscfg.InfrastructureConfiguration) (string, error) {
	if infra.DeviceState.Hostname == "" || infra.DeviceState.Port == 0 {
		return "", fmt.Errorf("device-state endpoint not configured (infrastructure.deviceState) — lwm2m-ingest cannot floor its presence epoch and a restart across a clock step-back could mint stale-rejected epochs")
	}
	return fmt.Sprintf("http://%s:%d/graphql", infra.DeviceState.Hostname, infra.DeviceState.Port), nil
}

// afterMicroserviceStarted starts NATS (so the presence emitter can publish), begins
// serving the CoAP/DTLS transport, opens readiness, and starts the HTTP server. Readiness
// is not gated on any device connecting — a device-less deployment is healthy.
func afterMicroserviceStarted(ctx context.Context) error {
	if NatsManager != nil {
		if err := NatsManager.Start(ctx); err != nil {
			return err
		}
	}
	go func() {
		err := Server.Serve()
		if stopping.Load() {
			return // Serve returned because Stop was called — the intended shutdown path.
		}
		// The transport exited while it was still meant to be serving. This pod is the
		// single serving replica (ADR-075 HA posture), so a downed socket is a total
		// ingest outage that readiness would otherwise keep hidden. Exit so Kubernetes
		// restarts the pod rather than leaving a Ready zombie.
		log.Fatal().Err(err).Msg("LwM2M CoAP/DTLS transport exited unexpectedly; terminating so the pod is restarted.")
	}()
	Microservice.MarkReady(nil)

	httpServer = &http.Server{Addr: fmt.Sprintf(":%d", httpPort)}
	go func() {
		log.Info().Int("port", httpPort).Msg("Starting LwM2M ingest HTTP server.")
		if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Error().Err(err).Msg("LwM2M ingest HTTP server exited with error.")
		}
	}()
	return nil
}

// beforeMicroserviceStopped drains readiness, stops the transport and the registration
// timers, and shuts the HTTP server down.
func beforeMicroserviceStopped(ctx context.Context) error {
	Microservice.Readiness.BeginDrain()
	stopping.Store(true) // mark intent BEFORE Stop so the serve goroutine treats its return as graceful
	if Server != nil {
		Server.Stop()
	}
	if Registry != nil {
		Registry.Stop()
	}
	if httpServer == nil {
		return nil
	}
	return httpServer.Shutdown(ctx)
}

// afterMicroserviceTerminated releases the NATS manager's resources.
func afterMicroserviceTerminated(ctx context.Context) error {
	if NatsManager != nil {
		if err := NatsManager.Terminate(ctx); err != nil {
			log.Error().Err(err).Msg("Error terminating the NATS manager.")
		}
	}
	return nil
}

// durationSeconds converts a whole-second config value to a Duration (0 stays 0).
func durationSeconds(seconds int) time.Duration {
	return time.Duration(seconds) * time.Second
}

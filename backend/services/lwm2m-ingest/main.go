// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command lwm2m-ingest is the DeviceChain LwM2M ingest adapter (ADR-075): a stateful
// service that terminates OMA LwM2M over CoAP/UDP+DTLS from constrained devices and folds
// their registration (presence), telemetry, and commands onto the canonical
// device-management model. Slice L0 stood up the transport (a DTLS-PSK CoAP server, CID
// on by default); L0.5 extracted the shared ingest adapter; L1 added the /rd registration
// interface and the device-presence it drives; L2 added the Observe/Notify telemetry
// lifecycle and the per-tenant ingest limiter; L3a (this) elects a single leader via a
// fenced ownership lease (ADR-070) so only one replica serves.
//
// Tenancy is bound to the AUTHENTICATED DTLS PSK identity, never the device's own
// registration payload (ADR-075 D1): the handler recovers the identity from the
// connection and resolves it to a (tenant, external id) binding from config.
//
// HA posture (ADR-075 C1, ADR-070): LwM2M devices connect IN to one bound UDP socket, so a
// warm standby that also bound the socket would silently receive and drop half the datagrams
// a Service sprays across replicas. GA runs a SINGLE serving replica; a fenced ownership
// lease gates SERVING — only the leader binds the socket and mounts the /rd handlers; a
// standby binds nothing and retries acquisition. At GA (replicas:1 + Recreate) the lease is
// rollout-fencing plus the N-shard hook. Because the leader binds only while it holds the
// lease, the transport Server is rebuilt per leadership term (its Stop is one-shot and it
// binds at construction). Failover RECONSTRUCTION of the registration table (rebuild from
// device-state, lifetime-timer sweep) is L3b: L3a leaves a device's presence missed across a
// leadership handover as the known gap L3b closes. The epoch floor is read from device-state
// on every acquisition, so a restart or re-election across a wall-clock step-back cannot mint
// stale-rejected presence epochs.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
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
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

const httpPort = 8080

const (
	// leasePartition names this service's single ownership lease within the shared instance
	// lease bucket (ADR-070). The LwM2M adapter is one Class-3 owner per instance (GA is one
	// shard; the key is instance-unique so N-shard later is config, not a rewrite).
	leasePartition = "lwm2m"
	// leaseRenewInterval renews well inside the TTL (<= TTL/3) on the KeepAlive goroutine,
	// decoupled from any processing loop (ADR-070 M4).
	leaseRenewInterval = messaging.DefaultLeaseTTL / 3
	// standbyRetryInterval is how often a warm standby re-attempts acquisition.
	standbyRetryInterval = 5 * time.Second
)

// reconcileQueryTimeout bounds EACH per-tenant device-state read that floors the epoch source
// AND rebuilds the shadow registration table on a leadership acquisition (ADR-075 L3b). A
// slow/absent device-state must not stall the acquisition — on timeout that tenant's read is
// skipped and RETRIED IN-TERM (retryReconstruct) with this same interval between rounds, so a
// device that would otherwise sit stuck CONNECTED is reconstructed once device-state recovers,
// without waiting for the next acquisition. It also paces the in-term retry loop.
const reconcileQueryTimeout = 5 * time.Second

// reconstructReadRetryInterval paces the in-term retry loop for tenants whose device-state read
// failed on the first reconstruction pass (ADR-075 L3b / F4). A var (not a const) only so a test
// can shorten it; production runs at reconcileQueryTimeout.
var reconstructReadRetryInterval = reconcileQueryTimeout

var (
	Microservice  *core.Microservice
	Configuration *config.Lwm2mConfiguration
	NatsManager   *messaging.NatsManager

	// Credentials is the resolved PSK identity->key map, built once at Init (a missing/mis-
	// projected Secret must crash startup, not degrade a term build).
	Credentials map[string][]byte
	// Writer is the durable inbound-events publish handle, built once after NatsManager
	// Initialize and reused by every leadership term's presence layer.
	Writer messaging.MessageWriter
	// ingestURL / deviceStateURL are validated once at Init (fail closed on a misconfiguration)
	// and reused by each term's presence layer.
	ingestURL      string
	deviceStateURL string

	// Metrics are created ONCE at Init and shared across every leadership term: a Prometheus
	// instrument may be registered only once, so a per-term rebuild that re-created them would
	// panic on the second acquisition. Their counts are cumulative across terms, which is the
	// right operational semantics (handshakes_total etc. do not reset on a re-election).
	serverMetrics   server.Metrics
	registryMetrics registry.Metrics
	ingestMetrics   adapter.IngestMetrics
	obsMetrics      observe.Metrics
	limiterMetrics  adapter.IngestLimiterMetrics
	leaderGauge     prometheus.Gauge

	Lease      *messaging.DistributedLease
	httpServer *http.Server

	// leadershipCancel stops the leadership loop on shutdown; leadershipDone closes when it has
	// fully unwound (this term's Server stopped, registry timers stopped, lease released).
	leadershipCancel context.CancelFunc
	leadershipDone   chan struct{}

	// inertServer is the always-on health-only transport for an identity-less deployment: with
	// no credentials there is no presence state to own, so it takes no lease and serves
	// unconditionally. inertStop records serve intent so a graceful shutdown is not fatal.
	inertServer *server.Server
	inertStop   func()
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

// afterMicroserviceInitialized parses config, resolves the PSK credential map, and builds the
// long-lived collaborators shared across leadership terms (all Prometheus metrics, and — when
// any credential is provisioned — the NATS manager, the durable writer, and the validated
// device-management/device-state endpoints). It builds NO transport and mounts NO handlers: the
// Server is rebuilt per leadership term (it binds at construction and its Stop is one-shot), so
// only the leader ever holds the socket. An identity-less deployment instead builds a single
// always-on health-only transport that takes no lease. It starts no bearer-token auth gate: this
// service authenticates devices at the DTLS layer, not with platform JWTs.
func afterMicroserviceInitialized(ctx context.Context) error {
	if err := parseConfiguration(); err != nil {
		return err
	}

	// Credentials are resolved here (not in the server) so a missing/mis-projected PSK Secret
	// crashes startup rather than degrading into a listener that cannot authenticate the identity
	// it was meant to. An empty identity set is a valid inert posture (bind, authenticate no one).
	creds, err := Configuration.ResolveCredentials()
	if err != nil {
		return err
	}
	Credentials = creds

	// All Prometheus instruments are created exactly once (register-once): they are shared by
	// every leadership term's transport + presence layer.
	buildMetrics()

	// The presence layer is REQUIRED whenever the adapter has any credential to serve — a
	// provisioned device whose presence path is unreachable would look registered but produce no
	// presence. Stand up its long-lived collaborators only when there are credentials so an inert
	// (no-identity) deployment does not demand the durable-emit + device-management coordinates it
	// will never use.
	if len(Configuration.Security.Identities) > 0 {
		infra := Microservice.InstanceConfiguration.Infrastructure
		// Validate the device-facing endpoints ONCE at startup (fail closed): a term that cannot
		// resolve devices or floor its epoch must not be a silent non-serving leader.
		if ingestURL, err = ingestEndpoint(infra); err != nil {
			return err
		}
		if deviceStateURL, err = deviceStateEndpoint(infra); err != nil {
			return err
		}
		NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(),
			func(*messaging.NatsManager) error { return nil })
		if err := NatsManager.Initialize(ctx); err != nil {
			return err
		}
		// Build the writer synchronously here (right after Initialize sets the JetStream context),
		// not in the oncreate callback which does not run until Start — the same ordering the
		// Sparkplug adapter relies on. It is reused by every leadership term.
		writer, err := NatsManager.NewWriter(streams.InboundEvents)
		if err != nil {
			return err
		}
		Writer = writer
		log.Info().Str("deviceManagement", ingestURL).Str("deviceState", deviceStateURL).
			Msg("LwM2M ingest will resolve + emit presence via device-management and floor its epoch via device-state (ADR-044/067).")
	} else {
		// Inert deployment: a single always-on health-only transport, no lease (nothing to own).
		srv, err := server.New(serverConfig(), serverMetrics)
		if err != nil {
			return err
		}
		inertServer = srv
		log.Info().Str("addr", srv.Addr().String()).
			Msg("Built an inert (no-credential) LwM2M CoAP/DTLS transport; it serves the health probe only and takes no leadership lease.")
	}

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

// buildMetrics creates every Prometheus instrument exactly once. All are shared across
// leadership terms (a per-term rebuild would panic re-registering them).
func buildMetrics() {
	serverMetrics = server.Metrics{
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

	leaderGauge = Microservice.NewGauge("is_leader",
		"1 when this replica holds the LwM2M leadership lease and is serving the transport, else 0 (warm standby).", nil)

	registryMetrics = registry.Metrics{
		Registrations: Microservice.NewCounter("registrations_total",
			"LwM2M /rd registrations accepted (a CONNECTED presence event was emitted).", nil),
		Updates: Microservice.NewCounter("registration_updates_total",
			"LwM2M registration lifetime refreshes (POST /rd/{id}).", nil),
		Deregistrations: Microservice.NewCounter("deregistrations_total",
			"LwM2M explicit deregistrations (DELETE /rd/{id}).", nil),
		Expiries: Microservice.NewCounter("registration_expiries_total",
			"LwM2M registrations that lapsed with no update (DISCONNECTED by lifetime timer).", nil),
		ActiveRegistrations: Microservice.NewGauge("active_registrations",
			"LwM2M registrations currently held. INCLUDES failover-reconstruction shadows immediately after a leadership takeover (ADR-075 L3b) — cross-check shadows_reconstructed_total and the per-tenant reconstruction log to tell a healthy takeover from a live fleet.", nil),
		DevicesRegistered: Microservice.NewCounter("devices_registered_total",
			"Devices auto-created on their first LwM2M registration.", nil),
		ShadowsReconstructed: Microservice.NewCounter("shadows_reconstructed_total",
			"Shadow registrations rebuilt from device-state on a leadership acquisition (ADR-075 L3b failover reconstruction) — a takeover marker; each is superseded by a re-Register or DISCONNECTED when its lifetime lapses.", nil),
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

	ingestMetrics = adapter.IngestMetrics{
		MeasurementsEmitted: Microservice.NewCounter("measurements_emitted_total",
			"Individual measurement samples durably written from LwM2M Observe/Notify.", nil),
		DevicesRegistered: Microservice.NewCounter("telemetry_devices_registered_total",
			"Devices auto-created on a first telemetry sample (rare: LwM2M devices are created at /rd registration).", nil),
		UnknownDropped: Microservice.NewCounter("telemetry_unknown_dropped_total",
			"Telemetry samples dropped for an unregistered device (auto-registration off for the credential).", nil),
	}

	obsMetrics = observe.Metrics{
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
		SamplesTruncated: Microservice.NewCounter("notify_samples_truncated_total",
			"Samples dropped from a single Notify past the per-message cap (decode.MaxSamplesPerNotify).", nil),
		IngestDropped: Microservice.NewCounter("notify_ingest_dropped_total",
			"LwM2M Notify samples dropped on a retryable ingest error (no retry in the notify path; the next Notify supersedes).", nil),
		ActiveObservations: Microservice.NewGauge("active_observations",
			"Live LwM2M observations currently held across all sessions.", nil),
	}

	limiterMetrics = adapter.IngestLimiterMetrics{
		MessagesShed: Microservice.NewCounter("ingest_messages_shed_total",
			"Device messages shed at the per-tenant ingest message-rate ceiling (Register/Update/Notify).", nil),
		SamplesShed: Microservice.NewCounter("ingest_samples_shed_total",
			"Decoded measurement samples shed at the per-tenant ingest sample-rate ceiling.", nil),
	}
}

// serverConfig builds the transport configuration from the parsed config. It is called per
// leadership term (and once for an inert deployment).
func serverConfig() server.Config {
	return server.Config{
		Addr:             fmt.Sprintf("%s:%d", Configuration.Listen.Host, Configuration.Listen.Port),
		Credentials:      Credentials,
		CIDLength:        Configuration.ConnectionIdLengthOrDefault(),
		HandshakeTimeout: durationSeconds(Configuration.Security.HandshakeTimeoutSeconds),
		MaxSessions:      Configuration.Security.MaxSessions,
		IdleTimeout:      durationSeconds(Configuration.Security.IdleTimeoutSeconds),
	}
}

// buildPresenceLayer assembles a leadership term's registration/presence layer over the
// long-lived collaborators (the durable writer, the once-built metrics): a fresh cross-service
// GraphQL client (device:read/write to resolve+register devices, state:read to floor the epoch,
// tenant:read for governance overrides), the shared adapter's Registrar + Emitter (LwM2M
// token/dedup prefixes "lw-"/"lw"), a fresh EpochSource, the per-tenant ingest limiter, the
// Observe/Notify manager, the Registry, and its CoAP handlers. It is rebuilt per term so each
// leadership tenure starts with an empty registration table; leaderCtx gates the /rd handlers
// (5.03) the instant the term is evicted.
func buildPresenceLayer(leaderCtx context.Context, bindings map[string]config.PskBinding) (*registry.Handlers, *registry.Registry, *adapter.EpochSource, *adapter.Reconciler, error) {
	if Writer == nil {
		return nil, nil, nil, nil, fmt.Errorf("inbound-events writer is nil — the durable emit path was not wired before the presence layer was built")
	}
	infra := Microservice.InstanceConfiguration.Infrastructure
	// TenantRead is added for the ADR-023 governance fetch (per-tenant ingest overrides, L2c):
	// the same client resolves devices (device:read/write), floors the epoch (state:read), and
	// reads tenantGovernance (tenant:read). Without the scope the override fetch fails and every
	// tenant meters at the platform default — fail-open-to-default, but overrides go dead.
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "lwm2m-ingest",
		[]string{string(auth.DeviceRead), string(auth.DeviceWrite), string(auth.StateRead), string(auth.TenantRead)})

	registrar := adapter.NewRegistrar(client, ingestURL, "lw-")
	emitter := adapter.NewEmitter(Writer, time.Now, "lw")
	epoch := adapter.NewEpochSource(time.Now)
	reconciler := adapter.NewReconciler(client, deviceStateURL)

	// L2b telemetry: the shared ingester (REUSING the same registrar + emitter the presence path
	// uses) behind the Observe/Notify manager. A Notify's samples flow through the same durable
	// emit Sparkplug uses, gated by the per-tenant ingest limiter (ADR-023 / ADR-075 L2c).
	ingester := adapter.NewIngester(registrar, emitter, ingestMetrics)

	// The shared per-tenant ingest limiter (ADR-023 / ADR-075 L2c): STAGE 1 gates every device
	// message (Register/Update/Notify) at the tenant's message-rate ceiling before decode; STAGE 2
	// gates the decoded sample volume. Fail-safe (fail-open to the positive platform default) and
	// label-free. Wired into both the /rd handlers and the Notify manager so one limiter meters the
	// whole device-facing surface.
	limiter := buildIngestLimiter(client, infra, Configuration.IngestRateLimit)
	obsManager := observe.NewManager(ingester, limiter, obsMetrics, observe.Options{})

	reg := registry.New(registrar, emitter, epoch, registryMetrics, registry.Options{
		Source: registry.SourceLwM2M,
		// The registration lifetime ceiling (ADR-075 L3b / F2): live registrations are clamped
		// down to it, and failover-reconstruction shadow timers are armed at it. Equal by
		// construction, so a shadow-expiry DISCONNECT is only ever late-true, never false.
		MaxLife: durationSeconds(Configuration.MaxLifetimeSeconds),
		// The registry ends a session (Deregister/expiry) → the observe manager cancels that
		// session's observations. This is the ONE hook that keeps the registry a pure presence
		// machine while the manager owns all conn/observation state (L2b).
		OnSessionEnd: obsManager.Cancel,
	})
	handlers := registry.NewHandlers(reg, bindings, registryMetrics, obsManager, decode.DefaultObjectAllowlist, limiter, leaderCtx)
	return handlers, reg, epoch, reconciler, nil
}

// buildIngestLimiter constructs the shared two-stage per-tenant ingest limiter (ADR-023 /
// ADR-075 L2c). When user-management is configured, per-tenant overrides are fetched over the
// service token (the same client, tenant:read-scoped) and cached, failing open to the platform
// default; otherwise every tenant meters at the platform default. Either way the ceiling is a
// real limit — ApplyDefaults guarantees the platform default is positive, and a zero bucket
// admits nothing. It mirrors event-sources' buildRateLimiter, minus the two-clock backlog split:
// the device-facing LwM2M path has no durable capture backlog to drain.
func buildIngestLimiter(client *svcclient.Client, infra mscfg.InfrastructureConfiguration,
	cfg config.IngestRateLimit) *adapter.IngestLimiter {
	def := governance.Limits{MessagesPerSecond: cfg.MessagesPerSecond, Burst: cfg.Burst}
	var resolve func(string) (float64, int)
	if infra.UserManagement.Hostname == "" || infra.UserManagement.Port == 0 {
		log.Warn().Msg("user-management endpoint not configured — per-tenant LwM2M ingest overrides disabled; metering every tenant at the platform default.")
		resolve = func(string) (float64, int) { return def.MessagesPerSecond, def.Burst }
	} else {
		umURL := fmt.Sprintf("http://%s:%d/graphql", infra.UserManagement.Hostname, infra.UserManagement.Port)
		resolve = governance.NewServiceLimitResolver(client, umURL, def, governance.Ingest).Resolve
		log.Info().Str("userManagement", umURL).
			Msg("Per-tenant LwM2M ingest overrides enabled (ADR-023, fail-open to platform default).")
	}
	// The sample budget is the message ceiling scaled by DefaultSamplesPerMessage, with its burst
	// floored at decode.MaxSamplesPerNotify — the SAME symbol the decoder caps a single Notify at,
	// so a compliant batch always fits the bucket and is shed only on sustained rate.
	return adapter.NewIngestLimiter(resolve, adapter.DefaultSamplesPerMessage, decode.MaxSamplesPerNotify, limiterMetrics)
}

// assertedActiveReader is the device-state read the reconstruction pass needs (*adapter.Reconciler
// implements it); the reconstruction functions depend on the interface so they are testable with a
// fake, without a live device-state.
type assertedActiveReader interface {
	AssertedActive(ctx context.Context, tenant, source string) ([]adapter.AssertedDevice, uint64, error)
}

// identityBinding pairs a PSK identity with its resolved tenancy binding — the reverse of the
// config's identity→binding map, keyed for the reconstruction lookup by (tenant, externalId).
type identityBinding struct {
	identity string
	binding  config.PskBinding
}

// reconstructPresence rebuilds LwM2M presence on a leadership acquisition (ADR-075 L3b). For each
// bound tenant it reads the asserted-active devices from device-state, floors the epoch source
// above the maximum stored session (ADR-067, so a fresh emission always supersedes a stored one),
// and reconstructs presence: a device with a live binding gets a shadow registration whose lifetime
// timer will DISCONNECT it if it stays silent; a device whose binding was decommissioned is
// DISCONNECTED now (B6). One EpochSource serves the whole socket, and SetFloor is a ratchet, so
// flooring per tenant converges on the global maximum. Tenants whose first read fails are retried
// in-term (F4): a skipped read leaves a device stuck CONNECTED until the next failover, so unlike
// the floor's one-shot best-effort posture the reconstruction cannot inherit it.
func reconstructPresence(ctx context.Context, epoch *adapter.EpochSource, reconciler assertedActiveReader, reg *registry.Registry, bindings map[string]config.PskBinding) {
	rev := buildReverseBindings(bindings)
	var pending []string
	for tenant := range rev {
		if !reconstructTenant(ctx, epoch, reconciler, reg, rev[tenant], tenant) {
			pending = append(pending, tenant)
		}
	}
	if len(pending) > 0 {
		// Retry the failed reads in the background so serving can start after the first pass (F4).
		go retryReconstruct(ctx, epoch, reconciler, reg, rev, pending)
	}
}

// reconstructTenant reads one tenant's asserted-active devices, floors the epoch from the maximum
// session, and reconstructs each device's presence. It returns whether the device-state read
// succeeded (false ⇒ the caller schedules an in-term retry). The unmatched-device DISCONNECTs (B6)
// run in a single background goroutine so a slow device-management resolve never stalls serving;
// the shadow installs are pure in-memory and run inline.
func reconstructTenant(ctx context.Context, epoch *adapter.EpochSource, reconciler assertedActiveReader, reg *registry.Registry, byExternalId map[string]identityBinding, tenant string) bool {
	// A PER-TENANT timeout: a slow device-state must not let the first tenant burn a shared budget
	// and leave every later tenant unreconstructed (the at-risk case is precisely a slow
	// restart/re-election, which correlates with the step-back the floor guards).
	qctx, cancel := context.WithTimeout(ctx, reconcileQueryTimeout)
	devices, maxSession, err := reconciler.AssertedActive(qctx, tenant, registry.SourceLwM2M)
	cancel()
	if err != nil {
		log.Warn().Err(err).Str("tenant", tenant).
			Msg("Could not read device-state to reconstruct LwM2M presence for this tenant; skipping (retried in-term).")
		return false
	}
	if maxSession > 0 {
		epoch.SetFloor(maxSession + 1)
		log.Info().Uint64("floor", maxSession+1).Str("tenant", tenant).
			Msg("Floored the LwM2M epoch source from the device-state projection.")
	}
	var orphans []string
	shadows := 0
	for _, d := range devices {
		ib, ok := byExternalId[d.ExternalId]
		if !ok {
			orphans = append(orphans, d.ExternalId) // B6: no current credential — DISCONNECT below
			continue
		}
		if reg.ReconstructShadow(ib.identity, ib.binding) {
			shadows++
		}
	}
	log.Info().Str("tenant", tenant).Int("assertedDevices", len(devices)).Int("shadows", shadows).Int("orphans", len(orphans)).
		Msg("Reconstructed LwM2M presence for tenant on leadership acquisition (ADR-075 L3b).")
	if len(orphans) > 0 {
		go func() {
			for _, ext := range orphans {
				if ctx.Err() != nil {
					return
				}
				reg.ReconstructDisconnect(ctx, tenant, ext)
			}
		}()
	}
	return true
}

// retryReconstruct re-attempts the reconstruction for tenants whose first device-state read failed,
// backing off between rounds, until every one succeeds or the leadership term ends (ADR-075 L3b /
// F4). A late-landing read re-floors the epoch (SetFloor ratchets) and installs any shadows the
// first pass missed; ReconstructShadow skips an identity that has since re-Registered (B1), so a
// device that came back while a tenant's read was failing is never shadowed over a live entry.
func retryReconstruct(ctx context.Context, epoch *adapter.EpochSource, reconciler assertedActiveReader, reg *registry.Registry, rev map[string]map[string]identityBinding, pending []string) {
	for len(pending) > 0 {
		if !sleepCtx(ctx, reconstructReadRetryInterval) {
			return // term ended
		}
		var still []string
		for _, tenant := range pending {
			if !reconstructTenant(ctx, epoch, reconciler, reg, rev[tenant], tenant) {
				still = append(still, tenant)
			}
		}
		pending = still
	}
	log.Info().Msg("In-term LwM2M presence reconstruction completed for all previously-failed tenants.")
}

// buildReverseBindings inverts the identity→binding map into tenant → externalId → (identity,
// binding), the lookup the reconstruction uses to map an asserted device's external id back to the
// credential that can re-authenticate it. A duplicate (tenant, externalId) across two identities is
// the credential-rotation overlap the config deliberately allows; it is resolved DETERMINISTICALLY
// (the lowest identity wins) and counted, so a shadow is installed under exactly one identity and
// the reconstruction is stable run-to-run (B8). Whichever identity the device actually re-Registers
// on supersedes the shadow at a higher epoch regardless of which one held it.
func buildReverseBindings(bindings map[string]config.PskBinding) map[string]map[string]identityBinding {
	ids := make([]string, 0, len(bindings))
	for id := range bindings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rev := map[string]map[string]identityBinding{}
	for _, id := range ids {
		b := bindings[id]
		byExt := rev[b.Tenant]
		if byExt == nil {
			byExt = map[string]identityBinding{}
			rev[b.Tenant] = byExt
		}
		if existing, dup := byExt[b.ExternalId]; dup {
			log.Warn().Str("tenant", b.Tenant).Str("externalId", b.ExternalId).
				Str("identity", existing.identity).Str("ignored", id).
				Msg("Two PSK identities share a (tenant, externalId); reconstruction uses the lower identity (credential-rotation overlap, ADR-075 L3b / B8).")
			continue
		}
		byExt[b.ExternalId] = identityBinding{identity: id, binding: b}
	}
	return rev
}

// ingestEndpoint validates the device-management coordinate the registrar resolves through and
// returns its GraphQL URL. Fail closed: a device-facing source that cannot resolve devices would
// refuse every registration.
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
// returns its GraphQL URL. Fail closed: without it a restart across a wall-clock step-back could
// mint stale-rejected epochs.
func deviceStateEndpoint(infra mscfg.InfrastructureConfiguration) (string, error) {
	if infra.DeviceState.Hostname == "" || infra.DeviceState.Port == 0 {
		return "", fmt.Errorf("device-state endpoint not configured (infrastructure.deviceState) — lwm2m-ingest cannot floor its presence epoch and a restart across a clock step-back could mint stale-rejected epochs")
	}
	return fmt.Sprintf("http://%s:%d/graphql", infra.DeviceState.Hostname, infra.DeviceState.Port), nil
}

// afterMicroserviceStarted starts NATS (so the presence emitter can publish) and, when the
// adapter has credentials, launches the leadership loop that serves the transport only while this
// replica holds the ownership lease; an inert deployment serves its health-only transport
// unconditionally. Readiness is opened regardless — a standby is "ready" to take over and a
// device-less deployment is healthy — and is not gated on any device connecting.
func afterMicroserviceStarted(ctx context.Context) error {
	if NatsManager != nil {
		if err := NatsManager.Start(ctx); err != nil {
			return err
		}
		lease, err := NatsManager.NewDistributedLease(messaging.DefaultLeaseTTL)
		if err != nil {
			return err
		}
		Lease = lease
		lctx, cancel := context.WithCancel(context.Background())
		leadershipCancel = cancel
		leadershipDone = make(chan struct{})
		go func() {
			defer close(leadershipDone)
			runLeadership(lctx, lease)
		}()
	} else if inertServer != nil {
		inertStop = superviseServe(inertServer, func(err error) {
			log.Fatal().Err(err).Msg("LwM2M CoAP/DTLS transport exited unexpectedly; terminating so the pod is restarted.")
		})
	}
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

// runLeadership is the acquire/serve/standby loop (ADR-070). It repeatedly tries to acquire the
// single ownership lease; while another replica holds it, this replica is a warm standby that
// binds no socket and retries. On acquisition it serves as leader until the lease is lost or the
// service is shutting down, then loops to re-acquire.
func runLeadership(ctx context.Context, lease *messaging.DistributedLease) {
	for {
		if ctx.Err() != nil {
			return
		}
		held, err := lease.Acquire(leasePartition)
		if err != nil {
			if errors.Is(err, messaging.ErrLeaseHeld) {
				log.Info().Msg("Another replica holds the LwM2M lease; standing by.")
			} else {
				log.Warn().Err(err).Msg("Failed to acquire the LwM2M leadership lease; retrying as standby.")
			}
			setLeader(false)
			if !sleepCtx(ctx, standbyRetryInterval) {
				return
			}
			continue
		}
		serveAsLeader(ctx, held)
	}
}

// serveAsLeader builds this term's presence layer + transport, floors the epoch from
// device-state, and serves until the lease is definitively lost (KeepAlive returns ErrNotHolder)
// or the service is shutting down (ctx). On either it self-evicts: cancel the term context (which
// 5.03s any in-flight /rd), stop the transport and the registry timers, and release the lease so a
// standby can take over. KeepAlive runs on its OWN goroutine so no processing stall can starve
// renewal (ADR-070 M4). The Server is built fresh here (it binds at construction, its Stop is
// one-shot) and torn down on eviction, so only the leader ever holds the socket.
func serveAsLeader(ctx context.Context, lease *messaging.Lease) {
	log.Info().Uint64("epoch", lease.Epoch()).Msg("Acquired LwM2M leadership; building the transport and serving.")

	// leaderCtx is this term's lifetime: cancelled on eviction (lease lost) or shutdown (parent
	// ctx). It gates the /rd handlers (5.03 once cancelled) and bounds the epoch floor read.
	leaderCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start KeepAlive on its own goroutine IMMEDIATELY after acquisition (ADR-070 M4), BEFORE the
	// term build: buildTerm's epoch floor read can take up to reconcileQueryTimeout per bound tenant
	// against a slow device-state — exactly the correlated state during a failover — and renewal
	// must not wait that out or the first renew lands past the TTL and the pod churns evict/rebuild.
	// keepaliveDone closes when the renewer has stopped, so the release below does not race an
	// in-flight Renew (a lost CAS there would strand the lease for a TTL on an otherwise clean exit).
	keepaliveDone := make(chan struct{})
	go func() {
		defer close(keepaliveDone)
		if errors.Is(lease.KeepAlive(leaderCtx, leaseRenewInterval), messaging.ErrNotHolder) {
			log.Error().Msg("Lost the LwM2M leadership lease; self-evicting.")
			cancel()
		}
	}()

	// evict cancels the term, waits for the renewer to stop, and releases the lease so a standby can
	// acquire promptly. Idempotent-safe to call on every exit path (cancel + Release both are).
	evict := func() {
		cancel()
		<-keepaliveDone
		if err := lease.Release(); err != nil {
			log.Warn().Err(err).Msg("Error releasing the LwM2M lease (it will age out via its TTL).")
		}
	}

	srv, reg, err := buildTerm(leaderCtx, Configuration.Bindings())
	if err != nil {
		// A term build that fails (e.g. the transport cannot bind) cannot serve. Release the lease
		// and back off before returning to the acquire loop, so a TRANSIENT local fault is a slow
		// retry rather than a hot loop. A PERSISTENT one (a bad listen address) would otherwise be an
		// invisible Ready-but-not-serving standby forever, so a consecutive-failure fuse restores the
		// pre-L3a crash-loop visibility a config-class fault used to get at startup.
		consecutiveTermBuildFailures++
		log.Error().Err(err).Int("consecutiveFailures", consecutiveTermBuildFailures).
			Msg("Could not build the LwM2M leadership term; releasing the lease and retrying.")
		if consecutiveTermBuildFailures >= maxConsecutiveTermBuildFailures {
			log.Fatal().Err(err).Int("consecutiveFailures", consecutiveTermBuildFailures).
				Msg("LwM2M leadership term build failed repeatedly; terminating so the pod is restarted (a persistent local fault, e.g. a bad listen address).")
		}
		setLeader(false)
		evict()
		sleepCtx(ctx, standbyRetryInterval)
		return
	}
	consecutiveTermBuildFailures = 0 // a successful build clears the fuse
	setLeader(true)

	// Serve this term's transport. A Serve return while the term still intends to serve is a downed
	// socket on the single serving replica — a total ingest outage a Ready pod would hide — so it is
	// fatal. stopServing records intent so the eviction Stop below is graceful, not fatal.
	stopServing := superviseServe(srv, func(err error) {
		log.Fatal().Err(err).Msg("LwM2M CoAP/DTLS transport exited unexpectedly; terminating so the pod is restarted.")
	})

	<-leaderCtx.Done() // eviction (lease lost) or shutdown (parent ctx cancelled)

	setLeader(false)
	stopServing() // records intent, THEN Stops the socket, so the ensuing Serve return is graceful
	reg.Stop()    // terminal: refuse further work + stop this term's lifetime timers (no presence emitted — a handover is not a device disconnect)
	evict()
	log.Info().Msg("Released LwM2M leadership; returning to standby.")
}

// consecutiveTermBuildFailures counts back-to-back leadership-term build failures; it is only ever
// touched on the single leadership goroutine (serveAsLeader), so it needs no synchronization. A
// successful build resets it; maxConsecutiveTermBuildFailures of them in a row is a persistent local
// fault and terminates the pod (the fuse).
var consecutiveTermBuildFailures int

// maxConsecutiveTermBuildFailures is the fuse threshold: at standbyRetryInterval between attempts,
// it gives a transient fault (a socket not yet freed by the prior term) ample time to clear before a
// genuinely persistent one (a misconfigured listen address) crash-loops the pod for visibility.
const maxConsecutiveTermBuildFailures = 5

// buildTerm assembles a leadership term: the presence layer, the epoch floor read from
// device-state (on every acquisition, ADR-075 L3a/F4), and a freshly-bound transport with the /rd
// handlers mounted. The returned Server is not yet serving.
func buildTerm(leaderCtx context.Context, bindings map[string]config.PskBinding) (*server.Server, *registry.Registry, error) {
	handlers, reg, epoch, reconciler, err := buildPresenceLayer(leaderCtx, bindings)
	if err != nil {
		return nil, nil, err
	}
	// Bind the transport BEFORE reconstruction. server.New binds the socket but does NOT serve
	// (Serve starts the read loop later, in serveAsLeader), and binding is the anticipated transient
	// failure on a handover — the fuse comment names "a socket not yet freed by the prior term". If
	// reconstruction ran first it would arm one maxLife+grace (~day-long) shadow timer per asserted
	// device, and a subsequent bind failure discards this term's registry WITHOUT ever calling
	// reg.Stop(), leaking every one of those timers to fire in a dead term. Binding first means a
	// bind failure returns before any timer is armed.
	srv, err := server.New(serverConfig(), serverMetrics, handlers.Mount)
	if err != nil {
		return nil, nil, err
	}
	// Reconstruct presence after the bind succeeds but BEFORE serving (ADR-075 L3b): read each bound
	// tenant's asserted-active LwM2M devices from device-state, floor the epoch above the maximum
	// stored session (so no registration the read loop processes after Serve mints a stale-rejected
	// epoch), and rebuild a shadow registration table so a device that went silent across the
	// handover is still DISCONNECTED. The socket is bound but not yet reading, so no Register is
	// processed until Serve — the floor + shadows are in place first. Bound by leaderCtx (not
	// context.Background): the pass is best-effort — a slow device-state never fails the term — but
	// an eviction or shutdown mid-pass must abort the reads promptly so the unwind is not delayed
	// reconcileQueryTimeout × N tenants (risking a SIGKILL before the lease is released). Tenants
	// whose first read fails are retried in-term.
	reconstructPresence(leaderCtx, epoch, reconciler, reg, bindings)

	log.Info().Str("addr", srv.Addr().String()).Int("cidLength", Configuration.ConnectionIdLengthOrDefault()).
		Int("credentials", len(Credentials)).Msg("Built the LwM2M CoAP/DTLS transport for this leadership term.")
	return srv, reg, nil
}

// serveServer is the minimal transport surface superviseServe drives (*server.Server satisfies
// it); the interface lets the serve-intent supervision be unit-tested with a fake transport.
type serveServer interface {
	Serve() error
	Stop()
}

// superviseServe runs srv.Serve() on a goroutine and watches for its return. Until the returned
// stopServing is called, a Serve return is treated as an unexpected transport death and onDeath
// runs (production wires it to log.Fatal — a single serving replica whose socket dies is a total
// ingest outage; the Serve error is passed through so the fatal log names the cause). stopServing
// records serve intent, THEN Stops the transport, so the ensuing Serve return is recognised as
// graceful and onDeath does not fire. This replaces a process-global "stopping" flag with
// per-serve-term intent: a leadership eviction Stops the socket without killing the pod (the "fix
// the shape, not the instance" case — any Stop outside shutdown is safe). The store happens-before
// Stop, and Stop causes the Serve return, so the goroutine always reads intent=true on the graceful
// path (no race); the only interleaving that fatals during a stop is a genuine socket death that
// returned before intent was recorded, which is correctly fatal.
func superviseServe(srv serveServer, onDeath func(error)) (stopServing func()) {
	var leaving atomic.Bool
	done := make(chan struct{})
	go func() {
		err := srv.Serve()
		if !leaving.Load() {
			onDeath(err)
		}
		close(done)
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			leaving.Store(true) // BEFORE Stop: the serve goroutine reads this after Serve returns
			srv.Stop()
			<-done // ensure the serve goroutine has observed the intent and unwound
		})
	}
}

// setLeader records leadership on the gauge (0/1), tolerating a nil gauge.
func setLeader(leader bool) {
	if leaderGauge == nil {
		return
	}
	if leader {
		leaderGauge.Set(1)
	} else {
		leaderGauge.Set(0)
	}
}

// sleepCtx waits for d or ctx cancellation; it returns false if ctx was cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// beforeMicroserviceStopped drains readiness, unwinds leadership (self-evict: stop the transport
// and registry timers, release the lease) or the inert transport, and shuts the HTTP server down.
func beforeMicroserviceStopped(ctx context.Context) error {
	Microservice.Readiness.BeginDrain()
	// When leadership is running, cancelling it makes the loop self-evict (stop the transport +
	// registry timers, release the lease) and exit; wait for that to unwind. With no credentials
	// there is no leadership loop, so stop the inert transport directly.
	if leadershipCancel != nil {
		leadershipCancel()
		<-leadershipDone
	} else if inertStop != nil {
		inertStop()
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

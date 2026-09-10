// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command sparkplug-ingest is the DeviceChain Sparkplug B Host Application
// (ADR-069): a stateful adapter that connects OUT to one or more per-tenant
// customer MQTT brokers, announces its Sparkplug 3.0 STATE on each, subscribes to
// the configured Sparkplug groups, and decodes edge-node / device telemetry.
// Tenancy is connection-scoped (M7): every message is attributed to the tenant
// configured for the broker it arrived on, never parsed from the untrusted
// Sparkplug topic. It maps each edge-node/device identity to a DeviceChain device
// (SP3b), emits measurements and presence StateChange events (SP4a), and elects a
// single leader via a core/lease so only one replica connects as the Host
// Application (SP4b — ADR-070; two would publish duplicate STATE). On every (re)connect
// the leader reconciles presence against the device-state projection (rebirth-all +
// liveness probe) so a node that died while no Host was connected is not stranded
// CONNECTED — the authoritative-presence correctness guarantee (ADR-067 SP4b).
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/auth"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
)

const httpPort = 8080

const (
	// leasePartition names this service's single ownership lease within the shared
	// instance lease bucket (ADR-070). The Sparkplug adapter is one Class-3 owner per
	// instance (GA is one shard; the key is instance-unique so N-shard later is config,
	// not a rewrite).
	leasePartition = "sparkplug"
	// leaseRenewInterval renews well inside the TTL (<= TTL/3) on the KeepAlive
	// goroutine, decoupled from any processing loop (ADR-070 M4).
	leaseRenewInterval = messaging.DefaultLeaseTTL / 3
	// standbyRetryInterval is how often a warm standby re-attempts acquisition.
	standbyRetryInterval = 5 * time.Second
)

var (
	Microservice  *core.Microservice
	Configuration *config.SparkplugConfiguration
	Manager       *host.Manager
	NatsManager   *messaging.NatsManager
	Lease         *messaging.DistributedLease
	httpServer    *core.HttpServer

	// leaderGauge is 1 while this replica holds the lease and is connecting sources,
	// 0 while it is a warm standby (ADR-070 A6 observability).
	leaderGauge prometheus.Gauge
	// leadershipCancel stops the leadership loop on shutdown; leadershipDone closes
	// when it has fully unwound (Manager stopped, lease released).
	leadershipCancel context.CancelFunc
	leadershipDone   chan struct{}
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
	cfg := &config.SparkplugConfiguration{}
	if err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, cfg); err != nil {
		return err
	}
	Configuration = cfg
	return nil
}

// afterMicroserviceInitialized parses config, builds one tenant-bound Host
// Application client per configured source, and registers the HTTP surface
// (probes + metrics). It starts no auth gate: this service validates no bearer
// tokens — it is a transport that connects out to customer brokers — so readiness
// is opened by the Manager once it is supervising (afterMicroserviceStarted).
func afterMicroserviceInitialized(ctx context.Context) error {
	if err := parseConfiguration(); err != nil {
		return err
	}

	// Every Prometheus instrument this service keeps for its whole life, built in the
	// INITIALIZE phase. See buildMetrics for why the phase matters.
	metrics := buildMetrics()

	// Bring up the durable emit path. The writer is created SYNCHRONOUSLY here, right
	// after Initialize sets the JetStream context — NOT in the NatsManager's oncreate
	// callback, which does not run until Start. Capturing the oncreate-populated
	// global at this point would hand the emitter a nil writer and panic the receive
	// goroutine on the first message; NewWriter only needs the JS context, so it is
	// safe (and correct) to build the writer now and pass it in by value.
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })
	if err := NatsManager.Initialize(ctx); err != nil {
		return err
	}

	// The ingest pipeline (device resolution + durable emit) is REQUIRED whenever the
	// adapter has any source to ingest from — a source with an unreachable device
	// path would silently drop all its telemetry. Build it only when there are
	// sources so an inert (empty-sources) deployment does not demand service auth it
	// will never use.
	var ingester *host.Ingester
	var reconciler *host.Reconciler
	if len(Configuration.Sources) > 0 {
		writer, err := NatsManager.NewWriter(streams.InboundEvents)
		if err != nil {
			return err
		}
		built, rec, err := buildIngester(writer)
		if err != nil {
			return err
		}
		ingester = built
		reconciler = rec
	}

	clients, err := resolveSources(Configuration, Microservice.InstanceId, ingester, reconciler, metrics)
	if err != nil {
		return err
	}
	Manager = host.NewManager(clients)
	log.Info().Int("sources", len(clients)).Msg("Built Sparkplug source connections.")

	// This service's whole HTTP surface, registered in the INITIALIZE phase. See
	// registerHttpRoutes for why it is here and not where the server starts.
	registerHttpRoutes()
	return nil
}

// registerHttpRoutes mounts this service's HTTP surface — /healthz, /readyz and
// /metrics — on the microservice's OWN mux rather than on http.DefaultServeMux.
//
// 🔴 IT IS CALLED FROM THE INITIALIZE PHASE, NOT FROM WHERE THE SERVER STARTS.
// RegisterProbes goes through ServeMux.Handle, which panics on a duplicate pattern,
// and LifecycleComponent does not promise ExecuteStart runs once: a start that fails
// restores the component to Initialized, so a retried start enters the start path
// again. Registering from there turns that retry into a crash. Initialize is where the
// routes belong, which is what makes this the safe half.
//
// 🔴 It is also a named function rather than a line inside the initializer so a test
// can drive the REGISTRATION ITSELF. A test that called RegisterProbes on its own
// would be asserting against its own copy of the wiring: it would keep passing if this
// went back to http.Handle on the default mux, which is the exact regression the
// switchover has to prevent. The uncovered remainder is one line — that the initializer
// calls this — because the initializer needs config, credentials and a broker.
func registerHttpRoutes() {
	Microservice.RegisterProbes(Microservice.Readiness)
}

// buildMetrics creates every Prometheus instrument exactly once and returns the ones
// the sources share. They are intentionally NOT labeled by tenant: a per-tenant label
// on a counter driven by broker traffic is a cardinality risk (the governance lesson)
// and the aggregate is enough here — per-source attribution lives in the
// (cardinality-safe) logs.
//
// 🔴 IT IS CALLED FROM THE INITIALIZE PHASE, NOT FROM WHERE LEADERSHIP STARTS, for the
// same reason registerHttpRoutes is called from there. Microservice.NewGauge goes
// through promauto, which panics on a duplicate registration, and LifecycleComponent
// does not promise ExecuteStart runs once: a start that fails restores the component to
// Initialized, so a retried start enters the start path again. Building an instrument
// there turns that retry into a crash. Initialize is where a process-scoped instrument
// belongs, which is what makes this the safe half. lwm2m-ingest's buildMetrics says the
// same thing for the same reason.
//
// 🔴 It is also a named function rather than a block inside the initializer so a test
// can drive the CONSTRUCTION ITSELF. A test that built its own gauge would be asserting
// against its own copy of the wiring and would keep passing if this moved back onto the
// start path, which is the exact regression it has to prevent.
//
// leaderGauge is built here unconditionally, including for a source-less deployment
// that takes no lease and connects nothing: it then reads 0 for the life of the pod,
// which is what it claims — this replica is not connecting sources.
func buildMetrics() host.Metrics {
	leaderGauge = Microservice.NewGauge("is_leader",
		"1 when this replica holds the Sparkplug leadership lease and is connecting sources, else 0 (warm standby).")

	return host.Metrics{
		Messages: Microservice.NewCounterVec("messages_total",
			"Sparkplug messages received, by message type.", []string{"type"}),
		DecodeErrors: Microservice.NewCounter("decode_errors_total",
			"Sparkplug messages that failed topic parse or payload decode."),
		RebirthRequests: Microservice.NewCounter("rebirth_requests_total",
			"Sparkplug node rebirth commands emitted by the session machine."),
		RebirthEnqueued: Microservice.NewCounter("rebirth_enqueued_total",
			"Rebirth requests the session machine put on the publish queue (rebirth_requests_total counts only the ones that then reached the wire)."),
		RebirthDropped: Microservice.NewCounter("rebirth_dropped_total",
			"Rebirth requests discarded because the publish queue was full. A latency signal, not an error rate: the per-node backoff re-requests on the next window."),
		ConnectFailures: Microservice.NewCounter("connect_failures_total",
			"Failed broker connect attempts across all sources (readiness is not broker-gated; this is how a broken source config surfaces)."),
		IngestFailures: Microservice.NewCounter("ingest_failures_total",
			"Accepted messages whose samples were dropped after the in-handler ingest retry budget was exhausted (device-management or NATS unreachable)."),
	}
}

// resolveSources turns each configured source into a resolved Host Application
// client. Failing here (bad scheme, an intended-but-empty password env) fails the
// service closed at startup rather than dialing a mis-authenticated broker and
// retry-looping silently — repo convention (ADR-022).
func resolveSources(cfg *config.SparkplugConfiguration, instanceId string, ingester *host.Ingester, reconciler *host.Reconciler, metrics host.Metrics) ([]*host.Client, error) {
	// Convert a possibly-nil *Ingester into the interface as a real nil rather than a
	// typed nil: passing a nil *Ingester directly would wrap into a NON-nil interface,
	// so the Client's `ingester != nil` guard would pass and Ingest would panic. This
	// makes the "non-nil whenever there are sources" invariant structural, not a
	// comment. (In production the caller only builds an ingester when Sources is
	// non-empty, so this stays nil only for the zero-source case.)
	var si host.SampleIngester
	if ingester != nil {
		si = ingester
	}
	clients := make([]*host.Client, 0, len(cfg.Sources))
	for i := range cfg.Sources {
		src := cfg.Sources[i]
		broker, err := resolveBroker(src, instanceId, i)
		if err != nil {
			return nil, err
		}
		client := host.NewClient(src, broker, si, time.Now, metrics)
		// The reconciler is shared across sources; a nil one (no ingest path) leaves
		// reconciliation off. Set before Connect so the first onConnected can use it.
		if reconciler != nil {
			client.SetReconciler(reconciler)
		}
		clients = append(clients, client)
	}
	return clients, nil
}

// buildIngester assembles the device-resolution + durable-emit pipeline around a
// live inbound-events writer. It fails closed when the ingest path is not configured
// (or the writer is nil): a Sparkplug adapter that cannot reach device-management or
// NATS would resolve no devices / emit nothing and silently drop every measurement,
// so a misconfigured deploy or a wiring-order regression must be visible at startup,
// not at runtime (ADR-022). The service token carries device:read (lookup by external
// id), device:write (auto-registration), and state:read (failover reconciliation reads
// the device-state presence projection, ADR-067 SP4b) only — least privilege.
func buildIngester(writer messaging.MessageWriter) (*host.Ingester, *host.Reconciler, error) {
	if writer == nil {
		return nil, nil, fmt.Errorf("inbound-events writer is nil — the durable emit path was not wired before the ingester was built")
	}
	infra := Microservice.InstanceConfiguration.Infrastructure
	graphqlURL, err := ingestEndpoint(infra)
	if err != nil {
		return nil, nil, err
	}
	deviceStateURL, err := deviceStateEndpoint(infra)
	if err != nil {
		return nil, nil, err
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "sparkplug-ingest",
		[]string{string(auth.DeviceRead), string(auth.DeviceWrite), string(auth.StateRead)})

	ingestMetrics := host.IngestMetrics{
		MeasurementsEmitted: Microservice.NewCounter("measurements_emitted_total",
			"Numeric Sparkplug samples durably written to the inbound-events stream."),
		PresenceEmitted: Microservice.NewCounter("presence_emitted_total",
			"Presence StateChange events (ADR-067) durably written on a Sparkplug BIRTH/DEATH."),
		DevicesRegistered: Microservice.NewCounter("devices_registered_total",
			"Devices auto-registered on first sight of their Sparkplug identity."),
		UnknownDropped: Microservice.NewCounter("unknown_device_dropped_total",
			"Samples/presence dropped for an unregistered device on a source with auto-registration off."),
		TenantGoneDropped: Microservice.NewCounter("tenant_deleted_dropped_total",
			"Samples/presence dropped because the tenant has been deleted and its data is being reclaimed."),
	}
	// The ADR-077 gate. A Sparkplug publisher's devices do pass the broker auth-callout,
	// so this is the second gate rather than the only one — but the callout stops a
	// CONNECT, and a Sparkplug host application publishes on behalf of devices over its
	// own long-lived connection, which a purge does not interrupt. A SEPARATE service
	// token from the client above, scoped to tenant:read alone.
	tenantGate := governance.NewTenantLifecycleGate(infra.UserManagement, infra.ServiceAuth.Secret, "sparkplug-ingest")
	registrar := host.NewRegistrar(client, graphqlURL, tenantGate)
	emitter := host.NewEmitter(writer, time.Now)
	reconciler := host.NewReconciler(client, deviceStateURL)
	log.Info().Str("deviceManagement", graphqlURL).Str("deviceState", deviceStateURL).
		Msg("Sparkplug ingest will resolve + emit telemetry via device-management and reconcile presence via device-state (ADR-044/067).")
	return host.NewIngester(registrar, emitter, ingestMetrics), reconciler, nil
}

// deviceStateEndpoint validates the infrastructure the failover reconciliation needs
// and returns device-state's GraphQL URL. It fails closed: reconciliation is the
// authoritative-presence correctness guarantee (ADR-067 SP4b) — an adapter that cannot
// reach device-state on failover would leave devices stranded CONNECTED after a missed
// death — so a missing coordinate must surface at startup, not as a silent gap.
func deviceStateEndpoint(infra mscfg.InfrastructureConfiguration) (string, error) {
	if infra.DeviceState.Hostname == "" || infra.DeviceState.Port == 0 {
		return "", fmt.Errorf("device-state endpoint not configured (infrastructure.deviceState) — sparkplug-ingest cannot reconcile presence on failover and would strand devices connected after a missed death")
	}
	return fmt.Sprintf("http://%s:%d/graphql", infra.DeviceState.Hostname, infra.DeviceState.Port), nil
}

// ingestEndpoint validates the infrastructure the ingest path needs and returns
// device-management's GraphQL URL. It fails closed: a Sparkplug adapter that cannot
// reach device-management resolves no devices and silently drops every measurement,
// so a missing service secret or device-management coordinate must surface at
// startup, not as invisible data loss at runtime.
func ingestEndpoint(infra mscfg.InfrastructureConfiguration) (string, error) {
	if infra.ServiceAuth.Secret == "" {
		return "", fmt.Errorf("service secret not configured (infrastructure.serviceAuth) — sparkplug-ingest cannot resolve devices and would drop all telemetry")
	}
	if infra.DeviceManagement.Hostname == "" || infra.DeviceManagement.Port == 0 {
		return "", fmt.Errorf("device-management endpoint not configured (infrastructure.deviceManagement) — sparkplug-ingest cannot resolve devices and would drop all telemetry")
	}
	return fmt.Sprintf("http://%s:%d/graphql", infra.DeviceManagement.Hostname, infra.DeviceManagement.Port), nil
}

// resolveBroker builds one source's MQTT connection: TLS is selected by the URL
// scheme (ssl:// ⇒ TLS with the system root CAs; custom-CA / mutual-TLS to a
// private broker is a post-GA follow-up), the password is read once from the named
// environment variable (a Kubernetes Secret projected by the chart, never
// cleartext in config), and the client id is made unique per source so two sources
// on one broker cannot race for the same id.
func resolveBroker(src config.SparkplugSource, instanceId string, index int) (host.Broker, error) {
	// The URL was already parsed + scheme-validated by config.Validate; parse again
	// only to derive the transport. Keep the switch to the schemes Validate accepts
	// (tcp / ssl / tls) so the two cannot drift.
	u, err := url.Parse(src.Broker.URL)
	if err != nil {
		return host.Broker{}, fmt.Errorf("sources[%d]: broker.url %q is not a valid URL: %w", index, src.Broker.URL, err)
	}
	var tlsCfg *tls.Config
	switch u.Scheme {
	case "ssl", "tls":
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
	case "tcp":
		// plaintext transport; no TLS config
	default:
		return host.Broker{}, fmt.Errorf("sources[%d]: unsupported broker URL scheme %q (want tcp:// or ssl://)", index, u.Scheme)
	}

	var password string
	if src.Broker.PasswordEnv != "" {
		password = os.Getenv(src.Broker.PasswordEnv)
		if password == "" {
			return host.Broker{}, fmt.Errorf("sources[%d]: passwordEnv %q is set but that environment variable is empty (the broker Secret was not projected)", index, src.Broker.PasswordEnv)
		}
	}

	return host.Broker{
		URL:      src.Broker.URL,
		ClientID: fmt.Sprintf("dc-sparkplug-host-%s-%d", instanceId, index),
		Username: src.Broker.Username,
		Password: password,
		TLS:      tlsCfg,
	}, nil
}

// afterMicroserviceStarted starts the Manager (which connects every source in the
// background) and opens readiness, then starts the HTTP server. Readiness is not
// gated on any broker being reachable — a customer broker being down degrades that
// one source, never the pod.
func afterMicroserviceStarted(ctx context.Context) error {
	// Start the durable writer BEFORE the sources connect, so the JetStream publish
	// path is live before the first broker message can arrive.
	if err := NatsManager.Start(ctx); err != nil {
		return err
	}

	// The Sparkplug adapter is a Class-3 single-owner operator (ADR-070): two replicas
	// connecting as the same Host Application would publish duplicate STATE and emit
	// duplicate telemetry/presence. So a leader-election lease gates the WHOLE Client
	// lifecycle — only the holder connects; a warm standby binds nothing. Readiness is
	// opened regardless: a standby is "ready" to take over (its /readyz must pass so it
	// is not killed while waiting). With no sources there is nothing to lead.
	if len(Configuration.Sources) > 0 {
		if err := startLeadership(); err != nil {
			return err
		}
	} else {
		Manager.Start()
	}
	// NO VALIDATOR, DELIBERATELY. This service's HTTP surface is /healthz, /readyz and
	// /metrics; edge nodes authenticate at the MQTT gateway and nothing here verifies a
	// JWT, so there is no token for a validator to check. See lwm2m-ingest, which says the
	// same thing for the same reason.
	Microservice.MarkReadyWithoutAuthSurface()

	return startHttpServer(httpPort)
}

// startLeadership acquires the single-owner lease and launches the leadership loop that
// owns the Manager for as long as this replica holds it.
//
// 🔴 IT BUILDS NOTHING THAT CANNOT BE BUILT TWICE, which is the same constraint
// startHttpServer carries. It runs from the start phase, which is entered again by any
// start retried after a failed one — so a Prometheus instrument constructed here would
// panic on the duplicate registration the second time round. The instruments are built
// once, in the initialize phase, by buildMetrics.
//
// It is a named function rather than a block inside afterMicroserviceStarted so a test
// can call the start path twice; the uncovered remainder is the one line that calls it.
func startLeadership() error {
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
	return nil
}

// startHttpServer builds this service's HTTP server over the microservice's own mux
// and starts it, returning any bind failure.
//
// 🔴 A FRESH SERVER PER START, AND IT REGISTERS NOTHING. Both halves matter, and they
// pull in opposite directions:
//
//   - It registers nothing because ServeMux.Handle panics on a duplicate pattern, and
//     this is entered again by any start retried after a failed one. The routes are
//     registered once, in the initialize phase.
//   - It builds a new server because an http.Server cannot be restarted: Shutdown
//     latches its shuttingDown flag permanently, so reusing one would bind and then
//     serve nothing.
//
// The port is a parameter so a test can ask for an ephemeral one rather than racing
// whatever holds 8080.
func startHttpServer(port int32) error {
	httpServer = Microservice.NewHttpServer(port)
	if err := httpServer.Start(); err != nil {
		return err
	}
	log.Info().Str("addr", httpServer.Addr()).Msg("Started Sparkplug ingest HTTP server.")
	return nil
}

// runLeadership is the acquire/serve/standby loop. It repeatedly tries to acquire the
// single ownership lease; while another replica holds it, this replica is a warm
// standby that connects nothing and retries. On acquisition it serves as leader until
// the lease is lost or the service is shutting down, then loops to re-acquire.
func runLeadership(ctx context.Context, lease *messaging.DistributedLease) {
	for {
		if ctx.Err() != nil {
			return
		}
		held, err := lease.Acquire(leasePartition)
		if err != nil {
			if errors.Is(err, messaging.ErrLeaseHeld) {
				log.Info().Msg("Another replica holds the Sparkplug lease; standing by.")
			} else {
				log.Warn().Err(err).Msg("Failed to acquire the Sparkplug leadership lease; retrying as standby.")
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

// leaseTerm is the narrow slice of *messaging.Lease that a leadership term drives.
// It exists so the term's teardown ORDERING — join the renewer, THEN release — can
// be exercised deterministically by a unit test with a fake lease, the way
// lwm2m-ingest's serveServer lets its serve supervision be tested with a fake
// transport. *messaging.Lease satisfies it.
type leaseTerm interface {
	Epoch() uint64
	KeepAlive(ctx context.Context, interval time.Duration) error
	Release() error
}

// serveAsLeader connects every source and holds them until the lease is definitively
// lost (KeepAlive returns ErrNotHolder) or the service is shutting down (ctx). On
// either it self-evicts: stop the sources (announcing OFFLINE, firing the wills),
// join the renewer, and release the lease so a standby can take over. KeepAlive runs
// on its OWN goroutine so no processing stall can starve renewal (ADR-070 M4).
func serveAsLeader(ctx context.Context, lease leaseTerm) {
	log.Info().Uint64("epoch", lease.Epoch()).Msg("Acquired Sparkplug leadership; connecting sources.")
	setLeader(true)

	leaderCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// keepAliveDone closes when the renewer has stopped. The release below does not
	// depend on that to order its revision CAS — Lease.Release serializes against an
	// in-flight Renew itself — but the teardown still joins on it so the term's
	// goroutines do not outlive the term. See the join in the teardown.
	keepAliveDone := make(chan struct{})
	go func() {
		defer close(keepAliveDone)
		if errors.Is(lease.KeepAlive(leaderCtx, leaseRenewInterval), messaging.ErrNotHolder) {
			log.Error().Msg("Lost the Sparkplug leadership lease; self-evicting.")
			cancel()
		}
	}()

	Manager.Start()
	<-leaderCtx.Done()

	// The teardown order is the whole of it, and it matches DETECT's endTerm.
	// 1. Stop everything that reads or writes: the sources announce OFFLINE and
	//    disconnect, so past this point nothing is still ingesting for this term.
	setLeader(false)
	Manager.Stop()
	// 2. Join the renewer BEFORE releasing. The revision CAS is NOT what this buys any
	//    more: Lease.Release serializes itself against an in-flight Renew, so the
	//    ordering holds whether or not a caller joins first. What the join buys is that
	//    no goroutine from this term outlives the term — leaderCtx is already cancelled
	//    here, so this waits only for the renewer to observe that and stop, and past
	//    this point nothing from the term is still running or still logging against a
	//    partition a successor may already own.
	<-keepAliveDone
	// 3. Release last, once nothing can still be writing to the entry.
	if err := lease.Release(); err != nil {
		log.Warn().Err(err).Msg("Error releasing the Sparkplug lease (it will age out via its TTL).")
	}
	log.Info().Msg("Released Sparkplug leadership; returning to standby.")
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

// beforeMicroserviceStopped announces OFFLINE on every source, disconnects them,
// and shuts the HTTP server down.
func beforeMicroserviceStopped(ctx context.Context) error {
	Microservice.Readiness.BeginDrain()
	// Stop the sources first (announce OFFLINE, disconnect) so no further message can
	// be emitted, THEN stop the writer. When leadership is running, cancelling it makes
	// the loop self-evict (Manager.Stop + lease Release) and exit; we wait for that to
	// unwind. With no sources there is no leadership loop, so stop the Manager directly.
	if leadershipCancel != nil {
		leadershipCancel()
		<-leadershipDone
	} else if Manager != nil {
		Manager.Stop()
	}
	if NatsManager != nil {
		if err := NatsManager.Stop(ctx); err != nil {
			log.Error().Err(err).Msg("Error stopping the NATS manager.")
		}
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

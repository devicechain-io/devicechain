// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package agent is the dc-edge-agent core: it embeds a nats-server MQTT gateway as
// a device-transparent local listener (ADR-006), captures device publishes durably
// to a local JetStream file store, and forwards them to the cloud Instance over a
// paho MQTT uplink — so the cloud's own MQTT-gateway capture (ADR-030) ingests them
// with no platform-side publish path.
//
// The agent is a GATEWAY (the only uplink mode): it authenticates to the cloud as its own
// device and forwards each local device's events in the gateway's own namespace, where the
// platform accepts an event only for a device assigned to this gateway. Local devices are
// unchanged (they publish their golden topic to the local listener); see uplink.go for the
// topic constructor and gateway.go for how a platform refusal is surfaced and kept.
//
// SLICE (ADR-068 Tier 1): this is E3, the bounded buffer + observability. Over E2's
// durable store-and-forward buffer it adds:
//
//   - A BOUNDED spool: the capture stream carries a MaxBytes budget (local.spoolMaxBytes)
//     with DiscardOld, so a multi-day outage can no longer grow the on-disk store until
//     the disk fills. When full it behaves as a ring buffer — the OLDEST un-forwarded
//     event is evicted to admit the newest (see ensureStream for why DiscardOld and not
//     DiscardNew). JetStreamMaxStore is pinned above the budget so admission does not
//     depend on boot-time free disk (a near-full spool must not crash-loop on restart).
//   - VISIBLE loss: eviction is counted from (FirstSeq-1) - ackedCount — the stream's
//     durable first sequence minus this agent's OWN persisted acked-count token — so a
//     drop is surfaced (dropped_total, a loud WARN) live during the outage and across a
//     restart. It is NOT derived from the durable consumer's ack floor, which nats-server
//     drags past limit-evicted messages and would silently erase the drops.
//   - A local health signal: a loopback (127.0.0.1) Prometheus /metrics + /healthz
//     endpoint (see metrics.go). The MQTT gateway stays the only LAN-exposed surface (F5).
//
// It retains E2's guarantees: the drain acks a captured event only AFTER the cloud
// PUBACKs it (store-and-forward across a WAN outage and an agent restart), the exactly-
// once minted altId + stamped occurredTime (see idempotency.go), and the two-phase start
// that brings the capture stream up before the device MQTT listener ever accepts.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/devicechain-io/dc-edge-agent/config"
)

const (
	// captureStream mirrors the cloud's ADR-030 capture stream name — it holds the
	// same shape of data (raw device publishes on the device-events subject), one
	// hop earlier.
	captureStream = "device-events-capture"

	// forwarderDurable is the durable consumer that drains the spool to the uplink.
	// What actually lets an un-acked event survive an agent restart is the stream's
	// WorkQueue retention (un-acked messages stay in the stream); a durable consumer
	// on top preserves delivery/redelivery bookkeeping across restarts rather than
	// re-reading from the start. It is intentionally NOT deleted on shutdown (see
	// Run: no Unsubscribe on the durable) so that bookkeeping persists.
	forwarderDurable = "edge-forwarder"

	// uplinkWindow bounds the publishes in flight to the uplink: the drain pulls at most
	// this many spool messages, publishes them all, and acks each on its OWN PUBACK. The
	// old drain waited for each PUBACK before the next publish, so a box topped out at
	// about 1/RTT events per second (about 20/s at 50 ms) and could not catch up after an
	// outage if its devices produced more. fetchWait paces the pull when the spool is idle.
	uplinkWindow = 64
	fetchWait    = time.Second

	// disconnectedPoll is how often the drain re-checks the uplink while it is down.
	// While disconnected the drain does NOT pull, so buffered events sit in the
	// WorkQueue stream rather than cycling through pending/redelivery.
	disconnectedPoll = time.Second

	// sampleEvery bounds how often the agent samples stream state: it sets the
	// stream-derived gauges, recomputes the drop count, persists the acked-count
	// progress token, refreshes the health flag, and logs a status line.
	sampleEvery = 30 * time.Second

	// mqttStoreHeadroom is added on top of spoolMaxBytes when pinning JetStreamMaxStore,
	// so the embedded server's internal MQTT streams ($MQTT_sess/_msgs/_qos2in, which
	// carry no MaxBytes of their own) have room within the reservation and the capture
	// stream's budget can always be satisfied regardless of boot-time free disk.
	mqttStoreHeadroom int64 = 128 * 1024 * 1024 // 128 MiB

	// highWaterFraction is the spool-utilisation level at which the status line escalates
	// to a WARN, giving an operator runway before the ring buffer starts dropping. It is
	// best-effort (sampled on the tick); dropped_total is the guarantee.
	highWaterFraction = 0.8
)

// Agent is one running dc-edge-agent.
type Agent struct {
	cfg    config.Configuration
	log    *slog.Logger
	uplink *Uplink

	srv *natsserver.Server
	nc  *nats.Conn
	js  nats.JetStreamContext

	// installId + streamEpoch are the stable, collision-free prefix of every minted
	// altId (edge:<installId>:<streamEpoch>:<streamSeq>). Both are tokens we persist in
	// StoreDir (see identity.go): installId is per-box (minted on first boot);
	// streamEpoch identifies the capture stream's incarnation (re-minted only when the
	// stream is created, fixed across a plain restart) so a delete+recreate that
	// restarts the sequence at 1 cannot re-issue keys the cloud already consumed (MAJOR-4).
	installId   string
	streamEpoch string

	// ready closes once the embedded broker, capture stream, and durable consumer
	// are all up and the drain is about to start — the point after which a device
	// publish is guaranteed to be captured. Tests wait on it; nothing else reads it.
	ready chan struct{}

	// publishOverride, if set, replaces the uplink publish for the forwarded payload.
	// It exists only for tests: it lets a test deterministically FAIL a forward while
	// the real uplink is genuinely connected (the mid-batch-drop case the drain's
	// Connected() pause can't otherwise produce), and capture the exact bytes each
	// attempt would send — the only way to pin ack-after-PUBACK (no loss) and the
	// replay-stable dedup key (identical altId/occurredTime across a redelivery). nil
	// in prod, so forwards go to the real uplink.
	publishOverride func(topic string, payload []byte) error

	// metrics is the E3 observability surface (loopback Prometheus /metrics + /healthz).
	// nil until Run starts it, and nil for the whole life of an agent whose configured
	// metricsPort is 0 (disabled).
	metrics *edgeMetrics

	// metricsAddrOverride, if set, is the listen address for the metrics endpoint,
	// replacing 127.0.0.1:<metricsPort>. It exists only for tests: they pass
	// "127.0.0.1:0" so many agents (and an agent restarted on the same storeDir) can run
	// in one process without colliding on a fixed port, then scrape a.metrics.addr.
	metricsAddrOverride string

	// ackedCount is the number of capture-stream messages this agent has removed by ack.
	// It is the durable basis for the drop metric (drops = (FirstSeq-1) - ackedCount):
	// advanced at every ack site, persisted on the sample tick + shutdown, reloaded on
	// start. See loadOrSeedAckedCount for why it must be the agent's OWN token and never
	// the durable consumer's ack floor.
	ackedCount atomic.Uint64

	// droppedTotal is the running count of events evicted by the ring buffer (overflow).
	// Set on the sample tick to max(0, (FirstSeq-1)-ackedCount); read by dropped_total.
	droppedTotal atomic.Int64

	// healthy reflects whether recent sample ticks' StreamInfo reads succeeded, so /healthz
	// is a LIVE probe (a post-startup wedged store reads unhealthy) rather than a one-shot
	// "was once ready". sampleFailures counts CONSECUTIVE failed reads; healthy flips false
	// only after two, so a single transient JS-API blip does not trigger a mid-outage restart.
	healthy        atomic.Bool
	sampleFailures atomic.Int64

	// localPassword is the resolved shared secret the local MQTT listener requires,
	// read once from cfg.Local.PasswordEnv in New (eagerly, like the uplink credential, so
	// a missing projected Secret fails at startup — before any disk state is written —
	// rather than silently degrading the gate to open). Empty when local auth is not
	// configured (cfg.Local.Username == ""); newServer then leaves the listener open.
	localPassword string

	// counters — basic observability so "is my edge working?" is answerable at a glance;
	// also the source of truth behind the Prometheus *_total counters (metrics.go).
	received      atomic.Int64
	forwarded     atomic.Int64
	forwardErrors atomic.Int64
	mismatched    atomic.Int64
	malformed     atomic.Int64

	// Gateway counters (see gateway.go). refused counts platform notices (and the agent's
	// own refusals), refusedKept those whose payload was written to the local dead-letter
	// file, refusedUnmatched those whose payload was no longer held. tenantMismatched counts
	// local publishes naming another tenant (never spooled); bodyStripped the credential
	// members removed from event bodies; inflight the publishes awaiting a PUBACK.
	refused          atomic.Int64
	refusedKept      atomic.Int64
	refusedUnmatched atomic.Int64
	tenantMismatched atomic.Int64
	bodyStripped     atomic.Int64
	inflight         atomic.Int64

	sent       *recentSent
	dead       *deadLetters
	refusalLog logLimiter
}

// New builds an Agent from validated config (the uplink credential is resolved
// eagerly so a missing projected Secret fails at startup, not on first forward).
func New(cfg config.Configuration, log *slog.Logger) (*Agent, error) {
	up, err := NewUplink(cfg.Uplink, cfg.InstanceId, log)
	if err != nil {
		return nil, err
	}

	// Resolve the local MQTT password eagerly (config already enforces username and
	// passwordEnv are both-or-neither). Fail CLOSED on an empty resolved value: an empty
	// MQTT password is not "no auth" — nats-server still requires the username but then
	// compares the password against "", which any client can present, so a Secret that
	// failed to project would leave a listener that CLAIMS to be gated but accepts an
	// empty password. Refusing to start is the only thing between a missing mount and a
	// silently-open gate. (Resolved here, not in newServer, so this fails before phase 1
	// mints any durable identity/stream state.)
	var localPassword string
	if cfg.Local.Username != "" {
		localPassword = os.Getenv(cfg.Local.PasswordEnv)
		if localPassword == "" {
			return nil, fmt.Errorf("local.passwordEnv %q is set but that environment variable is empty (the local-auth Secret was not projected); refusing to start with an effectively-open listener", cfg.Local.PasswordEnv)
		}
		// A password minted with `echo` carries a trailing newline that becomes part of the
		// secret, so devices presenting the trimmed value are rejected. Fail-closed and
		// discoverable, but warn — it is a classic provisioning footgun.
		if strings.TrimSpace(localPassword) != localPassword {
			log.Warn("local.passwordEnv value has leading/trailing whitespace — it is used verbatim; devices must present the exact same bytes (a trailing newline from `echo` is the usual cause)", "env", cfg.Local.PasswordEnv)
		}
	}

	a := &Agent{
		cfg: cfg, log: log, uplink: up, localPassword: localPassword, ready: make(chan struct{}),
		sent: newRecentSent(), dead: newDeadLetters(cfg.Local.StoreDir),
	}
	up.onNotice = a.handleNotice
	return a, nil
}

// Run brings the agent up and blocks until ctx is cancelled, then shuts down cleanly.
//
// Two-phase start closes E1's M2 startup window. On first boot the capture stream does
// not exist yet, and nats-server starts the MQTT listener accepting before we could
// create it — a device publishing in that window would be PUBACK'd with nothing
// capturing it. So phase 1 brings the stream up with the device MQTT listener DISABLED,
// then phase 2 does the real start with MQTT enabled: nats-server recovers the
// (now persisted) stream during EnableJetStream, which runs BEFORE it starts the MQTT
// accept loop, so the durability floor holds from the very first accepted publish.
// On every subsequent boot phase 1 simply adopts the existing stream.
func (a *Agent) Run(ctx context.Context) error {
	installId, err := loadOrCreateInstallId(a.cfg.Local.StoreDir)
	if err != nil {
		return fmt.Errorf("resolve install id: %w", err)
	}
	a.installId = installId

	// StoreDir now exists (loadOrCreateInstallId created it). Close a world-readable spool:
	// it holds buffered telemetry, which can carry payload credentials in flight (ADR-014),
	// plus the identity tokens. A store an operator pre-created with `mkdir -p` (or a
	// container named volume) is 0755 by default — world-readable — and nats-server's
	// MkdirAll(0700) is a no-op on an existing directory, so nothing else tightens it (E4-B).
	a.hardenStoreDirPerms()

	// Phase 1 (bootstrap): create/adopt the capture stream with MQTT disabled, and
	// resolve the stream-incarnation epoch it is (re)minted/read against — folded into
	// every minted altId so a stream delete/recreate cannot re-issue sequences that
	// collide with keys the cloud already consumed (MAJOR-4).
	epoch, err := a.bootstrapStream()
	if err != nil {
		return fmt.Errorf("bootstrap capture stream: %w", err)
	}
	a.streamEpoch = epoch

	// Phase 2 (serve): full start with the device MQTT listener enabled.
	srv, err := a.newServer(true)
	if err != nil {
		return fmt.Errorf("start embedded broker: %w", err)
	}
	a.srv = srv
	defer func() {
		a.srv.Shutdown()
		a.srv.WaitForShutdown() // release the StoreDir file locks before Run returns (a
		// same-store restart must not race a still-closing store).
	}()

	if err := a.connect(); err != nil {
		return fmt.Errorf("connect in-process client: %w", err)
	}
	defer a.nc.Close()
	a.healthy.Store(true) // JetStream is answering; /healthz is green before the first tick

	filter := a.captureSubject()
	if err := a.alignConsumerFilter(filter); err != nil {
		return fmt.Errorf("align durable consumer filter: %w", err)
	}
	sub, err := a.js.PullSubscribe(
		filter,
		forwarderDurable,
		nats.BindStream(captureStream),
		// Unlimited redelivery: a multi-day outage must never exhaust delivery
		// attempts and silently drop a buffered event. (No Nak anywhere — redelivery
		// comes from AckWait lapsing, never from a Nak fuse.)
		nats.MaxDeliver(-1),
		// AckWait bounds how long a delivered-but-un-acked message waits before
		// redelivery. It is a recovery-latency knob, not a correctness bound: a pull
		// consumer only redelivers in response to a Fetch, and this single-threaded
		// drain acks every message in a batch before it issues the next Fetch, so a
		// message can never be concurrently redelivered while still in flight — even if
		// a slow batch outlives AckWait. Sized above a single publish's worst case so
		// the redelivery latency after a mid-batch drop stays small.
		nats.AckWait(a.ackWait()),
	)
	if err != nil {
		return fmt.Errorf("bind durable consumer: %w", err)
	}
	// Deliberately NO `defer sub.Unsubscribe()`: the nats.go client deletes a JS
	// consumer it created on Unsubscribe, so unsubscribing on a clean shutdown would
	// delete the durable (and reset its redelivery bookkeeping) every time. nc.Close()
	// below severs the subscription without deleting the durable.

	// A disk sized below the budget still honours the reservation but will fill before
	// the ring buffer's cap is reached — surface it (does not fail the start). The
	// acked-count seed already happened in phase 1 (bootstrapStream), before MQTT.
	a.warnIfDiskUndersized()

	// Start the loopback metrics/health endpoint (E3), unless disabled (metricsPort 0).
	// A bind failure is fail-closed: fold down the broker first so the deferred Shutdowns
	// still run, then return the error loudly.
	if err := a.startMetrics(); err != nil {
		return fmt.Errorf("start metrics endpoint: %w", err)
	}
	defer a.stopMetrics()

	// The uplink runs its own bounded-backoff reconnect loop for the life of ctx.
	go a.uplink.Run(ctx)

	// A device that publishes on a DIFFERENT instanceId than we forward silently
	// misses the capture stream (F9). Watch the whole device-events space across any
	// instance and count the mismatches so a config typo is visible, not a silent drop.
	a.watchInstanceMismatch()

	go a.sampleLoop(ctx)

	// Announce the local-auth posture loudly so an unintentionally open listener is
	// visible in the log at every boot (and continuously in metrics via
	// local_auth_enabled). New already fail-closed on a configured-but-empty secret, so
	// reaching here with a username means the gate is genuinely on. Emitted BEFORE
	// close(a.ready) so a test that waits on readiness is guaranteed to observe it (the
	// close is the happens-before barrier).
	if a.cfg.Local.Username == "" {
		a.log.Warn("local MQTT listener is UNAUTHENTICATED — any client on the LAN can publish and subscribe; deploy only on a trusted device network, or set local.username + local.passwordEnv",
			"mqtt_listen", fmt.Sprintf("%s:%d", a.cfg.Local.ListenHost, a.cfg.Local.ListenPort))
	} else {
		a.log.Info("local MQTT listener requires a shared-secret credential", "local_user", a.cfg.Local.Username)
	}

	close(a.ready) // substrate is up; a device publish is now guaranteed captured

	a.log.Info("dc-edge-agent up",
		"instance_id", a.cfg.InstanceId,
		"install_id", a.installId,
		"mqtt_listen", fmt.Sprintf("%s:%d", a.cfg.Local.ListenHost, a.cfg.Local.ListenPort),
		"store_dir", a.cfg.Local.StoreDir,
		"spool_max_bytes", a.cfg.Local.SpoolMaxBytes,
		"metrics_addr", a.metricsAddr(),
		"uplink", a.cfg.Uplink.BrokerURL)

	a.drain(ctx, sub)

	// Persist the final acked-count on a clean shutdown so the next boot's drop baseline
	// is exact (the sample tick persists it periodically; this closes the last window).
	if err := persistAckedCount(a.cfg.Local.StoreDir, a.ackedCount.Load()); err != nil {
		a.log.Warn("could not persist final acked-count", "err", err)
	}
	return nil
}

// bootstrapStream starts a throwaway embedded server with the device MQTT listener
// DISABLED, creates or adopts the capture stream, seeds the acked-count progress token,
// then shuts that server down. Its job is to guarantee the stream exists on disk AND the
// drop baseline is fixed before phase 2 exposes the MQTT port (see Run's two-phase
// rationale). Seeding here — with MQTT disabled — is what makes the first-boot migration
// baseline exact: no device can publish (and so no RUNTIME eviction can occur and be
// silently folded into the seed) between adoption and the seed read. It returns the
// stream-incarnation epoch.
func (a *Agent) bootstrapStream() (string, error) {
	srv, err := a.newServer(false)
	if err != nil {
		return "", fmt.Errorf("start bootstrap broker: %w", err)
	}
	defer func() {
		srv.Shutdown()
		srv.WaitForShutdown() // release the StoreDir file locks before phase 2 opens it
	}()

	nc, err := nats.Connect("", nats.InProcessServer(srv), nats.Name("dc-edge-agent-bootstrap"))
	if err != nil {
		return "", err
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		return "", err
	}
	epoch, err := ensureStream(js, a.captureSubject(), a.cfg.Local.StoreDir, a.cfg.Local.SpoolMaxBytes)
	if err != nil {
		return "", err
	}
	// Seed the acked-count from the stream's first sequence, MQTT still disabled. An absent
	// token (fresh store, or first E3 boot over a pre-E3 store) assumes everything already
	// removed was acked, so drops count from this baseline forward; a present token is
	// loaded (and clamped) — never re-seeded — so a restart mid-outage keeps its drop count.
	info, err := js.StreamInfo(captureStream)
	if err != nil {
		return "", fmt.Errorf("read capture stream state: %w", err)
	}
	acked, err := loadOrSeedAckedCount(a.cfg.Local.StoreDir, info.State.FirstSeq)
	if err != nil {
		return "", fmt.Errorf("resolve acked-count token: %w", err)
	}
	a.ackedCount.Store(acked)
	return epoch, nil
}

// newServer builds and starts an embedded nats-server. DontListen keeps the plain
// NATS client port UNBOUND (F5) — otherwise anyone on the site LAN could connect with
// no auth and harvest telemetry, forge events onto golden subjects, or delete the
// spool. When mqttEnabled, the MQTT gateway (opts.MQTT.Port) is the ONLY exposed
// surface; the agent's own client reaches JetStream over an in-process connection.
// When not, nothing external is bound at all (the phase-1 bootstrap).
func (a *Agent) newServer(mqttEnabled bool) (*natsserver.Server, error) {
	opts := &natsserver.Options{
		Host:       "127.0.0.1",
		DontListen: true, // no bound client port; in-process only (F5)
		JetStream:  true, // required for the MQTT gateway and the durable spool
		StoreDir:   a.cfg.Local.StoreDir,
		// Fsync every store before it is acknowledged. An edge box on flaky site power
		// is exactly where "durable before PUBACK" must mean on-disk, not in the OS
		// page cache — so trade throughput (edge volumes are low) for power-loss
		// durability rather than riding the default multi-minute sync interval.
		SyncAlways: true,
		// Pin the JetStream store reservation ABOVE the spool budget explicitly. Left
		// unset it resolves to the free disk measured at boot, so adopting a near-full
		// spool on a small disk (free < spoolMaxBytes) would fail the stream's MaxBytes
		// reservation and crash-loop the agent — on exactly the box (small disk, long
		// outage, reboot) E3 exists for. Pinning it decouples admission from boot-time
		// free space; the headroom covers the internal MQTT streams too.
		JetStreamMaxStore: a.cfg.Local.SpoolMaxBytes + mqttStoreHeadroom,
		// Lift the per-stream consumer cap nats-server 2.15 introduced. Left at zero it
		// resolves to 1,000 consumers per stream, and every QoS 1 or 2 MQTT subscription
		// is a durable consumer on the server's own $MQTT_msgs stream (a QoS 2 session
		// adds one on $MQTT_out), which sets no limit of its own. So the 1,001st QoS 1
		// subscription on a site would be answered SUBACK 0x80 while its CONNECT
		// succeeded: the device stays connected and never receives what it subscribed
		// to. -1 is the documented way back to the 2.14 behaviour of no default; a
		// per-stream or account limit cannot opt out, because -1 there counts as "unset".
		// The cloud broker gets the same setting from the nats OpenTofu module.
		JetStreamLimits: natsserver.JSLimitOpts{DefaultMaxConsumers: -1},
	}
	if mqttEnabled {
		opts.MQTT = natsserver.MQTTOpts{
			Host: a.cfg.Local.ListenHost,
			Port: a.cfg.Local.ListenPort,
		}
		// Optional shared-secret gate on the device MQTT surface (E4). MQTTOpts.Username/
		// Password override authorization for MQTT clients ONLY: the in-process drain and
		// bootstrap clients use the regular (open, no global Users) auth path, so they need
		// no credentials threaded in. Set both together or neither — an empty password with
		// a username is fail-open (nats-server compares against ""), which New already
		// refused to reach by requiring a non-empty resolved secret.
		if a.cfg.Local.Username != "" {
			opts.MQTT.Username = a.cfg.Local.Username
			opts.MQTT.Password = a.localPassword
		}
	}
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		return nil, err
	}
	// Route the embedded server's own logs (a bind failure on the MQTT port, an
	// unwritable StoreDir) somewhere visible — otherwise a fatal startup fault is
	// invisible behind the generic "not ready within 15s" below.
	srv.ConfigureLogger()
	go srv.Start()
	if !srv.ReadyForConnections(15 * time.Second) {
		srv.Shutdown()
		return nil, fmt.Errorf("embedded broker not ready within 15s (mqtt=%t, storeDir %q) — check the server log above for a bind or store error",
			mqttEnabled, a.cfg.Local.StoreDir)
	}
	return srv, nil
}

// connect dials the phase-2 server over its in-process pipe (no TCP), so the drain
// client works even with the client port unbound (F5).
func (a *Agent) connect() error {
	nc, err := nats.Connect("", nats.InProcessServer(a.srv), nats.Name("dc-edge-agent"))
	if err != nil {
		return err
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return err
	}
	a.nc = nc
	a.js = js
	return nil
}

// ackWait sizes the consumer's redelivery timeout above a single publish's worst-case
// duration (the uplink's connect timeout doubles as its publish timeout), so a
// slow-but-succeeding forward is acked before AckWait could redeliver it.
func (a *Agent) ackWait() time.Duration {
	return 2 * time.Duration(a.cfg.Uplink.ConnectTimeoutSeconds) * time.Second
}

// captureSubject is the one subject the local spool stores and the drain reads:
// {instance}.{tenant}.devices.*.events. Narrowing it to the configured tenant (it used to
// be every tenant of the instance) is what keeps a local publish naming another tenant out
// of the spool: such a publish could only ever be refused by the cloud, so it is counted
// (tenant_mismatched_total) and never stored. A spool written by an earlier build may still
// hold such messages; they match no consumer and stay until the ring buffer evicts them.
func (a *Agent) captureSubject() string {
	return messaging.DeviceEventsSubject(a.cfg.InstanceId, a.cfg.Uplink.Tenant, "*")
}

// alignConsumerFilter moves an existing durable consumer's filter to subject. The durable
// outlives restarts, so a box that was configured for a wider subject keeps the old filter,
// and binding with a different one is refused. Updating in place keeps the delivery
// bookkeeping (and therefore the backlog) that the durable exists to preserve.
func (a *Agent) alignConsumerFilter(subject string) error {
	ci, err := a.js.ConsumerInfo(captureStream, forwarderDurable)
	if err != nil {
		if errors.Is(err, nats.ErrConsumerNotFound) {
			return nil // first boot: PullSubscribe creates it with this filter
		}
		return err
	}
	if ci.Config.FilterSubject == subject && len(ci.Config.FilterSubjects) == 0 {
		return nil
	}
	cfg := ci.Config
	cfg.FilterSubject = subject
	cfg.FilterSubjects = nil
	if _, err := a.js.UpdateConsumer(captureStream, &cfg); err != nil {
		return err
	}
	a.log.Warn("moved the forwarder's filter to the configured tenant", "was", ci.Config.FilterSubject, "now", subject)
	return nil
}

// ensureStream creates (or adopts) the local capture stream and returns its
// stream-incarnation epoch (see identity.go). It captures the same device-events subject
// the cloud does, so once it exists the embedded MQTT gateway writes each device publish
// here BEFORE it PUBACKs the device (the ADR-030 durability floor, one hop earlier).
// WorkQueue retention makes it a true spool: a message is removed only when the drain
// acks it (in E2, only after the cloud confirms), and un-acked messages persist on disk
// across a restart.
//
// The epoch is a token we persist ourselves: minted fresh whenever we CREATE the stream
// (so a delete+recreate that restarts the sequence at 1 gets a new altId namespace) and
// read back when we ADOPT an existing stream (so it is fixed across a plain restart). It
// is deliberately NOT StreamInfo().Created — nats-server resets Created on recovery, so a
// Created-derived epoch would change on every restart and re-key buffered events.
//
// MaxBytes + DiscardOld (E3) bound the spool as a ring buffer. DiscardOld — not
// DiscardNew — is the fail-safe: the embedded MQTT gateway PUBACKs a QoS-1 device from
// its OWN internal persistence, decoupled from this overlay capture stream, so a
// DiscardNew rejection at the capture layer would silently lose the NEWEST event AFTER
// the device was already told it was durable. DiscardOld instead drops the OLDEST
// un-forwarded event (counted + surfaced), keeps the agent alive, and favours recent
// telemetry. Retention MUST stay WorkQueue (the server forbids changing it on update, so
// it never drifts here). Adding MaxBytes to a pre-E3 (unbounded) stream is an accepted
// UpdateStream diff; shrinking below current usage evicts immediately.
func ensureStream(js nats.JetStreamContext, subject, storeDir string, maxBytes int64) (string, error) {
	cfg := &nats.StreamConfig{
		Name:      captureStream,
		Subjects:  []string{subject},
		Storage:   nats.FileStorage,
		Retention: nats.WorkQueuePolicy,
		MaxBytes:  maxBytes,
		Discard:   nats.DiscardOld,
	}
	if _, err := js.StreamInfo(captureStream); err != nil {
		if !errors.Is(err, nats.ErrStreamNotFound) {
			return "", err
		}
		if _, err := js.AddStream(cfg); err != nil {
			return "", err
		}
		return mintStreamEpoch(storeDir)
	}
	// Adopt an existing stream but keep its config current with our instanceId + budget
	// (a reconfigured agent must not keep forwarding an old namespace or an old cap).
	if _, err := js.UpdateStream(cfg); err != nil {
		return "", err
	}
	return readStreamEpoch(storeDir)
}

// warnIfDiskUndersized logs (does not fail) when the store's filesystem cannot hold the
// spool budget plus headroom. JetStreamMaxStore is pinned so admission does not depend on
// this, but an operator who sized the disk below the budget should hear about it: the
// reservation is honoured but the disk fills before the ring buffer's cap is reached. The
// spool's already-written bytes are added back to free space — otherwise an adopted
// near-full spool (the long-outage box this most matters for) would false-warn every boot.
// Best-effort — a statfs or StreamInfo error is ignored.
func (a *Agent) warnIfDiskUndersized() {
	var st syscall.Statfs_t
	if err := syscall.Statfs(a.cfg.Local.StoreDir, &st); err != nil {
		return
	}
	free := int64(st.Bavail) * int64(st.Bsize)
	var spoolBytes int64
	if info, err := a.js.StreamInfo(captureStream); err == nil {
		spoolBytes = int64(info.State.Bytes)
	}
	need := a.cfg.Local.SpoolMaxBytes + mqttStoreHeadroom
	if free+spoolBytes < need {
		a.log.Warn("store filesystem cannot hold the spool budget + headroom — "+
			"the disk may fill before the spool's ring buffer cap is reached",
			"store_dir", a.cfg.Local.StoreDir, "free_bytes", free, "spool_bytes", spoolBytes,
			"spool_max_bytes", a.cfg.Local.SpoolMaxBytes, "reserved_bytes", need)
	}
}

// hardenStoreDirPerms removes the WORLD bits from the store directory when present, and
// logs that it did. The spool holds buffered telemetry at rest, which per ADR-014 can
// carry payload credentials in flight, plus the identity tokens — so a world-traversable
// store is a real exposure, and the common provisioning defaults (`mkdir -p`, a container
// named volume) leave it 0755. Stripping ONLY the world bits (mode &^ 0o007) closes that
// without touching the GROUP bits: a deliberate group-shared setup (Kubernetes fsGroup
// mounts the volume group-owned + setgid, and the agent reaches it via that group, NOT as
// owner) must keep working — forcing 0700 there would lock the agent out of its own store.
//
// Best-effort and never fatal: in the fsGroup case the directory is root-owned (chmod
// would EPERM) but also already has no world bits (nothing to do); in the self-owned case
// (systemd, a chowned volume, bare metal) the chmod succeeds. A stat/chmod error is warned,
// not fatal — a genuinely missing/unwritable store surfaces at the server start below.
func (a *Agent) hardenStoreDirPerms() {
	fi, err := os.Stat(a.cfg.Local.StoreDir)
	if err != nil {
		return
	}
	perm := fi.Mode().Perm()
	if perm&0o007 == 0 {
		return // already not world-accessible (private 0700 or a group-shared 0770)
	}
	// Strip ONLY the world bits, and carry the setuid/setgid/sticky bits through unchanged:
	// Perm() masks them off, so a naive Chmod(perm&^0o007) would silently CLEAR setgid — the
	// bit a group-shared store often sets so JetStream-created files inherit the group. That
	// would rot the very group access this preserves group bits to protect.
	newPerm := perm &^ 0o007
	special := fi.Mode() & (os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	if err := os.Chmod(a.cfg.Local.StoreDir, special|newPerm); err != nil {
		a.log.Warn("store directory is world-accessible and could not be tightened — it holds buffered telemetry (which can carry in-flight credentials) and identity tokens; remove other-access manually (chmod o-rwx)",
			"store_dir", a.cfg.Local.StoreDir, "mode", fmt.Sprintf("%#o", perm), "err", err)
		return
	}
	a.log.Warn("store directory was world-accessible; removed other-access (a spool carries in-flight credentials) — provision it 0700 (or group-shared, e.g. a container fsGroup) to avoid this",
		"store_dir", a.cfg.Local.StoreDir, "was", fmt.Sprintf("%#o", perm), "now", fmt.Sprintf("%#o", newPerm))
}

// drain pulls captured events FIFO and forwards them to the cloud, until ctx ends. Each pull
// is at most uplinkWindow messages, all published before any PUBACK is awaited, so the
// publishes in flight never exceed the window and a box's drain rate is about window/RTT
// rather than 1/RTT.
func (a *Agent) drain(ctx context.Context, sub *nats.Subscription) {
	for {
		if ctx.Err() != nil {
			return
		}
		// While the uplink is down, don't pull. Leaving messages un-fetched keeps them
		// safely in the WorkQueue stream (not counting against redelivery) until the
		// uplink is back; the capture path keeps accepting new device publishes.
		if !a.uplink.Connected() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(disconnectedPoll):
			}
			continue
		}
		msgs, err := sub.Fetch(uplinkWindow, nats.MaxWait(fetchWait))
		if err != nil {
			// A fetch that finds nothing before MaxWait returns ErrTimeout — that is
			// the idle case, not a fault.
			if errors.Is(err, nats.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			a.log.Warn("drain fetch failed", "err", err)
			continue
		}
		a.forwardBatch(ctx, msgs)
	}
}

// forwardBatch publishes one pulled batch and returns when every publish it started has
// settled. Each message is acked by the goroutine that saw ITS PUBACK, and only then: a
// message whose publish failed or timed out stays un-acked and is redelivered after
// AckWait with the same minted key. A failure stops the batch from starting any more
// publishes (the uplink is likely down; the Connected() gate in drain pauses the pull).
func (a *Agent) forwardBatch(ctx context.Context, msgs []*nats.Msg) {
	var wg sync.WaitGroup
	defer wg.Wait()
	for _, msg := range msgs {
		// Bail a long batch promptly on shutdown rather than working through all of it.
		if ctx.Err() != nil {
			return
		}
		a.received.Add(1)
		ev, handled, err := a.prepare(msg)
		if handled {
			continue
		}
		if err != nil {
			a.forwardErrors.Add(1)
			a.log.Warn("forward failed — leaving event buffered for redelivery", "subject", msg.Subject, "err", err)
			return
		}
		// Held BEFORE the publish: a notice can arrive before this goroutine is scheduled
		// again, and a refusal that finds nothing held is a refusal whose payload was lost.
		a.sent.put(ev)
		wait, err := a.begin(ev.topic, ev.payload)
		if err != nil {
			a.forwardErrors.Add(1)
			a.log.Warn("forward failed — leaving event buffered for redelivery", "subject", msg.Subject, "err", err)
			return
		}
		a.inflight.Add(1)
		wg.Add(1)
		go func(msg *nats.Msg, wait func() error) {
			defer wg.Done()
			defer a.inflight.Add(-1)
			if err := wait(); err != nil {
				a.forwardErrors.Add(1)
				a.log.Warn("forward failed — leaving event buffered for redelivery", "subject", msg.Subject, "err", err)
				return
			}
			a.forwarded.Add(1)
			// ACK SITE: ack ONLY after the cloud PUBACK'd. This is what turns the spool into
			// a real store-and-forward buffer — an event that did not reach the cloud stays
			// in the stream for redelivery.
			a.ackAndCount(msg)
		}(msg, wait)
	}
}

// ackAndCount acks a message and, only if the ack was CONFIRMED, advances the acked-count
// progress token behind the drop metric. Both drain ack sites (a successful forward and
// a malformed-subject skip) go through it, so ackedCount is exactly the count of messages
// this agent REMOVED by ack — the complement of (FirstSeq-1) that isolates ring-buffer
// evictions (drops = (FirstSeq-1) - ackedCount; see sampleMetrics).
//
// AckSync, not Ack: a plain Ack is fire-and-forget, so on a shutdown-adjacent sever the
// ack can be counted while the server never processed it — the message then redelivers
// next boot and counts a SECOND time, permanently inflating ackedCount above true
// removals and clamping a real eviction into invisibility (an UNDER-report, the one
// direction the drop metric must never take). AckSync ties the count to a confirmed
// removal; its round-trip is negligible on the in-process pipe against the SyncAlways fsync.
func (a *Agent) ackAndCount(msg *nats.Msg) {
	if err := msg.AckSync(); err != nil {
		a.log.Warn("ack not confirmed; event will be redelivered", "err", err)
		return
	}
	a.ackedCount.Add(1)
}

// prepare maps one captured event to what the uplink publishes: the child's topic in the
// gateway's namespace (the ONE constructor, Uplink.EventTopic) and the payload made exactly-
// once-safe (stampIdempotency, derived from immutable stored-message metadata) with any body
// credential removed. handled reports that the message was disposed of here (acked) and is
// not to be published; err means it could not be prepared and must stay buffered.
//
// Two kinds of message are disposed of locally rather than forwarded, and neither is dropped
// silently: a subject that is not a device-events subject is garbage and is counted
// (malformed), and an event the cloud could only refuse (the gateway's own token, or a token
// the platform's grammar cannot hold) is counted as refused and written to the dead-letter
// file, where an operator can read it.
func (a *Agent) prepare(msg *nats.Msg) (ev *sentEvent, handled bool, err error) {
	// A well-formed device-events subject has no '/'. A device that put a '.' inside a
	// golden-MQTT topic level lands here as a subject with a '/' in a token
	// (SubjectToMqttTopic's documented precondition), which would round-trip to a mangled
	// topic. Skip and count rather than forward garbage — and ack it: it is not a transient
	// fault, so leaving it un-acked would poison the FIFO spool head forever.
	gateway, child, ok := messaging.ParseDeviceEventsSubject(msg.Subject)
	if strings.Contains(msg.Subject, "/") || !ok || gateway != "" {
		a.malformed.Add(1)
		a.log.Warn("dropping event on malformed subject (dot inside an MQTT topic level?)", "subject", msg.Subject)
		a.ackAndCount(msg)
		return nil, true, nil
	}
	meta, err := msg.Metadata()
	if err != nil {
		// A pull-consumer message always has JetStream metadata; without it we cannot
		// derive a replay-stable key, so refuse to forward rather than emit an event
		// the cloud might duplicate.
		return nil, false, fmt.Errorf("no JetStream metadata (cannot derive idempotency key): %w", err)
	}
	payload, altId, stripped, err := prepareUplinkPayload(msg.Data, a.installId, a.streamEpoch, meta.Sequence.Stream, meta.Timestamp, true)
	if err != nil {
		return nil, false, fmt.Errorf("stamp idempotency: %w", err)
	}
	if stripped > 0 {
		a.bodyStripped.Add(int64(stripped))
		if a.refusalLog.allow("strip:"+child, refusalLogEvery) {
			a.log.Warn("removed a credential from an event body before forwarding — the platform refuses a body credential on a gateway uplink; change the device to stop sending it",
				"device", child, "members_removed", stripped)
		}
	}
	topic, terr := a.uplink.EventTopic(child)
	if terr != nil {
		reason := refusalInvalidToken
		if errors.Is(terr, ErrGatewayAddressedItself) {
			reason = refusalGatewaySelf
		}
		held := &sentEvent{altId: altId, child: child, payload: payload}
		a.refuse(reason, held, "the agent will not forward this event: "+terr.Error())
		a.keep(reason, held)
		a.ackAndCount(msg)
		return nil, true, nil
	}
	return &sentEvent{altId: altId, child: child, topic: topic, payload: payload}, false, nil
}

// begin starts one publish, honouring a test override (which runs in the settling goroutine
// so a test can model a slow broker and a concurrent window).
func (a *Agent) begin(topic string, payload []byte) (func() error, error) {
	if a.publishOverride != nil {
		return func() error { return a.publishOverride(topic, payload) }, nil
	}
	return a.uplink.Begin(topic, payload)
}

// handleNotice is the uplink's callback for a platform notice: the platform refused an event
// this gateway published. It counts it (by reason), warns naming the child (rate limited per
// child and reason), and, when the refused event is still held, writes it to the local
// dead-letter file. See gateway.go for why the spool itself cannot be where it is kept.
func (a *Agent) handleNotice(n messaging.GatewayNotice) {
	reason := normaliseRefusalReason(n.Reason)
	ev, held := a.sent.take(n.AltId)
	if !held {
		ev = &sentEvent{altId: n.AltId, child: n.Device}
	}
	if ev.child == "" {
		ev.child = n.Device
	}
	a.refuse(reason, ev, "the platform refused an event this gateway published")
	if held {
		a.keep(reason, ev)
	} else {
		a.refusedUnmatched.Add(1)
		a.log.Warn("a refused event is no longer held on this box — the platform's failed-events record has its payload",
			"device", n.Device, "alt_id", n.AltId, "reason", reason)
	}
}

// refuse counts one refusal and logs it (rate limited per child and reason).
func (a *Agent) refuse(reason string, ev *sentEvent, why string) {
	a.refused.Add(1)
	if a.metrics != nil {
		a.metrics.refused.WithLabelValues(reason).Inc()
	}
	if a.refusalLog.allow("refused:"+reason+":"+ev.child, refusalLogEvery) {
		a.log.Warn("event refused — "+why,
			"device", ev.child, "reason", reason, "gateway", a.cfg.Uplink.GatewayToken,
			"hint", "assign the device to this gateway on the platform (a device belongs to one gateway), or stop it publishing here")
	}
}

// keep writes a refused event to the local dead-letter file and counts it.
func (a *Agent) keep(reason string, ev *sentEvent) {
	if err := a.dead.write(newRefusedRecord(reason, ev)); err != nil {
		a.log.Error("could not write a refused event to the local dead-letter file — the event is still recoverable from the platform's failed-events record if it got that far",
			"device", ev.child, "alt_id", ev.altId, "err", err)
		return
	}
	a.refusedKept.Add(1)
}

// watchInstanceMismatch counts device publishes seen on any instanceId other than the
// one we forward. It is a core (non-JetStream) subscription, so it observes the publish
// without competing with the capture stream. This makes an instanceId typo (which
// otherwise looks like a healthy agent forwarding nothing, F9) visible.
func (a *Agent) watchInstanceMismatch() {
	// {instance}.{tenant}.devices.{token}.events across ANY instance/tenant/device.
	//
	// Synced: the device MQTT listener is already accepting by the time this runs, so
	// publishes can be in flight before the SUB lands. The stake is small — the counter
	// is cumulative and any ONGOING misconfiguration is caught by the next publish — but
	// a device that publishes exactly once, on the wrong instance, inside the window is
	// invisible forever, and that is the single-shot case this watch exists to catch.
	_, err := messaging.SubscribeSynced(a.nc, "*.*.devices.*.events", func(m *nats.Msg) {
		instance, rest, _ := strings.Cut(m.Subject, ".")
		tenant, _, _ := strings.Cut(rest, ".")
		switch {
		case instance != a.cfg.InstanceId:
			a.mismatched.Add(1)
		case tenant != a.cfg.Uplink.Tenant:
			// Right instance, other tenant: not captured (the spool is narrowed to the
			// configured tenant), because the cloud could only refuse it.
			a.tenantMismatched.Add(1)
		}
	})
	if err != nil {
		a.log.Warn("could not start instance-mismatch watch", "err", err)
	}
}

// sampleLoop refreshes the sampled metrics on a fixed tick until ctx ends. It samples
// once immediately so /metrics is populated (and drops are counted) without waiting a
// full interval.
func (a *Agent) sampleLoop(ctx context.Context) {
	a.sampleMetrics()
	ticker := time.NewTicker(sampleEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.sampleMetrics()
		}
	}
}

// sampleMetrics reads the capture stream's state ONCE and from it: sets the stream-derived
// gauges, recomputes the ring-buffer drop count, persists the acked-count progress token,
// refreshes the /healthz liveness flag, and emits a status line (escalated to WARN near
// the high-water mark or on new drops / an instanceId mismatch).
func (a *Agent) sampleMetrics() {
	info, err := a.js.StreamInfo(captureStream)
	if err != nil {
		// A failed read may be a real JetStream wedge OR a transient blip (a JS-API timeout
		// under SyncAlways fsync load). Flip /healthz unhealthy only after TWO consecutive
		// failures, so one blip does not trip a supervisor into restarting the agent
		// mid-outage — the one thing E3 is trying not to cause. Skip this tick's gauges.
		if a.sampleFailures.Add(1) >= 2 {
			a.healthy.Store(false)
		}
		a.log.Warn("metrics sample failed to read capture stream state", "err", err)
		return
	}
	a.sampleFailures.Store(0)
	a.healthy.Store(true)

	usedBytes := int64(info.State.Bytes)
	usedMsgs := int64(info.State.Msgs)
	limit := a.cfg.Local.SpoolMaxBytes

	// Drops = messages removed from the stream front that we did NOT ack. The only
	// removals are our contiguous acks and DiscardOld eviction, so (FirstSeq-1) - ackedCount
	// isolates evictions. int64 throughout so a fresh stream's FirstSeq==0 can't underflow.
	var removed int64
	if info.State.FirstSeq > 0 {
		removed = int64(info.State.FirstSeq - 1)
	}
	drops := removed - int64(a.ackedCount.Load())
	if drops < 0 {
		drops = 0
	}
	// Ratchet dropped_total upward only. It MUST be monotonic (Prometheus reads a decrease
	// as a counter reset and fabricates a rate spike). Because sampleMetrics reads FirstSeq
	// (in `removed`) BEFORE ackedCount, a concurrent drain ack can only make the raw `drops`
	// read transiently LOW, never high — so max() never over-counts and never regresses; a
	// real eviction raises the true value on any tick with no concurrent ack (every tick
	// during an outage, when the drain is paused). The WARN fires only on a real increase.
	for {
		prev := a.droppedTotal.Load()
		if drops <= prev {
			break
		}
		if a.droppedTotal.CompareAndSwap(prev, drops) {
			a.log.Warn("spool overflow: oldest buffered events dropped to stay within spoolMaxBytes",
				"newly_dropped", drops-prev, "dropped_total", drops, "spool_max_bytes", limit)
			break
		}
	}
	dropped := a.droppedTotal.Load()

	// Oldest-event age: guard the empty-stream zero-value FirstTime (would read as decades).
	var oldestAge float64
	if usedMsgs > 0 && !info.State.FirstTime.IsZero() {
		oldestAge = time.Since(info.State.FirstTime).Seconds()
		if oldestAge < 0 {
			oldestAge = 0
		}
	}

	if a.metrics != nil {
		a.metrics.spoolUsedBytes.Set(float64(usedBytes))
		a.metrics.spoolLimitBytes.Set(float64(limit))
		a.metrics.spoolUsedMessages.Set(float64(usedMsgs))
		a.metrics.spoolOldestAgeSeconds.Set(oldestAge)
	}

	// Persist the acked-count so a restart's drop baseline is (near-)exact. Best-effort:
	// a write error is logged, not fatal — the next tick retries.
	if perr := persistAckedCount(a.cfg.Local.StoreDir, a.ackedCount.Load()); perr != nil {
		a.log.Warn("could not persist acked-count", "err", perr)
	}

	mismatch := a.mismatched.Load()
	a.log.Info("edge status",
		"uplink_connected", a.uplink.Connected(),
		"received", a.received.Load(),
		"forwarded", a.forwarded.Load(),
		"forward_errors", a.forwardErrors.Load(),
		"malformed", a.malformed.Load(),
		"instance_mismatched", mismatch,
		"tenant_mismatched", a.tenantMismatched.Load(),
		"refused", a.refused.Load(),
		"refused_kept", a.refusedKept.Load(),
		"body_credentials_stripped", a.bodyStripped.Load(),
		"inflight", a.inflight.Load(),
		"spool_used_messages", usedMsgs,
		"spool_used_bytes", usedBytes,
		"spool_max_bytes", limit,
		"spool_oldest_age_seconds", oldestAge,
		"dropped_total", dropped)

	// High-water WARN: give the operator runway before the ring buffer starts dropping.
	// Best-effort (30s tick); dropped_total is the guarantee, this is the early signal.
	if limit > 0 && usedBytes >= int64(float64(limit)*highWaterFraction) {
		a.log.Warn("spool near capacity — telemetry loss is imminent if the uplink stays down",
			"spool_used_bytes", usedBytes, "spool_max_bytes", limit,
			"spool_oldest_age_seconds", oldestAge, "uplink_connected", a.uplink.Connected())
	}
	if mismatch > 0 {
		a.log.Warn("device publishes seen on a DIFFERENT instanceId than configured — "+
			"check instanceId; those events are not forwarded",
			"configured_instance", a.cfg.InstanceId, "mismatched", mismatch)
	}
	if tm := a.tenantMismatched.Load(); tm > 0 {
		a.log.Warn("device publishes seen for a DIFFERENT tenant than uplink.tenant — "+
			"those events are not spooled or forwarded (a gateway speaks for one tenant)",
			"configured_tenant", a.cfg.Uplink.Tenant, "tenant_mismatched", tm)
	}
}

// spoolDepth reports how many events are buffered (captured but not yet forwarded). For
// a WorkQueue stream that is the message count. Best-effort: a read error reports -1.
func (a *Agent) spoolDepth() int64 {
	info, err := a.js.StreamInfo(captureStream)
	if err != nil {
		return -1
	}
	return int64(info.State.Msgs)
}

// startMetrics brings up the loopback Prometheus /metrics + /healthz endpoint, unless it
// is disabled (metricsPort 0 and no test override). A bind failure is returned (fail
// closed).
func (a *Agent) startMetrics() error {
	addr := a.metricsListenAddr()
	if addr == "" {
		return nil // disabled
	}
	a.metrics = newEdgeMetrics(a)
	if err := a.metrics.start(addr, a.healthzHandler); err != nil {
		a.metrics = nil
		return err
	}
	a.log.Info("metrics endpoint listening", "addr", a.metrics.addr)
	return nil
}

// stopMetrics shuts the endpoint down (releasing the port before Run returns).
func (a *Agent) stopMetrics() {
	if a.metrics != nil {
		a.metrics.stop()
	}
}

// metricsListenAddr resolves the endpoint's listen address: a test override wins;
// otherwise 127.0.0.1:<metricsPort>, or "" when disabled (port 0 / nil).
func (a *Agent) metricsListenAddr() string {
	if a.metricsAddrOverride != "" {
		return a.metricsAddrOverride
	}
	if a.cfg.Local.MetricsPort == nil || *a.cfg.Local.MetricsPort == 0 {
		return ""
	}
	return fmt.Sprintf("127.0.0.1:%d", *a.cfg.Local.MetricsPort)
}

// metricsAddr is the bound endpoint address (for logs/tests); "" when disabled.
func (a *Agent) metricsAddr() string {
	if a.metrics == nil {
		return ""
	}
	return a.metrics.addr
}

// healthzHandler is a LIVE liveness probe: 200 only while the agent is ready AND the last
// stream-state sample succeeded, so a post-startup wedged store reads unhealthy within a
// tick rather than staying green forever. Uplink state is deliberately NOT a gate —
// surviving a down uplink is the whole point, so it is a metric, never a readiness fail.
func (a *Agent) healthzHandler(w http.ResponseWriter, _ *http.Request) {
	if a.isReady() && a.healthy.Load() {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("unavailable\n"))
}

// isReady non-blockingly reports whether the ready channel has closed (substrate up).
func (a *Agent) isReady() bool {
	select {
	case <-a.ready:
		return true
	default:
		return false
	}
}

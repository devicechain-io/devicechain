// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package agent is the dc-edge-agent core: it embeds a nats-server MQTT gateway as
// a device-transparent local listener (ADR-006), captures device publishes durably
// to a local JetStream file store, and forwards them to the cloud Instance over a
// paho MQTT uplink — so the cloud's own MQTT-gateway capture (ADR-030) ingests them
// with no platform-side publish path.
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
	"strings"
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

	// fetchBatch / fetchWait pace the drain loop's pull from the durable consumer.
	fetchBatch = 64
	fetchWait  = time.Second

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

	// counters — basic observability so "is my edge working?" is answerable at a glance;
	// also the source of truth behind the Prometheus *_total counters (metrics.go).
	received      atomic.Int64
	forwarded     atomic.Int64
	forwardErrors atomic.Int64
	mismatched    atomic.Int64
	malformed     atomic.Int64
}

// New builds an Agent from validated config (the uplink credential is resolved
// eagerly so a missing projected Secret fails at startup, not on first forward).
func New(cfg config.Configuration, log *slog.Logger) (*Agent, error) {
	up, err := NewUplink(cfg.Uplink, cfg.InstanceId, cfg.AgentId, log)
	if err != nil {
		return nil, err
	}
	return &Agent{cfg: cfg, log: log, uplink: up, ready: make(chan struct{})}, nil
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

	sub, err := a.js.PullSubscribe(
		messaging.DeviceEventsWildcard(a.cfg.InstanceId),
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
	epoch, err := ensureStream(js, a.cfg.InstanceId, a.cfg.Local.StoreDir, a.cfg.Local.SpoolMaxBytes)
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
	}
	if mqttEnabled {
		opts.MQTT = natsserver.MQTTOpts{
			Host: a.cfg.Local.ListenHost,
			Port: a.cfg.Local.ListenPort,
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
func ensureStream(js nats.JetStreamContext, instanceId, storeDir string, maxBytes int64) (string, error) {
	subject := messaging.DeviceEventsWildcard(instanceId)
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

// drain pulls captured events FIFO and forwards each to the cloud, until ctx ends.
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
		msgs, err := sub.Fetch(fetchBatch, nats.MaxWait(fetchWait))
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
		for _, msg := range msgs {
			// Bail a long batch promptly on shutdown rather than working through up to
			// fetchBatch slow forwards first.
			if ctx.Err() != nil {
				return
			}
			a.received.Add(1)
			// A well-formed device-events subject has no '/'. A device that put a '.'
			// inside a golden-MQTT topic level lands here as a subject with a '/' in a
			// token (SubjectToMqttTopic's documented precondition), which would
			// round-trip to a mangled topic. Skip and count rather than forward garbage
			// the cloud will drop anyway — and ack it: it is not a transient fault, so
			// leaving it un-acked would poison the FIFO spool head forever.
			if strings.Contains(msg.Subject, "/") {
				a.malformed.Add(1)
				a.log.Warn("dropping event on malformed subject (dot inside an MQTT topic level?)",
					"subject", msg.Subject)
				a.ackAndCount(msg)
				continue
			}
			if ferr := a.forwardOne(msg); ferr != nil {
				a.forwardErrors.Add(1)
				a.log.Warn("forward failed — leaving event buffered for redelivery",
					"subject", msg.Subject, "err", ferr)
				// Leave this message (and the rest of the batch) un-acked: the uplink is
				// likely down, so stop pushing. The outer loop's Connected() gate pauses
				// us, and WorkQueue redelivers everything un-acked once the uplink is back.
				break
			}
			a.forwarded.Add(1)
			// ACK SITE: ack ONLY after the cloud PUBACK'd (forwardOne returned nil). This
			// is what turns the spool into a real store-and-forward buffer — an event that
			// did not reach the cloud stays in the stream for redelivery.
			a.ackAndCount(msg)
		}
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

// forwardOne maps one captured event's subject back to its golden MQTT topic, makes it
// exactly-once-safe (stampIdempotency, derived from immutable stored-message metadata),
// and publishes it to the cloud. It returns an error when the uplink could not confirm
// the publish — the caller leaves the message un-acked for redelivery.
func (a *Agent) forwardOne(msg *nats.Msg) error {
	meta, err := msg.Metadata()
	if err != nil {
		// A pull-consumer message always has JetStream metadata; without it we cannot
		// derive a replay-stable key, so refuse to forward rather than emit an event
		// the cloud might duplicate.
		return fmt.Errorf("no JetStream metadata (cannot derive idempotency key): %w", err)
	}
	payload, err := stampIdempotency(msg.Data, a.installId, a.streamEpoch, meta.Sequence.Stream, meta.Timestamp)
	if err != nil {
		return fmt.Errorf("stamp idempotency: %w", err)
	}
	topic := messaging.SubjectToMqttTopic(msg.Subject)
	return a.publish(topic, payload)
}

// publish sends the forwarded bytes to the cloud, honouring a test override.
func (a *Agent) publish(topic string, payload []byte) error {
	if a.publishOverride != nil {
		return a.publishOverride(topic, payload)
	}
	return a.uplink.Publish(topic, payload)
}

// watchInstanceMismatch counts device publishes seen on any instanceId other than the
// one we forward. It is a core (non-JetStream) subscription, so it observes the publish
// without competing with the capture stream. This makes an instanceId typo (which
// otherwise looks like a healthy agent forwarding nothing, F9) visible.
func (a *Agent) watchInstanceMismatch() {
	// {instance}.{tenant}.devices.{token}.events across ANY instance/tenant/device.
	_, err := a.nc.Subscribe("*.*.devices.*.events", func(m *nats.Msg) {
		instance, _, _ := strings.Cut(m.Subject, ".")
		if instance != a.cfg.InstanceId {
			a.mismatched.Add(1)
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

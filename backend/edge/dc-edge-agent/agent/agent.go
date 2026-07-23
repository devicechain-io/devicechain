// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package agent is the dc-edge-agent core: it embeds a nats-server MQTT gateway as
// a device-transparent local listener (ADR-006), captures device publishes durably
// to a local JetStream file store, and forwards them to the cloud Instance over a
// paho MQTT uplink — so the cloud's own MQTT-gateway capture (ADR-030) ingests them
// with no platform-side publish path.
//
// SLICE (ADR-068 Tier 1): this is E2, the durable buffer. Over E1's bridge skeleton
// it adds the three things that make it deployable at a site:
//
//   - Store-and-forward: the drain acks a captured event only AFTER the cloud PUBACKs
//     it (drain's E2 ack site). WorkQueue retention keeps an un-acked event in the
//     spool across a WAN outage AND an agent restart during that outage, so nothing
//     published while the uplink is down is lost.
//   - Exactly-once at the cloud: when a device omits them, the forward mints an altId
//     and stamps an occurredTime derived from immutable stored-message metadata
//     (see idempotency.go), so a redelivered event carries a byte-identical dedup key
//     and the cloud's (tenant_id, alt_id, occurred_time) partial unique index folds
//     the duplicate. JSON only; other payloads forward verbatim (at-least-once).
//   - No startup durability hole: a two-phase start (below) brings the capture stream
//     up before the device MQTT listener ever accepts, closing E1's M2 window.
//
// KNOWN E3 SCOPE: the capture stream is UNBOUNDED. A multi-day outage grows the spool
// until the disk fills; there is no MaxBytes/discard policy or spool-depth ceiling yet
// (E3). The periodic status line reports spool depth so the growth is at least visible.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
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

	// statusEvery bounds how often the agent logs a health line (E2 observability;
	// the full metric set is E3).
	statusEvery = 30 * time.Second
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

	// counters — basic E2 observability so "is my edge working?" is answerable
	// without the full E3 metric set.
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
	defer a.srv.Shutdown()

	if err := a.connect(); err != nil {
		return fmt.Errorf("connect in-process client: %w", err)
	}
	defer a.nc.Close()

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

	// The uplink runs its own bounded-backoff reconnect loop for the life of ctx.
	go a.uplink.Run(ctx)

	// A device that publishes on a DIFFERENT instanceId than we forward silently
	// misses the capture stream (F9). Watch the whole device-events space across any
	// instance and count the mismatches so a config typo is visible, not a silent drop.
	a.watchInstanceMismatch()

	go a.logStatus(ctx)

	close(a.ready) // substrate is up; a device publish is now guaranteed captured

	a.log.Info("dc-edge-agent up",
		"instance_id", a.cfg.InstanceId,
		"install_id", a.installId,
		"mqtt_listen", fmt.Sprintf("%s:%d", a.cfg.Local.ListenHost, a.cfg.Local.ListenPort),
		"store_dir", a.cfg.Local.StoreDir,
		"uplink", a.cfg.Uplink.BrokerURL)

	a.drain(ctx, sub)
	return nil
}

// bootstrapStream starts a throwaway embedded server with the device MQTT listener
// DISABLED, creates or adopts the capture stream, then shuts that server down. Its
// only job is to guarantee the stream exists on disk before phase 2 exposes the MQTT
// port (see Run's two-phase rationale).
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
	return ensureStream(js, a.cfg.InstanceId, a.cfg.Local.StoreDir)
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
func ensureStream(js nats.JetStreamContext, instanceId, storeDir string) (string, error) {
	subject := messaging.DeviceEventsWildcard(instanceId)
	cfg := &nats.StreamConfig{
		Name:      captureStream,
		Subjects:  []string{subject},
		Storage:   nats.FileStorage,
		Retention: nats.WorkQueuePolicy,
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
	// Adopt an existing stream but keep its subject list current with our instanceId
	// (a reconfigured agent must not keep forwarding an old namespace).
	if _, err := js.UpdateStream(cfg); err != nil {
		return "", err
	}
	return readStreamEpoch(storeDir)
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
				_ = msg.Ack()
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
			// E2 ACK SITE: ack ONLY after the cloud PUBACK'd (forwardOne returned nil).
			// This is what turns the spool into a real store-and-forward buffer — an
			// event that did not reach the cloud stays in the stream for redelivery.
			_ = msg.Ack()
		}
	}
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

// logStatus emits a periodic health line until ctx ends.
func (a *Agent) logStatus(ctx context.Context) {
	ticker := time.NewTicker(statusEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			mismatch := a.mismatched.Load()
			a.log.Info("edge status",
				"uplink_connected", a.uplink.Connected(),
				"received", a.received.Load(),
				"forwarded", a.forwarded.Load(),
				"forward_errors", a.forwardErrors.Load(),
				"malformed", a.malformed.Load(),
				"instance_mismatched", mismatch,
				"spool_depth", a.spoolDepth())
			if mismatch > 0 {
				a.log.Warn("device publishes seen on a DIFFERENT instanceId than configured — "+
					"check instanceId; those events are not forwarded",
					"configured_instance", a.cfg.InstanceId, "mismatched", mismatch)
			}
		}
	}
}

// spoolDepth reports how many events are buffered (captured but not yet acked). For a
// WorkQueue stream that is the stream's message count. Best-effort: a read error
// reports -1 rather than blocking the status line. The spool is unbounded in E2, so
// this is the signal an operator watches during an outage (bounding is E3).
func (a *Agent) spoolDepth() int64 {
	info, err := a.js.StreamInfo(captureStream)
	if err != nil {
		return -1
	}
	return int64(info.State.Msgs)
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package agent is the dc-edge-agent core: it embeds a nats-server MQTT gateway as
// a device-transparent local listener (ADR-006), captures device publishes durably
// to a local JetStream file store, and forwards them to the cloud Instance over a
// paho MQTT uplink — so the cloud's own MQTT-gateway capture (ADR-030) ingests them
// with no platform-side publish path.
//
// SLICE (ADR-068 Tier 1): this is E1, the bridge skeleton. It builds the durable
// substrate (embedded server + capture stream + durable consumer) and proves the
// device-transparent golden-path bridge and the uplink auth. It does NOT yet buffer
// across a WAN outage: the drain acks every message whether or not the forward
// succeeded (forward-through), so an event published while the uplink is down is
// DROPPED. E2 turns this into a real store-and-forward buffer by acking only after
// the cloud confirms (see the marked ack site in drain), and adds the exactly-once
// altId/occurredTime minting and the WAN-outage drive. E1 is therefore NOT
// deployable at a site — see the module README.
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

	// statusEvery bounds how often the agent logs a health line (E1 observability;
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

	// ready closes once the embedded broker, capture stream, and durable consumer
	// are all up and the drain is about to start — the point after which a device
	// publish is guaranteed to be captured. Tests wait on it; nothing else reads it.
	ready chan struct{}

	// counters — basic E1 observability so "is my edge working?" is answerable
	// without the full E3 metric set. All device-mismatch / silent-drop cases
	// (F9) surface through received-vs-mismatch and the periodic status line.
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

// Run brings the agent up and blocks until ctx is cancelled, then shuts down
// cleanly.
//
// STARTUP-WINDOW CAVEAT (E2 gate): startServer binds and PUBACKs on the MQTT
// listener as soon as it is ready, but ensureStream runs a moment later. A device
// that connects and publishes in that sub-second window is PUBACK'd by the gateway
// with no capture stream yet matching its subject, so the event is NOT captured —
// the "durable before PUBACK" floor does not hold during startup. E1 forwards
// through and drops on any fault anyway, so this only needs documenting here; E2,
// which claims the durability floor, MUST close this window (e.g. bring the stream
// up before the MQTT listener accepts) before it can make that claim.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.startServer(); err != nil {
		return fmt.Errorf("start embedded broker: %w", err)
	}
	defer a.srv.Shutdown()

	if err := a.connectInProcess(); err != nil {
		return fmt.Errorf("connect in-process client: %w", err)
	}
	defer a.nc.Close()

	if err := a.ensureStream(); err != nil {
		return fmt.Errorf("ensure capture stream: %w", err)
	}

	sub, err := a.js.PullSubscribe(
		messaging.DeviceEventsWildcard(a.cfg.InstanceId),
		forwarderDurable,
		nats.BindStream(captureStream),
	)
	if err != nil {
		return fmt.Errorf("bind durable consumer: %w", err)
	}
	// Deliberately NO `defer sub.Unsubscribe()`: the nats.go client deletes a
	// JS consumer it created on Unsubscribe, so unsubscribing on a clean shutdown
	// would delete the durable (and reset its redelivery bookkeeping) every time.
	// nc.Close() below severs the subscription without deleting the durable.

	// The uplink runs its own bounded-backoff reconnect loop for the life of ctx.
	go a.uplink.Run(ctx)

	// A device that publishes on a DIFFERENT instanceId than we forward silently
	// misses the capture stream (F9). Watch the whole device-events space across
	// any instance and count the mismatches so a config typo is visible, not a
	// silent total drop.
	a.watchInstanceMismatch()

	go a.logStatus(ctx)

	close(a.ready) // substrate is up; a device publish is now guaranteed captured

	a.log.Info("dc-edge-agent up",
		"instance_id", a.cfg.InstanceId,
		"mqtt_listen", fmt.Sprintf("%s:%d", a.cfg.Local.ListenHost, a.cfg.Local.ListenPort),
		"store_dir", a.cfg.Local.StoreDir,
		"uplink", a.cfg.Uplink.BrokerURL)

	a.drain(ctx, sub)
	return nil
}

// startServer starts the embedded nats-server. DontListen keeps the plain NATS
// client port UNBOUND (F5) — otherwise anyone on the site LAN could connect with no
// auth and harvest telemetry, forge events onto golden subjects, or delete the
// spool. The MQTT gateway (opts.MQTT.Port) is the ONLY exposed surface; the agent's
// own client reaches JetStream over an in-process connection.
func (a *Agent) startServer() error {
	opts := &natsserver.Options{
		Host:       "127.0.0.1",
		DontListen: true, // no bound client port; in-process only (F5)
		JetStream:  true, // required for the MQTT gateway and the durable spool
		StoreDir:   a.cfg.Local.StoreDir,
		MQTT: natsserver.MQTTOpts{
			Host: a.cfg.Local.ListenHost,
			Port: a.cfg.Local.ListenPort,
		},
	}
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		return err
	}
	// Route the embedded server's own logs (a bind failure on the MQTT port, an
	// unwritable StoreDir) somewhere visible — otherwise a fatal startup fault is
	// invisible behind the generic "not ready within 15s" below.
	srv.ConfigureLogger()
	go srv.Start()
	if !srv.ReadyForConnections(15 * time.Second) {
		srv.Shutdown()
		return fmt.Errorf("embedded broker not ready within 15s (MQTT %s:%d, storeDir %q) — check the server log above for a bind or store error",
			a.cfg.Local.ListenHost, a.cfg.Local.ListenPort, a.cfg.Local.StoreDir)
	}
	a.srv = srv
	return nil
}

// connectInProcess dials the embedded server over its in-process pipe (no TCP), so
// the drain client works even with the client port unbound (F5).
func (a *Agent) connectInProcess() error {
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

// ensureStream creates (or adopts) the local capture stream. It captures the same
// device-events subject the cloud does, so once it exists the embedded MQTT gateway
// writes each device publish here BEFORE it PUBACKs the device (the ADR-030
// durability floor, one hop earlier) — modulo the startup window noted on Run.
// WorkQueue retention makes it a true spool: a message is removed
// when the drain acks it (in E1, right after the forward attempt; in E2, only after
// the cloud confirms), and un-acked messages persist on disk across a restart.
func (a *Agent) ensureStream() error {
	subject := messaging.DeviceEventsWildcard(a.cfg.InstanceId)
	cfg := &nats.StreamConfig{
		Name:      captureStream,
		Subjects:  []string{subject},
		Storage:   nats.FileStorage,
		Retention: nats.WorkQueuePolicy,
	}
	if _, err := a.js.StreamInfo(captureStream); err != nil {
		if !errors.Is(err, nats.ErrStreamNotFound) {
			return err
		}
		if _, err := a.js.AddStream(cfg); err != nil {
			return err
		}
		return nil
	}
	// Adopt an existing stream but keep its subject list current with our
	// instanceId (a reconfigured agent must not keep forwarding an old namespace).
	if _, err := a.js.UpdateStream(cfg); err != nil {
		return err
	}
	return nil
}

// drain pulls captured events FIFO and forwards each to the cloud, until ctx ends.
func (a *Agent) drain(ctx context.Context, sub *nats.Subscription) {
	for {
		if ctx.Err() != nil {
			return
		}
		msgs, err := sub.Fetch(fetchBatch, nats.MaxWait(fetchWait))
		if err != nil {
			// A fetch that finds nothing before MaxWait returns ErrTimeout — that
			// is the idle case, not a fault.
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
			a.received.Add(1)
			// A well-formed device-events subject has no '/'. A device that put a
			// '.' inside a golden-MQTT topic level lands here as a subject with a
			// '/' in a token (SubjectToMqttTopic's documented precondition), which
			// would round-trip to a mangled topic. Skip and count rather than
			// forward garbage the cloud will drop anyway.
			if strings.Contains(msg.Subject, "/") {
				a.malformed.Add(1)
				a.log.Warn("dropping event on malformed subject (dot inside an MQTT topic level?)",
					"subject", msg.Subject)
				_ = msg.Ack()
				continue
			}
			if ferr := a.forward(msg); ferr != nil {
				a.forwardErrors.Add(1)
				a.log.Warn("forward failed — E1 forward-through: dropping event",
					"subject", msg.Subject, "err", ferr)
			} else {
				a.forwarded.Add(1)
			}
			// E1 ACK SITE (forward-through): ack unconditionally, so an event that
			// failed to forward is dropped rather than buffered. E2 changes exactly
			// this line to `if ferr == nil { msg.Ack() }` (else leave un-acked for
			// redelivery) — that one change turns the spool into a real buffer.
			_ = msg.Ack()
		}
	}
}

// forward maps one captured event's subject back to its golden MQTT topic and
// publishes it to the cloud unchanged (E1 forwards the raw bytes verbatim; the
// exactly-once altId/occurredTime augmentation is an E2 concern).
func (a *Agent) forward(msg *nats.Msg) error {
	topic := messaging.SubjectToMqttTopic(msg.Subject)
	return a.uplink.Publish(topic, msg.Data)
}

// watchInstanceMismatch counts device publishes seen on any instanceId other than
// the one we forward. It is a core (non-JetStream) subscription, so it observes the
// publish without competing with the capture stream. This makes an instanceId typo
// (which otherwise looks like a healthy agent forwarding nothing, F9) visible.
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
				"instance_mismatched", mismatch)
			if mismatch > 0 {
				a.log.Warn("device publishes seen on a DIFFERENT instanceId than configured — "+
					"check instanceId; those events are not forwarded",
					"configured_instance", a.cfg.InstanceId, "mismatched", mismatch)
			}
		}
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

// The command DOWNLINK: a cloud command for a device behind this agent reaches the device
// through the agent's existing cloud link, and the device's answer goes back the same way.
//
//	cloud ─ …gateways/{g}/devices/{c}/commands ─▶ uplink ─▶ INBOX (durable) ─▶ ledger claim
//	      ─▶ ChildAdapter (local MQTT) ─▶ child ─▶ RESPONSES stream (durable) ─▶ …/responses ─▶ cloud
//
// Four rules carry the whole design, and each exists because a command is a physical
// actuation rather than a telemetry sample:
//
//  1. DURABLE BEFORE ACKNOWLEDGED. The MQTT message from the cloud is acknowledged only after
//     the command is in the inbox; the child's response is acknowledged (by the local broker)
//     only after it is in the responses stream, and removed from it only after the cloud
//     PUBACKs the forward.
//  2. CLAIM BEFORE PUBLISH. The ledger moves a command accepted → delivering before the local
//     publish and delivering → delivered after it. A command is delivered to a child at most
//     once per token. Late is recoverable; twice is not.
//  3. NOTHING IS EVICTED TO MAKE ROOM. A full inbox refuses (and tells the cloud, UNSENT)
//     rather than dropping an older command, the opposite of the telemetry spool.
//  4. THE TOKEN IS THE ACTUATION, THE NONCE IS THE DISPATCH. A repeat of a token never reaches
//     the child twice; a repeat with a newer nonce only changes which dispatch the eventual
//     answer is addressed to.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/nats-io/nats.go"

	"github.com/devicechain-io/dc-edge-agent/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

const (
	cmdInboxStream      = "edge-command-inbox"
	cmdResponsesStream  = "edge-command-responses"
	cmdLedgerBucket     = "edge-command-ledger"
	cmdInboxDurable     = "edge-cmd-delivery"
	cmdResponsesDurable = "edge-cmd-responses"

	// The agent's own subjects. NOTE they are NOT unreachable from MQTT: the embedded gateway
	// maps topic "$EDGE/CMD/IN/x" onto this subject, so any local client can WRITE here. What
	// stops a forged command or answer is the seal: every message the agent itself publishes
	// carries a header with a per-box secret (command-seal in the store directory) that no MQTT
	// client can set or read, and every reader refuses a message without it
	// (TestLocalClientCannotInjectACommand).
	cmdInboxPrefix     = "$EDGE.CMD.IN."
	cmdSyntheticPrefix = "$EDGE.CMD.OUT."
	hdrSeal            = "Dc-Edge-Seal"
	commandSealFile    = "command-seal"

	// hdrReplay marks a synthetic answer that re-sends an answer already delivered once (the
	// cloud re-dispatched a command the child had already answered).
	hdrReplay = "Dc-Edge-Replay"

	maxCommandEnvelopeBytes = 256 * 1024
	// maxStoredResponseBytes caps the answer kept in the ledger for a re-dispatch replay; a
	// larger answer is still forwarded, it just cannot be replayed.
	maxStoredResponseBytes = 32 * 1024

	cmdFetchBatch = 16
	// cmdLedgerTTL keeps a ledger entry far beyond the longest wait a command can have (a
	// week is the config ceiling), so a late duplicate or answer is still recognised. The
	// entry is the only evidence a command was already actuated; losing it early is how a
	// repeat would act twice.
	cmdLedgerTTL = 14 * 24 * time.Hour
	// cmdStreamMargin keeps a message in the inbox beyond its logical expiry, so the AGENT
	// (which tells the cloud) rather than the server (which says nothing) observes it.
	cmdStreamMargin        = time.Hour
	cmdLocalPublishTimeout = 10 * time.Second
)

// Ledger states. Forward-only except the claim release (delivering → accepted).
const (
	stAccepted   = "accepted"   // durable in the inbox, not yet claimed
	stDelivering = "delivering" // claimed; the local publish is in flight
	stDelivered  = "delivered"  // the local broker took it
	stExpired    = "expired"    // never delivered; an UNSENT answer is owed
	stAnswered   = "answered"   // an answer was forwarded to the cloud
)

// Reach classes the agent reports (the platform's own vocabulary).
const (
	reachUnsent     = "UNSENT"
	reachUnanswered = "UNANSWERED"
)

// ChildAdapter delivers one command envelope to one local device over a local protocol. The
// only adapter in the edge tree today is the embedded MQTT gateway; this is the seam a
// second protocol plugs into. Deliver reports success only when the adapter took
// responsibility for the command.
//
// An error says whether the command MAY have been submitted. A publish whose outcome is
// unknown (a timeout waiting for the PUBACK, a connection lost mid-publish) may have reached
// the device, so retrying it could actuate twice; only an error wrapped with errNotSubmitted
// (nothing was sent) lets the agent release its claim and try again.
type ChildAdapter interface {
	Deliver(ctx context.Context, child string, envelope []byte) error
}

// errNotSubmitted marks an adapter error raised BEFORE anything was sent.
var errNotSubmitted = errors.New("not submitted")

// mqttChildAdapter delivers over the agent's own loopback MQTT client, to the golden command
// topic of the child, so the child is unchanged from a device on the cloud broker.
type mqttChildAdapter struct {
	client mqtt.Client
	topic  func(child string) string
}

func (m *mqttChildAdapter) Deliver(ctx context.Context, child string, envelope []byte) error {
	if err := core.ValidateToken(child); err != nil {
		return fmt.Errorf("%w: not a device token: %v", errNotSubmitted, err)
	}
	if !m.client.IsConnectionOpen() {
		return fmt.Errorf("%w: local MQTT client is not connected", errNotSubmitted)
	}
	tok := m.client.Publish(m.topic(child), 1, false, envelope)
	done := make(chan struct{})
	go func() { tok.Wait(); close(done) }()
	select {
	case <-done:
		return tok.Error()
	case <-time.After(cmdLocalPublishTimeout):
		return errors.New("local publish timed out")
	case <-ctx.Done():
		return ctx.Err() // the publish was started: its outcome is unknown
	}
}

// commandEnvelope is the part of the platform's command envelope the agent reads. The
// envelope is forwarded to the child byte for byte; ExpiresAt is the one additive field.
type commandEnvelope struct {
	Token         string     `json:"token"`
	DeviceToken   string     `json:"deviceToken"`
	DispatchNonce string     `json:"dispatchNonce"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
}

// ledgerEntry is what the agent remembers about one command token.
type ledgerEntry struct {
	Device string `json:"device"`
	// Nonce is the LATEST dispatch the cloud made for this token: the one any answer must quote.
	Nonce string `json:"nonce"`
	State string `json:"state"`
	// Delivered is the nonce of the envelope actually handed to the adapter. A child answer
	// must quote exactly it before the agent will restamp and forward the answer.
	Delivered string `json:"delivered,omitempty"`
	// Deadline is fixed ONCE, when the command is first accepted, and never re-derived: a
	// restart or a re-dispatch cannot give an old command a fresh lifetime.
	Deadline time.Time `json:"deadline"`
	// Response is the answer that was forwarded, when small enough to keep, for replay.
	Response json.RawMessage `json:"response,omitempty"`
}

// commandPlane is the downlink's state, present only when commands.enabled.
type commandPlane struct {
	kv      nats.KeyValue
	adapter ChildAdapter
	inbox   *nats.Subscription
	resp    *nats.Subscription

	maxAge     time.Duration
	retryDelay time.Duration

	received, delivered, expired, duplicates atomic.Int64
	refused                                  atomic.Int64
	respForwarded, respRefused, respDropped  atomic.Int64
	respAcked                                atomic.Int64
	refusalLog                               logLimiter

	// claims holds the tokens THIS process has claimed. A ledger entry in "delivering" that is
	// not in here was claimed by an earlier run that died mid-publish.
	claims sync.Map
}

// newCommandPlane builds the plane's configuration and counters. Everything that needs the
// running server (the ledger, the consumers, the adapter) is attached later by startCommandPlane.
func newCommandPlane(cc config.CommandsConfiguration) *commandPlane {
	return &commandPlane{maxAge: time.Duration(cc.MaxAgeSeconds) * time.Second,
		retryDelay: time.Duration(cc.RetryDelaySeconds) * time.Second}
}

func (a *Agent) commandsOn() bool { return a.cfg.Commands.Enabled }

// cmdResponseSubject is the subject a child's response lands on (the golden response topic).
func (a *Agent) cmdResponseSubject() string {
	return messaging.DeviceScopedSubject(a.cfg.InstanceId, a.cfg.Uplink.Tenant, messaging.SubjectCommandResponses, "*")
}

func (a *Agent) cmdLocalTopic(child string) string {
	return messaging.SubjectToMqttTopic(messaging.DeviceScopedSubject(a.cfg.InstanceId, a.cfg.Uplink.Tenant, messaging.SubjectDeviceCommands, child))
}

// ensureCommandPlane creates (or adopts) the inbox, the responses capture stream and the
// ledger. It runs in phase 1, with the device listener still disabled, for the same reason the
// telemetry spool does: the responses stream is the overlay capture the gateway writes to
// BEFORE it PUBACKs the child, so it must exist before the first child can connect.
func (a *Agent) ensureCommandPlane(js nats.JetStreamContext) error {
	seal, err := loadOrMintToken(a.cfg.Local.StoreDir, commandSealFile, "command seal")
	if err != nil {
		return err
	}
	a.cmdSeal = seal
	cc := a.cfg.Commands
	maxAge := time.Duration(cc.MaxAgeSeconds) * time.Second
	respRetention := 24 * time.Hour
	if 2*maxAge > respRetention {
		respRetention = 2 * maxAge
	}
	streams := []*nats.StreamConfig{
		{
			Name: cmdInboxStream, Subjects: []string{cmdInboxPrefix + "*"},
			Storage: nats.FileStorage, Retention: nats.WorkQueuePolicy,
			MaxBytes: cc.InboxMaxBytes, Discard: nats.DiscardNew,
			MaxAge: maxAge + cmdStreamMargin, Duplicates: 2 * time.Minute,
		},
		{
			// DiscardOld, not New: the gateway PUBACKs the child from its own persistence, so
			// DiscardNew would lose the NEWEST answer after the child was told it was durable
			// (the same reasoning as the telemetry spool). Evictions are counted.
			Name: cmdResponsesStream, Subjects: []string{a.cmdResponseSubject(), cmdSyntheticPrefix + "*"},
			Storage: nats.FileStorage, Retention: nats.WorkQueuePolicy,
			MaxBytes: cc.ResponsesMaxBytes, Discard: nats.DiscardOld,
			MaxAge: respRetention, Duplicates: 2 * time.Minute,
		},
	}
	for _, cfg := range streams {
		if _, err := js.StreamInfo(cfg.Name); err != nil {
			if !errors.Is(err, nats.ErrStreamNotFound) {
				return err
			}
			if _, err := js.AddStream(cfg); err != nil {
				return fmt.Errorf("create %s: %w", cfg.Name, err)
			}
			continue
		}
		if _, err := js.UpdateStream(cfg); err != nil {
			return fmt.Errorf("update %s: %w", cfg.Name, err)
		}
	}
	kvCfg := &nats.KeyValueConfig{Bucket: cmdLedgerBucket, TTL: cmdLedgerTTL, Storage: nats.FileStorage, History: 1}
	if _, err := js.KeyValue(cmdLedgerBucket); err != nil {
		if !errors.Is(err, nats.ErrBucketNotFound) {
			return err
		}
		if _, err := js.CreateKeyValue(kvCfg); err != nil {
			return fmt.Errorf("create ledger: %w", err)
		}
	}
	return nil
}

// commandStoreBytes is the JetStream store the command plane reserves, added to the spool's
// reservation so enabling the downlink cannot starve the embedded server of room.
func (a *Agent) commandStoreBytes() int64 {
	if !a.commandsOn() {
		return 0
	}
	return a.cfg.Commands.InboxMaxBytes + a.cfg.Commands.ResponsesMaxBytes + 64*1024*1024
}

// startCommandPlane binds the consumers, connects the local adapter and starts the two loops.
// It runs in phase 2 after the in-process client is up and the device listener is serving.
func (a *Agent) startCommandPlane(ctx context.Context) error {
	kv, err := a.js.KeyValue(cmdLedgerBucket)
	if err != nil {
		return fmt.Errorf("bind ledger: %w", err)
	}
	cp := a.cmd // built in New so the metrics, which read its counters, can be registered first
	cp.kv = kv

	if a.adapterOverride != nil {
		cp.adapter = a.adapterOverride
	} else {
		client, err := a.connectLocalClient(ctx)
		if err != nil {
			return err
		}
		cp.adapter = &mqttChildAdapter{client: client, topic: a.cmdLocalTopic}
	}

	// No Unsubscribe on the durables, for the reason the telemetry drain has none.
	cp.inbox, err = a.js.PullSubscribe(cmdInboxPrefix+"*", cmdInboxDurable, nats.BindStream(cmdInboxStream),
		nats.MaxDeliver(-1), nats.AckWait(a.cmdAckWait()))
	if err != nil {
		return fmt.Errorf("bind command inbox consumer: %w", err)
	}
	cp.resp, err = a.js.PullSubscribe("", cmdResponsesDurable, nats.BindStream(cmdResponsesStream),
		nats.MaxDeliver(-1), nats.AckWait(a.cmdAckWait()))
	if err != nil {
		return fmt.Errorf("bind command response consumer: %w", err)
	}
	// Seed the eviction baseline so a response evicted while the agent was down is the only
	// loss not counted (the live ratchet counts everything after this point).
	if info, err := a.js.StreamInfo(cmdResponsesStream); err == nil && info.State.FirstSeq > 0 {
		cp.respAcked.Store(int64(info.State.FirstSeq - 1))
	}

	go a.runCommandInbox(ctx)
	go a.runCommandResponses(ctx)
	return nil
}

func (a *Agent) cmdAckWait() time.Duration {
	if a.cmdAckWaitOverride > 0 {
		return a.cmdAckWaitOverride // tests only: a redelivery that does not take a minute
	}
	return 2*time.Duration(a.cfg.Uplink.ConnectTimeoutSeconds)*time.Second + cmdLocalPublishTimeout
}

// localDialHost is where the agent reaches its own listener: the configured bind address when
// it is a specific one, loopback when the listener binds every interface.
func (a *Agent) localDialHost() string {
	switch h := a.cfg.Local.ListenHost; h {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	default:
		return h
	}
}

// connectLocalClient dials the agent's own device listener on loopback, honouring the local
// shared secret. Auto-reconnect is on: this is a loopback link inside one process's care.
func (a *Agent) connectLocalClient(ctx context.Context) (mqtt.Client, error) {
	opts := mqtt.NewClientOptions().
		AddBroker(fmt.Sprintf("tcp://%s:%d", a.localDialHost(), a.cfg.Local.ListenPort)).
		SetClientID("dc-edge-agent-cmd-" + a.installId).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectTimeout(10 * time.Second)
	if a.cfg.Local.Username != "" {
		opts.SetUsername(a.cfg.Local.Username).SetPassword(a.localPassword)
	}
	c := mqtt.NewClient(opts)
	tok := c.Connect()
	if !tok.WaitTimeout(15*time.Second) || tok.Error() != nil {
		err := tok.Error()
		if err == nil {
			err = errors.New("timed out")
		}
		return nil, fmt.Errorf("connect the command adapter to the local listener: %w", err)
	}
	go func() { <-ctx.Done(); c.Disconnect(250) }()
	return c, nil
}

// ---- ledger ---------------------------------------------------------------------------

func (cp *commandPlane) get(token string) (*ledgerEntry, uint64, error) {
	e, err := cp.kv.Get(token)
	if err != nil {
		return nil, 0, err
	}
	var le ledgerEntry
	if err := json.Unmarshal(e.Value(), &le); err != nil {
		return nil, 0, fmt.Errorf("ledger entry %q is unreadable: %w", token, err)
	}
	return &le, e.Revision(), nil
}

func (cp *commandPlane) create(token string, le *ledgerEntry) error {
	b, _ := json.Marshal(le)
	_, err := cp.kv.Create(token, b)
	return err
}

// mutate applies fn to the entry under compare-and-set, retrying a lost race. fn returns false
// to leave the entry unchanged. It returns the entry as it stands afterwards.
func (cp *commandPlane) mutate(token string, fn func(*ledgerEntry) bool) (*ledgerEntry, error) {
	var lastErr error
	for i := 0; i < 8; i++ {
		le, rev, err := cp.get(token)
		if err != nil {
			return nil, err
		}
		if !fn(le) {
			return le, nil
		}
		b, _ := json.Marshal(le)
		if _, err := cp.kv.Update(token, b, rev); err != nil {
			lastErr = err
			continue
		}
		return le, nil
	}
	return nil, fmt.Errorf("ledger update for %q kept losing the race: %w", token, lastErr)
}

// ---- receive --------------------------------------------------------------------------

// acceptCommand is the uplink's callback for one command. It returns true when the MQTT
// message may be acknowledged: the command is durable here, or it is a refusal that no
// redelivery could change.
func (a *Agent) acceptCommand(child string, raw []byte) bool {
	cp := a.cmd
	if cp == nil {
		return false
	}
	cp.received.Add(1)
	if child == a.cfg.Uplink.GatewayToken {
		return a.refuseCommand("gateway_self", child, "", nil)
	}
	if len(raw) > maxCommandEnvelopeBytes {
		return a.refuseCommand("too_large", child, "", nil)
	}
	var env commandEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return a.refuseCommand("bad_envelope", child, "", nil)
	}
	if core.ValidateToken(env.Token) != nil || env.DispatchNonce == "" {
		// Without a token and a nonce the cloud would refuse any answer; there is nothing to say.
		return a.refuseCommand("bad_envelope", child, env.Token, nil)
	}
	if env.DeviceToken != child {
		// The subject is what the broker confined to this gateway; a body naming another device
		// is a platform fault, and the answer (which must quote the nonce) is still owed.
		return a.refuseCommand("device_mismatch", child, env.Token, &env)
	}

	if le, _, err := cp.get(env.Token); err == nil {
		return a.repeatCommand(child, &env, le)
	} else if !errors.Is(err, nats.ErrKeyNotFound) {
		a.log.Warn("command ledger unreadable; leaving the command unacknowledged", "err", err)
		return false
	}

	if err := a.sealedPublish(cmdInboxPrefix+child, raw, env.Token+":"+env.DispatchNonce); err != nil {
		if strings.Contains(err.Error(), "maximum bytes exceeded") {
			// Refused, visibly, and the cloud is told: an older command is never evicted.
			return a.refuseCommand("inbox_full", child, env.Token, &env)
		}
		a.log.Warn("could not store a command; leaving it unacknowledged", "device", child, "err", err)
		return false
	}
	if err := cp.create(env.Token, &ledgerEntry{Device: child, Nonce: env.DispatchNonce, State: stAccepted,
		Deadline: a.commandDeadline(time.Now(), &env)}); err != nil {
		if errors.Is(err, nats.ErrKeyExists) {
			// A concurrent repeat won the race; the inbox now holds a duplicate that delivery drops.
			if le, _, gerr := cp.get(env.Token); gerr == nil {
				return a.repeatCommand(child, &env, le)
			}
		} else {
			// The command IS durable in the inbox; delivery creates the entry if it is missing.
			a.log.Warn("could not record a command in the ledger; delivery will create it", "err", err)
		}
	}
	return true
}

// commandDeadline is the first and only effective deadline of a command: the earlier of the
// envelope own expiresAt and the configured maximum age from when it was accepted.
func (a *Agent) commandDeadline(from time.Time, env *commandEnvelope) time.Time {
	d := from.Add(a.cmd.maxAge)
	if env.ExpiresAt != nil && env.ExpiresAt.Before(d) {
		d = *env.ExpiresAt
	}
	return d
}

// repeatCommand handles a token the ledger already knows. It never reaches the child again.
func (a *Agent) repeatCommand(child string, env *commandEnvelope, le *ledgerEntry) bool {
	cp := a.cmd
	cp.duplicates.Add(1)
	if le.Device != child {
		return a.refuseCommand("token_reused", child, env.Token, nil)
	}
	if le.Nonce == env.DispatchNonce {
		if le.State == stExpired {
			// A redelivery of a refused command: the owed answer may not have been queued.
			return a.emitSynthetic(child, env.Token, env.DispatchNonce, reachUnsent, refusedWhy, "refuse:"+env.Token+":"+env.DispatchNonce)
		}
		return true
	}
	cur, err := cp.mutate(env.Token, func(e *ledgerEntry) bool {
		if e.Nonce == env.DispatchNonce {
			return false
		}
		e.Nonce = env.DispatchNonce
		return true
	})
	if err != nil {
		a.log.Warn("could not record a re-dispatch's nonce; leaving the command unacknowledged", "err", err)
		return false
	}
	if cur.State == stExpired {
		// Refused or expired earlier and never delivered: the new dispatch is refused too.
		return a.emitSynthetic(child, env.Token, env.DispatchNonce, reachUnsent, refusedWhy, "refuse:"+env.Token+":"+env.DispatchNonce)
	}
	if cur.State == stAnswered && len(cur.Response) > 0 {
		// The child already answered the earlier dispatch; the cloud is now waiting on this one.
		if !a.emitReplay(child, env.Token, cur.Response) {
			return false
		}
	}
	return true
}

func (a *Agent) refuseCommand(reason, child, token string, env *commandEnvelope) bool {
	cp := a.cmd
	cp.refused.Add(1)
	if a.metrics != nil {
		a.metrics.cmdRefused.WithLabelValues(reason).Inc()
	}
	if cp.refusalLog.allow("cmd:"+reason+":"+child, refusalLogEvery) {
		a.log.Warn("command refused at the edge agent", "reason", reason, "device", child, "command", token)
	}
	if env != nil && env.DispatchNonce != "" {
		// A TOMBSTONE first: without a ledger entry, a later redelivery of the same command
		// could be accepted and actuate after the cloud was told it was not. Then the owed
		// answer; acknowledge only when both are durable.
		tomb := &ledgerEntry{Device: child, Nonce: env.DispatchNonce, State: stExpired, Deadline: time.Now()}
		if err := cp.create(token, tomb); err != nil && !errors.Is(err, nats.ErrKeyExists) {
			a.log.Warn("could not record a refused command; leaving it unacknowledged", "command", token, "err", err)
			return false
		}
		return a.emitSynthetic(child, token, env.DispatchNonce, reachUnsent, refusedWhy, "refuse:"+token+":"+env.DispatchNonce)
	}
	return true
}

// refusedWhy is what the cloud reads on a command the agent would not take or could not deliver.
const refusedWhy = "the edge agent did not accept the command and never delivered it to the device"

// sealedPublish publishes a message the agent itself is making, stamped with the seal.
func (a *Agent) sealedPublish(subject string, data []byte, msgID string) error {
	m := nats.NewMsg(subject)
	m.Data = data
	m.Header.Set(hdrSeal, a.cmdSeal)
	if msgID != "" {
		m.Header.Set(nats.MsgIdHdr, msgID)
	}
	_, err := a.js.PublishMsg(m)
	return err
}

// sealed reports whether a message carries this box seal, i.e. was published by this agent and
// not by a local MQTT client that happens to know the subject.
func (a *Agent) sealed(m *nats.Msg) bool {
	return a.cmdSeal != "" && subtle.ConstantTimeCompare([]byte(m.Header.Get(hdrSeal)), []byte(a.cmdSeal)) == 1
}

// ---- synthetic answers ------------------------------------------------------------------

type edgeResponse struct {
	CommandToken  string `json:"commandToken"`
	Success       bool   `json:"success"`
	Error         string `json:"error,omitempty"`
	Reach         string `json:"reach,omitempty"`
	DispatchNonce string `json:"dispatchNonce"`
}

// emitSynthetic puts an answer the AGENT is making (the command could not be delivered) into
// the responses stream, where the drain forwards it like any child answer. msgID makes a
// repeat of the same emission collapse inside the server's duplicate window.
func (a *Agent) emitSynthetic(child, token, nonce, reach, why, msgID string) bool {
	body, _ := json.Marshal(edgeResponse{CommandToken: token, Error: why, Reach: reach, DispatchNonce: nonce})
	if err := a.sealedPublish(cmdSyntheticPrefix+child, body, "syn:"+msgID); err != nil {
		a.log.Warn("could not queue an answer for the cloud", "device", child, "command", token, "err", err)
		return false
	}
	return true
}

func (a *Agent) emitReplay(child, token string, stored json.RawMessage) bool {
	m := nats.NewMsg(cmdSyntheticPrefix + child)
	m.Data = []byte(stored)
	m.Header.Set(hdrSeal, a.cmdSeal)
	m.Header.Set(hdrReplay, "1")
	m.Header.Set(nats.MsgIdHdr, fmt.Sprintf("replay:%s:%d", token, time.Now().UnixNano()))
	if _, err := a.js.PublishMsg(m); err != nil {
		a.log.Warn("could not queue a replayed answer for the cloud", "device", child, "command", token, "err", err)
		return false
	}
	return true
}

// ---- local delivery -------------------------------------------------------------------

func (a *Agent) runCommandInbox(ctx context.Context) {
	for ctx.Err() == nil {
		msgs, err := a.cmd.inbox.Fetch(cmdFetchBatch, nats.MaxWait(fetchWait))
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				continue
			}
			a.log.Warn("command inbox fetch failed", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		for _, m := range msgs {
			if ctx.Err() != nil {
				return
			}
			a.deliverCommand(ctx, m)
		}
	}
}

// deliverCommand moves one inbox message through expiry, claim, local publish and ack.
func (a *Agent) deliverCommand(ctx context.Context, m *nats.Msg) {
	cp := a.cmd
	child := strings.TrimPrefix(m.Subject, cmdInboxPrefix)
	if !a.sealed(m) {
		// Written by something other than this agent (a local MQTT client can reach this subject).
		cp.refused.Add(1)
		if a.metrics != nil {
			a.metrics.cmdRefused.WithLabelValues("forged").Inc()
		}
		if cp.refusalLog.allow("forged:inbox", refusalLogEvery) {
			a.log.Warn("dropping an inbox message this agent did not write: a local client published to the agent's command subject")
		}
		_ = m.AckSync()
		return
	}
	var env commandEnvelope
	if err := json.Unmarshal(m.Data, &env); err != nil || core.ValidateToken(env.Token) != nil || core.ValidateToken(child) != nil {
		// Unreachable: acceptCommand validated it. Dropped (and counted) rather than left to
		// poison the head of the queue.
		a.log.Error("dropping an unreadable command from the inbox", "subject", m.Subject)
		cp.refused.Add(1)
		_ = m.AckSync()
		return
	}
	meta, err := m.Metadata()
	if err != nil {
		_ = m.NakWithDelay(cp.retryDelay)
		return
	}
	received := meta.Timestamp

	le, _, err := cp.get(env.Token)
	if errors.Is(err, nats.ErrKeyNotFound) {
		// The command was stored but the agent died before recording it.
		fresh := &ledgerEntry{Device: child, Nonce: env.DispatchNonce, State: stAccepted,
			Deadline: a.commandDeadline(received, &env)}
		if cerr := cp.create(env.Token, fresh); cerr != nil && !errors.Is(cerr, nats.ErrKeyExists) {
			_ = m.NakWithDelay(cp.retryDelay)
			return
		}
		le, _, err = cp.get(env.Token)
	}
	if err != nil {
		a.log.Warn("command ledger unreadable; will retry", "err", err)
		_ = m.NakWithDelay(cp.retryDelay)
		return
	}

	switch le.State {
	case stDelivered, stAnswered:
		cp.duplicates.Add(1)
		_ = m.AckSync()
		return
	case stDelivering:
		if _, mine := cp.claims.Load(env.Token); mine {
			_ = m.NakWithDelay(cp.retryDelay) // our own publish is still in flight
			return
		}
		// The agent died between the claim and the end of the local publish. Whether the child
		// has the command is unknowable, so it is NOT delivered again; the cloud is told it may
		// have acted (UNANSWERED), which asks more of an operator and offers no blind retry.
		if !a.emitSynthetic(child, env.Token, le.Nonce, reachUnanswered,
			"the edge agent restarted while delivering the command; whether the device received it is unknown", "lost:"+env.Token) {
			_ = m.NakWithDelay(cp.retryDelay)
			return
		}
		if _, err := cp.mutate(env.Token, func(e *ledgerEntry) bool {
			if e.State != stDelivering {
				return false
			}
			e.State = stDelivered
			return true
		}); err != nil {
			_ = m.NakWithDelay(cp.retryDelay)
			return
		}
		_ = m.AckSync()
		return
	case stExpired:
		a.expireCommand(m, child, env.Token, le.Nonce)
		return
	}

	// stAccepted
	if !time.Now().Before(le.Deadline) {
		if _, err := cp.mutate(env.Token, func(e *ledgerEntry) bool {
			if e.State != stAccepted {
				return false
			}
			e.State = stExpired
			return true
		}); err != nil {
			_ = m.NakWithDelay(cp.retryDelay)
			return
		}
		a.expireCommand(m, child, env.Token, le.Nonce)
		return
	}
	claimed, err := cp.mutate(env.Token, func(e *ledgerEntry) bool {
		if e.State != stAccepted {
			return false
		}
		e.State = stDelivering
		e.Delivered = env.DispatchNonce
		return true
	})
	if err != nil || claimed.State != stDelivering {
		_ = m.NakWithDelay(cp.retryDelay)
		return
	}
	cp.claims.Store(env.Token, true)
	defer cp.claims.Delete(env.Token)
	if err := cp.adapter.Deliver(ctx, child, m.Data); err != nil && !errors.Is(err, errNotSubmitted) {
		// The publish was started and its outcome is unknown, so the command may be on its way
		// to the device: it is NOT sent again. Treated as delivered; the device answer, or the
		// cloud own timeout, settles it.
		a.log.Warn("local delivery outcome unknown; not retrying, because the device may have the command", "device", child, "command", env.Token, "err", err)
	} else if err != nil {
		a.log.Warn("local delivery failed before anything was sent; the command stays queued", "device", child, "command", env.Token, "err", err)
		if _, rerr := cp.mutate(env.Token, func(e *ledgerEntry) bool {
			if e.State != stDelivering {
				return false
			}
			e.State = stAccepted
			e.Delivered = ""
			return true
		}); rerr != nil {
			a.log.Error("could not release the claim on a command; it will be reported UNANSWERED on retry", "command", env.Token, "err", rerr)
		}
		_ = m.NakWithDelay(cp.retryDelay)
		return
	}
	if _, err := cp.mutate(env.Token, func(e *ledgerEntry) bool {
		if e.State != stDelivering {
			return false
		}
		e.State = stDelivered
		return true
	}); err != nil {
		a.log.Warn("delivered a command but could not record it; it will be reported UNANSWERED on retry", "command", env.Token, "err", err)
		_ = m.NakWithDelay(cp.retryDelay)
		return
	}
	cp.delivered.Add(1)
	_ = m.AckSync()
}

// expireCommand tells the cloud a command never reached its device, then settles the message.
func (a *Agent) expireCommand(m *nats.Msg, child, token, nonce string) {
	cp := a.cmd
	if !a.emitSynthetic(child, token, nonce, reachUnsent,
		"the command expired at the edge agent before it could be delivered to the device", "expired:"+token) {
		_ = m.NakWithDelay(cp.retryDelay)
		return
	}
	cp.expired.Add(1)
	_ = m.AckSync()
}

// ---- answers ----------------------------------------------------------------------------

func (a *Agent) runCommandResponses(ctx context.Context) {
	for ctx.Err() == nil {
		if !a.uplink.Connected() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(disconnectedPoll):
			}
			continue
		}
		msgs, err := a.cmd.resp.Fetch(cmdFetchBatch, nats.MaxWait(fetchWait))
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				continue
			}
			a.log.Warn("command response fetch failed", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		for _, m := range msgs {
			if ctx.Err() != nil {
				return
			}
			a.forwardResponse(m)
		}
	}
}

// ackResponse settles a response message and counts it as removed by ack (for the eviction ratchet).
func (a *Agent) ackResponse(m *nats.Msg) {
	if err := m.AckSync(); err != nil {
		a.log.Warn("response ack not confirmed; it will be redelivered", "err", err)
		return
	}
	a.cmd.respAcked.Add(1)
}

func (a *Agent) refuseResponse(m *nats.Msg, reason, child, token string) {
	cp := a.cmd
	cp.respRefused.Add(1)
	if a.metrics != nil {
		a.metrics.cmdRespRefused.WithLabelValues(reason).Inc()
	}
	if cp.refusalLog.allow("resp:"+reason+":"+child, refusalLogEvery) {
		a.log.Warn("command response refused at the edge agent", "reason", reason, "device", child, "command", token)
	}
	a.ackResponse(m)
}

// forwardResponse verifies one answer against the ledger and forwards it to the cloud.
func (a *Agent) forwardResponse(m *nats.Msg) {
	cp := a.cmd
	synthetic := strings.HasPrefix(m.Subject, cmdSyntheticPrefix)
	var child string
	if synthetic {
		child = strings.TrimPrefix(m.Subject, cmdSyntheticPrefix)
	} else {
		var ok bool
		child, ok = messaging.ParseDeviceFromScopedSubject(m.Subject, messaging.SubjectCommandResponses)
		if !ok || strings.Contains(m.Subject, "/") {
			a.refuseResponse(m, "bad_subject", "", "")
			return
		}
	}
	if core.ValidateToken(child) != nil {
		a.refuseResponse(m, "bad_subject", child, "")
		return
	}
	if synthetic && !a.sealed(m) {
		a.refuseResponse(m, "forged", child, "")
		return
	}
	var body map[string]json.RawMessage
	var token string
	if err := json.Unmarshal(m.Data, &body); err != nil || body == nil {
		a.refuseResponse(m, "undecodable", child, "")
		return
	}
	if err := json.Unmarshal(body["commandToken"], &token); err != nil || core.ValidateToken(token) != nil {
		a.refuseResponse(m, "undecodable", child, "")
		return
	}
	le, _, err := cp.get(token)
	if errors.Is(err, nats.ErrKeyNotFound) {
		a.refuseResponse(m, "unknown_command", child, token)
		return
	}
	if err != nil {
		a.log.Warn("command ledger unreadable; will retry the response", "err", err)
		return // left unacked: redelivered after AckWait
	}
	if le.Device != child {
		// A device answering for a command that is not its own. The cloud refuses this too.
		a.refuseResponse(m, "not_owner", child, token)
		return
	}
	replay := m.Header.Get(hdrReplay) != ""
	if le.State == stAnswered && !replay {
		cp.duplicates.Add(1)
		a.refuseResponse(m, "duplicate", child, token)
		return
	}
	if !synthetic && le.State != stDelivering && le.State != stDelivered {
		// The agent has not handed this command to any child; an answer is a fabrication.
		a.refuseResponse(m, "not_dispatched", child, token)
		return
	}

	if !synthetic {
		// The child must quote the dispatch the agent actually handed it. Restamping an answer
		// that quotes anything else would launder a stale or invented nonce into a valid one,
		// which is the very check the platform makes on a response.
		var echoed string
		if json.Unmarshal(body["dispatchNonce"], &echoed) != nil || echoed == "" || echoed != le.Delivered {
			a.refuseResponse(m, "nonce_mismatch", child, token)
			return
		}
	}

	// The only member the agent rewrites: the nonce, with the value the platform issued for the
	// dispatch it is now waiting on. A stale value is only ever REFUSED by the platform, never
	// mistaken for another dispatch's answer.
	stamped := le.Nonce
	nonce, _ := json.Marshal(stamped)
	body["dispatchNonce"] = nonce
	out, err := json.Marshal(body)
	if err != nil {
		a.refuseResponse(m, "undecodable", child, token)
		return
	}
	topic, err := a.uplink.ResponseTopic(child)
	if err != nil {
		a.refuseResponse(m, "bad_subject", child, token)
		return
	}
	wait, err := a.begin(topic, out)
	if err == nil {
		err = wait()
	}
	if err != nil {
		a.forwardErrors.Add(1)
		a.log.Warn("forwarding a command response failed; it stays buffered", "device", child, "command", token, "err", err)
		return
	}
	cp.respForwarded.Add(1)
	var replayBody json.RawMessage
	if cur, merr := cp.mutate(token, func(e *ledgerEntry) bool {
		if e.State == stAnswered && !replay {
			return false
		}
		e.State = stAnswered
		if len(out) <= maxStoredResponseBytes {
			e.Response = out
		}
		return true
	}); merr == nil {
		// A re-dispatch that arrived while this answer was in flight is waiting on a different
		// nonce than the one just sent; it is answered again from the stored answer.
		if cur.Nonce != stamped && len(cur.Response) > 0 {
			replayBody = cur.Response
		}
	} else {
		// Forwarded but unrecorded: leave it unacked, and let the redelivery forward it again
		// (the cloud settles a command once and refuses a repeat).
		a.log.Warn("forwarded a command response but could not record it", "command", token, "err", merr)
		return
	}
	if replayBody != nil && !a.emitReplay(child, token, replayBody) {
		return
	}
	a.ackResponse(m)
}

// sampleCommandPlane runs on the sample tick: it ratchets the eviction count of the responses
// stream and sets the inbox-depth gauge.
func (a *Agent) sampleCommandPlane() {
	cp := a.cmd
	if cp == nil {
		return
	}
	if info, err := a.js.StreamInfo(cmdResponsesStream); err == nil && info.State.FirstSeq > 0 {
		drops := int64(info.State.FirstSeq-1) - cp.respAcked.Load()
		for {
			prev := cp.respDropped.Load()
			if drops <= prev {
				break
			}
			if cp.respDropped.CompareAndSwap(prev, drops) {
				a.log.Warn("command response overflow: the oldest unforwarded answers were dropped to stay within commands.responsesMaxBytes",
					"newly_dropped", drops-prev, "dropped_total", drops)
				break
			}
		}
	}
	if a.metrics != nil {
		if info, err := a.js.StreamInfo(cmdInboxStream); err == nil {
			a.metrics.cmdInboxDepth.Set(float64(info.State.Msgs))
		}
	}
}

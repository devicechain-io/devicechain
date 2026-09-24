// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const (
	TYPE_MQTT           = "mqtt"
	DECODE_WORKER_COUNT = 5
	// DECODE_CHANNEL_DEPTH bounds the in-flight decode queue. On the capture-stream
	// source this is also the maximum number of messages that can be UNACKED and
	// therefore redelivered after a crash — bounded loss became bounded REDELIVERY.
	DECODE_CHANNEL_DEPTH = 100
	// subscribeTimeout bounds the wait for a SUBACK, on the first connection and on
	// every reconnect. Generous, because the only thing that expires it is a broker that
	// has accepted the connection and then stopped answering — the point of the bound is
	// that the source fails loudly rather than hanging for the life of the pod.
	subscribeTimeout = 30 * time.Second
)

// GatewayTopic is the MQTT topic filter the NATS-gateway event source subscribes
// to for an instance: the device EVENTS shape, and nothing else.
//
// NOTE (ADR-030 amendment): the gateway no longer subscribes over MQTT at all —
// it consumes the capture stream (GatewayJetStreamSource) — so nothing in
// production calls this any more. It is retained deliberately, as the topic half
// of the subject/topic mapping that the broker-interop pin (slice I6) asserts:
// the property that a device's MQTT publish and our JetStream subject filter
// describe the same messages is now load-bearing for ingest and is exactly what a
// nats-server upgrade could break silently. Delete it only together with that pin.
//
// It matches the grant natsauth mints for a device (both are built from
// messaging's one declaration), which is the property that matters: the gateway
// ingests exactly what a device can be authorized to publish, so no subject the
// platform publishes internally can arrive here as telemetry.
//
// It lives in this package, rather than being assembled at the one call site in
// main.go, so the string that actually ships is the string under test — main.go
// is package main and its wiring is not reachable from a test.
func GatewayTopic(instanceId string) string {
	return messaging.SubjectToMqttTopic(messaging.DeviceEventsWildcard(instanceId))
}

type MqttEventSource struct {
	Id         string
	BrokerHost string
	BrokerPort int
	Topic      string

	// tlsConfig, when non-nil, dials the broker over TLS (ssl://) and verifies its
	// certificate — the client side of the ADR-025 TLS'd MQTT gateway. nil leaves
	// the connection plaintext (tcp://).
	tlsConfig *tls.Config

	// username/password present the shared service credential when broker auth is
	// enabled (ADR-025). event-sources connects to the MQTT gateway as a trusted
	// service, not a device: presenting the static service login authenticates it
	// statically (exempt from the device callout). Empty = no auth (pre-cutover).
	username string
	password string

	Client  mqtt.Client
	Decoder Decoder

	messages  chan rawMessage
	workers   []*DecodeWorker
	lifecycle core.LifecycleManager
	received  func(string, []byte)
	decoded   func(string, string, *model.UnresolvedEvent, interface{}, uint64) error
	failed    func(string, string, []byte, error) error
	// allow meters an inbound message against its tenant's ingest rate limit
	// before it is queued for decode; a false return sheds the message. nil
	// disables metering (used by tests that exercise decoding in isolation).
	allow RateGate

	// fail ends the process for a cause found after startup: a broker that refuses
	// this source's subscription on a reconnect. It must not block its caller, which is
	// one of paho's callback goroutines. Required — see NewMqttEventSource.
	fail func(error)
	// ready carries the FIRST connection's subscribe result to ExecuteStart. Buffered,
	// so onConnect never blocks on a Start that has already given up waiting.
	ready chan error
	// connections counts OnConnect calls. The first connection's result goes to ready;
	// a later one uses the count to tell whether a newer connection has superseded it
	// while its SUBSCRIBE was outstanding. See classifyResubscribe.
	connections atomic.Uint64
	// stopping is set before this source disconnects on purpose, so a re-subscribe that
	// errors because WE closed the connection is dropped without a word. Belt and braces:
	// paho's Disconnect fails an outstanding SUBSCRIBE with a token error, which
	// classifyResubscribe already retries, and a fail that races Stop lands in a
	// lifecycle that is already stopping and is ignored there. What the flag buys is that
	// onConnect does not depend on either of those staying true.
	stopping atomic.Bool
}

// errNoFailHook is what NewMqttEventSource returns for a nil fail. A source with no way
// to end the process would have to treat a refused re-subscribe as a log line, which is
// the silent connected-and-idle state this hook exists to rule out.
var errNoFailHook = errors.New("an MQTT event source needs a way to end the process " +
	"when a reconnect's subscription is refused; none was given")

// Create a new MQTT event source based on the given configuration. tlsConfig is
// non-nil when the broker terminates TLS on the MQTT gateway (ADR-025), in which
// case the client dials ssl:// and verifies the server; nil dials plaintext.
// username/password present the shared service credential when broker auth is on
// (empty = anonymous). fail ends the process when a reconnect's subscription is
// refused (see onConnect); it must return promptly, and nil is refused.
func NewMqttEventSource(id string, config map[string]string, tlsConfig *tls.Config, username, password string, decoder Decoder,
	received func(string, []byte),
	decoded func(string, string, *model.UnresolvedEvent, interface{}, uint64) error,
	failed func(string, string, []byte, error) error,
	allow RateGate, fail func(error)) (*MqttEventSource, error) {
	if fail == nil {
		return nil, errNoFailHook
	}
	port, err := strconv.Atoi(config["port"])
	if err != nil {
		return nil, err
	}

	es := &MqttEventSource{
		Id:         id,
		BrokerHost: config["host"],
		BrokerPort: port,
		Topic:      config["topic"],
		tlsConfig:  tlsConfig,
		username:   username,
		password:   password,
		Decoder:    decoder,
		fail:       fail,
		ready:      make(chan error, 1),
	}

	es.lifecycle = core.NewLifecycleManager("mqtt-event-source", es, core.NewNoOpLifecycleCallbacks())
	es.received = received
	es.decoded = decoded
	es.failed = failed
	es.allow = allow
	return es, nil
}

// tenantFromTopic derives the tenant from an inbound MQTT topic of the form
// "{instanceId}/{tenant}/..." (ADR-006/ADR-048): the tenant is the second of at
// least three non-empty slash-separated segments. Only the second segment is read,
// so this is agnostic to the (instance-id) prefix. Parsed directly (no whole-string
// rewrite) since this runs per inbound message.
func tenantFromTopic(topic string) (string, bool) {
	parts := strings.SplitN(topic, "/", 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	return parts[1], true
}

// commandPlaneSuffixes are the device-plane topics that carry COMMAND traffic
// rather than device events: the platform's downlink to devices, and a device's
// response to a command. Both are consumed by command-delivery over its own
// JetStream durable.
//
// The gateway source no longer needs this: its subscription is now the device
// EVENTS shape (messaging.DeviceEventsWildcard), which command topics cannot
// match. It is kept for the EXTERNAL-broker source, whose topic is operator-
// configured and defaults to the permissive "+/#" — there, this service genuinely
// has not been told the topic shape, so a denylist of what is definitely not an
// event is the only exclusion available.
//
// Treat it as a second line of defence, not the mechanism. A denylist can only
// exclude what it has been told to name: this one named the two command suffixes
// and stayed silent about the other thirteen internal ones, which is how the
// gateway ended up re-ingesting its own traffic. The subscription is what closes
// that class; this only narrows the one case where a narrow subscription is not
// available.
//
// The names come from core, not from literals here: command-delivery owns these
// subjects, and a copy of the strings in this file would let a rename there turn
// this recognition off silently, with every test still passing.
var commandPlaneSuffixes = map[string]struct{}{
	messaging.SubjectDeviceCommands:   {},
	messaging.SubjectCommandResponses: {},
}

// deviceFromTopic returns the device token an events topic addresses, for the
// documented shape "{instanceId}/{tenant}/devices/{token}/events". The broker grant
// confines a device to its OWN such topic, so this token is authorized rather than
// merely asserted — which is what makes it worth checking the payload against.
//
// Returns "" for any other topic shape, which means "the transport carried no device
// identity", not "no check needed" — the caller treats it as nothing to compare.
// The segment literals come from core (messaging.SegmentDevices / SegmentEvents),
// which is the same declaration the broker grant and the gateway subscription are
// built from — so this parser cannot recognise a shape the subscription no longer
// delivers, or miss one it does.
func deviceFromTopic(topic string) string {
	parts := strings.Split(topic, "/")
	if len(parts) != messaging.DeviceEventsSegmentCount ||
		parts[messaging.DeviceEventsDevicesIndex] != messaging.SegmentDevices ||
		parts[messaging.DeviceEventsEventsIndex] != messaging.SegmentEvents {
		return ""
	}
	return parts[messaging.DeviceEventsTokenIndex]
}

// isCommandPlane reports whether a topic addresses command traffic rather than a
// device event, by matching the segment immediately after {instanceId}/{tenant}.
func isCommandPlane(topic string) bool {
	parts := strings.Split(topic, "/")
	if len(parts) < 3 {
		return false
	}
	_, found := commandPlaneSuffixes[parts[2]]
	return found
}

// Called when message is received from topic.
func (es *MqttEventSource) onMessage(client mqtt.Client, msg mqtt.Message) {
	// Record the arrival by topic and SIZE, never by content. Nothing here has
	// decoded the body yet, so this line cannot tell what the body holds — under
	// deviceAuthMode=required an inbound event normally carries the device's
	// credential, and an operator raising the log level to diagnose an ingest
	// problem is not choosing to copy that into a log pipeline. Topic and byte
	// count are what distinguishes "nothing is arriving" from "messages are
	// arriving and being dropped further down", which is what this line is read
	// for; every drop below reports its own reason.
	if log.Debug().Enabled() {
		log.Debug().Str("topic", msg.Topic()).Int("bytes", len(msg.Payload())).
			Msg("Received MQTT message")
	}
	// Derive the per-message tenant from the topic up front; a message whose topic
	// carries no tenant cannot be published to a tenant-scoped subject, so it is
	// dropped (fail-closed) rather than decoded and published unscoped.
	tenant, ok := tenantFromTopic(msg.Topic())
	if !ok {
		log.Warn().Msg(fmt.Sprintf("Dropping message with no parseable tenant in MQTT topic %q", msg.Topic()))
		return
	}
	// Validate the tenant token grammar before it is used as a rate-limiter key
	// (fail-closed, mirroring the HTTP path): tenantFromTopic only checks the
	// segment is non-empty, so without this an arbitrary or oversized topic
	// segment could seed an unbounded set of limiter buckets.
	if err := core.ValidateToken(tenant); err != nil {
		log.Warn().Msg(fmt.Sprintf("Dropping message with invalid tenant %q in MQTT topic: %v", tenant, err))
		return
	}

	// Command traffic shares this topic tree but is not telemetry. Drop it BEFORE
	// the rate limiter, because metering it is the actual harm: the gate below
	// spends a tenant's ingest budget, so every command the platform sent and every
	// response a device returned was counted against that tenant's telemetry
	// ceiling — a busy command session could rate-limit the tenant's real events.
	// It also counted as an inbound device event in the RED metrics, and then failed
	// to decode (a command envelope is not an event), so the failure counter rose
	// too. None of that is a device's doing and none of it is an event.
	if isCommandPlane(msg.Topic()) {
		return
	}

	// Meter against the tenant's ingest ceiling before enqueue so a tenant over
	// its limit sheds here, spending no decode CPU. MQTT has no per-message
	// acknowledgement back to the publisher, so an over-limit message is simply
	// dropped (the HTTP path returns 429 instead).
	if es.allow != nil && !es.allow(es.Id, tenant, time.Time{}, false, OriginAuthenticated) {
		return
	}

	// Count the arrival only once it clears the gate, so a shed message is not
	// counted as both inbound and rate-limited (matches the HTTP path, which
	// accounts after the gate).
	es.received(es.Id, msg.Payload())
	es.messages <- rawMessage{
		tenant:  tenant,
		payload: msg.Payload(),
		device:  deviceFromTopic(msg.Topic()),
	}
}

// onConnect subscribes on EVERY connection, the first included. paho runs it on a
// goroutine of its own each time a connection comes up.
//
// 🔴 SUBSCRIBING ONCE, IN START, WAS THE DEFECT. paho reconnects on its own after a
// broker restart or a network drop, and it does so with a clean session (paho's default,
// which this source keeps), so the broker holds no subscription for the new connection.
// paho's ResumeSubs does not help: it re-sends only SUBSCRIBEs still in flight, not ones
// already acknowledged. A source that subscribed once was therefore connected, reported
// nothing wrong, and ingested nothing from the first reconnect until the pod restarted.
//
// 🔴 CONFIRMED, not merely awaited, on every connection. paho reports a refused
// subscription (SUBACK 0x80) as a successful token with a nil Error(), so a wait that
// only reads the token logs "subscribed" over a source that will never receive anything;
// see SubscribeMqttConfirmed.
//
// What each outcome does:
//   - The FIRST connection's result goes to ExecuteStart, which fails the start on any
//     error — the behaviour this source has always had.
//   - A later connection's error is classified by classifyResubscribe. A refusal, or a
//     SUBACK that never came on a connection nothing has replaced, ends the process
//     through fail. A connection that went away under the SUBSCRIBE is left to paho,
//     which reconnects and calls this again.
func (es *MqttEventSource) onConnect(client mqtt.Client) {
	generation := es.connections.Add(1)
	err := messaging.SubscribeMqttConfirmed(client, es.Topic, 1, es.onMessage, subscribeTimeout)
	if generation == 1 {
		es.ready <- err
		return
	}
	if err == nil {
		log.Info().Str("source", es.Id).Str("topic", es.Topic).
			Msg("MQTT event source reconnected and re-subscribed.")
		return
	}
	if es.stopping.Load() {
		// Stop disconnected us under the SUBSCRIBE. Not the broker's doing, and the
		// process is already on its way down.
		return
	}
	superseded := es.connections.Load() != generation
	if classifyResubscribe(err, superseded) == resubscribeRetry {
		log.Warn().Err(err).Str("source", es.Id).Str("topic", es.Topic).
			Msg("MQTT event source lost its connection while re-subscribing; the client " +
				"reconnects on its own and subscribes again on the next connection.")
		return
	}
	es.fail(fmt.Errorf("MQTT event source %q reconnected to %s:%d but could not re-subscribe, "+
		"so it would stay connected and ingest nothing: %w", es.Id, es.BrokerHost, es.BrokerPort, err))
}

// resubscribeAction is what onConnect does with a failed re-subscribe.
type resubscribeAction int

const (
	// resubscribeRetry leaves it to paho, which reconnects and fires OnConnect again.
	resubscribeRetry resubscribeAction = iota
	// resubscribeFatal ends the process.
	resubscribeFatal
)

// classifyResubscribe decides a failed re-subscribe from the ERROR and from whether a
// newer connection has come up since the SUBSCRIBE was sent (superseded) — never from
// the client's current connection status.
//
// 🔴 THE STATUS IS A RACE, WHICH IS WHY IT IS NOT CONSULTED. When a connection drops,
// paho fails the outstanding SUBSCRIBE and starts reconnecting, and its first reconnect
// attempt does not wait. So by the time this goroutine wakes with its error, paho may
// already be connected again, and "is the connection open?" answers yes for a
// connection that is not the one the SUBSCRIBE went out on. Read that way, a benign drop
// would end the process — and with more than one replica sharing a client id on the
// same broker, where each replica's connect evicts the other's, it would do so on every
// eviction.
//
//   - A REFUSAL is fatal whatever else has happened. It is the broker's answer about
//     this credential and this filter, and a newer connection will be given the same one.
//     It is the same condition that fails the source at startup, so it gets the same
//     result: the process ends, the pod restarts, the next start meets the same refusal
//     and fails with the reason in its log — a visible crash loop rather than a pod that
//     reports Ready and ingests nothing. Logging and retrying instead would need a retry
//     loop paho does not provide, with no health signal to report it.
//   - A MISSING SUBACK is fatal only if nothing has replaced the connection it was
//     asked on: that broker took the connection and stopped answering, which is the
//     condition subscribeTimeout exists to turn into a failure. If a newer connection
//     has come up, that connection is subscribing for itself. That second case is
//     DEFENSIVE, not a path paho v1.5.1 takes: on connection loss its internalConnLost
//     runs cleanUpSubscribe before reconnecting, so a SUBSCRIBE outstanding on a lost
//     connection ends as a token error (the default case below), not as a timeout. It
//     is kept so that a paho that stops doing that retries rather than ending the
//     process over a connection already replaced; only the classify table exercises it.
//   - Anything else is paho's token error: the connection went away under the
//     SUBSCRIBE. paho reconnects, and the next OnConnect subscribes again.
func classifyResubscribe(err error, superseded bool) resubscribeAction {
	switch {
	case errors.Is(err, messaging.ErrSubscriptionRefused):
		return resubscribeFatal
	case errors.Is(err, messaging.ErrSubscriptionUnacknowledged):
		if superseded {
			return resubscribeRetry
		}
		return resubscribeFatal
	default:
		return resubscribeRetry
	}
}

// Called when connection is lost. paho reconnects on its own and onConnect
// re-subscribes; this is logged at Warn because ingest from this source stops until then.
func (es *MqttEventSource) onConnectionLost(client mqtt.Client, err error) {
	log.Warn().Err(err).Str("source", es.Id).
		Msg("MQTT event source connection lost; reconnecting.")
}

// Initialize event source
func (es *MqttEventSource) Initialize(ctx context.Context) error {
	return es.lifecycle.Initialize(ctx)
}

// Initialize event source (as called by lifecycle manager)
func (es *MqttEventSource) ExecuteInitialize(ctx context.Context) error {
	opts := mqtt.NewClientOptions()
	// ssl:// + a verified TLS config when the gateway terminates TLS (ADR-025),
	// otherwise plaintext tcp://.
	scheme := "tcp"
	if es.tlsConfig != nil {
		scheme = "ssl"
		opts.SetTLSConfig(es.tlsConfig)
	}
	opts.AddBroker(fmt.Sprintf("%s://%s:%d", scheme, es.BrokerHost, es.BrokerPort))
	opts.SetClientID("devicechain")
	// Present the service credential when broker auth is enabled (ADR-025) so the
	// gateway authenticates this connection statically rather than routing it
	// through the device callout.
	if es.username != "" {
		opts.SetUsername(es.username)
		opts.SetPassword(es.password)
	}
	opts.SetDefaultPublishHandler(es.onMessage)
	// Every connection subscribes, the first included; see onConnect.
	opts.OnConnect = es.onConnect
	opts.OnConnectionLost = es.onConnectionLost
	// Built, not connected. The connection is made in ExecuteStart, AFTER the decode
	// workers exist: the first connection subscribes at once, and a message delivered
	// on it would otherwise find no channel to go to.
	es.Client = mqtt.NewClient(opts)
	log.Info().Str("source", es.Id).Msg("MQTT event source initialized; it connects when started.")
	return nil
}

// Start event source
func (es *MqttEventSource) Start(ctx context.Context) error {
	return es.lifecycle.Start(ctx)
}

// Initialize pool of workers for decoding raw messages.
func (es *MqttEventSource) initializeDecodeWorkers() {
	// Make channels and workers for distributed processing.
	es.messages = make(chan rawMessage, DECODE_CHANNEL_DEPTH)
	es.workers = make([]*DecodeWorker, 0)
	for w := 1; w <= DECODE_WORKER_COUNT; w++ {
		worker := NewDecodeWorker(w, es.Id, es.Decoder, es.messages, es.decoded, es.failed)
		es.workers = append(es.workers, worker)
		go worker.Process()
	}
}

// Start event source (as called by lifecycle manager)
func (es *MqttEventSource) ExecuteStart(ctx context.Context) error {
	// Initialize pool of workers for decoding raw messages. First, so the channel
	// exists before the first connection can deliver anything.
	es.initializeDecodeWorkers()

	if token := es.Client.Connect(); token.Wait() && token.Error() != nil {
		return token.Error()
	}

	// The first connection subscribes in onConnect, like every later one; wait for its
	// confirmed result. It is bounded by subscribeTimeout, and a refusal or any other
	// error fails the start, as it always has.
	var err error
	select {
	case err = <-es.ready:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		// Do not leave a live, auto-reconnecting client behind a start that failed: its
		// next connection would subscribe again and report into a process that is
		// already going down. stopping first, so that onConnect stays quiet about it.
		es.stopping.Store(true)
		es.Client.Disconnect(250)
		return fmt.Errorf("this event source would ingest nothing: %w", err)
	}
	log.Info().Msg(fmt.Sprintf("MQTT event source subscribed to topic '%s'.", es.Topic))
	return nil
}

// Stop event source
func (es *MqttEventSource) Stop(ctx context.Context) error {
	return es.lifecycle.Stop(ctx)
}

// Stop event source (as called by lifecycle manager)
func (es *MqttEventSource) ExecuteStop(ctx context.Context) error {
	// Quiesce inbound traffic before tearing down the channel: unsubscribe and
	// disconnect the broker client so paho can no longer invoke onMessage, then
	// close the channel the decode workers drain. Closing first would race a
	// late-arriving message into a send-on-closed-channel panic.
	if es.Client != nil {
		// Before the disconnect: a re-subscribe in flight fails when we close the
		// connection, and that is not something to end the process over.
		es.stopping.Store(true)
		if token := es.Client.Unsubscribe(es.Topic); token.Wait() && token.Error() != nil {
			log.Warn().Err(token.Error()).Msg("MQTT event source failed to unsubscribe on stop.")
		}
		es.Client.Disconnect(250)
	}
	close(es.messages)
	return nil
}

// Terminate microservice
func (es *MqttEventSource) Terminate(ctx context.Context) error {
	return es.lifecycle.Terminate(ctx)
}

// Terminate event source (as called by lifecycle manager)
func (es *MqttEventSource) ExecuteTerminate(ctx context.Context) error {
	return nil
}

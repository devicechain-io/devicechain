// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"crypto/tls"
	"fmt"
	"strconv"
	"strings"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const (
	TYPE_MQTT           = "mqtt"
	DECODE_WORKER_COUNT = 5
)

// GatewayTopic is the MQTT topic filter the NATS-gateway event source subscribes
// to for an instance: the device EVENTS shape, and nothing else.
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
	decoded   func(string, string, *model.UnresolvedEvent, interface{})
	failed    func(string, string, []byte, error)
	// allow meters an inbound message against its tenant's ingest rate limit
	// before it is queued for decode; a false return sheds the message. nil
	// disables metering (used by tests that exercise decoding in isolation).
	allow func(string, string) bool
}

// Create a new MQTT event source based on the given configuration. tlsConfig is
// non-nil when the broker terminates TLS on the MQTT gateway (ADR-025), in which
// case the client dials ssl:// and verifies the server; nil dials plaintext.
// username/password present the shared service credential when broker auth is on
// (empty = anonymous).
func NewMqttEventSource(id string, config map[string]string, tlsConfig *tls.Config, username, password string, decoder Decoder,
	received func(string, []byte),
	decoded func(string, string, *model.UnresolvedEvent, interface{}),
	failed func(string, string, []byte, error),
	allow func(string, string) bool) (*MqttEventSource, error) {
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
	if log.Debug().Enabled() {
		log.Debug().Msg(fmt.Sprintf("Received message:\n%s from MQTT topic: %s\n", msg.Payload(), msg.Topic()))
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
	if es.allow != nil && !es.allow(es.Id, tenant) {
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

// Called on successful connection.
func (es *MqttEventSource) onConnect(client mqtt.Client) {
	log.Info().Msg("MQTT event source connected successfully.")
}

// Called when connection is lost.
func (es *MqttEventSource) onConnectionLost(client mqtt.Client, err error) {
	log.Info().Msg("MQTT event source connection lost.")
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
	opts.OnConnect = es.onConnect
	opts.OnConnectionLost = es.onConnectionLost
	es.Client = mqtt.NewClient(opts)
	if token := es.Client.Connect(); token.Wait() && token.Error() != nil {
		return token.Error()
	}
	log.Info().Msg("MQTT event source initialized.")
	return nil
}

// Start event source
func (es *MqttEventSource) Start(ctx context.Context) error {
	return es.lifecycle.Start(ctx)
}

// Initialize pool of workers for decoding raw messages.
func (es *MqttEventSource) initializeDecodeWorkers() {
	// Make channels and workers for distributed processing.
	es.messages = make(chan rawMessage, 100)
	es.workers = make([]*DecodeWorker, 0)
	for w := 1; w <= DECODE_WORKER_COUNT; w++ {
		worker := NewDecodeWorker(w, es.Id, es.Decoder, es.messages, es.decoded, es.failed)
		es.workers = append(es.workers, worker)
		go worker.Process()
	}
}

// Start event source (as called by lifecycle manager)
func (es *MqttEventSource) ExecuteStart(ctx context.Context) error {
	// Initialize pool of workers for decoding raw messages.
	es.initializeDecodeWorkers()

	// Create subscription to start receiving messages.
	token := es.Client.Subscribe(es.Topic, 1, es.onMessage)
	token.Wait()
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

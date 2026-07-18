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
	"github.com/rs/zerolog/log"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const (
	TYPE_MQTT           = "mqtt"
	DECODE_WORKER_COUNT = 5
)

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
// They sit under the same {instanceId}/{tenant}/… tree this source subscribes to,
// so a wildcard subscription sees them. They are named here rather than excluded
// by narrowing the subscription because a device may legitimately publish events
// on a topic this service has never been told about — the topic segment after the
// tenant is not constrained anywhere — so an allowlist would silently stop
// ingesting somebody's telemetry. Naming what is definitely NOT an event cannot.
var commandPlaneSuffixes = map[string]struct{}{
	"device-commands":   {},
	"command-responses": {},
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
	es.messages <- rawMessage{tenant: tenant, payload: msg.Payload()}
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

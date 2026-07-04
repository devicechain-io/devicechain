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

	Client  mqtt.Client
	Decoder Decoder

	messages  chan rawMessage
	workers   []*DecodeWorker
	lifecycle core.LifecycleManager
	received  func(string, []byte)
	decoded   func(string, string, *model.UnresolvedEvent, interface{})
	failed    func(string, string, []byte, error)
}

// Create a new MQTT event source based on the given configuration. tlsConfig is
// non-nil when the broker terminates TLS on the MQTT gateway (ADR-025), in which
// case the client dials ssl:// and verifies the server; nil dials plaintext.
func NewMqttEventSource(id string, config map[string]string, tlsConfig *tls.Config, decoder Decoder,
	received func(string, []byte),
	decoded func(string, string, *model.UnresolvedEvent, interface{}),
	failed func(string, string, []byte, error)) (*MqttEventSource, error) {
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
		Decoder:    decoder,
	}

	es.lifecycle = core.NewLifecycleManager("mqtt-event-source", es, core.NewNoOpLifecycleCallbacks())
	es.received = received
	es.decoded = decoded
	es.failed = failed
	return es, nil
}

// tenantFromTopic derives the tenant from an inbound MQTT topic of the form
// "dc/{tenant}/..." (ADR-006): the tenant is the second of at least three
// non-empty slash-separated segments. Parsed directly (no whole-string rewrite)
// since this runs per inbound message.
func tenantFromTopic(topic string) (string, bool) {
	parts := strings.SplitN(topic, "/", 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	return parts[1], true
}

// Called when message is received from topic.
func (es *MqttEventSource) onMessage(client mqtt.Client, msg mqtt.Message) {
	if log.Debug().Enabled() {
		log.Debug().Msg(fmt.Sprintf("Received message:\n%s from MQTT topic: %s\n", msg.Payload(), msg.Topic()))
	}
	es.received(es.Id, msg.Payload())

	// Derive the per-message tenant from the topic up front; a message whose topic
	// carries no tenant cannot be published to a tenant-scoped subject, so it is
	// dropped (fail-closed) rather than decoded and published unscoped.
	tenant, ok := tenantFromTopic(msg.Topic())
	if !ok {
		log.Warn().Msg(fmt.Sprintf("Dropping message with no parseable tenant in MQTT topic %q", msg.Topic()))
		return
	}
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

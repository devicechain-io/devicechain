/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package processor

import (
	"context"
	"fmt"
	"strconv"

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

	Client  mqtt.Client
	Decoder Decoder

	messages  chan []byte
	workers   []*DecodeWorker
	lifecycle core.LifecycleManager
	received  func(string, []byte)
	decoded   func(string, *model.UnresolvedEvent, interface{})
	failed    func(string, []byte, error)
}

// Create a new MQTT event source based on the given configuration.
func NewMqttEventSource(id string, config map[string]string, decoder Decoder,
	received func(string, []byte),
	decoded func(string, *model.UnresolvedEvent, interface{}),
	failed func(string, []byte, error)) (*MqttEventSource, error) {
	port, err := strconv.Atoi(config["port"])
	if err != nil {
		return nil, err
	}

	es := &MqttEventSource{
		Id:         id,
		BrokerHost: config["host"],
		BrokerPort: port,
		Topic:      config["topic"],
		Decoder:    decoder,
	}

	es.lifecycle = core.NewLifecycleManager("mqtt-event-source", es, core.NewNoOpLifecycleCallbacks())
	es.received = received
	es.decoded = decoded
	es.failed = failed
	return es, nil
}

// Called when message is received from topic.
func (es *MqttEventSource) onMessage(client mqtt.Client, msg mqtt.Message) {
	if log.Debug().Enabled() {
		log.Debug().Msg(fmt.Sprintf("Received message:\n%s from MQTT topic: %s\n", msg.Payload(), msg.Topic()))
	}
	es.received(es.Id, msg.Payload())
	es.messages <- msg.Payload()
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
	opts.AddBroker(fmt.Sprintf("tcp://%s:%d", es.BrokerHost, es.BrokerPort))
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
	es.messages = make(chan []byte, 100)
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

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package mqtt is a stand-in for github.com/eclipse/paho.mqtt.golang, cut down to
// the API surface this analyzer reads. See the note in the nats stub beside it for
// why a stub is sound here and what keeps it honest.
//
// Client is an INTERFACE in the real library too, which matters: the analyzer has to
// resolve an interface method to its declaring type, not just a concrete receiver.
package mqtt

// Message is a delivered publication.
type Message interface {
	Topic() string
	Payload() []byte
}

// MessageHandler receives deliveries.
type MessageHandler func(Client, Message)

// Token is paho's asynchronous result handle.
type Token interface {
	Wait() bool
	Error() error
}

// Client is the paho client.
type Client interface {
	Subscribe(topic string, qos byte, callback MessageHandler) Token
	SubscribeMultiple(filters map[string]byte, callback MessageHandler) Token
	Publish(topic string, qos byte, retained bool, payload any) Token
}

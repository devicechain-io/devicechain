// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package decoy stands in for the OTHER things in this repo that spell a method
// `Subscribe` — core/graphqlws's client and core/graphql's subscription plumbing.
// Neither talks to a broker, and a textual search cannot tell them from the two that
// do. If the analyzer ever reports one of these, it has stopped being type-aware.
package decoy

// Conn deliberately reuses the type NAME the real nats connection has, so a check
// written against the receiver's spelling rather than its declaring package would
// pass the rest of the suite and fail here.
type Conn struct{}

func (c *Conn) Subscribe(query string) error { return nil }

// Client deliberately reuses paho's type name for the same reason.
type Client struct{}

func (c *Client) Subscribe(topic string, qos byte) error { return nil }

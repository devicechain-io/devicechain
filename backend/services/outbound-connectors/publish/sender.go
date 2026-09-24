// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package publish is the ADR-060 Tier-2 outbound sink: it delivers one rendered payload to
// the Target a versioned Connector describes, over MQTT, Kafka, SNS or SQS.
//
// # Every connection goes through one dial
//
// The clients are ours to configure, and the reason for owning them is the dial. Each
// client is handed Sender.dial (dial.go) — the MQTT open function, the Kafka dialer, the
// AWS HTTP transport — and that function is the only place a publish connection is made.
// It runs the platform egress guard on the address the kernel is about to connect to, for
// every connection the client makes: the brokers a Kafka cluster advertises in metadata,
// not only the seeds; the WebSocket endpoint of an MQTT broker; an AWS endpoint override.
// Nothing reads a proxy from the environment, and the AWS client never reads the pod's
// credentials, configuration files or instance metadata.
//
// # A send is one bounded conversation
//
// Each Send builds its client, delivers, and tears the client down. Every connection is
// closed when the send's context ends, whatever the client library is still doing, and
// every connection carries a cap on what the destination may send back, so a hostile
// broker cannot hold a worker or its memory past the send.
package publish

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
)

// ErrPublishConfig marks a TERMINAL publish failure that originates from the target rather
// than from delivery: a client that cannot be constructed from it, or a destination shape
// that reached this package without passing connectorspec.Build. A redelivery cannot fix
// it, so the executor dead-letters it. A transient DELIVERY failure (a broker briefly
// down) is a plain error, and a destination the egress guard refused wraps
// egress.ErrBlocked.
var ErrPublishConfig = errors.New("publish: invalid target")

// Sender delivers payloads through the one egress guard it was built with.
type Sender struct {
	guard *egress.Guard
}

// NewSender builds a Sender over guard. A nil guard is a guard with no allowances, so a
// missed wiring NARROWS the boundary rather than removing it.
func NewSender(g *egress.Guard) *Sender {
	if g == nil {
		g = egress.NewGuard(nil)
	}
	return &Sender{guard: g}
}

// Send delivers payload to t, blocking until the destination acknowledged it, refused it,
// or ctx ended. ctx must carry a deadline: it bounds the connect as well as the send.
//
// idempotencyKey is forwarded where the protocol carries metadata (a Kafka record header,
// an SNS/SQS message attribute) so a downstream consumer can deduplicate on it. MQTT 3.1.1
// has nowhere to put it.
//
// The error is the egress refusal whenever any connection this send attempted was
// refused (it wraps egress.ErrBlocked, and the send was cancelled at that moment), in
// preference to whatever the client library reported afterwards.
//
// SECURITY: t carries the credential and payload may carry PII; neither is logged or
// placed in an error.
func (s *Sender) Send(ctx context.Context, t connectorspec.Target, payload []byte, idempotencyKey string) error {
	if _, ok := ctx.Deadline(); !ok {
		return fmt.Errorf("%w: a publish needs a deadline", ErrPublishConfig)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	// Cancelling on return is what closes every connection the send opened: each one is
	// registered with context.AfterFunc on this ctx (dial.go).
	defer cancel(nil)
	log := &dialLog{ctx: ctx, cancel: cancel}

	var err error
	switch t := t.(type) {
	case connectorspec.MQTTTarget:
		err = s.sendMQTT(ctx, log, t, payload)
	case connectorspec.KafkaTarget:
		err = s.sendKafka(ctx, log, t, payload, idempotencyKey)
	case connectorspec.SNSTarget:
		err = s.sendSNS(ctx, log, t, payload, idempotencyKey)
	case connectorspec.SQSTarget:
		err = s.sendSQS(ctx, log, t, payload, idempotencyKey)
	default:
		return fmt.Errorf("%w: no client for target %T", ErrPublishConfig, t)
	}
	if err == nil {
		return nil
	}
	return log.explain(err)
}

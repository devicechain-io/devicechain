// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"encoding/json"
	"errors"
	"fmt"

	nats "github.com/nats-io/nats.go"

	"github.com/devicechain-io/dc-microservice/core"
)

// A NOTICE tells a gateway that something it published for a child was refused.
//
// The broker cannot say so: nats-server speaks MQTT 3.1.1, which has no way to refuse a
// publish, so a publish the PLATFORM refuses (a gateway naming a device it is not assigned to)
// is PUBACKed like any other, and a gateway that only watched PUBACKs would believe the event
// was stored. The platform refuses it in code, records it (failed events, a counter, a WARN),
// and, best effort, sends the gateway this notice on its own notices subject
// ("{instance}.{tenant}.gateways.{gateway}.notices", granted to that gateway alone).
//
// 🔴 BEST EFFORT, AND NEVER THE RECORD OF TRUTH. A gateway that is offline when the notice is
// sent misses it (it is a plain publish, not a stored message). The record is the failed-events
// entry plus the counter; the notice exists so the person at the gateway's end sees a named
// refusal rather than silence. It carries no payload and no credential.

// GatewayNoticeReasonNotGatewayChild is the notice reason for a publish for a device that is
// not assigned to the publishing gateway, or does not exist.
const GatewayNoticeReasonNotGatewayChild = "not_gateway_child"

// GatewayNotice is the body of a notice. Device is the child the refused publish named; AltId
// is the refused event's idempotency key, when it carried one, so the gateway can tell which of
// its events it was.
type GatewayNotice struct {
	AltId  string `json:"altId,omitempty"`
	Device string `json:"device"`
	Reason string `json:"reason"`
}

// GatewayNoticePublisher publishes notices over the area's broker connection.
type GatewayNoticePublisher struct {
	conn       func() *nats.Conn
	instanceId string
}

// NewGatewayNoticePublisher builds a publisher. conn is read at publish time, because the
// connection is replaced on a reconnect and the publisher is built before it exists.
func NewGatewayNoticePublisher(conn func() *nats.Conn, instanceId string) *GatewayNoticePublisher {
	return &GatewayNoticePublisher{conn: conn, instanceId: instanceId}
}

// Notice sends n to gateway's notices subject. It never blocks on the broker (a core publish
// is buffered by the client) and reports only a failure to hand the message over.
func (p *GatewayNoticePublisher) Notice(tenant, gateway string, n GatewayNotice) error {
	if err := core.ValidateToken(tenant); err != nil {
		return fmt.Errorf("refusing to notify a gateway of an invalid tenant: %w", err)
	}
	if err := core.ValidateToken(gateway); err != nil {
		return fmt.Errorf("refusing to notify an invalid gateway token: %w", err)
	}
	nc := p.conn()
	if nc == nil {
		return errors.New("no broker connection to send a gateway notice on")
	}
	body, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return nc.Publish(GatewayNoticesSubject(p.instanceId, tenant, gateway), body)
}

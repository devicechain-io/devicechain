// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"encoding/json"
	"sync/atomic"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// GatewayFeedback is how this source answers a gateway about the events it took from the
// gateway's namespace. A gateway's PUBACK says only that the broker took a message, so the
// platform says the rest: Accepted when the event is durably held on a platform stream (the
// gateway may retire it from its spool), Refused when it is shed at the tenant's ceiling (the
// gateway must keep it). messaging.GatewayFeedback is the production implementation.
//
// Both are best effort and non-blocking by contract. The ingest path never waits on them and
// never fails on them: a missed acceptance costs the gateway a resend the idempotency key
// suppresses, and a missed refusal costs it a resend nobody asked for, never a loss.
type GatewayFeedback interface {
	Accepted(tenant, gateway, altId string)
	Refused(tenant, gateway string, n messaging.GatewayNotice)
}

// UseGatewayFeedback sets the feedback sent to gateways. It must be called before Start.
func (es *GatewayJetStreamSource) UseGatewayFeedback(f GatewayFeedback) { es.feedback = f }

// gatewayEvent names the gateway and the idempotency key of a captured message on a gateway's
// namespace. ok is false for a device's own message, a message with no key (nothing the gateway
// could match an answer to), or when no feedback is configured: no answer is owed.
func (es *GatewayJetStreamSource) gatewayEvent(msg messaging.Message) (gateway, altId string, ok bool) {
	if es.feedback == nil {
		return "", "", false
	}
	gateway, _, parsed := messaging.ParseDeviceEventsSubject(msg.Subject)
	if !parsed || gateway == "" {
		return "", "", false
	}
	var key struct {
		AltId string `json:"altId"`
	}
	if json.Unmarshal(msg.Value, &key) != nil || key.AltId == "" {
		return "", "", false
	}
	return gateway, key.AltId, true
}

// noticeShed tells the gateway that msg was shed at the tenant's ingest ceiling.
func (es *GatewayJetStreamSource) noticeShed(msg messaging.Message, tenant string) {
	gateway, altId, ok := es.gatewayEvent(msg)
	if !ok {
		return
	}
	_, child, _ := messaging.ParseDeviceEventsSubject(msg.Subject)
	es.feedback.Refused(tenant, gateway, messaging.GatewayNotice{
		AltId: altId, Device: child, Reason: messaging.GatewayNoticeReasonOverCeiling,
	})
}

// answers returns the pair of hooks that answer the gateway for one captured message: settle
// wraps the message's settler so that a durable handling is reported as an acceptance, and shed
// is the rawMessage.shed hook, which reports a shed at the reading stage and stops the settle
// that follows it (a shed is settled nil, like any drop) from also reporting an acceptance.
// With nothing owed it returns the settler unchanged and a nil shed.
func (es *GatewayJetStreamSource) answers(msg messaging.Message, tenant string, settle func(error)) (func(error), func()) {
	gateway, altId, ok := es.gatewayEvent(msg)
	if !ok {
		return settle, nil
	}
	var shed atomic.Bool
	return func(err error) {
			if err == nil && !shed.Load() {
				es.feedback.Accepted(tenant, gateway, altId)
			}
			settle(err)
		}, func() {
			shed.Store(true)
			es.noticeShed(msg, tenant)
		}
}

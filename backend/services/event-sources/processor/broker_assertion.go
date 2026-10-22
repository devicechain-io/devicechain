// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// brokerAssertion records that the device a message names was authenticated by THIS
// PLATFORM'S MQTT broker, and confined by it to the topic the message arrived on.
//
// 🔴 THE CAPTURE SOURCE AND AN EXTERNAL MQTT SOURCE LOOK ALIKE INSIDE THIS PACKAGE. Both
// carry a topic-derived device and OriginAuthenticated, and a comment on each parser calls
// the topic device "broker-authorized". For the capture source that broker is ours: the
// auth callout resolved the CONNECT credential to one device and granted it that device's
// own events subject. For an external source it is the CUSTOMER's broker, which never
// authenticated anyone against this platform, so any author on it can name any device in
// a topic. Treating the topic device as authenticated on that path would hand every
// external-broker topic author a forgery primitive, so the distinction is made here, in a
// type, instead of in a flag two sources could both set.
//
// What makes it structural:
//   - the only way to obtain a true value is assertedByCaptureBroker, whose parameter is a
//     messaging.Message read off the capture stream (or the MQTT message store). A paho
//     source has no such value to pass it;
//   - the zero value, which is what every other rawMessage carries, asserts nothing;
//   - the field is unexported and has no setter, and TestOnlyTheCaptureSourceAssertsTheBroker
//     fails the build of the package if any file but the capture source names the
//     constructor.
//
// 🔑 A GATEWAY SUBJECT ASSERTS THE GATEWAY, NOT THE DEVICE. On "{i}.{t}.gateways.{g}.devices.{c}.events"
// the broker authenticated g and confined it to its own namespace, and c is only the child g NAMES.
// So the assertion carries both, and the event leaves marked with the gateway (UnresolvedEvent.Gateway)
// for the resolver to check against the child's recorded gateway. The child is whatever the subject
// says, never a body field: mark sets Device from the subject, so a body that omits it still
// resolves and a body that contradicts it was already refused (checkDeviceMatchesTransport).
type brokerAssertion struct {
	asserted bool
	// gateway and child are set together, only for a message on a gateway's namespace.
	gateway, child string
}

// assertedByCaptureBroker is the ONLY constructor of a true brokerAssertion. It asserts only
// when the subject is the device-events shape the broker grants a device, so a message on
// any other subject asserts nothing.
func assertedByCaptureBroker(msg messaging.Message) brokerAssertion {
	gateway, device, ok := messaging.ParseDeviceEventsSubject(msg.Subject)
	if !ok {
		return brokerAssertion{}
	}
	if gateway != "" {
		return brokerAssertion{asserted: true, gateway: gateway, child: device}
	}
	return brokerAssertion{asserted: true}
}

// mark stamps an event that has already passed every check on its way out of the decode
// worker. The event it is given is never one a decoder could have marked: no decoder has a
// field for it (see TestJsonDecoderCannotSetBrokerAsserted).
//
// 🔴 A GATEWAY EVENT LEAVES WITH BrokerAsserted FALSE, AND THAT IS WHAT KEEPS A ROLLING UPGRADE
// CLOSED. BrokerAsserted says "the broker authenticated THIS DEVICE", and on a gateway subject it
// did not: it authenticated the gateway. A device-management replica that predates the gateway
// field ignores it (an unknown proto field), so a gateway event marked BrokerAsserted would reach
// it as the child's own broker-asserted event and resolve under deviceAuthMode required with no
// assignment check at all: any device could speak for any device of its tenant for as long as the
// rollout lasted. Unmarked, that replica asks for a credential and refuses it. The current
// resolver keys a gateway event on Gateway alone (resolveGatewayChild), which only this mark sets.
func (a brokerAssertion) mark(event *model.UnresolvedEvent) {
	if a.gateway != "" {
		event.BrokerAsserted = false
		event.Gateway = a.gateway
		event.Device = a.child
		return
	}
	event.BrokerAsserted = a.asserted
}

// credentialInBody reports whether a decoded event carries any of the three credential
// members. On a broker-asserted message none may be present, in any deviceAuthMode: the
// identity is the topic's, and a secret in the body is a secret the broker already wrote to
// the capture stream before any platform code could seal it.
//
// Presence is the test, not content: an empty string is still a device populating a field
// that exists to carry a secret.
func credentialInBody(event *model.UnresolvedEvent) bool {
	return event != nil && (event.CredentialType != nil || event.CredentialId != nil || event.CredentialSecret != nil)
}

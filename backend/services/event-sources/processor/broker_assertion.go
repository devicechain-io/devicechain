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
type brokerAssertion struct {
	asserted bool
}

// assertedByCaptureBroker is the ONLY constructor of a true brokerAssertion. It asserts only
// when the subject is the device-events shape the broker grants a device, so a message on
// any other subject asserts nothing.
func assertedByCaptureBroker(msg messaging.Message) brokerAssertion {
	return brokerAssertion{asserted: deviceFromSubject(msg.Subject) != ""}
}

// mark stamps an event that has already passed every check on its way out of the decode
// worker. The event it is given is never one a decoder could have marked: no decoder has a
// field for it (see TestJsonDecoderCannotSetBrokerAsserted).
func (a brokerAssertion) mark(event *model.UnresolvedEvent) {
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

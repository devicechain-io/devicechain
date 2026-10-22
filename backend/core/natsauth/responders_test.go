// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth_test

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
)

// 🔑 A RESPONDER ANSWERS WITHOUT A PUBLISH GRANT ON ANY INBOX. The areas that answer a request (the
// device authorization callout, the session kick, the DETECT tenant eviction, a consumer-leader
// census) reply to whatever reply subject the request carried, and the broker allows that reply
// because the login received the request (allow_responses). If that did not work these services
// would be silent on a real broker while every unit test, which replies through a permissive
// connection, stayed green. So each is exercised against the generated grant: a requester asks, the
// area's own login answers, and the broker logs no violation.
//
// The requester is an unrestricted login standing in for the other end (the broker's own
// authorization request is not publishable by any area, which is the point of its grant).

const respInstance = "rinst"

func TestEveryResponderAnswersUnderItsGrant(t *testing.T) {
	cases := []struct {
		name    string
		area    string
		subject string
		queue   string
	}{
		{"device authorization callout", "device-management", natsauth.AuthCalloutRequestSubject, "dc-device-callout"},
		{"session kick", "event-sources", messaging.SessionKickSubject(respInstance), "dc-session-kick"},
		{"DETECT tenant eviction", "event-processing", messaging.DetectPurgeSubject(respInstance), ""},
		{"consumer-leader census", "event-management", messaging.CensusSubject(respInstance, "event-management"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := permtest.Start(t, respInstance, permtest.WithOpenUser())

			responder := b.Connect(t, tc.area)
			handler := func(m *nats.Msg) { _ = m.Respond([]byte("answered")) }
			var sub *nats.Subscription
			var err error
			if tc.queue != "" {
				sub, err = messaging.QueueSubscribeSynced(responder, tc.subject, tc.queue, handler)
			} else {
				sub, err = messaging.SubscribeSynced(responder, tc.subject, handler)
			}
			require.NoError(t, err, "the area's own login must be allowed to listen on %s", tc.subject)
			t.Cleanup(func() { _ = sub.Unsubscribe() })

			reply, err := b.ConnectOpen(t).Request(tc.subject, []byte("ask"), 5*time.Second)
			require.NoError(t, err, "the area did not answer under its grant")
			require.Equal(t, "answered", string(reply.Data))
			b.RequireNoViolations(t)
		})
	}
}

// The negative control, in both directions. Without the subscribe grant the responder never hears
// the request (and the broker says so); and an area the grant does not name cannot listen at all.
func TestAResponderIsRefusedWithoutItsGrantAndAnotherAreaCannotListen(t *testing.T) {
	subject := messaging.SessionKickSubject(respInstance)

	b := permtest.Start(t, respInstance, permtest.WithOpenUser(), permtest.WithoutSubscribe(subject))
	responder := b.Connect(t, "event-sources")
	_, err := messaging.SubscribeSynced(responder, subject, func(*nats.Msg) {})
	_ = err // the client may or may not surface it; the broker's log is the verdict
	require.NoError(t, responder.Flush())
	require.Eventually(t, func() bool { return len(b.Violations()) > 0 }, 5*time.Second, 20*time.Millisecond,
		"withdrawing the subscribe grant must be refused by the broker")

	// An area that is not the session-kick responder cannot become one.
	b2 := permtest.Start(t, respInstance)
	other := b2.Connect(t, "event-management")
	_, _ = messaging.SubscribeSynced(other, subject, func(*nats.Msg) {})
	require.NoError(t, other.Flush())
	require.Eventually(t, func() bool { return len(b2.Violations()) > 0 }, 5*time.Second, 20*time.Millisecond,
		"an area the catalog does not name must not be able to answer the session kick")

	// Nor can anyone publish an authorization response on its own: only a reply to a request the
	// callout area received is allowed.
	b3 := permtest.Start(t, respInstance)
	forger := b3.Connect(t, "event-management")
	require.NoError(t, forger.Publish("_INBOX_device-management.forged", []byte("jwt")))
	require.NoError(t, forger.Flush())
	require.Eventually(t, func() bool { return len(b3.Violations()) > 0 }, 5*time.Second, 20*time.Millisecond,
		"publishing into another area's inbox must be refused")
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/require"
)

// bodySecret stands in for a device's secret: an assertion about never keeping one should
// not put credential-shaped text in a CI log.
const bodySecret = "fixture-body-secret-9f3a"

// A measurement body, with the credential members spliced in.
func measurementWith(credential string) string {
	return `{"device":"sensor-001","eventType":"Measurement",` + credential +
		`"payload":{"entries":[{"measurements":{"temp":"21.5"}}]}}`
}

var credentialBodies = map[string]string{
	"a secret":                  `"credentialSecret":"` + bodySecret + `",`,
	"an id":                     `"credentialId":"cred-1",`,
	"a type":                    `"credentialType":"MQTT_BASIC",`,
	"all three":                 `"credentialType":"MQTT_BASIC","credentialId":"cred-1","credentialSecret":"` + bodySecret + `",`,
	"an empty secret":           `"credentialSecret":"",`,
	"an access token as the id": `"credentialType":"ACCESS_TOKEN","credentialId":"` + bodySecret + `",`,
}

// S2, THE REFUSAL. A body credential on the capture source is refused, never published,
// routed to failed-decode under ErrCredentialInMqttBody, and the capture message is ACKED
// (terminal): left unacked it would sit in the stream, secret and all, until MaxDeliver or
// MaxAge. The error that travels carries none of what it refused.
func TestACaptureSourceEventWithABodyCredentialIsRefusedAndAcked(t *testing.T) {
	for name, cred := range credentialBodies {
		t.Run(name, func(t *testing.T) {
			h := newCaptureHarness(t)
			ack := &recordingAck{}
			h.source.handle(capturedMsgAt(captureSubject, measurementWith(cred), 1, time.Now(), ack))

			require.True(t, ack.settled(t), "the refusal must ack the capture message, never leave it to redeliver")
			require.Equal(t, 1, ack.counts(), "acked exactly once")
			published, failed, _, _ := h.counts()
			require.Zero(t, published, "a refused event must never reach inbound-events")
			require.Equal(t, 1, failed, "the refusal must reach failed-decode")
			h.mu.Lock()
			defer h.mu.Unlock()
			require.ErrorIs(t, h.failedReasons[0], model.ErrCredentialInMqttBody)
			require.NotContains(t, h.failedReasons[0].Error(), bodySecret, "the refusal must not carry the secret")
			require.NotContains(t, h.failedReasons[0].Error(), "cred-1")
		})
	}
}

// S2, ACCEPTANCE. The same event with no credential is accepted on the broker's assertion,
// and the published event is marked so the resolver can accept it under required.
func TestACaptureSourceEventWithoutACredentialIsAcceptedAndMarkedBrokerAsserted(t *testing.T) {
	h := newCaptureHarness(t)
	ack := &recordingAck{}
	h.source.handle(capturedMsgAt(captureSubject, measurementWith(""), 1, time.Now(), ack))
	require.True(t, ack.settled(t))

	events := h.publishedEvents()
	require.Len(t, events, 1)
	require.True(t, events[0].BrokerAsserted, "an accepted capture event carries the broker's assertion")
	require.Nil(t, events[0].CredentialSecret)
	_, failed, _, _ := h.counts()
	require.Zero(t, failed)
}

// A subject that is not the device-events shape asserts nothing, so the refusal and the
// mark apply only to a message the broker confined a device to.
func TestOnlyADeviceEventsSubjectIsAsserted(t *testing.T) {
	require.True(t, assertedByCaptureBroker(messaging.Message{Subject: captureSubject}).asserted)
	for _, subject := range []string{
		"", "inst-1.acme.events", "inst-1.acme.devices.sensor-001", "inst-1.acme.devices.sensor-001.reports",
		"inst-1/acme/devices/sensor-001/events", "inst-1.acme.other.sensor-001.events",
	} {
		require.False(t, assertedByCaptureBroker(messaging.Message{Subject: subject}).asserted, subject)
	}
}

// S2, THE FORGERY TEST. An EXTERNAL broker's MQTT source reads the same topic shape and the
// same device, but the customer's broker authenticated nobody against this platform. Its
// message must assert nothing: the credential in the body stays required (and is not
// refused), and the decoded event is NOT marked, so the resolver still refuses it under
// required with no credential.
//
// Negative control: give mqtt.go's rawMessage an assertion (or the decode worker a way to
// mark every message) and this goes red.
func TestAnExternalBrokerMessageNeverAssertsTheBroker(t *testing.T) {
	es, _ := newTestMqttSource(t, func(string, string, time.Time, bool, Origin) bool { return true })
	// The device-shaped topic, in the external source's own separator.
	es.onMessage(nil, &fakeMqttMessage{
		topic:   "inst-1/acme/devices/sensor-001/events",
		payload: []byte(measurementWith(credentialBodies["all three"])),
	})
	require.Len(t, es.messages, 1)
	raw := <-es.messages
	require.False(t, raw.assertion.asserted, "an external broker's topic device is not authenticated by this platform")
	require.Equal(t, "sensor-001", raw.device, "the topic device is still parsed, for the match check")

	for name, cred := range credentialBodies {
		t.Run(name, func(t *testing.T) {
			raw := raw
			raw.payload = []byte(measurementWith(cred))
			events, failed := runDecodeWorker(t, raw)
			require.Empty(t, failed, "a body credential is the external source's only authentication: not refused")
			require.Len(t, events, 1)
			require.False(t, events[0].BrokerAsserted, "an external event must reach the resolver unmarked")
			require.True(t, credentialInBody(events[0]), "the credential must still be carried to the resolver")
		})
	}

	// And with no credential at all it is decoded UNMARKED: under required the resolver refuses it.
	raw.payload = []byte(measurementWith(""))
	events, failed := runDecodeWorker(t, raw)
	require.Empty(t, failed)
	require.Len(t, events, 1)
	require.False(t, events[0].BrokerAsserted)
}

// runDecodeWorker drives one raw message through a real DecodeWorker and returns what it
// handed on and what it routed to failed-decode.
func runDecodeWorker(t *testing.T, raw rawMessage) ([]*model.UnresolvedEvent, []error) {
	t.Helper()
	in := make(chan rawMessage, 1)
	var events []*model.UnresolvedEvent
	var failed []error
	w := NewDecodeWorker(1, "src", NewJsonDecoder(map[string]string{}), in, admitAllReadings,
		func(_, _ string, e *model.UnresolvedEvent, _ interface{}, _ string, done func(error)) {
			events = append(events, e)
			done(nil)
		},
		func(_ string, _ string, _ []byte, err error) error {
			failed = append(failed, err)
			return nil
		})
	in <- raw
	close(in)
	w.Process()
	return events, failed
}

// THE STRUCTURAL GUARD. brokerAssertion's constructor takes a messaging.Message, which only
// the capture reader produces, and the field it sets is unexported; this pins that no file
// of the package but the capture source (and the type's own file) ever names either, so a
// second source cannot grow the assertion by accident or by a copy of the line.
func TestOnlyTheCaptureSourceAssertsTheBroker(t *testing.T) {
	allowed := map[string]bool{"broker_assertion.go": true, "gateway_jetstream.go": true, "decode_worker.go": true}
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || allowed[f] {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		for _, banned := range []string{"assertedByCaptureBroker", "brokerAssertion{", "assertion:", ".assertion", "BrokerAsserted"} {
			require.NotContains(t, string(src), banned, "%s must not assert or touch the broker assertion", f)
		}
	}
	// Of the three allowed files, only the capture source may CONSTRUCT one.
	worker, err := os.ReadFile("decode_worker.go")
	require.NoError(t, err)
	require.NotContains(t, string(worker), "assertedByCaptureBroker(", "the decode worker only reads an assertion")
	require.NotContains(t, string(worker), "brokerAssertion{", "the decode worker only reads an assertion")
	// The capture source does name it, or the guard above reads clean over nothing.
	src, err := os.ReadFile("gateway_jetstream.go")
	require.NoError(t, err)
	require.Contains(t, string(src), "assertedByCaptureBroker(msg)")
}

// THE SAME GUARD, ACROSS THE WORKSPACE. brokerAssertion keeps the mark private to this
// package, but the field it sets, model.UnresolvedEvent.BrokerAsserted, is exported, and every
// ingest service (HTTP, Sparkplug, LwM2M, the edge agent) builds UnresolvedEvents. Any of them
// setting it would hand the resolver a credential-free event it trusts for every type. So the
// only writes allowed anywhere in backend/ are the mark itself and the proto round trip.
func TestNothingOutsideTheMarkWritesBrokerAsserted(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	allowed := map[string]bool{
		filepath.Join("services", "event-sources", "processor", "broker_assertion.go"): true,
		filepath.Join("services", "event-sources", "proto", "marshal.go"):              true,
	}
	write := regexp.MustCompile(`BrokerAsserted\s*(:[^=]|=[^=])`)
	scanned, sawMark := 0, false
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, ".pb.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		require.NoError(t, err)
		src, err := os.ReadFile(path)
		require.NoError(t, err)
		scanned++
		if !write.Match(src) {
			return nil
		}
		if allowed[rel] {
			sawMark = sawMark || strings.HasSuffix(rel, "broker_assertion.go")
			return nil
		}
		t.Errorf("%s writes BrokerAsserted: only the capture source's mark may", rel)
		return nil
	}))
	require.Greater(t, scanned, 500, "the walk must have covered the workspace, not an empty directory")
	require.True(t, sawMark, "the mark itself must be found, or the pattern matches nothing")
}

// A device controls the bytes a decoder parses, so no decoder may be able to mark an event
// broker-asserted: an injected member is ignored.
func TestJsonDecoderCannotSetBrokerAsserted(t *testing.T) {
	body := `{"device":"sensor-001","eventType":"Measurement","brokerAsserted":true,` +
		`"payload":{"entries":[{"measurements":{"temp":"21.5"}}]}}`
	event, _, err := NewJsonDecoder(map[string]string{}).Decode([]byte(body), time.Time{})
	require.NoError(t, err)
	require.False(t, event.BrokerAsserted)
}

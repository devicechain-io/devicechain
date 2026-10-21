// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// The marker stands in for a credential: an assertion about not keeping credential
// material should not itself put credential-shaped text in a CI log.
const fixtureSecret = "fixture-credential-secret-value"

// capturingWriter records every message handed to the failed-decode stream.
type capturingWriter struct {
	mu   sync.Mutex
	sent []messaging.Message
}

func (w *capturingWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sent = append(w.sent, msgs...)
	return nil
}
func (w *capturingWriter) WriteToDevice(context.Context, string, ...messaging.Message) error {
	return nil
}
func (w *capturingWriter) HandleResponse(error) {}

// routeFailedDecode runs body through the real decoder and, on failure, through the
// service's own failed-decode handler, returning the bytes that reached the stream.
func routeFailedDecode(t *testing.T, body string) []byte {
	t.Helper()
	savedMs, savedWriter := Microservice, FailedDecodeWriter
	t.Cleanup(func() { Microservice, FailedDecodeWriter = savedMs, savedWriter })
	Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	initializeMetrics()
	w := &capturingWriter{}
	FailedDecodeWriter = w

	_, _, err := processor.NewJsonDecoder(nil).Decode([]byte(body), time.Time{})
	require.Error(t, err, "the fixture must fail to decode or it proves nothing")
	require.NoError(t, onEventDecodeFailed("src", "t1", []byte(body), err))
	require.Len(t, w.sent, 1)
	return w.sent[0].Value
}

func TestFailedDecodeKeepsNoCredentialFromABodyThatDecodesExceptForItsTime(t *testing.T) {
	body := `{"device":"dev-1","eventType":"measurements","occurredTime":"not-a-time",` +
		`"credentialType":"mqtt-basic","credentialId":"cred-1","credentialSecret":"` + fixtureSecret + `",` +
		`"payload":{"entries":[{"name":"temp","value":21.5}]}}`
	stored := string(routeFailedDecode(t, body))
	require.NotContains(t, stored, fixtureSecret)
	// Diagnostics survive: the field names and the other values are still there.
	for _, want := range []string{`"device":"dev-1"`, `"occurredTime":"not-a-time"`, `"credentialId":"cred-1"`, `"temp"`, `21.5`, `"credentialSecret"`} {
		require.Contains(t, stored, want)
	}
}

func TestFailedDecodeKeepsNoCredentialFromAnUnparseableBody(t *testing.T) {
	body := `{"device":"dev-1","credentialSecret":"` + fixtureSecret + `","payload":{"entries":[` // truncated
	stored := string(routeFailedDecode(t, body))
	require.NotContains(t, stored, fixtureSecret)
	require.NotContains(t, stored, "dev-1")
	require.Contains(t, stored, `"bytes":`)
}

func TestFailedDecodeStoredPayloadIsBounded(t *testing.T) {
	big := strings.Repeat("x", 4*failedDecodeMaxBytes)
	body := `{"device":"dev-1","eventType":"measurements","occurredTime":"not-a-time",` +
		`"credentialSecret":"` + fixtureSecret + `","note":"` + big + `","payload":{}}`
	stored := routeFailedDecode(t, body)
	require.NotContains(t, string(stored), fixtureSecret)
	require.LessOrEqual(t, len(stored), failedDecodeMaxBytes+64, "the cut note is the only thing allowed past the bound")
	require.Less(t, len(stored), len(body))
}

// S2: a body credential on the platform broker is refused under its own counter, and what
// reaches the failed-decode stream carries no secret. The counter is the operator's signal
// that a device's credential should be treated as exposed and rotated: the device itself is
// not told (the broker PUBACKed at capture).
func TestABodyCredentialOnThePlatformBrokerIsCountedAndRecordedWithoutItsSecret(t *testing.T) {
	savedMs, savedWriter := Microservice, FailedDecodeWriter
	t.Cleanup(func() { Microservice, FailedDecodeWriter = savedMs, savedWriter })
	Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	initializeMetrics()
	w := &capturingWriter{}
	FailedDecodeWriter = w

	body := `{"device":"dev-1","eventType":"Measurement","credentialType":"MQTT_BASIC",` +
		`"credentialId":"cred-1","credentialSecret":"` + fixtureSecret + `",` +
		`"payload":{"entries":[{"measurements":{"temp":"21.5"}}]}}`
	require.NoError(t, onEventDecodeFailed("gw", "t1", []byte(body), model.ErrCredentialInMqttBody))

	require.Equal(t, 1.0, testutil.ToFloat64(CredentialInMqttBodyCounter.WithLabelValues("gw")))
	require.Equal(t, 1.0, testutil.ToFloat64(FailedDecodeCounter.WithLabelValues("gw")), "still a failed decode")
	require.Len(t, w.sent, 1)
	stored := string(w.sent[0].Value)
	require.NotContains(t, stored, fixtureSecret)
	require.Contains(t, stored, `"device":"dev-1"`, "the record must name the device, so the operator can rotate its credential")

	// A different refusal does not move it.
	require.NoError(t, onEventDecodeFailed("gw", "t1", []byte(body), errors.New("bad json")))
	require.Equal(t, 1.0, testutil.ToFloat64(CredentialInMqttBodyCounter.WithLabelValues("gw")))
}

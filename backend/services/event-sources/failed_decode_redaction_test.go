// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
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
	require.NotContains(t, stored, "cred-1", "the credential id is redacted with the secret")
	// Diagnostics survive: the field names and the other values are still there.
	for _, want := range []string{`"device":"dev-1"`, `"occurredTime":"not-a-time"`, `"credentialType":"mqtt-basic"`, `"temp"`, `21.5`, `"credentialSecret"`} {
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

// The bearer token of an ACCESS_TOKEN credential is carried as credentialId. Every scan below
// looks at the WHOLE persisted record, not one field: a value that survives in a field the
// test did not think to name is exactly the leak.
const fixtureToken = "fixture-access-token-bearer-value"

func requireAbsent(t *testing.T, stored []byte, needles ...string) {
	t.Helper()
	for _, n := range needles {
		require.NotContains(t, string(stored), n)
	}
}

func TestFailedDecodeKeepsNoAccessTokenCredentialId(t *testing.T) {
	body := `{"device":"dev-1","eventType":"measurements","occurredTime":"not-a-time",` +
		`"credentialType":"access-token","credentialId":"` + fixtureToken + `",` +
		`"payload":{"entries":[{"name":"temp","value":21.5}]}}`
	stored := routeFailedDecode(t, body)
	requireAbsent(t, stored, fixtureToken)
	require.Contains(t, string(stored), `"credentialType":"access-token"`, "the type stays: it says what kind of failure to look for")
}

func TestFailedDecodeKeepsNoMqttBasicIdOrSecret(t *testing.T) {
	body := `{"device":"dev-1","eventType":"measurements","occurredTime":"not-a-time",` +
		`"credentialType":"mqtt-basic","credentialId":"fixture-login-name","credentialSecret":"` + fixtureSecret + `",` +
		`"payload":{"entries":[{"name":"temp","value":21.5}]}}`
	requireAbsent(t, routeFailedDecode(t, body), "fixture-login-name", fixtureSecret)
}

func TestFailedDecodeKeepsNoCredentialFromOddlyPlacedOrDuplicatedFields(t *testing.T) {
	for name, body := range map[string]string{
		"duplicated": `{"credentialId":"` + fixtureToken + `","device":"d","occurredTime":"not-a-time",` +
			`"credentialId":"` + fixtureToken + `-2","credentialSecret":"` + fixtureSecret + `","credentialSecret":"` + fixtureSecret + `-2"}`,
		"nested": `{"device":"d","occurredTime":"not-a-time","payload":{"credentialId":"` + fixtureToken + `",` +
			`"inner":[{"credentialSecret":"` + fixtureSecret + `"}]}}`,
		"case and separator": `{"device":"d","occurredTime":"not-a-time","Credential_ID":"` + fixtureToken + `","CREDENTIAL-SECRET":"` + fixtureSecret + `"}`,
		"escaped key":        `{"device":"d","occurredTime":"not-a-time","credentialId":"` + fixtureToken + `"}`,
		"first of many":      `{"credentialId":"` + fixtureToken + `","credentialSecret":"` + fixtureSecret + `","device":"d","occurredTime":"not-a-time"}`,
		"non-string id":      `{"device":"d","occurredTime":"not-a-time","credentialId":{"v":"` + fixtureToken + `"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			requireAbsent(t, routeFailedDecode(t, body), fixtureToken, fixtureSecret)
		})
	}
}

func TestFailedDecodeKeepsNoCredentialIdFromAMalformedBody(t *testing.T) {
	body := `{"credentialId":"` + fixtureToken + `","credentialSecret":"` + fixtureSecret + `","payload":[`
	requireAbsent(t, routeFailedDecode(t, body), fixtureToken, fixtureSecret)
}

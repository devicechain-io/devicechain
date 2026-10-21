// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletter

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/payloadredact"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

const fixtureSecret = "fixture-credential-secret-value"

func lastLetter(t *testing.T, w *fakeWriter) Envelope {
	t.Helper()
	require.NotEmpty(t, w.got)
	e, err := Unmarshal(w.got[len(w.got)-1].Value)
	require.NoError(t, err)
	return e
}

// A device's reply archived as a dead letter is kept without credential material, and the
// rest of the reply is kept as sent.
func TestACommandResponseLetterKeepsNoCredential(t *testing.T) {
	p, _ := testProducer(t, "command-delivery")
	w := &fakeWriter{}
	e := good()
	e.Kind = KindCommandResponse
	e.Payload = []byte(`{"correlationId":"c-1","success":true,"credentialSecret":"` + fixtureSecret + `","payload":{"ok":1}}`)
	require.NoError(t, p.NewSink(w).Write(tenantCtx(), e))
	got := lastLetter(t, w)
	require.NotContains(t, string(got.Payload), fixtureSecret)
	require.Contains(t, string(got.Payload), `"correlationId":"c-1"`)
	require.Contains(t, string(got.Payload), `"ok":1`)
}

func TestAnUnparseableCommandResponseLetterKeepsNoCredential(t *testing.T) {
	p, _ := testProducer(t, "command-delivery")
	w := &fakeWriter{}
	e := good()
	e.Kind = KindCommandResponse
	e.Payload = []byte(`{"credentialSecret":"` + fixtureSecret + `", broken`)
	require.NoError(t, p.NewSink(w).Write(tenantCtx(), e))
	require.NotContains(t, string(lastLetter(t, w).Payload), fixtureSecret)
}

// The counterweight: a letter about platform-authored work keeps its payload byte for byte,
// even where the payload happens to name a field "token".
func TestAPlatformAuthoredLetterPayloadIsUntouched(t *testing.T) {
	p, _ := testProducer(t, "notification-management")
	w := &fakeWriter{}
	e := good()
	e.Payload = []byte(`{"token":"dev-1"}`)
	require.NoError(t, p.NewSink(w).Write(tenantCtx(), e))
	require.Equal(t, []byte(`{"token":"dev-1"}`), lastLetter(t, w).Payload)
}

// The recorder letters a cold stream's original by the stream's authorship declaration:
// device-authored streams are redacted, platform-authored ones kept byte for byte.
func TestRecorderRedactsByDeclaredAuthorship(t *testing.T) {
	cases := map[string]struct {
		suffix, body string
		want         func(t *testing.T, payload string)
	}{
		"device-reports": {streams.DeviceReports, `{"state":"ok","credentialSecret":"` + fixtureSecret + `"}`,
			func(t *testing.T, payload string) {
				require.NotContains(t, payload, fixtureSecret)
				require.Contains(t, payload, `"state":"ok"`)
			}},
		"device-desired": {streams.DeviceDesired, `{"token":"dev-1","document":"x"}`,
			func(t *testing.T, payload string) { require.Equal(t, `{"token":"dev-1","document":"x"}`, payload) }},
		"entity-deleted": {streams.EntityDeleted, `{"token":"dev-1"}`,
			func(t *testing.T, payload string) { require.Equal(t, `{"token":"dev-1"}`, payload) }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p, _ := testProducer(t, "rec-redact-"+name)
			letters := &fakeWriter{}
			r := &maxDeliveryRecorder{
				producer:  p,
				sink:      &Sink{writer: letters, source: p.source, onLoss: func(error) {}},
				maxLetter: 1 << 20,
			}
			d := abandoned(c.suffix, true)
			d.Original = &nats.RawStreamMsg{Subject: messaging.ScopedSubject("test", "acme", c.suffix),
				Data: []byte(c.body), Header: nats.Header{}}
			outcome, err := r.record(context.Background(), d)
			require.NoError(t, err)
			require.Equal(t, messaging.MaxDeliveryLettered, outcome)
			c.want(t, string(lastLetter(t, letters).Payload))
		})
	}
}

// The size check is made on what would be stored, after redaction: a body whose redacted
// form fits is kept (as its description), not dropped for the size of the original.
func TestRecorderSizeChecksTheRedactedPayload(t *testing.T) {
	p, _ := testProducer(t, "rec-size")
	letters := &fakeWriter{}
	r := &maxDeliveryRecorder{
		producer:  p,
		sink:      &Sink{writer: letters, source: p.source, onLoss: func(error) {}},
		maxLetter: payloadOverhead + 4096,
	}
	d := abandoned(streams.DeviceReports, true)
	d.Original = &nats.RawStreamMsg{Subject: messaging.ScopedSubject("test", "acme", streams.DeviceReports),
		Data: []byte("not json " + strings.Repeat("x", 1<<20)), Header: nats.Header{}}
	_, err := r.record(context.Background(), d)
	require.NoError(t, err)
	got := lastLetter(t, letters)
	require.NotEmpty(t, got.Payload, "the redacted description fits and must be kept")
	require.Contains(t, string(got.Payload), `"sha256"`)
}

// Authorship is read from the origin stream, so a letter about a device-authored stream is
// redacted whatever its kind - the path a stream added with a control-fact kind takes.
func TestWriteForRedactsADeviceAuthoredOrigin(t *testing.T) {
	p, _ := testProducer(t, "device-management")
	w := &fakeWriter{}
	msg := messaging.NewConsumedMessage("test.acme.device-reports.dev-1", []byte(`{}`), 5, nil, nil).
		WithOrigin(messaging.Origin{Suffix: streams.DeviceReports, Stream: "test_device-reports",
			Consumer: "test_device-management_device-reports", Seq: 3})
	e := armEnvelope()
	e.Payload = []byte(`{"state":"ok","password":"` + fixtureSecret + `"}`)
	require.NoError(t, p.NewSink(w).WriteFor(tenantCtx(), msg, e))
	got := lastLetter(t, w)
	require.Equal(t, KindControlFact, got.Kind)
	require.NotContains(t, string(got.Payload), fixtureSecret)
	require.Contains(t, string(got.Payload), `"state":"ok"`)
}

// FAIL CLOSED: a stream nobody declared platform-authored is redacted, and only the pinned
// set of declared streams keeps a payload verbatim.
func TestPayloadForIsFailClosedOnAnUndeclaredStream(t *testing.T) {
	body := []byte(`{"state":"ok","credentialSecret":"` + fixtureSecret + `"}`)
	for _, suffix := range []string{"a-stream-added-later", "unknown"} {
		require.NotContains(t, string(payloadFor(suffix, KindControlFact, body)), fixtureSecret, suffix)
	}
	for _, st := range streams.All {
		kept := string(payloadFor(st.Suffix, KindControlFact, body))
		if st.PlatformAuthored {
			require.Equal(t, string(body), kept, st.Suffix)
		} else {
			require.NotContains(t, kept, fixtureSecret, st.Suffix)
		}
	}
	// With no origin stream the kind decides: a kind any device-authored stream shares is redacted.
	require.NotContains(t, string(payloadFor("", KindCommandResponse, body)), fixtureSecret)
	require.NotContains(t, string(payloadFor("", Kind("not-a-kind"), body)), fixtureSecret)
}

func TestPayloadForLeavesEmptyPayloadsEmpty(t *testing.T) {
	require.Nil(t, payloadFor(streams.CommandResponses, KindCommandResponse, nil))
	require.Empty(t, payloadFor(streams.CommandResponses, KindCommandResponse, []byte{}))
}

func TestACommandResponseLetterIsBounded(t *testing.T) {
	p, _ := testProducer(t, "command-delivery")
	w := &fakeWriter{}
	e := good()
	e.Kind = KindCommandResponse
	e.Payload = []byte(`{"credentialSecret":"` + fixtureSecret + `","note":"` + strings.Repeat("x", 4*payloadredact.DiagnosticMaxBytes) + `"}`)
	require.NoError(t, p.NewSink(w).Write(tenantCtx(), e))
	got := lastLetter(t, w)
	require.NotContains(t, string(got.Payload), fixtureSecret)
	require.LessOrEqual(t, len(got.Payload), payloadredact.DiagnosticMaxBytes+64)
}

// An ACCESS_TOKEN credential's id IS the bearer token, so a device-authored body that carries
// one keeps no trace of it on ANY of the three paths a letter is written by: Write (the kind
// decides), WriteFor (the origin stream decides) and the max-delivery recorder. Each scan is
// over the WHOLE letter as it went onto the stream, not its Payload field: a token copied into
// any other part of the envelope is the same leak.
func TestALetterKeepsNoAccessTokenCredentialIdOnAnyWritePath(t *testing.T) {
	const token = "fixture-access-token-bearer-value"
	body := []byte(`{"correlationId":"c-1","credentialType":"access-token","credentialId":"` + token +
		`","payload":{"inner":[{"Credential_ID":"` + token + `"}]}}`)
	requireNoToken := func(t *testing.T, w *fakeWriter) {
		t.Helper()
		require.NotEmpty(t, w.got)
		// The envelope carries Payload as base64, so the raw letter alone is blind to it: a
		// scan of the letter bytes passes with the token intact inside the payload. Both the
		// letter and its decoded payload are scanned.
		raw := string(w.got[len(w.got)-1].Value)
		payload := string(lastLetter(t, w).Payload)
		require.NotContains(t, raw, token)
		require.NotContains(t, payload, token)
		require.Contains(t, payload, `"credentialType":"access-token"`, "the type is kept: it is the diagnostic")
	}
	t.Run("Write", func(t *testing.T) {
		p, _ := testProducer(t, "command-delivery")
		w := &fakeWriter{}
		e := good()
		e.Kind = KindCommandResponse
		e.Payload = body
		require.NoError(t, p.NewSink(w).Write(tenantCtx(), e))
		requireNoToken(t, w)
	})
	t.Run("WriteFor", func(t *testing.T) {
		p, _ := testProducer(t, "device-management")
		w := &fakeWriter{}
		msg := messaging.NewConsumedMessage("test.acme.device-reports.dev-1", body, 5, nil, nil).
			WithOrigin(messaging.Origin{Suffix: streams.DeviceReports, Stream: "test_device-reports",
				Consumer: "test_device-management_device-reports", Seq: 3})
		e := armEnvelope()
		e.Payload = body
		require.NoError(t, p.NewSink(w).WriteFor(tenantCtx(), msg, e))
		requireNoToken(t, w)
	})
	t.Run("recorder", func(t *testing.T) {
		p, _ := testProducer(t, "rec-redact-credential-id")
		w := &fakeWriter{}
		r := &maxDeliveryRecorder{producer: p, sink: &Sink{writer: w, source: p.source, onLoss: func(error) {}},
			maxLetter: 1 << 20}
		d := abandoned(streams.DeviceReports, true)
		d.Original = &nats.RawStreamMsg{Subject: messaging.ScopedSubject("test", "acme", streams.DeviceReports),
			Data: body, Header: nats.Header{}}
		outcome, err := r.record(context.Background(), d)
		require.NoError(t, err)
		require.Equal(t, messaging.MaxDeliveryLettered, outcome)
		requireNoToken(t, w)
	})
}

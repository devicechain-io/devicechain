// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletter

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
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

// The recorder letters a cold device-shaped stream's original with the credential removed,
// and a cold platform-shaped stream's original untouched.
func TestRecorderRedactsOnlyDeviceAuthoredCopies(t *testing.T) {
	cases := map[string]struct {
		suffix, body string
		want         func(t *testing.T, payload string)
	}{
		"device-shaped": {streams.DeviceReports, `{"state":"ok","credentialSecret":"` + fixtureSecret + `"}`,
			func(t *testing.T, payload string) {
				require.NotContains(t, payload, fixtureSecret)
				require.Contains(t, payload, `"state":"ok"`)
			}},
		"platform-shaped": {streams.EntityDeleted, `{"token":"dev-1"}`,
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
			d.At = time.Now()
			outcome, err := r.record(context.Background(), d)
			require.NoError(t, err)
			require.Equal(t, messaging.MaxDeliveryLettered, outcome)
			c.want(t, string(lastLetter(t, letters).Payload))
		})
	}
}

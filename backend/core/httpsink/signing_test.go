// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package httpsink

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/webhooksig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const signingSecretA = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
const signingSecretB = "whsec_CwwNDg8QERITFBUWFxgZGhscHR4fICEi"

type captured struct {
	header http.Header
	body   []byte
	hits   int
}

func capturingServer(t *testing.T) (*httptest.Server, *captured) {
	t.Helper()
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.hits++
		c.header = r.Header.Clone()
		c.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

// The receiver verifies what the sink sent, with the verifier the docs hand out: a body the
// signature does not cover, or a timestamp it does not cover, would fail here.
func TestSignedSendVerifiesAtTheReceiver(t *testing.T) {
	srv, got := capturingServer(t)
	at := time.Unix(1700000000, 0)
	body := []byte(`{"alarm":"a1","state":"raised"}`)

	require.NoError(t, Send(context.Background(), srv.Client(), Request{
		URL: srv.URL, Body: body, Auth: Auth{Mode: AuthNone},
		Signing: &Signing{ID: "msg_1", Secrets: []string{signingSecretA}, At: at},
	}))

	assert.Equal(t, "msg_1", got.header.Get(webhooksig.HeaderID))
	assert.Equal(t, "1700000000", got.header.Get(webhooksig.HeaderTimestamp))
	require.NoError(t, webhooksig.Verify(got.header, got.body, at, 0, signingSecretA))
	assert.ErrorIs(t, webhooksig.Verify(got.header, append(got.body, ' '), at, 0, signingSecretA), webhooksig.ErrBadSignature)
}

func TestSignedSendCarriesBothSignaturesDuringRotation(t *testing.T) {
	srv, got := capturingServer(t)
	at := time.Unix(1700000000, 0)
	require.NoError(t, Send(context.Background(), srv.Client(), Request{
		URL: srv.URL, Body: []byte(`{}`), Auth: Auth{Mode: AuthNone},
		Signing: &Signing{ID: "msg_1", Secrets: []string{signingSecretA, signingSecretB}, At: at},
	}))
	assert.NoError(t, webhooksig.Verify(got.header, got.body, at, 0, signingSecretA))
	assert.NoError(t, webhooksig.Verify(got.header, got.body, at, 0, signingSecretB))
}

// Each call stamps a fresh timestamp (a retry is a new attempt) and keeps the id.
func TestSignedSendStampsNowWhenAtIsZero(t *testing.T) {
	srv, got := capturingServer(t)
	before := time.Now()
	require.NoError(t, Send(context.Background(), srv.Client(), Request{
		URL: srv.URL, Body: []byte(`{}`), Auth: Auth{Mode: AuthNone},
		Signing: &Signing{ID: "msg_1", Secrets: []string{signingSecretA}},
	}))
	require.NoError(t, webhooksig.Verify(got.header, got.body, time.Now(), time.Minute, signingSecretA))
	ts := got.header.Get(webhooksig.HeaderTimestamp)
	assert.NotEmpty(t, ts)
	assert.GreaterOrEqual(t, time.Now().Unix()+1, before.Unix())
}

// A configured header named like a signature header cannot stand in for the real one.
func TestSignedSendOverridesConfiguredWebhookHeaders(t *testing.T) {
	srv, got := capturingServer(t)
	at := time.Unix(1700000000, 0)
	require.NoError(t, Send(context.Background(), srv.Client(), Request{
		URL: srv.URL, Body: []byte(`{}`), Auth: Auth{Mode: AuthNone},
		Headers: map[string]string{"Webhook-Signature": "v1,forged", "webhook-id": "forged", "WEBHOOK-TIMESTAMP": "1"},
		Signing: &Signing{ID: "msg_1", Secrets: []string{signingSecretA}, At: at},
	}))
	assert.Len(t, got.header.Values(webhooksig.HeaderSignature), 1)
	assert.NoError(t, webhooksig.Verify(got.header, got.body, at, 0, signingSecretA))
}

// A delivery meant to be signed that cannot be is refused before anything is sent: never
// delivered unsigned.
func TestSignedSendRefusesWhatItCannotSign(t *testing.T) {
	srv, got := capturingServer(t)
	for name, s := range map[string]*Signing{
		"no id":       {Secrets: []string{signingSecretA}},
		"no secrets":  {ID: "m"},
		"bad secret":  {ID: "m", Secrets: []string{"whsec_short"}},
		"one bad key": {ID: "m", Secrets: []string{signingSecretA, "nope"}},
		"bad id":      {ID: "m\r\nX: y", Secrets: []string{signingSecretA}},
	} {
		err := Send(context.Background(), srv.Client(), Request{
			URL: srv.URL, Body: []byte(`{}`), Auth: Auth{Mode: AuthNone}, Signing: s,
		})
		assert.ErrorIs(t, err, ErrSigningRefused, name)
	}
	assert.Zero(t, got.hits, "nothing reached the endpoint")
}

// The counterweight: with no Signing the request carries none of the three headers, whatever the
// package now knows how to do.
func TestUnsignedSendCarriesNoWebhookHeaders(t *testing.T) {
	srv, got := capturingServer(t)
	require.NoError(t, Send(context.Background(), srv.Client(), Request{
		URL: srv.URL, Body: []byte(`{}`), Auth: Auth{Mode: AuthNone},
	}))
	for _, h := range []string{webhooksig.HeaderID, webhooksig.HeaderTimestamp, webhooksig.HeaderSignature} {
		assert.Empty(t, got.header.Values(h), h)
	}
}

// Golden: the exact header set of an unsigned delivery, so a change that adds, drops or renames a
// header on the unsigned path fails. Go's transport adds Accept-Encoding, User-Agent and
// Content-Length, which are stable for the pinned toolchain's client.
func TestUnsignedSendHeaderSetIsGolden(t *testing.T) {
	srv, got := capturingServer(t)
	require.NoError(t, Send(context.Background(), srv.Client(), Request{
		URL: srv.URL, Body: []byte(`{"a":1}`), Secret: "tok", Auth: Auth{Mode: AuthBearer},
		Headers: map[string]string{"X-Custom": "v"}, IdempotencyKey: "k1",
	}))
	want := map[string]string{
		"Authorization":        "Bearer tok",
		"Content-Type":         "application/json",
		"X-Custom":             "v",
		"X-Dc-Idempotency-Key": "k1",
	}
	for k, v := range want {
		assert.Equal(t, v, got.header.Get(k), k)
	}
	for k := range got.header {
		switch k {
		case "Authorization", "Content-Type", "X-Custom", "X-Dc-Idempotency-Key", "User-Agent", "Content-Length", "Accept-Encoding":
		default:
			t.Errorf("unexpected header on an unsigned delivery: %s", k)
		}
	}
	assert.Equal(t, `{"a":1}`, string(got.body))
}

// A credential header that is also a signing header would be overwritten by the signature while
// the request still went out. It is refused before a request exists, for every signing header and
// any casing; an unsigned send with the same auth is untouched (the control).
func TestSigningRefusesAnAuthHeaderItWouldOverwrite(t *testing.T) {
	srv, got := capturingServer(t)
	for _, h := range []string{"webhook-signature", "Webhook-Id", "WEBHOOK-TIMESTAMP"} {
		err := Send(context.Background(), srv.Client(), Request{
			URL: srv.URL, Body: []byte(`{}`), Auth: Auth{Mode: AuthHeader, Header: h}, Secret: "s3cret",
			Signing: &Signing{ID: "m", Secrets: []string{signingSecretA}},
		})
		assert.ErrorIs(t, err, ErrSigningRefused, h)
	}
	assert.Zero(t, got.hits, "nothing reached the wire")

	require.NoError(t, Send(context.Background(), srv.Client(), Request{
		URL: srv.URL, Body: []byte(`{}`), Auth: Auth{Mode: AuthHeader, Header: "webhook-signature"}, Secret: "s3cret",
	}))
	require.NoError(t, Send(context.Background(), srv.Client(), Request{
		URL: srv.URL, Body: []byte(`{}`), Auth: Auth{Mode: AuthHeader, Header: "X-Api-Key"}, Secret: "k",
		Signing: &Signing{ID: "m", Secrets: []string{signingSecretA}},
	}))
	assert.Equal(t, 2, got.hits)
}

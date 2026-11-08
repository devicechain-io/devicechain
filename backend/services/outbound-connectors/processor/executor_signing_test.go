// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	"github.com/devicechain-io/dc-microservice/httpsink"
	"github.com/devicechain-io/dc-microservice/webhooksig"
	"github.com/devicechain-io/dc-outbound-connectors/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	keyNew = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	keyOld = "whsec_CwwNDg8QERITFBUWFxgZGhscHR4fICEi"
)

type seen struct {
	mu     sync.Mutex
	hits   int
	header http.Header
	body   []byte
}

func (s *seen) get() (int, http.Header, []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits, s.header, s.body
}

func recordingEndpoint(t *testing.T) (*httptest.Server, *seen) {
	t.Helper()
	s := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.hits++
		s.header = r.Header.Clone()
		s.body = b
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, s
}

func signingStore(current, previous string) *fakeSecretStore {
	cur, prev := webhooksig.KeyNames(model.WebhookSigningBase("hooks/prod"))
	values := map[string][]byte{}
	if current != "" {
		values[cur] = []byte(current)
	}
	if previous != "" {
		values[prev] = []byte(previous)
	}
	return &fakeSecretStore{values: values}
}

func signedCall(url string) *connectorwire.ConnectorDispatchRequest {
	return &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme", IdempotencyKey: "0123abcd",
		Payload:  `{"alarm":"overheat","state":"raised"}`,
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: url, SigningSecretRef: "hooks/prod"},
	}
}

// The delivery verifies at the receiver under the stored key, over the exact rendered payload,
// and its webhook-id is the dispatch idempotency key so a redelivery keeps the same id.
func TestSignedHTTPCallVerifiesAtTheReceiver(t *testing.T) {
	srv, got := recordingEndpoint(t)
	res := newTestExecutor(signingStore(keyNew, "")).Execute(context.Background(), signedCall(srv.URL))
	require.Equal(t, outcomeSent, res.outcome, "%v", res.err)

	hits, h, body := got.get()
	require.Equal(t, 1, hits)
	assert.Equal(t, `{"alarm":"overheat","state":"raised"}`, string(body))
	assert.Equal(t, "0123abcd", h.Get(webhooksig.HeaderID))
	require.NoError(t, webhooksig.Verify(h, body, time.Now(), 0, keyNew))
	assert.Len(t, h.Values(webhooksig.HeaderSignature), 1)
	assert.ErrorIs(t, webhooksig.Verify(h, append(body, '!'), time.Now(), 0, keyNew), webhooksig.ErrBadSignature)
}

// The rotation overlap: with a previous key stored the delivery carries two signatures, so a
// receiver on either key verifies it; with the rotation ended it carries one.
func TestSignedHTTPCallCarriesBothSignaturesDuringRotation(t *testing.T) {
	srv, got := recordingEndpoint(t)
	res := newTestExecutor(signingStore(keyNew, keyOld)).Execute(context.Background(), signedCall(srv.URL))
	require.Equal(t, outcomeSent, res.outcome, "%v", res.err)

	_, h, body := got.get()
	require.NoError(t, webhooksig.Verify(h, body, time.Now(), 0, keyNew), "receiver already on the new key")
	require.NoError(t, webhooksig.Verify(h, body, time.Now(), 0, keyOld), "receiver still on the old key")
	assert.Len(t, strings.Fields(h.Get(webhooksig.HeaderSignature)), 2)
}

// A handle that names no stored key is a promise the call cannot keep: it is terminal, and the
// endpoint is never reached, so nothing goes out unsigned.
func TestSignedHTTPCallWithNoKeyIsRefusedNotSentUnsigned(t *testing.T) {
	srv, got := recordingEndpoint(t)
	res := newTestExecutor(signingStore("", "")).Execute(context.Background(), signedCall(srv.URL))

	assert.Equal(t, outcomeInvalid, res.outcome)
	assert.False(t, res.retryable)
	assert.ErrorIs(t, res.err, httpsink.ErrSigningRefused)
	assert.ErrorIs(t, res.err, webhooksig.ErrNoSigningSecret)
	hits, _, _ := got.get()
	assert.Zero(t, hits)
}

func TestSignedHTTPCallWithACorruptKeyIsTerminal(t *testing.T) {
	srv, got := recordingEndpoint(t)
	res := newTestExecutor(signingStore("not-a-signing-secret", "")).Execute(context.Background(), signedCall(srv.URL))
	assert.Equal(t, outcomeInvalid, res.outcome)
	assert.False(t, res.retryable)
	hits, _, _ := got.get()
	assert.Zero(t, hits)
}

// A signed call that has no message id to sign under (a forged or corrupt request carrying no
// idempotency key) is refused at the sink, terminally, and nothing reaches the endpoint.
func TestSignedHTTPCallWithoutAMessageIDIsTerminal(t *testing.T) {
	srv, got := recordingEndpoint(t)
	req := signedCall(srv.URL)
	req.IdempotencyKey = ""
	res := newTestExecutor(signingStore(keyNew, "")).Execute(context.Background(), req)

	assert.Equal(t, outcomeInvalid, res.outcome)
	assert.False(t, res.retryable)
	assert.ErrorIs(t, res.err, httpsink.ErrSigningRefused)
	hits, _, _ := got.get()
	assert.Zero(t, hits)
}

// A store failure is transient: retried, and never answered with an unsigned send.
func TestSignedHTTPCallStoreErrorIsRetryableAndSendsNothing(t *testing.T) {
	srv, got := recordingEndpoint(t)
	cur, _ := webhooksig.KeyNames(model.WebhookSigningBase("hooks/prod"))
	store := &fakeSecretStore{failRef: cur, err: errors.New("db down")}
	res := newTestExecutor(store).Execute(context.Background(), signedCall(srv.URL))

	assert.Equal(t, outcomeRetry, res.outcome)
	assert.True(t, res.retryable)
	hits, _, _ := got.get()
	assert.Zero(t, hits)
}

// A failing previous slot must not silently drop to one signature: a receiver still on the old
// key would start rejecting deliveries.
func TestSignedHTTPCallFailingPreviousSlotIsRetryable(t *testing.T) {
	srv, got := recordingEndpoint(t)
	_, prev := webhooksig.KeyNames(model.WebhookSigningBase("hooks/prod"))
	store := signingStore(keyNew, "")
	store.failRef, store.err = prev, errors.New("db down")
	res := newTestExecutor(store).Execute(context.Background(), signedCall(srv.URL))

	assert.Equal(t, outcomeRetry, res.outcome)
	hits, _, _ := got.get()
	assert.Zero(t, hits)
}

// The key lives under a namespace of its own: a handle that happens to equal a Bearer secret
// handle does not resolve to it.
func TestSigningHandleDoesNotAddressOtherSecrets(t *testing.T) {
	srv, got := recordingEndpoint(t)
	store := &fakeSecretStore{values: map[string][]byte{"hooks/prod/current": []byte(keyNew), "hooks/prod": []byte(keyNew)}}
	res := newTestExecutor(store).Execute(context.Background(), signedCall(srv.URL))
	assert.Equal(t, outcomeInvalid, res.outcome)
	hits, _, _ := got.get()
	assert.Zero(t, hits)
}

// Golden: an unsigned call is byte-for-byte what it was before signing existed. It carries none of
// the signature headers and never consults the store for a key.
func TestUnsignedHTTPCallCarriesNoSignatureAndTouchesNoKey(t *testing.T) {
	srv, got := recordingEndpoint(t)
	store := signingStore(keyNew, "")
	req := signedCall(srv.URL)
	req.HTTPCall.SigningSecretRef = ""
	res := newTestExecutor(store).Execute(context.Background(), req)
	require.Equal(t, outcomeSent, res.outcome, "%v", res.err)

	_, h, body := got.get()
	for _, name := range []string{webhooksig.HeaderID, webhooksig.HeaderTimestamp, webhooksig.HeaderSignature} {
		assert.Empty(t, h.Values(name), name)
	}
	assert.Equal(t, `{"alarm":"overheat","state":"raised"}`, string(body))
	assert.Zero(t, store.resolveCalls())
	for _, k := range []string{"Content-Type", "X-Dc-Idempotency-Key", "User-Agent", "Content-Length", "Accept-Encoding"} {
		assert.NotEmpty(t, h.Get(k), k)
	}
	assert.Len(t, h, 5, "exactly the headers an unsigned call always carried")
}

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

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/httpsink"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-microservice/webhooksig"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	sigKeyNew = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	sigKeyOld = "whsec_CwwNDg8QERITFBUWFxgZGhscHR4fICEi"
)

// namedStore is a SecretStore keyed by (tenant, name), so a test can place a key exactly where
// the write path would and see whether the adapter finds it.
type namedStore struct {
	values map[secrets.SecretRef]string
	err    map[string]error
}

func (s *namedStore) Resolve(_ context.Context, r secrets.SecretRef) ([]byte, error) {
	if e := s.err[r.Name]; e != nil {
		return nil, e
	}
	if v, ok := s.values[r]; ok {
		return []byte(v), nil
	}
	return nil, secrets.ErrSecretNotFound
}
func (s *namedStore) Put(context.Context, secrets.SecretRef, []byte) error    { return nil }
func (s *namedStore) Rotate(context.Context, secrets.SecretRef, []byte) error { return nil }
func (s *namedStore) Delete(context.Context, secrets.SecretRef) error         { return nil }
func (s *namedStore) Exists(context.Context, secrets.SecretRef) (bool, error) { return false, nil }

func signingChannel(id uint, config string) *model.NotificationChannel {
	c := channelWith("hook", model.ChannelTypeWebhook, config)
	c.ID = id
	return c
}

func keyAt(tenant string, id uint, slot string, value string) (secrets.SecretRef, string) {
	cur, prev := webhooksig.KeyNames(model.ChannelSigningBase(id))
	name := cur
	if slot == "previous" {
		name = prev
	}
	return secrets.SecretRef{Scope: secrets.ScopeTenant, Tenant: tenant, Name: name}, value
}

func storeWith(tenant string, id uint, current, previous string) *namedStore {
	s := &namedStore{values: map[secrets.SecretRef]string{}, err: map[string]error{}}
	if current != "" {
		r, v := keyAt(tenant, id, "current", current)
		s.values[r] = v
	}
	if previous != "" {
		r, v := keyAt(tenant, id, "previous", previous)
		s.values[r] = v
	}
	return s
}

type wireRecord struct {
	mu     sync.Mutex
	hits   int
	header http.Header
	body   []byte
}

func (w *wireRecord) snapshot() (int, http.Header, []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hits, w.header, w.body
}

func recordingServer(t *testing.T) (*httptest.Server, *wireRecord) {
	t.Helper()
	rec := &wireRecord{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.hits++
		rec.header = r.Header.Clone()
		rec.body = b
		rec.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

var signMsg = &RenderedNotification{Payload: map[string]any{"text": "hi", "severity": "CRITICAL"}}

func TestSigningChannelDeliversAVerifiableSignature(t *testing.T) {
	srv, rec := recordingServer(t)
	ctx := withMessageID(core.WithTenant(context.Background(), "acme"), "msg_fixed")
	a := &webhookAdapter{client: srv.Client(), store: storeWith("acme", 7, sigKeyNew, "")}
	ch := signingChannel(7, `{"url":"`+srv.URL+`","auth":"none","signing":true}`)

	require.NoError(t, a.Deliver(ctx, ch, "", nil, signMsg))

	_, h, body := rec.snapshot()
	assert.Equal(t, "msg_fixed", h.Get(webhooksig.HeaderID))
	require.NoError(t, webhooksig.Verify(h, body, time.Now(), 0, sigKeyNew))
	assert.ErrorIs(t, webhooksig.Verify(h, append(body, ' '), time.Now(), 0, sigKeyNew), webhooksig.ErrBadSignature)
}

// The rotation overlap: both keys sign, so a receiver on either verifies.
func TestSigningChannelCarriesTwoSignaturesDuringRotation(t *testing.T) {
	srv, rec := recordingServer(t)
	ctx := withMessageID(core.WithTenant(context.Background(), "acme"), "msg_fixed")
	a := &webhookAdapter{client: srv.Client(), store: storeWith("acme", 7, sigKeyNew, sigKeyOld)}
	ch := signingChannel(7, `{"url":"`+srv.URL+`","auth":"none","signing":true}`)

	require.NoError(t, a.Deliver(ctx, ch, "", nil, signMsg))

	_, h, body := rec.snapshot()
	assert.Len(t, strings.Fields(h.Get(webhooksig.HeaderSignature)), 2)
	assert.NoError(t, webhooksig.Verify(h, body, time.Now(), 0, sigKeyNew))
	assert.NoError(t, webhooksig.Verify(h, body, time.Now(), 0, sigKeyOld))
}

// Signing composes with a Bearer credential: both reach the wire.
func TestSigningChannelStillPresentsItsBearerCredential(t *testing.T) {
	srv, rec := recordingServer(t)
	ctx := withMessageID(core.WithTenant(context.Background(), "acme"), "msg_fixed")
	a := &webhookAdapter{client: srv.Client(), store: storeWith("acme", 7, sigKeyNew, "")}
	ch := signingChannel(7, `{"url":"`+srv.URL+`","auth":"bearer","signing":true}`)

	require.NoError(t, a.Deliver(ctx, ch, "tok", nil, signMsg))
	_, h, body := rec.snapshot()
	assert.Equal(t, "Bearer tok", h.Get("Authorization"))
	assert.NoError(t, webhooksig.Verify(h, body, time.Now(), 0, sigKeyNew))
}

// A channel set to sign that cannot is refused, and the endpoint is never reached.
func TestSigningChannelWithoutAKeyIsRefusedNotSentUnsigned(t *testing.T) {
	srv, rec := recordingServer(t)
	ctx := withMessageID(core.WithTenant(context.Background(), "acme"), "msg_fixed")
	ch := signingChannel(7, `{"url":"`+srv.URL+`","auth":"none","signing":true}`)

	for name, store := range map[string]secrets.SecretStore{
		"no key stored":     storeWith("acme", 7, "", ""),
		"key of another id": storeWith("acme", 8, sigKeyNew, ""),
		"key of a tenant":   storeWith("globex", 7, sigKeyNew, ""),
		"corrupt key":       storeWith("acme", 7, "not-a-secret", ""),
		"no store at all":   nil,
	} {
		a := &webhookAdapter{client: srv.Client(), store: store}
		err := a.Deliver(ctx, ch, "", nil, signMsg)
		assert.ErrorIs(t, err, httpsink.ErrSigningRefused, name)
	}
	hits, _, _ := rec.snapshot()
	assert.Zero(t, hits)
}

// A store failure is not a refusal: it must surface as an ordinary (retried) error.
func TestSigningChannelStoreErrorIsNotARefusal(t *testing.T) {
	srv, rec := recordingServer(t)
	ctx := withMessageID(core.WithTenant(context.Background(), "acme"), "msg_fixed")
	store := storeWith("acme", 7, sigKeyNew, "")
	cur, _ := webhooksig.KeyNames(model.ChannelSigningBase(7))
	store.err[cur] = errors.New("db down")
	a := &webhookAdapter{client: srv.Client(), store: store}
	err := a.Deliver(ctx, signingChannel(7, `{"url":"`+srv.URL+`","auth":"none","signing":true}`), "", nil, signMsg)

	require.Error(t, err)
	assert.NotErrorIs(t, err, httpsink.ErrSigningRefused)
	hits, _, _ := rec.snapshot()
	assert.Zero(t, hits)
}

// Golden: a channel that does not ask to sign delivers exactly as it did before signing existed,
// and never reads the store for a key, even when one is stored.
func TestUnsignedChannelIsUnchangedAndNeverReadsAKey(t *testing.T) {
	srv, rec := recordingServer(t)
	store := storeWith("acme", 7, sigKeyNew, "")
	cur, _ := webhooksig.KeyNames(model.ChannelSigningBase(7))
	store.err[cur] = errors.New("must not be read")
	ctx := withMessageID(core.WithTenant(context.Background(), "acme"), "msg_fixed")
	a := &webhookAdapter{client: srv.Client(), store: store}

	require.NoError(t, a.Deliver(ctx, signingChannel(7, `{"url":"`+srv.URL+`","auth":"none"}`), "", nil, signMsg))

	_, h, body := rec.snapshot()
	for _, name := range []string{webhooksig.HeaderID, webhooksig.HeaderTimestamp, webhooksig.HeaderSignature} {
		assert.Empty(t, h.Values(name), name)
	}
	assert.JSONEq(t, `{"text":"hi","severity":"CRITICAL"}`, string(body))
	for k := range h {
		switch k {
		case "Content-Type", "User-Agent", "Content-Length", "Accept-Encoding":
		default:
			t.Errorf("unexpected header on an unsigned delivery: %s", k)
		}
	}
}

// Through the dispatcher: the refusal is terminal (one attempt, classified refused), and a retry
// of a delivery that does sign reuses ONE webhook-id with a fresh attempt each time.
func TestDispatcherTreatsASigningRefusalAsTerminal(t *testing.T) {
	srv, rec := recordingServer(t)
	ca := &countingAdapter{inner: &webhookAdapter{client: srv.Client(), store: storeWith("acme", 7, "", "")}}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeWebhook: ca})
	refused := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "refused_signing_test"}, []string{"reason"})
	n.refused = refused
	n.timeout = 2 * time.Second
	d := delivery{channel: signingChannel(7, `{"url":"`+srv.URL+`","auth":"none","signing":true}`)}

	got := n.deliverWithRetry(core.WithTenant(context.Background(), "acme"), d, &RenderedNotification{Payload: map[string]any{}})
	assert.Equal(t, deliveryRefused, got)
	assert.Equal(t, 1.0, counterValue(t, refused, refusalReasonSigning), "counted as a signing refusal")
	assert.Equal(t, 0.0, counterValue(t, refused, refusalReasonCredential))
	assert.Equal(t, 1, ca.calls)
	hits, _, _ := rec.snapshot()
	assert.Zero(t, hits)
}

type idRecordingAdapter struct {
	ids   []string
	fails int
}

func (a *idRecordingAdapter) Deliver(ctx context.Context, _ *model.NotificationChannel, _ string, _ []string, _ *RenderedNotification) error {
	a.ids = append(a.ids, messageIDFrom(ctx))
	if len(a.ids) <= a.fails {
		return errors.New("endpoint down")
	}
	return nil
}

func TestRetriesOfOneDeliveryShareAWebhookIDAndDeliveriesDoNot(t *testing.T) {
	ad := &idRecordingAdapter{fails: 2}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeWebhook: ad})
	n.attempts = 3
	n.timeout = time.Second
	d := delivery{channel: signingChannel(7, `{"url":"https://example.invalid","auth":"none"}`)}

	require.Equal(t, deliveryOK, n.deliverWithRetry(context.Background(), d, &RenderedNotification{Payload: map[string]any{}}))
	require.Len(t, ad.ids, 3)
	assert.NotEmpty(t, ad.ids[0])
	assert.Equal(t, ad.ids[0], ad.ids[1])
	assert.Equal(t, ad.ids[0], ad.ids[2])

	first := ad.ids[0]
	ad.ids, ad.fails = nil, 0
	require.Equal(t, deliveryOK, n.deliverWithRetry(context.Background(), d, &RenderedNotification{Payload: map[string]any{}}))
	assert.NotEqual(t, first, ad.ids[0], "a new delivery is a new message")
	assert.True(t, strings.HasPrefix(ad.ids[0], "msg_"))
}

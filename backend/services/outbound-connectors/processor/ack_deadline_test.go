// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-outbound-connectors/config"
)

// capacityMessage reads one message through a REAL capacity reader over an embedded JetStream
// whose durables use the given AckWait, and returns it. Its AckDeadline is the broker's: the
// fetch time plus that AckWait. There is no other way to get one — the deadline is carried only
// by a message a capacity reader produced, which is the property being relied on.
func capacityMessage(t *testing.T, ackWait time.Duration) messaging.Message {
	t.Helper()
	return capacityMessageWith(t, ackWait, []byte("x"))
}

// capacityMessageWith is capacityMessage carrying the given body.
func capacityMessageWith(t *testing.T, ackWait time.Duration, body []byte) messaging.Message {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)
	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	area := fmt.Sprintf("ackdeadline-%d", time.Now().UnixNano())
	ms := &core.Microservice{InstanceId: area, FunctionalArea: "outbound-connectors"}
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: u.Hostname(), Port: uint32(port)}

	var reader messaging.MessageReader
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(m *messaging.NatsManager) error {
		r, err := m.NewReader(streams.ConnectorDispatch, messaging.ReaderWithCapacity(1))
		reader = r
		return err
	})
	nmgr.SetAckWaitForTesting(t, ackWait)
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	t.Cleanup(func() {
		if c := nmgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})

	writer, err := nmgr.NewWriter(streams.ConnectorDispatch)
	require.NoError(t, err)
	require.NoError(t, writer.WriteMessages(core.WithTenant(context.Background(), "acme"),
		messaging.Message{Value: body}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg, err := reader.ReadMessage(ctx)
	require.NoError(t, err)
	t.Cleanup(msg.Release)
	require.False(t, msg.AckDeadline().IsZero(), "a capacity reader's message must carry an AckDeadline")
	return msg
}

// deadlineRecordingTransport records the deadline on the context of every request it carries,
// then sends it on through the loopback-only egress transport the other tests use.
type deadlineRecordingTransport struct {
	next http.RoundTripper

	mu       sync.Mutex
	deadline time.Time
	had      bool
}

func (d *deadlineRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.deadline, d.had = req.Context().Deadline()
	d.mu.Unlock()
	return d.next.RoundTrip(req)
}

// One dispatch — the longest rate wait, the secret resolve, the longest send and the margin —
// fits inside AckWait. This is the arithmetic MaxEgressWaitBudgetMs's comment states, checked
// against the constants that enforce each term, so moving any of them past the bound goes red
// here rather than surfacing as duplicate sends.
func TestOneDispatchFitsAckWait(t *testing.T) {
	wait := time.Duration(config.MaxEgressWaitBudgetMs) * time.Millisecond
	send := time.Duration(connectorwire.MaxTimeoutMs) * time.Millisecond
	total := wait + secretResolveTimeout + send + sendMargin
	require.Equal(t, send, maxSend, "maxSend must be the shared per-send ceiling")
	require.Less(t, total, messaging.AckWait,
		"waitBudget (%v) + secretResolve (%v) + maxSend (%v) + sendMargin (%v) = %v does not fit "+
			"AckWait (%v): a dispatch at its limits would still be sending when the broker redelivers it",
		wait, secretResolveTimeout, send, sendMargin, total, messaging.AckWait)
}

// The executor's send ends sendMargin before the message's AckDeadline when that is sooner than
// the send's own timeout.
//
// The message's AckWait is 10s, so its AckDeadline is ~10s out and the cap lands ~5s out — well
// inside the 20s send timeout the action asks for. The instrument is the deadline the HTTP
// request actually carries, read at the transport: the exact value, not "some deadline".
func TestSendIsCappedByAckDeadline(t *testing.T) {
	msg := capacityMessage(t, 10*time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	t.Cleanup(srv.Close)

	rec := &deadlineRecordingTransport{next: loopbackClient().Transport}
	e := NewExecutor(NewSecretResolver(&fakeSecretStore{}), nil, &http.Client{Transport: rec}, 5*time.Second)
	ctx := messaging.WithAckDeadline(core.WithTenant(context.Background(), "acme"), msg)

	res := e.Execute(ctx, &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme",
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: srv.URL, TimeoutMs: connectorwire.MaxTimeoutMs},
	})
	require.NoError(t, res.err)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.True(t, rec.had, "the send carried no deadline")
	require.Equal(t, msg.AckDeadline().Add(-sendMargin), rec.deadline,
		"the send's deadline must be the message's AckDeadline less sendMargin")

	// The counterweight: without an AckDeadline the send keeps its own timeout.
	rec.had = false
	rec.mu.Unlock()
	start := time.Now()
	res = e.Execute(core.WithTenant(context.Background(), "acme"), &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme",
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: srv.URL, TimeoutMs: connectorwire.MaxTimeoutMs},
	})
	rec.mu.Lock()
	require.NoError(t, res.err)
	require.True(t, rec.had)
	got := rec.deadline.Sub(start)
	require.InDelta(t, float64(maxSend), float64(got), float64(time.Second),
		"without an AckDeadline the send must keep its own %v timeout, got %v", maxSend, got)
}

// The rate wait ends early enough for a resolve and a send to finish before redelivery, and only
// a message carrying an AckDeadline is capped.
func TestRateWaitIsCappedByAckDeadline(t *testing.T) {
	c := newTestConsumerWithRate(&fakeWriter{}, &fakeSecretStore{}, nil, 8*time.Second)
	now := time.Now()

	// No AckDeadline: the budget alone.
	d, capped := c.rateWaitDeadline(context.Background(), now)
	require.False(t, capped)
	require.Equal(t, now.Add(8*time.Second), d)

	// An AckDeadline 60s out leaves 60 − 5 − 5 − 20 = 30s: the 8s budget is tighter.
	far := capacityMessage(t, 60*time.Second)
	d, capped = c.rateWaitDeadline(messaging.WithAckDeadline(context.Background(), far), now)
	require.False(t, capped)
	require.Equal(t, now.Add(8*time.Second), d)

	// An AckDeadline 35s out leaves 5s: the AckDeadline sets the wait.
	near := capacityMessage(t, 35*time.Second)
	d, capped = c.rateWaitDeadline(messaging.WithAckDeadline(context.Background(), near), now)
	require.True(t, capped)
	require.Equal(t, near.AckDeadline().Add(-(sendMargin + secretResolveTimeout + maxSend)), d)
}

// A rate wait cut short by the message's redelivery deadline leaves the dispatch for that
// redelivery. It is NOT a rate shed: the tenant was not shown to be over quota for the wait
// budget, only out of time on this delivery, so dead-lettering it as shed would record healthy work
// as refused.
//
// The message's AckWait is 31s, so its wait may run only ~1s (31 − 5 − 5 − 20) of the 8s budget,
// and the limiter has no token to give for ~1000s, so the wait fails either way (the limiter refuses
// at once a wait its deadline cannot cover). What differs is the disposition: with the budget alone
// in force it is a shed and is dead-lettered; capped, it is left for redelivery. The instrument is
// therefore the dead-letter subject, which must stay empty.
func TestRateWaitCutByAckDeadlineIsNotAShed(t *testing.T) {
	rl := core.NewTenantRateLimiter(func(string) (float64, int) { return 0.001, 1 })
	drain, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_ = rl.Wait(drain, "acme")
	cancel()

	body, err := connectorwire.MarshalConnectorDispatchRequest(&connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme",
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: "http://127.0.0.1:1/unused"},
	})
	require.NoError(t, err)
	msg := capacityMessageWith(t, 31*time.Second, body)
	require.Equal(t, 1, msg.NumDelivered)

	dead := &fakeWriter{}
	c := newTestConsumerWithRate(dead, &fakeSecretStore{}, rl, 8*time.Second)
	start := time.Now()
	c.handle(context.Background(), msg)
	elapsed := time.Since(start)

	require.Empty(t, dead.written(), "a wait cut by the redelivery deadline was dead-lettered as a shed")
	require.Less(t, elapsed, 4*time.Second, "the wait ran %v, past the AckDeadline cap", elapsed)
}

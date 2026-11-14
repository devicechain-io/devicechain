// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/require"
)

type flightReader struct{ ch chan messaging.Message }

func (r *flightReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	select {
	case m := <-r.ch:
		return m, nil
	case <-ctx.Done():
		return messaging.Message{}, ctx.Err()
	}
}
func (*flightReader) HandleResponse(error) {}

func TestSlowTenantLeavesWorkersForOtherTenants(t *testing.T) {
	started, fast, release := make(chan struct{}, 3), make(chan struct{}, 3), make(chan struct{})
	var once sync.Once
	var slowHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			slowHits.Add(1)
			started <- struct{}{}
			<-release
		} else {
			fast <- struct{}{}
		}
		w.WriteHeader(http.StatusOK)
	}))
	dead := &fakeWriter{}
	reader := &flightReader{ch: make(chan messaging.Message, 3)}
	executor := NewExecutor(NewSecretResolver(&fakeSecretStore{}), nil, loopbackGuard(), 5*time.Second)
	c := NewDispatchConsumer(reader, dead, nil, testProducer(), executor, nil, time.Second, nil, 2, nil, core.NewReadPacer(nil, "test"))
	require.NoError(t, c.Start(context.Background()))
	defer func() {
		once.Do(func() { close(release) })
		require.NoError(t, c.Stop(context.Background()))
		srv.Close()
	}()
	makeMsg := func(tenant, path string, ack *fakeAck) messaging.Message {
		return wireMsg(t, "inst."+tenant+".connector-dispatch", 1, &connectorwire.ConnectorDispatchRequest{
			Kind: connectorwire.ConnectorKindHTTPCall, Tenant: tenant,
			HTTPCall: &connectorwire.HTTPCallDispatch{URL: srv.URL + path}}, ack)
	}
	first, excess, other := &fakeAck{}, &fakeAck{}, &fakeAck{}
	reader.ch <- makeMsg("acme", "/slow", first)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first tenant did not start")
	}
	retry := makeMsg("acme", "/slow", excess)
	reader.ch <- retry
	reader.ch <- makeMsg("other", "/fast", other)
	select {
	case <-fast:
	case <-time.After(2 * time.Second):
		t.Fatal("another tenant stalled behind the slow tenant")
	}
	require.EqualValues(t, 1, slowHits.Load(), "excess must not send")
	once.Do(func() { close(release) })
	require.NoError(t, c.Stop(context.Background()))
	require.True(t, first.acked)
	require.True(t, other.acked)
	require.False(t, excess.acked, "excess must remain eligible for redelivery")
	require.Empty(t, dead.written(), "concurrency pressure is not a terminal send failure")
	c.handle(context.Background(), retry)
	require.True(t, excess.acked, "completion must release the tenant slot")
	require.EqualValues(t, 2, slowHits.Load())
}

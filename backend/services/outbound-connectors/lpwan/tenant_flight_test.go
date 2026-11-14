// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
	"github.com/stretchr/testify/require"
)

type flightDevices struct{ fakeDevices }

func (*flightDevices) DevEUI(_ context.Context, tenant, _ string) (string, error) {
	return tenant, nil
}

type flightEnqueuer struct {
	started, fast chan struct{}
	release       chan struct{}
	hits          atomic.Int32
}

func (e *flightEnqueuer) Enqueue(_ context.Context, _ connectorspec.LoRaWANDownlinkTarget, eui string, _ Params, _ time.Time) (string, error) {
	e.hits.Add(1)
	if eui == "acme" {
		e.started <- struct{}{}
		<-e.release
	} else {
		e.fast <- struct{}{}
	}
	return "q", nil
}

func TestSlowTenantLeavesDownlinkWorkersForOtherTenants(t *testing.T) {
	r := newRig(t, func(o *Options) { o.Workers = 2 })
	reader := &queueReader{ch: make(chan messaging.Message, 3)}
	sender := &flightEnqueuer{started: make(chan struct{}, 3), fast: make(chan struct{}, 3), release: make(chan struct{})}
	r.a.reader, r.a.sender, r.a.devices = reader, sender, &flightDevices{}
	var once sync.Once
	r.a.Start()
	defer func() {
		once.Do(func() { close(sender.release) })
		r.a.Stop()
	}()
	first, excess, other := &fakeAck{}, &fakeAck{}, &fakeAck{}
	makeMsg := func(tenant string, ack *fakeAck) messaging.Message {
		return messaging.NewConsumedMessage("inst."+tenant+".device-commands.dev-1", envelope(t, goodParams), 1, routed(), ack)
	}
	reader.ch <- makeMsg("acme", first)
	select {
	case <-sender.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first tenant did not start")
	}
	retry := makeMsg("acme", excess)
	reader.ch <- retry
	reader.ch <- makeMsg("other", other)
	select {
	case <-sender.fast:
	case <-time.After(2 * time.Second):
		t.Fatal("another tenant stalled behind the slow network server")
	}
	require.EqualValues(t, 2, sender.hits.Load(), "excess must not send")
	once.Do(func() { close(sender.release) })
	r.a.Stop()
	require.True(t, first.acked)
	require.True(t, other.acked)
	require.False(t, excess.acked)
	require.Equal(t, 2, r.claim.calls, "excess must not consume its dispatch claim")
	require.Len(t, r.resp.envs, 2, "excess must not be answered as a terminal refusal")
	r.a.handle(context.Background(), retry)
	require.True(t, excess.acked, "completion must release the tenant slot")
	require.EqualValues(t, 3, sender.hits.Load())
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	piondtls "github.com/pion/dtls/v3"
	coapdtls "github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-lwm2m-ingest/decode"
	"github.com/devicechain-io/dc-lwm2m-ingest/downlink"
	"github.com/devicechain-io/dc-lwm2m-ingest/server"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// idleReader is a command stream with nothing on it: it blocks until the term ends, which is all
// the dispatcher's Run needs from it to start its shard workers.
type idleReader struct{}

func (idleReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	<-ctx.Done()
	return messaging.Message{}, ctx.Err()
}
func (idleReader) HandleResponse(error) {}

// noBacklog is a drain fetcher for a device with nothing held.
type noBacklog struct{}

func (noBacklog) Pending(context.Context, string, string, int) ([]downlink.DrainCommand, error) {
	return nil, nil
}

// attrSink records the attribute writes the harvest makes.
type attrSink struct {
	mu   sync.Mutex
	sets map[string]string
	typ  map[string]string
}

func (a *attrSink) Query(_ context.Context, _, tenant, _ string, vars map[string]any, _ any) error {
	req := vars["request"].(map[string]any)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sets == nil {
		a.sets, a.typ = map[string]string{}, map[string]string{}
	}
	key := req["attrKey"].(string)
	a.sets[key] = req["scope"].(string) + "|" + tenant + "|" + req["entity"].(string) + "|" + req["value"].(string)
	a.typ[key] = req["valueType"].(string)
	return nil
}

func (a *attrSink) get(key string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v, ok := a.sets[key]
	return v, ok
}

// TestRegisterHarvestsDeviceInfoWithoutDelayingTheRegistration drives the whole chain over a real
// DTLS-PSK handshake: a client registers and also serves Device-object reads, and the Register
// handler, the real conn table, the real dispatcher worker and the harvester run as main.go wires
// them. The client holds its first read, so the registration is shown to complete (2.01, presence
// emitted) while the harvest is still waiting on the device; releasing it then lands the CLIENT
// attributes.
func TestRegisterHarvestsDeviceInfoWithoutDelayingTheRegistration(t *testing.T) {
	sink := &attrSink{}
	harvester := downlink.NewHarvester(sink, "http://device-management/graphql", downlink.HarvestMetrics{})
	connTable := downlink.NewConnTable()
	disp := downlink.NewDispatcher(idleReader{}, nil, connTable, downlink.NewOps(), noBacklog{}, nil,
		downlink.Metrics{}, downlink.Options{
			Workers: 2, Harvester: harvester,
			ReadPacer: core.NewReadPacer(nil, "device commands").UseClock(core.VirtualClock()),
		})
	connTable.SetOnLive(func(tenant, token string) {
		disp.Drain(tenant, token)
		disp.Harvest(tenant, token)
	})
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { disp.Run(runCtx); close(done) }()
	t.Cleanup(func() { stop(); <-done })
	<-disp.Ready()

	res := &fakeResolver{token: "tok-1", outcome: adapter.ResolveCreated}
	em := &fakeEmitter{}
	reg := New(res, em, adapter.NewEpochSource(nil), Metrics{}, Options{OnSessionEnd: func(id string, ep uint64) { connTable.End(id, ep) }})
	handlers := NewHandlers(reg, itBindings, Metrics{}, nil, connTable, decode.DefaultObjectAllowlist, nil, nil)
	srv, err := server.New(server.Config{
		Addr: "127.0.0.1:0", Credentials: map[string][]byte{"dev-1": itPSK, "dev-2": itPSK},
		CIDLength: 8, MaxSessions: 1000,
	}, server.Metrics{}, handlers.Mount)
	require.NoError(t, err)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Stop)

	// The device: serves Object 3 text/plain reads, the first of which it holds until released.
	release := make(chan struct{})
	gotFirst := make(chan struct{}, 1)
	var once sync.Once
	values := map[string]string{"/3/0/0": "Acme", "/3/0/1": "AC-100", "/3/0/2": "SN-42", "/3/0/3": "1.2.3"} // no battery
	router := mux.NewRouter()
	router.DefaultHandle(mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		path, _ := r.Options().Path()
		once.Do(func() { gotFirst <- struct{}{}; <-release })
		v, ok := values["/"+strings.TrimPrefix(path, "/")]
		if !ok {
			_ = w.SetResponse(codes.NotFound, message.TextPlain, nil)
			return
		}
		_ = w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte(v)))
	}))
	conn, err := coapdtls.Dial(srv.Addr().String(), &piondtls.Config{
		PSK:             func([]byte) ([]byte, error) { return itPSK, nil },
		PSKIdentityHint: []byte("dev-1"),
		CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
	}, options.WithMux(router))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := conn.Post(ctx, "/rd", message.TextPlain, nil, query("lt=300")...)
	require.NoError(t, err)
	require.Equal(t, codes.Created, resp.Code())

	// The harvest is now blocked on the device, and the registration has already finished.
	select {
	case <-gotFirst:
	case <-time.After(5 * time.Second):
		t.Fatal("the harvest never asked the device for its information")
	}
	require.Len(t, em.connects(), 1, "presence was emitted while the harvest was still waiting on the device")
	_, written := sink.get("manufacturer")
	assert.False(t, written, "nothing is written until the device has answered")

	close(release)
	require.Eventually(t, func() bool { harvester.WaitForWrites(); _, ok := sink.get("lwm2m.deviceInfoMissing"); return ok },
		5*time.Second, 10*time.Millisecond, "the harvest recorded its result")

	for key, want := range map[string]string{
		"manufacturer":    "CLIENT|acme|tok-1|Acme",
		"modelNumber":     "CLIENT|acme|tok-1|AC-100",
		"serialNumber":    "CLIENT|acme|tok-1|SN-42",
		"firmwareVersion": "CLIENT|acme|tok-1|1.2.3",
	} {
		got, ok := sink.get(key)
		require.True(t, ok, key)
		assert.Equal(t, want, got, key)
	}
	_, hasBattery := sink.get("batteryLevel")
	assert.False(t, hasBattery, "a missing resource is not written as a value")
	missing, _ := sink.get("lwm2m.deviceInfoMissing")
	assert.Contains(t, missing, `"resource":"batteryLevel"`)
	assert.Contains(t, missing, "4.04")
}

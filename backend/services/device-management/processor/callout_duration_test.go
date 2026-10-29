// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/nats-io/jwt/v2"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

// durationCount is how many requests the latency histogram has observed for path and result.
func durationCount(t *testing.T, m *CalloutMetrics, path, result string) uint64 {
	t.Helper()
	o, err := m.Duration.GetMetricWithLabelValues(path, result)
	require.NoError(t, err)
	var out dto.Metric
	require.NoError(t, o.(prometheus.Metric).Write(&out))
	return out.GetHistogram().GetSampleCount()
}

// durationTotal is the observations across every series.
func durationTotal(t *testing.T, m *CalloutMetrics) uint64 {
	t.Helper()
	var n uint64
	for _, p := range calloutPaths {
		for _, r := range calloutClasses {
			n += durationCount(t, m, p, r)
		}
	}
	return n
}

func newDurationMetrics(t *testing.T) *CalloutMetrics {
	t.Helper()
	ms := &core.Microservice{InstanceId: "inst-1", FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	return NewCalloutMetrics(ms)
}

// The histogram is registered once, with every path and class at zero, and the labels are the
// closed vocabulary only.
func TestCalloutDurationIsRegisteredOnceWithBoundedLabels(t *testing.T) {
	ms := &core.Microservice{InstanceId: "inst-1", FunctionalArea: "device-management"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	_ = NewCalloutMetrics(ms)

	families, err := reg.Gather()
	require.NoError(t, err)
	found := 0
	for _, f := range families {
		if f.GetName() != "devicechain_devicemanagement_callout_duration_seconds" {
			continue
		}
		found++
		require.Len(t, f.GetMetric(), len(calloutPaths)*len(calloutClasses))
		for _, m := range f.GetMetric() {
			require.Len(t, m.GetLabel(), 2)
			require.Equal(t, "path", m.GetLabel()[0].GetName())
			require.Equal(t, "result", m.GetLabel()[1].GetName())
		}
	}
	require.Equal(t, 1, found, "the histogram family must exist exactly once")
	require.Panics(t, func() { NewCalloutMetrics(ms) },
		"a second registration must collide, which is why it lives in the build function")
}

// durationRig answers real requests through handle over a plain in-process broker.
type durationRig struct {
	r       *CalloutResponder
	metrics *CalloutMetrics
	conn    *nats.Conn
}

func newDurationRig(t *testing.T, authFn func(context.Context, *model.PresentedCredential) (*model.Device, error)) *durationRig {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{Port: -1, NoLog: true, NoSigs: true})
	require.NoError(t, err)
	go srv.Start()
	t.Cleanup(srv.Shutdown)
	require.True(t, srv.ReadyForConnections(5*time.Second))
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	creds, err := natsauth.GenerateCredentials()
	require.NoError(t, err)
	r := mustResponder(t, nc, fakeAuthApi{authFn: authFn}, testChecker(t, credentialtest.NewStore()), creds.IssuerSeed, nil)
	metrics := newDurationMetrics(t)
	r.Configure(0, 0, 0, metrics)
	return &durationRig{r: r, metrics: metrics, conn: nc}
}

// send hands one request to handle and waits for the response it publishes.
func (d *durationRig) send(t *testing.T, user, pass string) {
	t.Helper()
	req := testRequest(t, user, pass)
	srvKP, err := nkeys.CreateServer()
	require.NoError(t, err)
	claims := jwt.NewAuthorizationRequestClaims(req.UserNkey)
	claims.AuthorizationRequest = req
	claims.Audience = "$G"
	token, err := claims.Encode(srvKP)
	require.NoError(t, err)

	inbox := nats.NewInbox()
	got := make(chan struct{}, 1)
	sub, err := d.conn.Subscribe(inbox, func(*nats.Msg) { got <- struct{}{} })
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()
	require.NoError(t, d.conn.Flush())
	d.r.handle(&nats.Msg{Subject: AuthCalloutSubject, Reply: inbox, Data: []byte(token)})
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no response published")
	}
}

func okDevice(context.Context, *model.PresentedCredential) (*model.Device, error) {
	d := &model.Device{}
	d.Token = "sensor-001"
	return d, nil
}

// Each request is observed exactly once, on the series for its path and result class, and no
// other series moves. Removing the observe call in handle fails this test.
func TestCalloutDurationIsObservedOncePerRequestWithPathAndResult(t *testing.T) {
	var fail error
	rig := newDurationRig(t, func(ctx context.Context, p *model.PresentedCredential) (*model.Device, error) {
		if fail != nil {
			return nil, fail
		}
		return okDevice(ctx, p)
	})

	cases := []struct {
		name       string
		user, pass string
		fail       error
		path, res  string
	}{
		{"token granted", "acme-corp:tok", "", nil, calloutPathToken, calloutResultGranted},
		{"token refused", "acme-corp:tok", "", model.ErrCredentialNotResolved, calloutPathToken, calloutResultRefused},
		{"token error", "acme-corp:tok", "", errors.New("db down"), calloutPathToken, calloutResultError},
		{"password granted", "acme-corp:dev1", "s3cret", nil, calloutPathPassword, calloutResultGranted},
		{"password error", "acme-corp:dev1", "s3cret", errors.New("db down"), calloutPathPassword, calloutResultError},
		{"x509 refused (no certificate)", "acme-corp:@x509", "", nil, calloutPathX509, calloutResultRefused},
		{"unparsed refused", "no-colon", "", nil, calloutPathUnparsed, calloutResultRefused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fail = tc.fail
			beforeAll := durationTotal(t, rig.metrics)
			before := durationCount(t, rig.metrics, tc.path, tc.res)
			rig.send(t, tc.user, tc.pass)
			require.Equal(t, before+1, durationCount(t, rig.metrics, tc.path, tc.res), "the request was not observed on its own series")
			require.Equal(t, beforeAll+1, durationTotal(t, rig.metrics), "exactly one observation per request")
		})
	}
}

// A responder with no metrics attached handles requests without observing (and without panicking).
func TestCalloutDurationIsOptional(t *testing.T) {
	rig := newDurationRig(t, okDevice)
	rig.r.metrics = nil
	rig.send(t, "acme-corp:tok", "")
}

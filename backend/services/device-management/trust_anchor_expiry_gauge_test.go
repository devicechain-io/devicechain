// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The gauge is read at scrape time from a cached count, and says NOTHING while it cannot count.
func TestTrustAnchorExpiryGauge(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	var calls int
	var n int64 = 2
	var failWith error
	g := &trustAnchorExpiryGauge{now: func() time.Time { return clock },
		count: func(_ context.Context, within time.Duration, now time.Time) (int64, error) {
			calls++
			assert.Equal(t, 30*24*time.Hour, within, "the window is 30 days")
			assert.Equal(t, clock, now)
			return n, failWith
		}}

	got := g.sample()
	require.Len(t, got, 1)
	assert.Equal(t, []string{"30d"}, got[0].Labels)
	assert.Equal(t, 2.0, got[0].Value)

	// A scrape inside the refresh interval reuses the reading.
	n, clock = 5, clock.Add(time.Minute)
	assert.Equal(t, 2.0, g.sample()[0].Value)
	assert.Equal(t, 1, calls, "a scrape inside the refresh interval queried the database")
	// Past it, it reads again.
	clock = clock.Add(5 * time.Minute)
	assert.Equal(t, 5.0, g.sample()[0].Value)
	assert.Equal(t, 2, calls)

	// A failed read reports no series, not the last value and not zero...
	failWith, clock = errors.New("database unreachable"), clock.Add(6*time.Minute)
	assert.Empty(t, g.sample(), "an unreadable count must not be reported as a number")
	// ...retries soon, and recovers.
	failWith, clock = nil, clock.Add(time.Minute)
	assert.Equal(t, 5.0, g.sample()[0].Value)
}

// The gauge is registered by the service's own metric construction, under the name the alert
// reads, and reads the real Api: a CA that expires inside the window counts, one that lasts
// longer, and one that is disabled, do not.
func TestTrustAnchorExpiryGaugeIsRegisteredByBuildMetrics(t *testing.T) {
	prevMs, prevApi := Microservice, Api
	t.Cleanup(func() { Microservice, Api = prevMs, prevApi })
	reg := prometheus.NewRegistry()
	Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
	Microservice.UseMetricsRegistry(reg)
	buildTrustAnchorExpiryGauge()
	read := func() (float64, bool) {
		families, err := reg.Gather()
		require.NoError(t, err)
		for _, f := range families {
			if f.GetName() != "devicechain_devicemanagement_device_trust_anchors_expiring" {
				continue
			}
			require.Len(t, f.GetMetric(), 1)
			m := f.GetMetric()[0]
			require.Equal(t, "window", m.GetLabel()[0].GetName())
			require.Equal(t, "30d", m.GetLabel()[0].GetValue())
			return m.GetGauge().GetValue(), true
		}
		return 0, false
	}
	Api = model.NewApi(&rdb.RdbManager{Database: putest.NewSQLiteDB(t, &model.DeviceTrustAnchor{}, &model.DeviceCredential{}, &model.PendingSessionKick{}, &model.Device{}, &model.ProvisioningProfile{})})
	ctx := putest.TenantContext("acme")()
	ca := func(cn string, life time.Duration) string {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(life), IsCA: true,
			BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, MaxPathLen: -1}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		require.NoError(t, err)
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	for token, life := range map[string]time.Duration{"soon": 10 * 24 * time.Hour, "later": 90 * 24 * time.Hour, "soon-off": 5 * 24 * time.Hour} {
		_, err := Api.CreateDeviceTrustAnchor(ctx, &model.DeviceTrustAnchorCreateRequest{Token: token, CertificatePem: ca(token, life)})
		require.NoError(t, err)
	}
	_, err := Api.UpdateDeviceTrustAnchor(ctx, "soon-off", &model.DeviceTrustAnchorUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)

	// The Api's answer, across tenants (the gauge is instance-wide).
	other := putest.TenantContext("other")()
	_, err = Api.CreateDeviceTrustAnchor(other, &model.DeviceTrustAnchorCreateRequest{Token: "soon-b", CertificatePem: ca("soon-b", 20*24*time.Hour)})
	require.NoError(t, err)

	v, ok := read()
	require.True(t, ok, "buildMetrics did not register device_trust_anchors_expiring")
	assert.Equal(t, 2.0, v, "the enabled anchors inside the window, in every tenant; not the long-lived one, not the disabled one")
}

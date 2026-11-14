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
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/processor"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/x509trust/ocspclient"
	"github.com/stretchr/testify/require"
)

func ocspWiringTarget(t *testing.T, responder string) ocspclient.Target {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	issuerTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "wiring CA"},
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, issuerTemplate, issuerTemplate, &key.PublicKey, key)
	require.NoError(t, err)
	issuer, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "device"},
		NotBefore: issuer.NotBefore, NotAfter: issuer.NotAfter, OCSPServer: []string{responder},
	}
	der, err = x509.CreateCertificate(rand.Reader, leafTemplate, issuer, &key.PublicKey, key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return ocspclient.Target{Cert: leaf, Issuer: issuer}
}

func TestOcspWiringSeparatesOverrideFromAIA(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	target := ocspWiringTarget(t, server.URL)
	cfg := config.NewDeviceManagementConfiguration().Callout.Ocsp
	cfg.FetchDeadline = "2s"
	infra := mscfg.InfrastructureConfiguration{}
	infra.Egress.AllowedDestinations = []string{"127.0.0.1/32"}

	// Use fresh clients: a cached failure must not hide which transport AIA uses.
	check := func(override string) ocspclient.Outcome {
		client, err := newOcspClient(cfg, infra, nil)
		require.NoError(t, err)
		return client.Check(context.Background(), ocspclient.Request{
			Tenant: "acme", Anchor: "anchor", OverrideURL: override, Targets: []ocspclient.Target{target},
		})
	}
	out := check(server.URL)
	require.Equal(t, int64(1), hits.Load(), "allowed anchor override must reach the responder")
	require.Equal(t, ocspclient.ReasonBadResponse, out.Reasons[target.Key()])
	out = check("")
	require.Equal(t, ocspclient.ReasonBlocked, out.Reasons[target.Key()], "certificate AIA must retain the strict guard")
	require.Equal(t, int64(1), hits.Load(), "AIA must send no request to the same loopback URL")
}

func TestOcspWiringDisabledInstallsNoFetcher(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("disabled OCSP dialled a responder")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	cfg := config.NewDeviceManagementConfiguration()
	cfg.Callout.Ocsp.Enabled = false
	resp := &processor.CalloutResponder{}
	infra := mscfg.InfrastructureConfiguration{}
	infra.Egress.AllowedDestinations = []string{"127.0.0.1/32"}
	require.NoError(t, wireCalloutOcsp(resp, nil, cfg, infra))
	// Read the installed wiring without adding a production inspection API. No checker
	// exists that could fetch even if an anchor requests REQUIRE_CURRENT.
	wiring := reflect.ValueOf(resp).Elem().FieldByName("ocsp").Elem()
	require.False(t, wiring.FieldByName("enabled").Bool())
	require.True(t, wiring.FieldByName("checker").IsNil())
	// Off must also bypass construction; on must fail closed on a malformed allow list.
	infra.Egress.AllowedDestinations = []string{"invalid-CIDR"}
	require.NoError(t, wireCalloutOcsp(resp, nil, cfg, infra))
	cfg.Callout.Ocsp.Enabled = true
	require.Error(t, wireCalloutOcsp(resp, nil, cfg, infra))
}

func TestRevocationLimitsWiring(t *testing.T) {
	got := revocationLimitsOf(config.RevocationConfiguration{
		MaxCrlBytes: 2, MaxCrlEntries: 3, MaxListsPerAnchor: 5,
		MaxManualEntriesPerTenant: 7, DefaultStaleGrace: "11s",
		MaxGraphqlCrlBytes: 13, MaxConcurrentWrites: 17,
	})
	require.Equal(t, model.RevocationLimits{
		MaxCrlBytes: 2, MaxCrlEntries: 3, MaxListsPerAnchor: 5,
		MaxManualEntriesPerTenant: 7, DefaultStaleGrace: 11 * time.Second,
		MaxGraphqlCrlBytes: 13, MaxConcurrentWrites: 17,
	}, got)
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-lwm2m-ingest/config"
	"github.com/devicechain-io/dc-microservice/auth"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// The ONE construction site of this service's ingest limiter must report admissions made
// at the platform default because user-management could not be asked — with this
// service's dimension, and as the cause that pages.
func TestBuildIngestLimiterCountsUnresolvedAdmissions(t *testing.T) {
	um := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == auth.ServiceTokenPath {
			_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "svc", ExpiresAt: 1 << 40})
			return
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer um.Close()
	host, portStr, err := net.SplitHostPort(um.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	infra := mscfg.InfrastructureConfiguration{
		UserManagement: mscfg.UserManagementConfiguration{Hostname: host, Port: uint32(port)},
	}
	client := svcclient.New(infra.UserManagement, "shh", "lwm2m-ingest", []string{string(auth.TenantRead)})

	reg := prometheus.NewRegistry()
	ms := &core.Microservice{FunctionalArea: "lwm2m-ingest"}
	ms.UseMetricsRegistry(reg)
	unresolved := governance.NewUnresolvedAdmissions(ms, governance.Ingest)

	limiter := buildIngestLimiter(client, infra, config.IngestRateLimit{MessagesPerSecond: 1, Burst: 1000}, unresolved)

	unreachable := func() float64 {
		mfs, err := reg.Gather()
		require.NoError(t, err)
		for _, mf := range mfs {
			if mf.GetName() != "devicechain_lwm2mingest_governance_unresolved_admissions_total" {
				continue
			}
			for _, m := range mf.GetMetric() {
				labels := map[string]string{}
				for _, lp := range m.GetLabel() {
					labels[lp.GetName()] = lp.GetValue()
				}
				if labels["cause"] == "unreachable" && labels["dimension"] == "ingest" {
					return m.GetCounter().GetValue()
				}
			}
		}
		return -1
	}
	require.Equal(t, float64(0), unreachable(), "the counter is exported at zero before anything is admitted")
	// The first admissions are made before the resolver's refresh has failed (pending);
	// keep admitting until one lands after it.
	for deadline := time.Now().Add(5 * time.Second); unreachable() == 0; time.Sleep(5 * time.Millisecond) {
		require.True(t, time.Now().Before(deadline), "an admission with user-management failing was never counted as unreachable")
		require.True(t, limiter.AllowMessage("acme"))
	}
	require.Equal(t, float64(1), unreachable(), "exactly the one admission made after the failure")
}

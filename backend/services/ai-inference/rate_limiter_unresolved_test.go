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

	"github.com/devicechain-io/dc-microservice/auth"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// failingUserManagement is a user-management that mints service tokens and then fails
// every tenantGovernance read with a 503, and the infrastructure config pointing at it.
func failingUserManagement(t *testing.T) mscfg.InfrastructureConfiguration {
	t.Helper()
	um := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == auth.ServiceTokenPath {
			_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "svc", ExpiresAt: 1 << 40})
			return
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(um.Close)
	host, portStr, err := net.SplitHostPort(um.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	var infra mscfg.InfrastructureConfiguration
	infra.ServiceAuth.Secret = "shh"
	infra.UserManagement = mscfg.UserManagementConfiguration{Hostname: host, Port: uint32(port)}
	return infra
}

// unreachableAdmissions reads the unreachable-cause series for dim from reg.
func unreachableAdmissions(t *testing.T, reg *prometheus.Registry, name, dim string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["cause"] == "unreachable" && labels["dimension"] == dim {
				return m.GetCounter().GetValue()
			}
		}
	}
	return -1
}

// The ONE construction site of this service's limiter must report admissions made at the
// platform default because user-management could not be asked — with this service's
// dimension, and as the cause that pages.
func TestBuildRateLimiterCountsUnresolvedAdmissions(t *testing.T) {
	infra := failingUserManagement(t)
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{FunctionalArea: "ai-inference"}
	ms.UseMetricsRegistry(reg)
	unresolved := governance.NewUnresolvedAdmissions(ms, governance.AIInference)

	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "ai-inference", []string{string(auth.TenantRead)})
	limiter := buildRateLimiter(governance.Limits{MessagesPerSecond: 1, Burst: 1000}, infra, client, unresolved)

	const series = "devicechain_aiinference_governance_unresolved_admissions_total"
	require.Equal(t, float64(0), unreachableAdmissions(t, reg, series, "ai-inference"),
		"the counter is exported at zero before anything is admitted")
	// The first admissions are made before the resolver's refresh has failed (pending);
	// keep admitting until one lands after it.
	for deadline := time.Now().Add(5 * time.Second); unreachableAdmissions(t, reg, series, "ai-inference") == 0; time.Sleep(5 * time.Millisecond) {
		require.True(t, time.Now().Before(deadline), "an admission with user-management failing was never counted as unreachable")
		require.True(t, limiter.Allow("acme"))
	}
	require.Equal(t, float64(1), unreachableAdmissions(t, reg, series, "ai-inference"),
		"exactly the one admission made after the failure")
}

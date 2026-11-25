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

	"github.com/devicechain-io/dc-event-processing/config"
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
	infra.UserManagement = mscfg.UserManagementConfiguration{Hostname: host, Port: uint32(port), InternalPort: uint32(port)}
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

// The shared builder for this service's limiters must report admissions made at the
// platform default because user-management could not be asked — with this service's
// dimension, and as the cause that pages.
func TestBuildEgressLimiterCountsUnresolvedAdmissions(t *testing.T) {
	infra := failingUserManagement(t)
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(reg)
	unresolved := governance.NewUnresolvedAdmissions(ms, governance.Outbound)

	limiter := buildEgressLimiter(&config.EventProcessingConfiguration{OutboundCallsPerSecond: 1, OutboundBurst: 1000},
		svcclient.NewPeers(infra, ms.FunctionalArea), unresolved)

	const series = "devicechain_eventprocessing_governance_unresolved_admissions_total"
	require.Equal(t, float64(0), unreachableAdmissions(t, reg, series, "outbound"),
		"the counter is exported at zero before anything is admitted")
	// The first admissions are made before the resolver's refresh has failed (pending);
	// keep admitting until one lands after it.
	for deadline := time.Now().Add(5 * time.Second); unreachableAdmissions(t, reg, series, "outbound") == 0; time.Sleep(5 * time.Millisecond) {
		require.True(t, time.Now().Before(deadline), "an admission with user-management failing was never counted as unreachable")
		require.True(t, limiter.Allow("acme"))
	}
	require.Equal(t, float64(1), unreachableAdmissions(t, reg, series, "outbound"),
		"exactly the one admission made after the failure")
}

// Commands and connector calls have independent fallback-admission counters, just
// as they have independent buckets. Register both on the same service registry.
func TestCommandFallbackAdmissionsAreSeparate(t *testing.T) {
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(reg)
	connectorUnresolved := governance.NewUnresolvedAdmissions(ms, governance.Outbound)
	commandUnresolved := newCommandUnresolvedAdmissions(ms)
	const connectorSeries = "devicechain_eventprocessing_governance_unresolved_admissions_total"
	const commandSeries = "devicechain_eventprocessing_react_command_governance_unresolved_admissions_total"
	require.Equal(t, float64(0), unreachableAdmissions(t, reg, commandSeries, ""))
	commandUnresolved(core.CeilingUnreachable)
	require.Equal(t, float64(1), unreachableAdmissions(t, reg, commandSeries, ""))
	require.Equal(t, float64(0), unreachableAdmissions(t, reg, connectorSeries, "outbound"))
	connectorUnresolved(core.CeilingUnreachable)
	require.Equal(t, float64(1), unreachableAdmissions(t, reg, connectorSeries, "outbound"))
	require.Equal(t, float64(1), unreachableAdmissions(t, reg, commandSeries, ""))
}

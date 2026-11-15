// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestSafetyGatesSharedAndNilSafe(t *testing.T) {
	require.Nil(t, (*Service)(nil).SafetyGates())
	require.Nil(t, FromManagers(nil, Managers{}).SafetyGates())
	for _, specBuilt := range []bool{false, true} {
		ms := testMicroservice(t)
		reg := prometheus.NewRegistry()
		ms.UseMetricsRegistry(reg)
		svc := FromManagers(ms, Managers{})
		if specBuilt {
			svc = New(ms, Spec{})
		}
		var wg sync.WaitGroup
		gates := make([]*governance.SafetyGates, 16)
		for i := range gates {
			wg.Add(1)
			go func(i int) { defer wg.Done(); gates[i] = svc.SafetyGates() }(i)
		}
		wg.Wait()
		require.NotNil(t, gates[0])
		for _, g := range gates {
			require.Same(t, gates[0], g)
		}
		require.Equal(t, 0, testutil.CollectAndCount(reg)) // no label values seeded by construction
		gates[0].Set(governance.GatePresence, false)
		gates[1].Set(governance.GateTenantLifecycle, true)
		families, err := reg.Gather()
		require.NoError(t, err)
		require.Len(t, families, 1)
		require.Equal(t, "devicechain_svctest_safety_gate_enabled", families[0].GetName())
		require.Len(t, families[0].Metric, 2)
		values := map[string]float64{}
		for _, metric := range families[0].Metric {
			require.Len(t, metric.Label, 1)
			require.Equal(t, "gate", metric.Label[0].GetName())
			values[metric.Label[0].GetValue()] = metric.Gauge.GetValue()
		}
		require.Equal(t, map[string]float64{"presence": 0, "tenant_lifecycle": 1}, values)
	}
}

type refreshProbe chan struct{}

func (p refreshProbe) Fetch(context.Context, string) (governance.Limits, error) {
	p <- struct{}{}
	return governance.Limits{RatePerSecond: 7, Burst: 7}, nil
}

// A fixture stop must not terminally close refreshes for the rest of this binary.
func TestFixtureStopLeavesRefreshesRunning(t *testing.T) {
	for _, svc := range []*Service{nil, FromManagers(nil, Managers{}), FromManagers(testMicroservice(t), Managers{}), New(testMicroservice(t), Spec{})} {
		if svc != nil {
			require.NoError(t, svc.Stop(context.Background()))
		}
		require.NoError(t, svc.StopRefreshes(context.Background()))
		fetched := make(refreshProbe, 1)
		resolver := governance.NewTenantLimitResolver(fetched, governance.Limits{RatePerSecond: 1, Burst: 1}, "stop-test")
		resolver.Ceiling("tenant")
		select {
		case <-fetched:
		case <-time.After(5 * time.Second):
			t.Fatal("fixture stop disabled resolver fetches")
		}
	}
}

// Run terminal shutdown in a child so the rest of this binary can still resolve.
func TestOwnedRefreshStop(t *testing.T) {
	if os.Getenv("DC_TEST_OWNED_REFRESH_STOP") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestOwnedRefreshStop$", "-test.count=1")
		cmd.Env = append(os.Environ(), "DC_TEST_OWNED_REFRESH_STOP=1")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		return
	}
	started, cancelled := make(chan struct{}), make(chan struct{})
	resolver := governance.NewTenantLimitResolver(cancellingRefresh{started, cancelled}, governance.Limits{RatePerSecond: 1, Burst: 1}, "owned-stop")
	resolver.Ceiling("tenant")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("resolver did not fetch before stop")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc := New(nil, Spec{GovernanceRefreshes: true})
	// Stop alone leaves refreshes running: they are joined by StopRefreshes, which a service
	// runs as its Stopper.Postprocess, after the microservice's own stop.
	require.NoError(t, svc.Stop(ctx))
	select {
	case <-cancelled:
		t.Fatal("Stop closed the refreshes; that belongs to StopRefreshes")
	default:
	}
	require.NoError(t, svc.StopRefreshes(ctx))
	select {
	case <-cancelled:
	default:
		t.Fatal("StopRefreshes returned without cancelling and joining the owned refresh")
	}
}

type cancellingRefresh struct{ started, cancelled chan struct{} }

func (f cancellingRefresh) Fetch(ctx context.Context, _ string) (governance.Limits, error) {
	close(f.started)
	<-ctx.Done()
	close(f.cancelled)
	return governance.Limits{}, ctx.Err()
}

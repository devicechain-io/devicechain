// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedFetcher answers per tenant from a map of errors (nil = success).
type scriptedFetcher struct{ errs map[string]error }

func (f *scriptedFetcher) Fetch(_ context.Context, tenant string) (Limits, error) {
	if err := f.errs[tenant]; err != nil {
		return Limits{}, err
	}
	return Limits{MessagesPerSecond: 5, Burst: 10}, nil
}

// settled waits until the tenant's refresh has finished (nothing in flight for it) and
// returns its ceiling.
func settled(t *testing.T, r *TenantLimitResolver, tenant string) core.TenantCeiling {
	t.Helper()
	r.Ceiling(tenant) // trigger
	require.Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		_, running := r.inflight[tenant]
		_, cached := r.cache[tenant]
		return !running && cached
	}, time.Second, time.Millisecond)
	return r.Ceiling(tenant)
}

// The Source the rate limiter counts on is resolveOK's answer, classified once.
func TestCeilingSourceFollowsResolveOK(t *testing.T) {
	unknown := &svcclient.GraphQLError{URL: "um", Messages: []string{"unknown tenant"}, Codes: []string{CodeUnknownTenant}}
	other := &svcclient.GraphQLError{URL: "um", Messages: []string{"forbidden"}, Codes: []string{"FORBIDDEN"}}
	f := &scriptedFetcher{errs: map[string]error{
		"down":          errors.New("svcclient: call um: connection refused"),
		"status":        errors.New("svcclient: um returned 503: unavailable"),
		"ghost":         fmt.Errorf("wrapped: %w", unknown),
		"other-gql":     other,
		"no-code-gql":   &svcclient.GraphQLError{URL: "um", Messages: []string{"boom"}},
		"known-then-up": nil,
	}}
	r := NewTenantLimitResolver(f, platformDefault, "test")

	// A cold miss serves the default and says so.
	assert.Equal(t, core.CeilingPending, r.Ceiling("never-asked-before").Source)

	for tenant, want := range map[string]core.CeilingSource{
		"fetched":     core.CeilingResolved,
		"down":        core.CeilingUnreachable,
		"status":      core.CeilingUnreachable,
		"ghost":       core.CeilingUnknownTenant,
		"other-gql":   core.CeilingUnreachable,
		"no-code-gql": core.CeilingUnreachable,
	} {
		got := settled(t, r, tenant)
		assert.Equalf(t, want, got.Source, "tenant %q", tenant)
		if want != core.CeilingResolved {
			assert.Equalf(t, platformDefault.MessagesPerSecond, got.RatePerSecond, "tenant %q serves the default", tenant)
		}
	}

	// A known tenant whose refresh then fails keeps its last-known value and stays Resolved.
	now := time.Now()
	r.now = func() time.Time { return now }
	require.Equal(t, core.CeilingResolved, settled(t, r, "known-then-up").Source)
	f.errs["known-then-up"] = errors.New("connection refused")
	now = now.Add(r.ttl + time.Second)
	r.Ceiling("known-then-up") // stale: triggers the failing refresh
	require.Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		_, running := r.inflight["known-then-up"]
		return !running && r.cache["known-then-up"].cause == core.CeilingUnreachable
	}, time.Second, time.Millisecond)
	got := r.Ceiling("known-then-up")
	assert.Equal(t, core.CeilingResolved, got.Source, "a failed refresh of a known tenant is still resolved")
	assert.Equal(t, float64(5), got.RatePerSecond, "and keeps its last-known value")

	// A refresh the resolver's own budget refuses leaves the tenant pending — a spray of
	// novel names must not read as user-management being unreachable.
	budgeted := NewTenantLimitResolver(f, platformDefault, "test")
	budgeted.Configure(ResolverOptions{RefreshesPerSecond: 1e-9, RefreshBurst: 1})
	settled(t, budgeted, "down") // spends the only token
	for i := 0; i < 50; i++ {
		assert.Equal(t, core.CeilingPending, budgeted.Ceiling(fmt.Sprintf("novel-%d", i)).Source)
	}
}

func TestNewUnresolvedAdmissionsExportsEveryCauseAtZero(t *testing.T) {
	ms := &core.Microservice{FunctionalArea: "event-sources"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)

	count := NewUnresolvedAdmissions(ms, Ingest)

	want := `
# HELP devicechain_eventsources_governance_unresolved_admissions_total PLACEHOLDER
# TYPE devicechain_eventsources_governance_unresolved_admissions_total counter
devicechain_eventsources_governance_unresolved_admissions_total{cause="pending",dimension="ingest"} 0
devicechain_eventsources_governance_unresolved_admissions_total{cause="unknown-tenant",dimension="ingest"} 0
devicechain_eventsources_governance_unresolved_admissions_total{cause="unreachable",dimension="ingest"} 1
`
	count(core.CeilingUnreachable)
	count(core.CeilingResolved) // not a cause: ignored
	count(core.CeilingStatic)   // not a cause: ignored
	assert.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(withHelp(t, reg, want)),
		"devicechain_eventsources_governance_unresolved_admissions_total"))
}

func TestNewOverflowAdmissionsExportsAtZero(t *testing.T) {
	ms := &core.Microservice{FunctionalArea: "event-sources"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)

	NewOverflowAdmissions(ms)

	n, err := testutil.GatherAndCount(reg, "devicechain_eventsources_ratelimit_overflow_admissions_total")
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the overflow counter is exported before anything overflows")
}

// withHelp substitutes the registered HELP text for PLACEHOLDER, so the comparison is
// about the series and their values, not the prose.
func withHelp(t *testing.T, reg *prometheus.Registry, want string) string {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() == "devicechain_eventsources_governance_unresolved_admissions_total" {
			return strings.Replace(want, "PLACEHOLDER", mf.GetHelp(), 1)
		}
	}
	t.Fatal("the unresolved-admissions counter is not registered")
	return ""
}

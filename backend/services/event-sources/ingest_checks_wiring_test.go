// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type directoryFetcher struct{}

func (directoryFetcher) Fetch(_ context.Context, tenant string) (governance.Limits, error) {
	if tenant == "typo" {
		return governance.Limits{}, &svcclient.GraphQLError{URL: "um", Messages: []string{"unknown tenant"},
			Codes: []string{governance.CodeUnknownTenant}}
	}
	return governance.Limits{RatePerSecond: 10, Burst: 10}, nil
}

// THE WIRING OF THE SYNCHRONOUS CHECKS, through the code that builds them. The processor's tests
// hand the source a check by hand; nothing else runs buildIngestChecks, so a service that stopped
// handing the source its tenant directory, or stopped counting a refusal, would pass every one of
// them.
func TestBuildIngestChecksWiresTheTenantDirectoryAndTheRefusalCounter(t *testing.T) {
	sealingFixture(t)
	saved := TenantDirectory
	t.Cleanup(func() { TenantDirectory = saved })

	// With no authority for tenants, there is no tenant check: the door is as it was.
	TenantDirectory = nil
	checks := buildIngestChecks()
	require.NotNil(t, checks)
	require.Nil(t, checks.TenantUnknown, "no user-management means no authority on which tenants exist")
	require.Nil(t, checks.Credential, "no device-management endpoint means nobody to ask about a credential")

	// With one, an unknown tenant is refused (the first ask waits for the lookup) and a known one is not.
	TenantDirectory = governance.NewTenantLimitResolver(directoryFetcher{}, governance.Limits{RatePerSecond: 1, Burst: 1}, "ingest")
	checks = buildIngestChecks()
	require.NotNil(t, checks.TenantUnknown)
	require.True(t, checks.TenantUnknown(context.Background(), "typo"))
	require.False(t, checks.TenantUnknown(context.Background(), "acme"))
	// A tenant the authority has not answered for is not refused.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, checks.TenantUnknown(cancelled, "never-asked"), "an answer that did not arrive is no opinion")

	// A refusal is counted on the counter the metrics registry serves, by reason.
	require.NotNil(t, checks.Refused)
	before := testutil.ToFloat64(IngestRefusedCounter.WithLabelValues("http1", processor.RefusedUnknownTenant))
	checks.Refused("http1", processor.RefusedUnknownTenant)
	require.Equal(t, before+1, testutil.ToFloat64(IngestRefusedCounter.WithLabelValues("http1", processor.RefusedUnknownTenant)))
}

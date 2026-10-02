// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package observe

import (
	"fmt"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-microservice/core"
)

// numericNotifyBody is one SenML-JSON Notify body of n numeric records /3303/0/0 … at an
// absolute base time.
func numericNotifyBody(n int) string {
	var b strings.Builder
	b.WriteString(`[{"bn":"/3303/0/","bt":1700000500,"n":"0","v":0}`)
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, `,{"n":"%d","v":%d}`, i, i)
	}
	b.WriteString(`]`)
	return b.String()
}

// 🔴 A 300-SAMPLE NOTIFY REACHES THE INGESTER WHOLE. The Notify path used to keep the first
// 256 samples and drop the other 44; the emitter now splits a large batch into events of at
// most 256, so the Manager hands over everything it decoded.
func TestA300SampleNotifyIsIngestedWhole(t *testing.T) {
	m, ing, _ := newHarness(t)
	c := newFakeConn(1)
	require.True(t, m.Establish("id-1", 1, c, testTarget, []string{"/3303/0"}))

	c.deliver("/3303/0", senmlNotify(numericNotifyBody(300)))

	require.Equal(t, 1, ing.callCount(), "one Notify is one ingest call")
	got := ing.last().samples
	require.Equal(t, 300, len(got), "every sample of the Notify is ingested")
	assert.Equal(t, "/3303/0/299", got[299].Name, "including the tail")
}

// 🔴 THE SAMPLE BUDGET IS CHARGED PER EVENT, SO A NOTIFY LARGER THAN THE TENANT'S SAMPLE
// BURST IS NEVER SHED WHOLE FOR EVER. This runs the REAL limiter at the smallest ceiling a
// tenant can be given (1 reading/s, burst 1), which floors the sample burst at the per-event
// limit of 256. Charging the whole 300-sample Notify at once could never fit that bucket, so
// every such Notify from the device would be lost; charging each ≤256 piece admits what the
// budget allows and sheds the rest, counted.
func TestANotifyLargerThanTheSampleBurstKeepsWhatTheBudgetAdmits(t *testing.T) {
	shed := prometheus.NewCounter(prometheus.CounterOpts{Name: "samples_shed"})
	lim := adapter.NewIngestLimiter(core.StaticCeiling(1, 1),
		adapter.IngestLimiterMetrics{SamplesShed: shed}, nil)
	m, ing, _, _ := newGatedHarness(t, lim)
	c := newFakeConn(1)
	require.True(t, m.Establish("id-1", 1, c, testTarget, []string{"/3303/0"}))

	c.deliver("/3303/0", senmlNotify(numericNotifyBody(300)))

	require.Equal(t, 1, ing.callCount(), "the budget admits the first event's worth of samples")
	got := ing.last().samples
	require.Equal(t, 256, len(got), "the admitted prefix is one full event")
	assert.Equal(t, "/3303/0/255", got[255].Name)
	assert.Equal(t, float64(44), testutil.ToFloat64(shed), "the 44 the budget could not admit are counted as shed")
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
)

// A message admitted at the platform default is counted ONCE: on the message stage,
// which sees every message. The sample stage sees the same message again after decode,
// and the Source is copied onto the sample ceiling it derives, so counting there as
// well would count one message twice.
func TestIngestLimiterCountsOncePerMessage(t *testing.T) {
	var counted []core.CeilingSource
	unreachable := func(string) core.TenantCeiling {
		return core.TenantCeiling{RatePerSecond: 1, Burst: 100, Source: core.CeilingUnreachable}
	}
	l := NewIngestLimiter(unreachable, DefaultSamplesPerMessage, 256, IngestLimiterMetrics{},
		func(s core.CeilingSource) { counted = append(counted, s) })

	for i := 0; i < 3; i++ {
		assert.True(t, l.AllowMessage("acme"))
		assert.True(t, l.AllowSamples("acme", 10))
	}
	assert.Equal(t, []core.CeilingSource{core.CeilingUnreachable, core.CeilingUnreachable, core.CeilingUnreachable},
		counted, "three messages, each counted once, with their cause")
}

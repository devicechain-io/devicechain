// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"testing"

	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/assert"
)

func fptr(f float64) *float64 { return &f }
func iptr(i int) *int         { return &i }

// A nil override (inherit the platform default) and any positive override are
// accepted; a zero or negative override is rejected — the only way to inherit is
// to omit, never to set zero. Every dimension follows the same rule.
func TestGovernanceOverrides_Validate(t *testing.T) {
	cases := []struct {
		name    string
		in      GovernanceOverrides
		wantErr bool
	}{
		{"all nil inherits", GovernanceOverrides{}, false},

		{"positive ingest rate + burst", GovernanceOverrides{IngestMessagesPerSecond: fptr(500), IngestBurst: iptr(1000)}, false},
		{"fractional ingest rate ok", GovernanceOverrides{IngestMessagesPerSecond: fptr(0.5)}, false},
		{"zero ingest rate rejected", GovernanceOverrides{IngestMessagesPerSecond: fptr(0)}, true},
		{"negative ingest rate rejected", GovernanceOverrides{IngestMessagesPerSecond: fptr(-1)}, true},
		{"zero ingest burst rejected", GovernanceOverrides{IngestBurst: iptr(0)}, true},
		{"negative ingest burst rejected", GovernanceOverrides{IngestBurst: iptr(-5)}, true},
		{"good ingest rate but bad ingest burst rejected", GovernanceOverrides{IngestMessagesPerSecond: fptr(100), IngestBurst: iptr(0)}, true},

		{"positive outbound rate + burst", GovernanceOverrides{OutboundMessagesPerSecond: fptr(50), OutboundBurst: iptr(100)}, false},
		{"zero outbound rate rejected", GovernanceOverrides{OutboundMessagesPerSecond: fptr(0)}, true},
		{"negative outbound rate rejected", GovernanceOverrides{OutboundMessagesPerSecond: fptr(-2)}, true},
		{"zero outbound burst rejected", GovernanceOverrides{OutboundBurst: iptr(0)}, true},
		{"negative outbound burst rejected", GovernanceOverrides{OutboundBurst: iptr(-3)}, true},

		{"positive ai rate + burst", GovernanceOverrides{AiInferenceRequestsPerMinute: fptr(30), AiInferenceBurst: iptr(15)}, false},
		{"fractional ai rate ok", GovernanceOverrides{AiInferenceRequestsPerMinute: fptr(0.5)}, false},
		{"zero ai rate rejected", GovernanceOverrides{AiInferenceRequestsPerMinute: fptr(0)}, true},
		{"negative ai rate rejected", GovernanceOverrides{AiInferenceRequestsPerMinute: fptr(-1)}, true},
		{"zero ai burst rejected", GovernanceOverrides{AiInferenceBurst: iptr(0)}, true},
		{"negative ai burst rejected", GovernanceOverrides{AiInferenceBurst: iptr(-3)}, true},

		{"valid ingest but bad outbound rejected", GovernanceOverrides{
			IngestMessagesPerSecond: fptr(500), IngestBurst: iptr(1000), OutboundMessagesPerSecond: fptr(-1)}, true},
		{"valid ingest + outbound but bad ai rejected", GovernanceOverrides{
			IngestMessagesPerSecond: fptr(500), OutboundMessagesPerSecond: fptr(50), AiInferenceBurst: iptr(-1)}, true},
		{"every dimension positive", GovernanceOverrides{
			IngestMessagesPerSecond: fptr(500), IngestBurst: iptr(1000),
			OutboundMessagesPerSecond: fptr(50), OutboundBurst: iptr(100),
			AiInferenceRequestsPerMinute: fptr(30), AiInferenceBurst: iptr(15)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.validate()
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// The error names the GraphQL field the caller actually sent, so an operator can
// tell WHICH dimension they got wrong.
func TestGovernanceOverrides_ValidateNamesTheField(t *testing.T) {
	err := GovernanceOverrides{AiInferenceRequestsPerMinute: fptr(-1)}.validate()
	assert.ErrorContains(t, err, "aiInferenceRequestsPerMinute")
	assert.ErrorContains(t, err, "omit it to inherit the platform default")
}

// applyTo writes every dimension onto the row, and a nil field writes nil (clearing
// the override back to the platform default) rather than being skipped. A field
// missing from this method would be silently unwritable through the admin API.
func TestGovernanceOverrides_ApplyTo(t *testing.T) {
	full := GovernanceOverrides{
		IngestMessagesPerSecond: fptr(500), IngestBurst: iptr(1000),
		OutboundMessagesPerSecond: fptr(50), OutboundBurst: iptr(100),
		AiInferenceRequestsPerMinute: fptr(30), AiInferenceBurst: iptr(15),
	}
	var tenant iam.Tenant
	full.applyTo(&tenant)
	assert.Equal(t, fptr(500), tenant.IngestMessagesPerSecond)
	assert.Equal(t, iptr(1000), tenant.IngestBurst)
	assert.Equal(t, fptr(50), tenant.OutboundMessagesPerSecond)
	assert.Equal(t, iptr(100), tenant.OutboundBurst)
	assert.Equal(t, fptr(30), tenant.AiInferenceRequestsPerMinute)
	assert.Equal(t, iptr(15), tenant.AiInferenceBurst)

	// A full replace: clearing every override reverts the tenant to the defaults.
	GovernanceOverrides{}.applyTo(&tenant)
	assert.Nil(t, tenant.IngestMessagesPerSecond)
	assert.Nil(t, tenant.IngestBurst)
	assert.Nil(t, tenant.OutboundMessagesPerSecond)
	assert.Nil(t, tenant.OutboundBurst)
	assert.Nil(t, tenant.AiInferenceRequestsPerMinute)
	assert.Nil(t, tenant.AiInferenceBurst)
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"errors"
	"math"
	"testing"

	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// i32 unwraps a governance Int resolver for the tests that read an in-range value and only
// care what it is. A refusal there is a test bug, so it panics rather than being dropped.
func i32(v *int32, err error) *int32 {
	if err != nil {
		panic("a governance Int resolver refused an in-range value: " + err.Error())
	}
	return v
}

// 🔴 A GOVERNANCE VALUE WIDER THAN A GraphQL Int IS REFUSED ON READ, NOT WRAPPED.
//
// The overrides are bigint columns behind a Go int, and the wire type is a 32-bit Int. The
// read used to narrow with a bare int32 conversion, so a stored 2147483648 read back as
// -2147483648 — a negative ceiling, served to the operator and to every enforcing service,
// with nothing to say it was wrong. Every governance Int now reads through
// util.IntPtrInt32, on both planes.
func TestAGovernanceValueWiderThanAnIntIsRefusedOnRead(t *testing.T) {
	wide := math.MaxInt32 + 1
	tenant := iam.Tenant{
		IngestBurst: &wide, OutboundBurst: &wide, AiInferenceBurst: &wide,
		ShedPriority: &wide, HeldCommandCeiling: &wide,
		GeoFencePositionCeiling: &wide, GeoFenceCeiling: &wide, GeoFencePositionBudget: &wide,
	}
	admin := &AdminTenantResolver{M: tenant}
	reads := map[string]func() (*int32, error){
		"admin ingestBurst":             admin.IngestBurst,
		"admin outboundBurst":           admin.OutboundBurst,
		"admin aiInferenceBurst":        admin.AiInferenceBurst,
		"admin shedPriority":            admin.ShedPriority,
		"admin heldCommandCeiling":      admin.HeldCommandCeiling,
		"admin geoFencePositionCeiling": admin.GeoFencePositionCeiling,
		"admin geoFenceCeiling":         admin.GeoFenceCeiling,
		"admin geoFencePositionBudget":  admin.GeoFencePositionBudget,
	}
	burst := &AdminBurstSettingResolver{value: &wide, tier: &wide, override: &wide}
	reads["settings value"] = burst.Value
	reads["settings tier"] = burst.Tier
	reads["settings override"] = burst.Override

	for name, read := range reads {
		got, err := read()
		if !errors.Is(err, util.ErrStoredIntOutOfRange) || got != nil {
			t.Errorf("%s: a stored 2147483648 read back as (%v, %v), want a refusal", name, got, err)
		}
	}

	// The data plane serves the EFFECTIVE value, and the cascade already treats an
	// out-of-band override it cannot honour as "inherit" — so an enforcing service keeps
	// getting a null it falls back from, not an error. The refusal must not change that.
	data := &TenantGovernanceResolver{t: &tenant}
	for name, read := range map[string]func() (*int32, error){
		"data ingestBurst":        data.IngestBurst,
		"data shedPriority":       data.ShedPriority,
		"data heldCommandCeiling": data.HeldCommandCeiling,
	} {
		got, err := read()
		if err != nil || got != nil {
			t.Errorf("%s: an out-of-band override read back as (%v, %v), want (nil, nil) — inherit", name, got, err)
		}
	}

	// The counterweight: the largest value an Int can hold still reads back exactly.
	max := math.MaxInt32
	got, err := (&AdminTenantResolver{M: iam.Tenant{IngestBurst: &max}}).IngestBurst()
	require.NoError(t, err)
	require.EqualValues(t, math.MaxInt32, *got)
}

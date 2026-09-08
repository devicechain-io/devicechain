// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

// deviceProfileTables is the table set every profile-scoped fixture needs. The profile's
// own definitions are listed because gorm resolves their associations on the profile's
// preload, so migrating the profile alone leaves the fixture failing on a missing table
// rather than on whatever it meant to assert.
var deviceProfileTables = []any{&Device{}, &DeviceType{}, &DeviceProfile{}, &DeviceProfileVersion{},
	&MetricDefinition{}, &CommandDefinition{}, &DetectionRule{}, &DetectionRuleScopeRef{}}

// seedDeviceProfile creates the device profile the profile-scoped fixtures hang off.
func seedDeviceProfile(t *testing.T, api *Api, ctx context.Context, token string) {
	t.Helper()
	if _, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: token}); err != nil {
		t.Fatalf("seed profile %q: %v", token, err)
	}
}

// Shorthands for building three-state update requests in tests.
//
// 🔴 THEY EXIST TO KEEP WHAT A TEST LEAVES ABSENT VISIBLE. Under a full-replace input,
// a field a test omitted was a field it silently cleared, so the two readings were the
// same and nobody had to choose. Under three states they differ, and a test whose call
// site is a wall of dcgraphql.OptionalStringOf(...) hides which fields it is actually
// exercising. geoFenceEdit names the one shape that dominates these files — replace the
// geometry, touch nothing else — so a call site that does anything MORE has to spell the
// literal out, and the extra field is then the thing that stands out.

// geoFenceEdit builds an update naming a new geometry and nothing else. Every other
// field is ABSENT, which is what the pre-conversion call sites meant by omitting it —
// except that they cleared it instead.
func geoFenceEdit(geometry string) *GeoFenceUpdateRequest {
	return &GeoFenceUpdateRequest{Geometry: dcgraphql.OptionalStringOf(geometry)}
}

// optionalStr adapts a fixture's *string to the three-state field: nil means ABSENT
// (leave the stored value alone), which is the reading a table-driven fixture wants when
// a row declines to say anything about the field.
//
// It is deliberately NOT the "nil clears it" reading the pointer used to have. A test
// table whose nil rows cleared a value would be asserting the full-replace semantic this
// package no longer has; if a case means to clear, it says dcgraphql.ClearedString().
func optionalStr(v *string) dcgraphql.OptionalString {
	if v == nil {
		return dcgraphql.OptionalString{}
	}
	return dcgraphql.OptionalStringOf(*v)
}

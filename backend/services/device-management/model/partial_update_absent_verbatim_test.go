// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

// 🔴 A FIELD THE CALLER DID NOT NAME IS NOT REWRITTEN.
//
// The shared partial-update suites cannot see this: every one of their seeds is already
// trimmed, so an absent branch that re-trims the stored value is a no-op there. A row
// holding surrounding whitespace — written by a raw path, an import, or a release that did
// not trim — was rewritten by any update that touched a DIFFERENT field, because the fold
// sent the stored value back through the nullable-text rule on its way through.
func TestAnUpdateLeavesAnUntrimmedFieldItDidNotName(t *testing.T) {
	api, ctx := resolveFixture(t)
	if err := api.RDB.DB(ctx).Model(&Device{}).Where("token = ?", "dev").
		Update("name", " padded ").Error; err != nil {
		t.Fatalf("seed an untrimmed name: %v", err)
	}

	if _, err := api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{
		Description: dcgraphql.OptionalStringOf("described"),
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	var reloaded Device
	if err := api.RDB.DB(ctx).Where("token = ?", "dev").First(&reloaded).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if want := (sql.NullString{String: " padded ", Valid: true}); reloaded.Name != want {
		t.Fatalf("name = %#v after an update that named only description, want %#v", reloaded.Name, want)
	}
	if want := (sql.NullString{String: "described", Valid: true}); reloaded.Description != want {
		t.Fatalf("description = %#v, want %#v — the update this test drives did not land", reloaded.Description, want)
	}
}

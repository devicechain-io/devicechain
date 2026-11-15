// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"gorm.io/datatypes"
)

// Exercise real authoring and historical reads: equal tokens and geometry must
// not make two incarnations of a hard-deleted fence look like the same entity.
func TestSnapshotFreezesFenceIdentityAcrossRecreation(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	create := func() *GeoFence {
		t.Helper()
		f, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "yard", Geometry: yardGeometry})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	assertID := func(version int32, id uint) {
		t.Helper()
		s, err := api.GeoFenceSetSnapshotAt(ctx, version)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Fences) != 1 || s.Fences[0].FenceID != fmt.Sprint(id) {
			t.Fatalf("version %d: got %+v, want frozen fence ID %d", version, s.Fences, id)
		}
	}
	first := create()
	assertID(1, first.ID)
	if _, err := api.UpdateGeoFence(ctx, "yard", geoFenceEdit(boxGeometry(10, 10, 11, 11))); err != nil {
		t.Fatal(err)
	}
	assertID(2, first.ID)
	if _, err := api.DeleteGeoFence(ctx, "yard"); err != nil {
		t.Fatal(err)
	}
	second := create()
	if first.ID == second.ID {
		t.Fatal("recreation reused the row ID")
	}
	assertID(4, second.ID)
	assertID(1, first.ID)
	assertID(2, first.ID)
}

// An old pod can still mint a snapshot with no identity during a rollout. Do
// not guess from the current token; the next new-pod edit upgrades only the head.
func TestLegacySnapshotIdentityRemainsUnknownAndNextMintCapturesIt(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	f, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "yard", Geometry: yardGeometry})
	if err != nil {
		t.Fatal(err)
	}
	var row GeoFenceSetVersion
	db := api.RDB.DB(ctx)
	if err := db.Where("version = ?", 1).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(row.Snapshot, &legacy); err != nil {
		t.Fatal(err)
	}
	var refs []map[string]json.RawMessage
	if err := json.Unmarshal(legacy["fences"], &refs); err != nil {
		t.Fatal(err)
	}
	delete(refs[0], "fenceId")
	legacy["fences"], err = json.Marshal(refs)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&GeoFenceSetVersion{}).Where("version = ?", 1).Update("snapshot", datatypes.JSON(raw)).Error; err != nil {
		t.Fatal(err)
	}
	assertLegacy := func() {
		t.Helper()
		s, err := api.GeoFenceSetSnapshotAt(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Fences) != 1 || s.Fences[0].FenceID != "" || s.Fences[0].Token != "yard" {
			t.Fatalf("legacy snapshot guessed an identity or lost a fence: %+v", s.Fences)
		}
	}
	assertLegacy()
	if _, err := api.UpdateGeoFence(ctx, "yard", &GeoFenceUpdateRequest{Name: dcgraphql.OptionalStringOf("Renamed")}); err != nil {
		t.Fatal(err)
	}
	s, err := api.CurrentGeoFenceSetSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != 2 || len(s.Fences) != 1 || s.Fences[0].FenceID != fmt.Sprint(f.ID) {
		t.Fatalf("next new-pod edit failed to capture identity: %+v", s)
	}
	assertLegacy()
}

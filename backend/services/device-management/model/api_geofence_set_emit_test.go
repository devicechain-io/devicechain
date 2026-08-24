// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// captureFenceSets records the fence-set facts emitted, so a test can assert that every
// authoring action that MINTS a version also ANNOUNCES it — the property the live geofence
// projection in event-processing is entirely built on (ADR-078).
type captureFenceSets struct {
	events []*GeoFenceSetMintedEvent
}

func (c *captureFenceSets) PublishGeoFenceSet(_ context.Context, e *GeoFenceSetMintedEvent) {
	c.events = append(c.events, e)
}

func newFenceSetEmitTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&GeoFence{}, &GeoFenceSetVersion{}, &GeoFenceGeometryBlob{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

// boxGeometry builds a POLYGON_2D envelope for an axis-aligned lon/lat box.
func boxGeometry(lonMin, latMin, lonMax, latMax float64) string {
	raw, err := json.Marshal(map[string]any{
		"kind": GeoFenceKindPolygon2D,
		"geometry": map[string]any{
			"type": "Polygon",
			"coordinates": [][][2]float64{{
				{lonMin, latMin}, {lonMax, latMin}, {lonMax, latMax}, {lonMin, latMax}, {lonMin, latMin},
			}},
		},
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// Every authoring action that mints a fence-set version announces that version's FROZEN set,
// and the fact's fences are the ones the version actually froze — not the ones live at emit
// time.
//
// The three actions are asserted in one sequence on purpose: the create/update/delete paths
// each mint independently, so a fact wired onto two of the three is exactly the defect this
// catches. The DELETE case carries the most weight — it announces version 3 with an EMPTY
// fence list, which is the "known-empty set" the consumer must not confuse with an absent
// one; an implementation that skipped emitting when there is nothing left to describe would
// leave the projection holding the pre-delete geometry forever.
func TestGeoFenceMutationsEmitTheFrozenFenceSet(t *testing.T) {
	api := newFenceSetEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	cap := &captureFenceSets{}
	api.GeoFenceSetPublisher = cap

	_, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "yard", Geometry: boxGeometry(0, 0, 1, 1)})
	assert.NoError(t, err)
	if assert.Len(t, cap.events, 1, "a create emits exactly one fence-set fact") {
		ev := cap.events[0]
		assert.Equal(t, int32(1), ev.Version)
		if assert.Len(t, ev.Fences, 1) {
			assert.Equal(t, "yard", ev.Fences[0].Token)
			assert.JSONEq(t, boxGeometry(0, 0, 1, 1), string(ev.Fences[0].Geometry))
		}
		assert.False(t, ev.MintedAt.IsZero(), "the fact carries the mint time")
	}

	// An edit mints the NEXT version and announces the MOVED geometry.
	_, err = api.UpdateGeoFence(ctx, "yard", &GeoFenceCreateRequest{Token: "yard", Geometry: boxGeometry(10, 10, 11, 11)})
	assert.NoError(t, err)
	if assert.Len(t, cap.events, 2, "an update emits a second fence-set fact") {
		ev := cap.events[1]
		assert.Equal(t, int32(2), ev.Version)
		if assert.Len(t, ev.Fences, 1) {
			assert.JSONEq(t, boxGeometry(10, 10, 11, 11), string(ev.Fences[0].Geometry),
				"the fact must carry the geometry the version froze, not the pre-edit one")
		}
	}

	// A delete mints a version whose set is EMPTY — knowledge, not absence.
	deleted, err := api.DeleteGeoFence(ctx, "yard")
	assert.NoError(t, err)
	assert.True(t, deleted)
	if assert.Len(t, cap.events, 3, "a delete emits a third fence-set fact") {
		ev := cap.events[2]
		assert.Equal(t, int32(3), ev.Version)
		assert.Empty(t, ev.Fences, "deleting the last fence announces a KNOWN-EMPTY set")
	}

	// A delete that names nothing mints nothing, so it must announce nothing: a fact for a
	// version that does not exist would file a phantom entry in the consumer's projection.
	deleted, err = api.DeleteGeoFence(ctx, "no-such-fence")
	assert.NoError(t, err)
	assert.False(t, deleted)
	assert.Len(t, cap.events, 3, "a no-op delete mints no version and must emit no fact")
}

// The fact survives the wire: what the consumer decodes is what the producer froze. This is
// the seam between two SERVICES, so a codec asymmetry here would show up as an empty or
// mis-versioned fence set at the far end with nothing failing in between.
func TestGeoFenceSetFactRoundTrips(t *testing.T) {
	api := newFenceSetEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	cap := &captureFenceSets{}
	api.GeoFenceSetPublisher = cap

	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "yard", Geometry: boxGeometry(0, 0, 1, 1)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	encoded, err := MarshalGeoFenceSetMintedEvent(cap.events[0])
	assert.NoError(t, err)

	decoded, err := UnmarshalGeoFenceSetMintedEvent(encoded)
	assert.NoError(t, err)
	assert.Equal(t, int32(1), decoded.Version)
	if assert.Len(t, decoded.Fences, 1) {
		assert.Equal(t, "yard", decoded.Fences[0].Token)
		assert.JSONEq(t, boxGeometry(0, 0, 1, 1), string(decoded.Fences[0].Geometry))
	}

	// A fact with no fence list decodes to an EMPTY set, never a nil one: the version minted
	// by deleting a tenant's last fence is knowledge that there are no fences, and the
	// consumer's whole miss/known-empty distinction depends on the two staying apart.
	empty, err := UnmarshalGeoFenceSetMintedEvent([]byte(`{"version":3}`))
	assert.NoError(t, err)
	assert.NotNil(t, empty.Fences)
	assert.Empty(t, empty.Fences)
}

// CurrentGeoFenceSetSnapshot returns the LATEST version's frozen set — version and fences
// from the same row, which is what event-processing's startup reconcile seeds from.
//
// The superseded version is the control: it still resolves through GeoFenceSetSnapshotAt to
// the geometry it froze, so "current" is genuinely selecting the newest rather than the only
// row that exists.
func TestCurrentGeoFenceSetSnapshotIsTheLatestVersion(t *testing.T) {
	api := newFenceSetEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "yard", Geometry: boxGeometry(0, 0, 1, 1)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := api.UpdateGeoFence(ctx, "yard", &GeoFenceCreateRequest{
		Token: "yard", Geometry: boxGeometry(10, 10, 11, 11)}); err != nil {
		t.Fatalf("update: %v", err)
	}

	current, err := api.CurrentGeoFenceSetSnapshot(ctx)
	assert.NoError(t, err)
	assert.Equal(t, int32(2), current.Version)
	if assert.Len(t, current.Fences, 1) {
		assert.JSONEq(t, boxGeometry(10, 10, 11, 11), string(current.Fences[0].Geometry))
	}

	superseded, err := api.GeoFenceSetSnapshotAt(ctx, 1)
	assert.NoError(t, err)
	assert.Equal(t, int32(1), superseded.Version)
	if assert.Len(t, superseded.Fences, 1) {
		assert.JSONEq(t, boxGeometry(0, 0, 1, 1), string(superseded.Fences[0].Geometry),
			"the superseded version must still resolve to the geometry IT froze")
	}
}

// A tenant that has never had a fence reads version 0 with an empty set — matching the stamp
// its events carry — rather than an error the reconcile would log as a failure on every
// restart.
func TestCurrentGeoFenceSetSnapshotForATenantWithNoFences(t *testing.T) {
	api := newFenceSetEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	current, err := api.CurrentGeoFenceSetSnapshot(ctx)
	assert.NoError(t, err)
	assert.Equal(t, int32(0), current.Version)
	assert.NotNil(t, current.Fences)
	assert.Empty(t, current.Fences)
}

// One tenant's fence set is not readable through another tenant's context, at the API layer
// the GraphQL doors sit on. The owner's read is the counterweight in the same test: without
// it, "the other tenant saw nothing" would pass against an Api that can see nothing at all.
func TestGeoFenceSetSnapshotIsTenantScoped(t *testing.T) {
	api := newFenceSetEmitTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	globex := core.WithTenant(context.Background(), "globex")

	if _, err := api.CreateGeoFence(acme, &GeoFenceCreateRequest{
		Token: "yard", Geometry: boxGeometry(0, 0, 1, 1)}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// The owner sees its own fence set (control).
	owned, err := api.GeoFenceSetSnapshotAt(acme, 1)
	assert.NoError(t, err)
	assert.Len(t, owned.Fences, 1, "the owning tenant must be able to read its own fence set")

	// The other tenant asking for the SAME version number gets not-found: version 1 is not a
	// global identifier, it is a per-tenant one, and there is no version 1 in globex.
	_, err = api.GeoFenceSetSnapshotAt(globex, 1)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound,
		"another tenant must not be able to read acme's fence set by naming its version")

	// ...and its current set is empty, not acme's.
	current, err := api.CurrentGeoFenceSetSnapshot(globex)
	assert.NoError(t, err)
	assert.Equal(t, int32(0), current.Version)
	assert.Empty(t, current.Fences)
}

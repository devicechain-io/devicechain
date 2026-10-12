// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/geofence"
)

// The tenant eviction drops cached fence geometry by asking the wired fence-set source to purge
// it, so the production source has to drop it from its own cache — for that tenant only, and
// reporting what it removed, because the eviction counts that figure as work done.
func TestTheFenceSetClientPurgesOnlyThatTenantsCachedGeometry(t *testing.T) {
	c := &fenceSetClient{cache: geofence.NewGeometryCache(geofence.DefaultMaxCachedVertices)}
	doc := `{"kind":"` + geofence.KindPolygon2D +
		`","geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}}`
	hash := docHash(doc)
	fetch := func(context.Context) ([]byte, error) { return []byte(doc), nil }
	for _, tenant := range []string{"acme", "acme-corp"} {
		if _, err := c.cache.Get(context.Background(), tenant, hash, fetch); err != nil {
			t.Fatalf("caching %s's geometry: %v", tenant, err)
		}
	}

	if n := c.PurgeTenant("acme"); n != 1 {
		t.Fatalf("PurgeTenant reported %d entries removed, want 1", n)
	}
	if c.cache.Held("acme", []string{hash})[hash] {
		t.Fatal("the purged tenant's compiled geometry is still cached")
	}
	if !c.cache.Held("acme-corp", []string{hash})[hash] {
		t.Fatal("another tenant's compiled geometry was dropped with it")
	}
}

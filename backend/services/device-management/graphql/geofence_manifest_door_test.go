// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// The manifest doors are the read half of manifest delivery: an engine that missed a fence
// edit, or is starting cold, learns WHICH fences a version holds here and fetches only the
// geometry it does not already hold through geoFenceGeometry.
//
// They are cross-service doors, so as with the snapshot doors their tenancy is not a UI
// concern — the caller is another service holding a service token, and the only thing between
// one tenant's fence geometry and another is that the request cannot name a tenant at all.
// These tests therefore drive the resolvers over ONE shared database holding two tenants,
// which is the arrangement that can actually fail.

// resolveGeometry reads documents through the real geoFenceGeometry resolver.
func resolveGeometry(t *testing.T, r *SchemaResolver, ctx context.Context, hashes ...string) []*GeoFenceGeometryResolver {
	t.Helper()
	found, err := r.GeoFenceGeometry(ctx, struct{ Hashes []string }{Hashes: hashes})
	if err != nil {
		t.Fatalf("geoFenceGeometry(%v): %v", hashes, err)
	}
	return found
}

// A tenant's current manifest names its OWN fence and only its own, and the addresses it
// names resolve — through the same doors — to that fence's geometry.
//
// Both directions are asserted, so this cannot pass by the doors returning nothing to
// anybody, and the resolution step is what makes the manifest more than a list of strings: a
// manifest whose addresses resolved to nothing would satisfy a token-only assertion.
func TestManifestDoorsAreTenantScoped(t *testing.T) {
	acme, globex := twoTenantFenceCtx(t)
	r := &SchemaResolver{}

	for _, tc := range []struct {
		ctx   context.Context
		token string
	}{
		{acme, "acme-yard"},
		{globex, "globex-depot"},
	} {
		manifest, err := r.CurrentGeoFenceSetManifest(tc.ctx)
		if err != nil {
			t.Fatalf("currentGeoFenceSetManifest for %s: %v", tc.token, err)
		}
		fences := manifest.Fences()
		if len(fences) != 1 {
			t.Fatalf("%s: manifest names %d fences; want 1", tc.token, len(fences))
		}
		if got := fences[0].Token(); got != tc.token {
			t.Fatalf("manifest named %q; want %q", got, tc.token)
		}
		if manifest.MintedAt() == nil {
			t.Fatalf("%s: manifest carries no mint time", tc.token)
		}

		hash := fences[0].Hash()
		docs := resolveGeometry(t, r, tc.ctx, hash)
		if len(docs) != 1 {
			t.Fatalf("%s: address %s resolved to %d documents; want 1", tc.token, hash, len(docs))
		}
		if got := model.GeoFenceGeometryHash([]byte(docs[0].Geometry())); got != hash {
			t.Fatalf("%s: the served document does not hash to the address asked for:\n asked %s\n  got %s",
				tc.token, hash, got)
		}
	}
}

// 🔴 TWO TENANTS WHO AUTHOR THE SAME GEOMETRY SHARE A CONTENT ADDRESS, AND THAT IS NOT A LEAK.
//
// This surprised a test written the obvious way — "the other tenant's address must not
// resolve" — which failed against correct code, because the fixture gives both tenants the
// same box and an address IS its content. Isolation here is at the ROW, not at the address:
// each tenant holds its own archive row, the read is confined by the tenant in context, and
// what an address discloses is a document the asking tenant already stores.
//
// Stated as a test because the wrong intuition is the natural one, and acting on it would
// mean keying the archive or the address by tenant — buying nothing, and breaking the
// deduplication that is the whole reason the archive exists.
func TestIdenticalGeometryAcrossTenantsSharesAnAddress(t *testing.T) {
	acme, globex := twoTenantFenceCtx(t)
	r := &SchemaResolver{}

	acmeHash := currentHash(t, r, acme)
	globexHash := currentHash(t, r, globex)
	if acmeHash != globexHash {
		t.Fatalf("two tenants authoring identical geometry got different addresses:\n acme %s\n globex %s",
			acmeHash, globexHash)
	}
	// Each resolves it out of its OWN row, which is what makes the sharing harmless.
	if got := resolveGeometry(t, r, acme, acmeHash); len(got) != 1 {
		t.Fatalf("acme cannot resolve the shared address")
	}
	if got := resolveGeometry(t, r, globex, globexHash); len(got) != 1 {
		t.Fatalf("globex cannot resolve the shared address")
	}
}

// Geometry only ONE tenant holds is not reachable by the other. This is the isolation
// assertion the shared-address case above cannot make: the fences must genuinely differ, or
// "the other tenant cannot resolve it" is true for a reason that has nothing to do with
// tenancy.
func TestGeometryOnlyOneTenantHoldsIsNotReachableByTheOther(t *testing.T) {
	acme, globex := twoTenantFenceCtx(t)
	r := &SchemaResolver{}

	if _, err := r.CreateGeoFence(withAuthorities(acme, auth.DeviceWrite, auth.DeviceRead, auth.LocationRead),
		struct {
			Request model.GeoFenceCreateRequest
		}{Request: model.GeoFenceCreateRequest{Token: "acme-only", Geometry: distinctTestGeometry}}); err != nil {
		t.Fatalf("seed acme-only fence: %v", err)
	}

	var unique string
	manifest, err := r.CurrentGeoFenceSetManifest(acme)
	if err != nil {
		t.Fatalf("currentGeoFenceSetManifest: %v", err)
	}
	for _, fence := range manifest.Fences() {
		if fence.Token() == "acme-only" {
			unique = fence.Hash()
		}
	}
	if unique == "" {
		t.Fatal("acme's manifest does not name the fence just created")
	}
	if unique == currentHash(t, r, globex) {
		t.Fatal("the geometry meant to be unique to acme shares globex's address, so the " +
			"assertion below would prove nothing")
	}

	if got := resolveGeometry(t, r, globex, unique); len(got) != 0 {
		t.Fatalf("globex resolved geometry only acme holds")
	}
	// The control: acme itself can.
	if got := resolveGeometry(t, r, acme, unique); len(got) != 1 {
		t.Fatalf("acme cannot resolve its own address, so the refusal above is not about tenancy")
	}
}

// distinctTestGeometry is a box nowhere near authTestGeometry, so a fence built from it has
// a different content address.
const distinctTestGeometry = `{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":` +
	`[[[10.0,20.0],[10.5,20.0],[10.5,20.5],[10.0,20.0]]]}}`

// currentHash reads the single address a tenant's current manifest names.
func currentHash(t *testing.T, r *SchemaResolver, ctx context.Context) string {
	t.Helper()
	manifest, err := r.CurrentGeoFenceSetManifest(ctx)
	if err != nil {
		t.Fatalf("currentGeoFenceSetManifest: %v", err)
	}
	fences := manifest.Fences()
	if len(fences) == 0 {
		t.Fatal("the tenant's manifest names no fences")
	}
	return fences[0].Hash()
}

// A version belonging to another tenant is not on record, and reads exactly like a version
// that never existed. The positive half is the control: the same version number IS resolvable
// for the tenant that minted it, so "not found" is about tenancy and not about the door
// failing for everyone.
func TestManifestDoorCannotReachAnotherTenantsVersion(t *testing.T) {
	acme, globex := twoTenantFenceCtx(t)
	r := &SchemaResolver{}

	mine, err := r.GeoFenceSetManifest(acme, struct{ Version int32 }{Version: 1})
	if err != nil {
		t.Fatalf("acme reading its own version 1: %v", err)
	}
	if len(mine.Fences()) != 1 || mine.Fences()[0].Token() != "acme-yard" {
		t.Fatalf("acme's version 1 manifest is %v", mine.Fences())
	}

	// globex also minted a version 1, and it must be ITS version 1 — not acme's.
	theirs, err := r.GeoFenceSetManifest(globex, struct{ Version int32 }{Version: 1})
	if err != nil {
		t.Fatalf("globex reading its own version 1: %v", err)
	}
	if len(theirs.Fences()) != 1 || theirs.Fences()[0].Token() != "globex-depot" {
		t.Fatalf("globex's version 1 manifest is %v", theirs.Fences())
	}
	if theirs.Fences()[0].Hash() == "" {
		t.Fatal("globex's manifest entry carries no address")
	}
}

// 🔴 ASKING FOR NO ADDRESSES MUST NOT ANSWER WITH EVERYTHING, asserted at the WIRE level
// rather than only over the model function.
//
// This is the shape that once made xById(ids: []) hand back the whole table, and the reason
// it is re-asserted here is that `hashes: [String!]!` is a legal document with an empty list —
// nothing upstream of the resolver rejects one. The control below is what makes the empty
// answer mean something: the same door, one line later, returns a document for a real address.
func TestGeometryDoorWithNoAddressesReturnsNothing(t *testing.T) {
	acme, _ := twoTenantFenceCtx(t)
	r := &SchemaResolver{}

	if got := resolveGeometry(t, r, acme); len(got) != 0 {
		t.Fatalf("asking for no geometry addresses returned %d documents", len(got))
	}

	manifest, err := r.CurrentGeoFenceSetManifest(acme)
	if err != nil {
		t.Fatalf("currentGeoFenceSetManifest: %v", err)
	}
	hash := manifest.Fences()[0].Hash()
	if got := resolveGeometry(t, r, acme, hash); len(got) != 1 {
		t.Fatalf("the tenant holds %d documents for a real address, so the empty answer above "+
			"proves nothing", len(got))
	}
}

// Over the per-request limit is refused rather than silently shortened, at the wire level.
func TestGeometryDoorRefusesAnOversizedRequest(t *testing.T) {
	acme, _ := twoTenantFenceCtx(t)
	r := &SchemaResolver{}

	hashes := make([]string, 0, model.MaxGeoFenceGeometryHashesPerRequest+1)
	for i := 0; i <= model.MaxGeoFenceGeometryHashesPerRequest; i++ {
		hashes = append(hashes, model.GeoFenceGeometryHash([]byte{byte(i)}))
	}
	_, err := r.GeoFenceGeometry(acme, struct{ Hashes []string }{Hashes: hashes})
	if err == nil {
		t.Fatalf("a request of %d addresses was accepted; over the limit must be refused", len(hashes))
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("the refusal should name the limit; got: %v", err)
	}

	// The control: one fewer address is accepted, so the refusal is about the limit and not
	// about long lists in general.
	if _, err := r.GeoFenceGeometry(acme, struct{ Hashes []string }{
		Hashes: hashes[:model.MaxGeoFenceGeometryHashesPerRequest]}); err != nil {
		t.Fatalf("a request of exactly the limit must be accepted: %v", err)
	}
}

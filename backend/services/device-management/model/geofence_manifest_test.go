// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// manifestFences reads the current manifest and returns its entries, asserting on the way
// past that every address it names is one the archive can actually answer.
//
// 🔴 THE ASSERTION LIVES IN THE READER FOR THE SAME REASON archivedBlobs's DOES. A manifest
// is a list of promises, and a test that checks only its LENGTH passes just as happily on a
// manifest naming a hundred addresses that resolve to nothing — which is precisely the
// failure splitting geometry out of the fact introduces. Putting the resolvability check
// where every test already looks means a test has to opt OUT of it.
func manifestFences(t *testing.T, api *Api, ctx context.Context) []GeoFenceManifestEntry {
	t.Helper()
	manifest, err := api.CurrentGeoFenceSetManifest(ctx)
	if err != nil {
		t.Fatalf("read current manifest: %v", err)
	}
	for _, entry := range manifest.Fences {
		found, err := api.GeoFenceGeometryDocuments(ctx, []string{entry.Hash})
		if err != nil {
			t.Fatalf("resolve %q -> %s: %v", entry.Token, entry.Hash, err)
		}
		if len(found) != 1 {
			t.Fatalf("manifest names %s for fence %q, which the archive answered with %d documents",
				entry.Hash, entry.Token, len(found))
		}
		if got := GeoFenceGeometryHash(found[0].Document); got != entry.Hash {
			t.Fatalf("the document served for fence %q does not hash to the address asked for:\n asked %s\n  got %s",
				entry.Token, entry.Hash, got)
		}
	}
	return manifest.Fences
}

// A manifest names every fence of its version and carries no geometry, and its size is a
// function of the fence count alone — the property the whole delivery change exists for.
//
// The second half is the one with teeth. Asserting "no geometry" on a struct that has no
// geometry field would be vacuous, so this measures the MARSHALLED manifest across two fence
// sets whose fences differ enormously in size, and requires the two to measure the same.
func TestManifestSizeIsAFunctionOfFenceCountAlone(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "tiny", Geometry: boxGeometry(0, 0, 1, 1)}); err != nil {
		t.Fatalf("create tiny: %v", err)
	}
	small, err := MarshalGeoFenceSetManifest(mustManifest(t, api, ctx))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Same fence count, vastly more geometry: a 400-position polygon in place of a box.
	if _, err := api.UpdateGeoFence(ctx, "tiny", &GeoFenceCreateRequest{
		Token: "tiny", Geometry: manyPointGeometry(400)}); err != nil {
		t.Fatalf("update tiny: %v", err)
	}
	large, err := MarshalGeoFenceSetManifest(mustManifest(t, api, ctx))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if len(small) != len(large) {
		t.Fatalf("a manifest changed size when its fence's GEOMETRY grew: %d -> %d bytes.\n"+
			"That is the one thing manifest delivery exists to prevent — it means something in the\n"+
			"manifest still depends on what a fence contains.\nsmall: %s\nlarge: %s",
			len(small), len(large), small, large)
	}

	// The control: the geometry really did change, so the equality above is not passing
	// because the update was a no-op.
	fences := manifestFences(t, api, ctx)
	if len(fences) != 1 {
		t.Fatalf("expected one fence, got %d", len(fences))
	}
	doc := mustResolve(t, api, ctx, fences[0].Hash)
	if len(doc) < 4000 {
		t.Fatalf("the enlarged geometry resolved to %d bytes; the update did not take effect, "+
			"so the size comparison above compared a fence set to itself", len(doc))
	}
}

// An address the tenant does not hold is ABSENT from the answer rather than an error — the
// contract that makes the door safe to expose, and the reason the caller carries the duty of
// turning a missing body into an errored fence rather than a vanished one.
func TestUnknownGeometryAddressIsAbsentNotAnError(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "yard", Geometry: boxGeometry(0, 0, 1, 1)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	real := manifestFences(t, api, ctx)[0].Hash
	absent := GeoFenceGeometryHash([]byte("no geometry was ever stored under this address"))

	found, err := api.GeoFenceGeometryDocuments(ctx, []string{real, absent})
	if err != nil {
		t.Fatalf("asking for one held and one unheld address must not be an error: %v", err)
	}
	if len(found) != 1 || found[0].Hash != real {
		t.Fatalf("want exactly the held address answered, got %+v", found)
	}
}

// 🔴 AN EMPTY ADDRESS LIST ANSWERS NOTHING, AND MUST NEVER ANSWER EVERYTHING.
//
// This is the shape that once made xById(ids: []) return the whole table. The string-list
// form is safe where the primary-key form is not — gorm renders an empty slice as
// `hash in (NULL)` — but the safety is a property of the SQL form and is invisible at the
// call site, so it is pinned here rather than assumed. The control proves the archive was
// non-empty, which is what makes the empty answer meaningful.
func TestNoAddressesAsksForNothing(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	for _, token := range []string{"a", "b", "c"} {
		if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
			Token: token, Geometry: boxGeometry(float64(len(token)), 0, float64(len(token))+1, 1)}); err != nil {
			t.Fatalf("create %s: %v", token, err)
		}
	}
	if held := archivedBlobs(t, api, ctx); len(held) == 0 {
		t.Fatal("the archive is empty, so an empty answer below would prove nothing")
	}

	found, err := api.GeoFenceGeometryDocuments(ctx, []string{})
	if err != nil {
		t.Fatalf("asking for no addresses: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("asking for NO geometry documents returned %d of them", len(found))
	}
	nilFound, err := api.GeoFenceGeometryDocuments(ctx, nil)
	if err != nil {
		t.Fatalf("asking for a nil address list: %v", err)
	}
	if len(nilFound) != 0 {
		t.Fatalf("a nil address list returned %d documents", len(nilFound))
	}
}

// Over the per-request limit is an ERROR, never a partial answer: a caller that asked for
// more than the cap and silently received the cap could not distinguish that from a tenant
// holding only that many, which is the confusion the absence contract depends on not
// existing. The at-the-limit case is the control — the refusal has to be about the limit and
// not about long lists in general.
func TestTooManyAddressesIsRefusedNotTruncated(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	atLimit := make([]string, 0, MaxGeoFenceGeometryHashesPerRequest)
	for i := 0; i < MaxGeoFenceGeometryHashesPerRequest; i++ {
		atLimit = append(atLimit, GeoFenceGeometryHash([]byte{byte(i)}))
	}
	if _, err := api.GeoFenceGeometryDocuments(ctx, atLimit); err != nil {
		t.Fatalf("a request of exactly %d addresses must be accepted: %v",
			MaxGeoFenceGeometryHashesPerRequest, err)
	}

	overLimit := append(atLimit, GeoFenceGeometryHash([]byte("one too many")))
	found, err := api.GeoFenceGeometryDocuments(ctx, overLimit)
	if err == nil {
		t.Fatalf("a request of %d addresses was accepted, answering %d documents; over the "+
			"limit must be refused rather than silently shortened", len(overLimit), len(found))
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("the refusal should name the limit so a caller can act on it; got: %v", err)
	}
}

// The archive is tenant-scoped, so one tenant's address is not on record for another — the
// same mechanism that confines every other geofence read, asserted here because this door
// takes caller-supplied addresses and so LOOKS like it could reach across.
func TestGeometryDoorIsTenantScoped(t *testing.T) {
	api := newGeoFenceTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	other := core.WithTenant(context.Background(), "globex")

	if _, err := api.CreateGeoFence(acme, &GeoFenceCreateRequest{
		Token: "yard", Geometry: boxGeometry(0, 0, 1, 1)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	acmeHash := manifestFences(t, api, acme)[0].Hash

	found, err := api.GeoFenceGeometryDocuments(other, []string{acmeHash})
	if err != nil {
		t.Fatalf("reading another tenant's address must be an ordinary empty answer: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("tenant globex resolved acme's geometry address %s", acmeHash)
	}

	// The control: the address is genuinely resolvable, for its own tenant.
	if len(mustResolve(t, api, acme, acmeHash)) == 0 {
		t.Fatal("the address does not resolve for its own tenant either, so the isolation " +
			"assertion above proves nothing")
	}
}

// Version 0 is an empty manifest (the tenant never had a fence — knowledge, not absence),
// while an unknown non-zero version is an error, because a stamp naming a version that is not
// on record means the history was truncated and "empty" would read as "no fences".
func TestManifestAtUnknownVersion(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	zero, err := api.GeoFenceSetManifestAt(ctx, 0)
	if err != nil {
		t.Fatalf("version 0: %v", err)
	}
	if zero.Version != 0 || zero.Fences == nil || len(zero.Fences) != 0 {
		t.Fatalf("version 0 must be an empty, non-nil manifest; got %+v", zero)
	}

	if _, err := api.GeoFenceSetManifestAt(ctx, 99); err != gorm.ErrRecordNotFound {
		t.Fatalf("an unknown non-zero version must be ErrRecordNotFound; got %v", err)
	}
}

// The current manifest tracks the LATEST version, and a superseded version still resolves to
// what it froze. The second half is the control: without it, "current" could be selecting the
// only row that exists rather than the newest.
func TestCurrentManifestIsTheLatestVersion(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "yard", Geometry: boxGeometry(0, 0, 1, 1)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	first := manifestFences(t, api, ctx)[0].Hash

	if _, err := api.UpdateGeoFence(ctx, "yard", &GeoFenceCreateRequest{
		Token: "yard", Geometry: boxGeometry(5, 5, 6, 6)}); err != nil {
		t.Fatalf("update: %v", err)
	}
	current, err := api.CurrentGeoFenceSetManifest(ctx)
	if err != nil {
		t.Fatalf("current manifest: %v", err)
	}
	if current.Version != 2 {
		t.Fatalf("current manifest is version %d; want 2", current.Version)
	}
	if current.Fences[0].Hash == first {
		t.Fatal("the current manifest still names the pre-edit geometry")
	}
	if current.MintedAt.IsZero() {
		t.Fatal("the manifest carries no mint time")
	}

	// The superseded version is still resolvable, and still names what it froze — which is
	// what makes a replay of an event stamped with it correct.
	old, err := api.GeoFenceSetManifestAt(ctx, 1)
	if err != nil {
		t.Fatalf("manifest at version 1: %v", err)
	}
	if len(old.Fences) != 1 || old.Fences[0].Hash != first {
		t.Fatalf("version 1's manifest no longer names the geometry it froze: %+v", old.Fences)
	}
}

// The served document is byte-identical to what the archive holds, which is what lets a
// consumer verify a body against the address it asked for. Any re-encoding on the way out —
// however equivalent the JSON — breaks every caller's verification at once.
func TestServedDocumentsAreTheStoredBytes(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "yard", Geometry: manyPointGeometry(64)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	stored := archivedBlobs(t, api, ctx)
	if len(stored) != 1 {
		t.Fatalf("want one archived document, got %d", len(stored))
	}
	served := mustResolve(t, api, ctx, stored[0].Hash)
	if string(served) != string(stored[0].Document) {
		t.Fatalf("the served document is not the stored bytes:\nstored %s\nserved %s",
			stored[0].Document, served)
	}
}

func mustManifest(t *testing.T, api *Api, ctx context.Context) *GeoFenceSetManifest {
	t.Helper()
	manifest, err := api.CurrentGeoFenceSetManifest(ctx)
	if err != nil {
		t.Fatalf("current manifest: %v", err)
	}
	return manifest
}

func mustResolve(t *testing.T, api *Api, ctx context.Context, hash string) []byte {
	t.Helper()
	found, err := api.GeoFenceGeometryDocuments(ctx, []string{hash})
	if err != nil {
		t.Fatalf("resolve %s: %v", hash, err)
	}
	if len(found) != 1 {
		t.Fatalf("resolving %s answered %d documents", hash, len(found))
	}
	return found[0].Document
}

// manyPointGeometry builds a valid POLYGON_2D ring with n distinct positions — a fence whose
// geometry document is large, so a size claim can be made against a real one rather than a
// padded string. The ring is a circle, which keeps it simple and non-self-intersecting at any
// n; the closing position repeats the first, as GeoJSON requires.
func manyPointGeometry(n int) string {
	positions := make([][2]float64, 0, n+1)
	for i := 0; i < n; i++ {
		angle := 2 * math.Pi * float64(i) / float64(n)
		positions = append(positions, [2]float64{
			math.Round(math.Cos(angle)*1e6) / 1e6,
			math.Round(math.Sin(angle)*1e6) / 1e6,
		})
	}
	positions = append(positions, positions[0])
	raw, err := json.Marshal(map[string]any{
		"kind": GeoFenceKindPolygon2D,
		"geometry": map[string]any{
			"type":        "Polygon",
			"coordinates": [][][2]float64{positions},
		},
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

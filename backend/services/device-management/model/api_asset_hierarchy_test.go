// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// hierarchyTestApi builds the sqlite-backed Api these tests run against. It
// registers the token grammar for the same reason the replacement harness does: it
// is registered unconditionally in production, SetAssetParent MINTS an edge token,
// and a fixture more permissive than production cannot tell a valid minted token
// from an invalid one.
func hierarchyTestApi(t *testing.T) (*Api, context.Context) {
	t.Helper()

	// A shared-cache, per-test named in-memory DB, for the reason
	// newGroupMemberTestApi documents: CreateEntityRelationships resolves its
	// source/target on a NON-transaction connection while its write transaction is
	// open (the production Postgres pattern), so every connection in the pool has to
	// see the same tables — which a bare ":memory:" (one database per connection)
	// does not provide.
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err, "open sqlite")
	require.NoError(t, rdb.RegisterTenantScoping(db), "register tenant scoping")
	require.NoError(t, rdb.RegisterTokenGrammar(db), "register token grammar")
	require.NoError(t, db.AutoMigrate(
		&AssetType{}, &Asset{}, &DeviceType{}, &Device{},
		&CustomerType{}, &Customer{}, &DeviceClaim{},
		&EntityRelationshipType{}, &EntityRelationship{},
	), "migrate")

	api := NewApi(&rdb.RdbManager{Database: db})
	return api, core.WithTenant(context.Background(), "acme")
}

// seedAssets creates the named assets and returns them by token.
func seedAssets(t *testing.T, api *Api, ctx context.Context, tokens ...string) map[string]*Asset {
	t.Helper()

	assetType := &AssetType{}
	assetType.Token = "machine"
	require.NoError(t, api.RDB.DB(ctx).Create(assetType).Error, "seed asset type")

	assets := make(map[string]*Asset, len(tokens))
	for _, token := range tokens {
		created, err := api.CreateAsset(ctx, &AssetCreateRequest{
			Token:          token,
			AssetTypeToken: assetType.Token,
		})
		require.NoError(t, err, "seed asset %q", token)
		assets[token] = created
	}
	return assets
}

// tokensOf renders a slice of assets as their tokens, for readable assertions.
func tokensOf(assets []*Asset) []string {
	tokens := make([]string, 0, len(assets))
	for _, asset := range assets {
		tokens = append(tokens, asset.Token)
	}
	return tokens
}

// A three-level tree is authored, and every read answers about it: parent, the
// nearest-first ancestor path, the children of a node, and the roots.
func TestAssetHierarchyRoundTrips(t *testing.T) {
	api, ctx := hierarchyTestApi(t)
	seedAssets(t, api, ctx, "plant", "line-a", "cell-1", "cell-2", "unrelated")

	_, err := api.SetAssetParent(ctx, "line-a", "plant")
	require.NoError(t, err, "place line under plant")
	_, err = api.SetAssetParent(ctx, "cell-1", "line-a")
	require.NoError(t, err, "place cell-1 under line")
	_, err = api.SetAssetParent(ctx, "cell-2", "line-a")
	require.NoError(t, err, "place cell-2 under line")

	parent, err := api.AssetParent(ctx, "cell-1")
	require.NoError(t, err, "read parent")
	require.NotNil(t, parent, "cell-1 has no parent")
	require.Equal(t, "line-a", parent.Token, "wrong parent")

	// A root reports NO parent rather than an error — most assets are roots.
	root, err := api.AssetParent(ctx, "plant")
	require.NoError(t, err, "read a root's parent")
	require.Nil(t, root, "a root reported a parent")

	// Nearest first. The order IS the answer: a breadcrumb printed root-first is
	// wrong in a way that still looks like a breadcrumb.
	ancestors, err := api.AssetAncestors(ctx, "cell-1")
	require.NoError(t, err, "read ancestors")
	require.Equal(t, []string{"line-a", "plant"}, tokensOf(ancestors),
		"ancestors are not nearest-first")

	children, err := api.AssetChildren(ctx, strPtr("line-a"), rdb.Pagination{PageNumber: 1, PageSize: 10})
	require.NoError(t, err, "read children")
	require.ElementsMatch(t, []string{"cell-1", "cell-2"}, tokensOf(assetPtrs(children.Results)),
		"wrong children")

	// Roots: everything with no incoming containment edge. "unrelated" was never
	// placed, so it is one — a tree browser must show assets nobody filed.
	roots, err := api.AssetChildren(ctx, nil, rdb.Pagination{PageNumber: 1, PageSize: 10})
	require.NoError(t, err, "read roots")
	require.ElementsMatch(t, []string{"plant", "unrelated"}, tokensOf(assetPtrs(roots.Results)),
		"the root level is wrong")
}

// Re-parenting moves the asset and its whole subtree, and leaves exactly one parent
// edge behind.
//
// This is the case that would fail if the single-parent check read on a different
// handle from the delete: the clear happens inside the transaction, so a check
// issued on a fresh session would still see the old edge and refuse every move.
func TestSetAssetParentReParents(t *testing.T) {
	api, ctx := hierarchyTestApi(t)
	seedAssets(t, api, ctx, "plant-a", "plant-b", "line", "cell")

	_, err := api.SetAssetParent(ctx, "line", "plant-a")
	require.NoError(t, err, "initial placement")
	_, err = api.SetAssetParent(ctx, "cell", "line")
	require.NoError(t, err, "place cell under line")

	_, err = api.SetAssetParent(ctx, "line", "plant-b")
	require.NoError(t, err, "re-parent")

	parent, err := api.AssetParent(ctx, "line")
	require.NoError(t, err, "read parent after re-parent")
	require.Equal(t, "plant-b", parent.Token, "the re-parent did not take")

	// The subtree travelled with it.
	ancestors, err := api.AssetAncestors(ctx, "cell")
	require.NoError(t, err, "read ancestors after re-parent")
	require.Equal(t, []string{"line", "plant-b"}, tokensOf(ancestors), "the subtree did not travel")

	// Exactly one live parent edge — the old one was removed, not left alongside.
	children, err := api.AssetChildren(ctx, strPtr("plant-a"), rdb.Pagination{PageNumber: 1, PageSize: 10})
	require.NoError(t, err, "read old parent's children")
	require.Empty(t, children.Results, "the old parent still claims the asset")
}

// ClearAssetParent makes an asset a root, its children travel with it, and the
// return value distinguishes "detached something" from "already a root".
func TestClearAssetParent(t *testing.T) {
	api, ctx := hierarchyTestApi(t)
	seedAssets(t, api, ctx, "plant", "line", "cell")
	_, err := api.SetAssetParent(ctx, "line", "plant")
	require.NoError(t, err, "place line")
	_, err = api.SetAssetParent(ctx, "cell", "line")
	require.NoError(t, err, "place cell")

	removed, err := api.ClearAssetParent(ctx, "line")
	require.NoError(t, err, "clear parent")
	require.True(t, removed, "clearing a placed asset reported no change")

	parent, err := api.AssetParent(ctx, "line")
	require.NoError(t, err, "read parent")
	require.Nil(t, parent, "the asset still has a parent")

	ancestors, err := api.AssetAncestors(ctx, "cell")
	require.NoError(t, err, "read ancestors")
	require.Equal(t, []string{"line"}, tokensOf(ancestors), "the child did not travel with its parent")

	// Idempotent, and it says so. A bare true would make "already a root" and
	// "detached one" indistinguishable, which is the difference between a no-op and
	// a change the caller may need to log.
	removed, err = api.ClearAssetParent(ctx, "line")
	require.NoError(t, err, "clear an already-root asset")
	require.False(t, removed, "clearing a root reported a change")
}

// The three structural refusals, each stated as its own sentinel so a caller can
// tell them apart.
func TestSetAssetParentRefusalsThroughTheAuthoringDoor(t *testing.T) {
	api, ctx := hierarchyTestApi(t)
	seedAssets(t, api, ctx, "plant", "line", "cell")
	_, err := api.SetAssetParent(ctx, "line", "plant")
	require.NoError(t, err, "place line")
	_, err = api.SetAssetParent(ctx, "cell", "line")
	require.NoError(t, err, "place cell")

	_, err = api.SetAssetParent(ctx, "plant", "plant")
	require.ErrorIs(t, err, ErrAssetParentSelf, "an asset was accepted as its own parent")

	// A cycle at one remove: plant is already above cell, so putting plant under
	// cell closes a loop. The direct case (line under cell) is not the interesting
	// one — a check that only compared the two ends would catch that and miss this.
	_, err = api.SetAssetParent(ctx, "plant", "cell")
	require.ErrorIs(t, err, ErrAssetParentCycle, "a cycle two levels deep was accepted")

	// The tree survived both refusals intact.
	ancestors, err := api.AssetAncestors(ctx, "cell")
	require.NoError(t, err, "read ancestors")
	require.Equal(t, []string{"line", "plant"}, tokensOf(ancestors), "a refused move damaged the tree")
}

// 🔴 THE INVARIANTS HOLD THROUGH THE GENERIC EDGE API TOO, which is the whole reason
// admitContainmentEdge lives where it does. createEntityRelationship is a public
// mutation any device:write holder can call with relationshipType "contains"; an
// invariant checked only in SetAssetParent is an invariant with a public bypass, and
// every assertion in the test above would still have passed with that bypass wide
// open.
func TestContainmentInvariantsHoldOnTheGenericEdgeApi(t *testing.T) {
	api, ctx := hierarchyTestApi(t)
	seedAssets(t, api, ctx, "plant", "line", "cell")
	seedDeviceForHierarchy(t, api, ctx, "dozer-01")

	_, err := api.SetAssetParent(ctx, "line", "plant")
	require.NoError(t, err, "place line")

	// Self-containment.
	_, err = api.CreateEntityRelationship(ctx, containsRequest("plant", "plant"))
	require.ErrorIs(t, err, ErrAssetParentSelf, "the generic API accepted a self-containment edge")

	// A second parent.
	_, err = api.CreateEntityRelationship(ctx, containsRequest("line", "cell"))
	require.NoError(t, err, "the generic API refused a legal containment edge")
	_, err = api.CreateEntityRelationship(ctx, containsRequest("plant", "line"))
	require.Error(t, err, "the generic API gave an asset a second parent")
	require.Contains(t, err.Error(), "already has a parent", "unexpected error: %v", err)

	// A cycle.
	_, err = api.CreateEntityRelationship(ctx, containsRequest("line", "plant"))
	require.ErrorIs(t, err, ErrAssetParentCycle, "the generic API closed a cycle")

	// A non-asset end. "contains" is an ASSET hierarchy; the same token pointed at a
	// device is a different concept wearing this one's name, and the tree walks would
	// follow it out of the family.
	request := containsRequest("plant", "dozer-01")
	request.TargetType = string(entity.TypeDevice)
	_, err = api.CreateEntityRelationship(ctx, request)
	require.ErrorIs(t, err, ErrContainmentEndsMustBeAssets, "a device was accepted as a containment child")

	// The counterweight: a legal edge of ANOTHER type between the same two entities
	// is untouched, so the gate is scoped to "contains" and is not just refusing
	// asset edges wholesale.
	assignment, err := api.EnsureAssignmentType(ctx)
	require.NoError(t, err, "ensure assignment type")
	_, err = api.CreateEntityRelationship(ctx, &EntityRelationshipCreateRequest{
		Token:            uuid.New().String(),
		SourceType:       string(entity.TypeDevice),
		Source:           "dozer-01",
		TargetType:       string(entity.TypeAsset),
		Target:           "plant",
		RelationshipType: assignment.Token,
	})
	require.NoError(t, err, "the containment gate refused an unrelated relationship type")
}

// The BULK edge path is gated too, and it sees edges created earlier in its OWN
// batch. Checking each edge against the state before the transaction would let a
// pair of edges close a loop that neither closes alone — the whole batch legal, the
// result cyclic.
func TestContainmentInvariantsHoldWithinOneBulkBatch(t *testing.T) {
	api, ctx := hierarchyTestApi(t)
	seedAssets(t, api, ctx, "plant", "line")

	_, err := api.CreateEntityRelationships(ctx, []*EntityRelationshipCreateRequest{
		containsRequest("line", "plant"),
		containsRequest("plant", "line"),
	})
	require.Error(t, err, "a batch of two edges closed a cycle")
	require.ErrorIs(t, err, ErrAssetParentCycle, "unexpected error: %v", err)

	// All-or-nothing: the first edge did not survive the batch's refusal.
	parent, err := api.AssetParent(ctx, "plant")
	require.NoError(t, err, "read parent")
	require.Nil(t, parent, "a refused batch left one of its edges behind")

	// The counterweight: a legal batch applies. A gate that refused every batch
	// would pass the assertions above.
	_, err = api.CreateEntityRelationships(ctx, []*EntityRelationshipCreateRequest{
		containsRequest("line", "plant"),
	})
	require.NoError(t, err, "a legal containment batch was refused")
}

// The depth bound is enforced on incremental authoring: a chain cannot be extended
// past MaxAssetHierarchyDepth one edge at a time.
func TestAssetHierarchyDepthBoundOnAppend(t *testing.T) {
	api, ctx := hierarchyTestApi(t)

	tokens := make([]string, 0, MaxAssetHierarchyDepth+1)
	for i := 0; i <= MaxAssetHierarchyDepth; i++ {
		tokens = append(tokens, fmt.Sprintf("level-%02d", i))
	}
	seedAssets(t, api, ctx, tokens...)

	// Build the deepest legal chain: MaxAssetHierarchyDepth assets, so the lowest has
	// MaxAssetHierarchyDepth-1 ancestors.
	for i := 1; i < MaxAssetHierarchyDepth; i++ {
		_, err := api.SetAssetParent(ctx, tokens[i], tokens[i-1])
		require.NoError(t, err, "placing level %d was refused inside the limit", i)
	}

	_, err := api.SetAssetParent(ctx, tokens[MaxAssetHierarchyDepth], tokens[MaxAssetHierarchyDepth-1])
	require.ErrorIs(t, err, ErrAssetHierarchyTooDeep, "the depth bound did not stop an over-deep append")

	// The deepest legal chain still READS, so the bound refuses the edge past it
	// rather than the walk of the tree it allows.
	ancestors, err := api.AssetAncestors(ctx, tokens[MaxAssetHierarchyDepth-1])
	require.NoError(t, err, "the deepest legal chain cannot be walked")
	require.Len(t, ancestors, MaxAssetHierarchyDepth-1, "wrong ancestor count at the limit")
}

// The depth bound also survives a RE-PARENT, which is the case a parent-chain check
// alone cannot see: the subtree travelling with the moved asset is what breaches the
// limit, and its height is not visible from the parent's ancestors.
//
// 🔑 This is the input class that separates "every edge was legal when created" from
// "the tree they form is within the limit". Without the subtree-height check the
// move below succeeds and the bound reads as enforced while not being.
func TestAssetHierarchyDepthBoundOnReParentOfASubtree(t *testing.T) {
	api, ctx := hierarchyTestApi(t)

	// Two chains, each about half the limit, both built legally and independently.
	half := MaxAssetHierarchyDepth / 2
	left := make([]string, 0, half)
	right := make([]string, 0, half+2)
	for i := 0; i < half; i++ {
		left = append(left, fmt.Sprintf("left-%02d", i))
	}
	for i := 0; i < half+2; i++ {
		right = append(right, fmt.Sprintf("right-%02d", i))
	}
	seedAssets(t, api, ctx, append(append([]string{}, left...), right...)...)

	for i := 1; i < len(left); i++ {
		_, err := api.SetAssetParent(ctx, left[i], left[i-1])
		require.NoError(t, err, "build left chain")
	}
	for i := 1; i < len(right); i++ {
		_, err := api.SetAssetParent(ctx, right[i], right[i-1])
		require.NoError(t, err, "build right chain")
	}

	// Hanging the whole right chain off the bottom of the left one makes a chain
	// longer than the limit, even though every edge in both chains was legal.
	_, err := api.SetAssetParent(ctx, right[0], left[len(left)-1])
	require.ErrorIs(t, err, ErrAssetHierarchyTooDeep,
		"re-parenting a subtree pushed the tree past the depth limit")

	// The refusal left the right chain where it was, rather than detaching its root.
	parent, err := api.AssetParent(ctx, right[0])
	require.NoError(t, err, "read the moved asset's parent")
	require.Nil(t, parent, "the refused move changed the subtree's placement")

	// The counterweight: a subtree that DOES fit is accepted, so the check is a
	// bound and not a blanket refusal of subtree moves.
	seedShallow := seedAssets(t, api, ctx, "shallow-root", "shallow-child")
	_, err = api.SetAssetParent(ctx, "shallow-child", "shallow-root")
	require.NoError(t, err, "build the shallow subtree")
	_, err = api.SetAssetParent(ctx, seedShallow["shallow-root"].Token, left[0])
	require.NoError(t, err, "a subtree that fits within the limit was refused")
}

// An asset that does not exist is refused, on every door, before anything is
// written.
func TestAssetHierarchyRefusesUnknownAssets(t *testing.T) {
	api, ctx := hierarchyTestApi(t)
	seedAssets(t, api, ctx, "plant")

	_, err := api.SetAssetParent(ctx, "no-such-asset", "plant")
	require.Error(t, err, "an unknown child was accepted")
	_, err = api.SetAssetParent(ctx, "plant", "no-such-asset")
	require.Error(t, err, "an unknown parent was accepted")
	_, err = api.AssetParent(ctx, "no-such-asset")
	require.Error(t, err, "reading an unknown asset's parent succeeded")
	_, err = api.AssetAncestors(ctx, "no-such-asset")
	require.Error(t, err, "reading an unknown asset's ancestors succeeded")
	_, err = api.AssetChildren(ctx, strPtr("no-such-asset"), rdb.Pagination{PageNumber: 1, PageSize: 10})
	require.Error(t, err, "reading an unknown asset's children succeeded")
}

// Containment edges are asset-scoped: an asset that is only a group MEMBER or a
// device ASSIGNMENT target is still a root, because the roots predicate matches the
// containment type alone.
//
// Without the relationship-type predicate the roots list would silently drop every
// asset that had ever been added to a group — a wrong answer that looks like a
// correct, shorter one.
func TestAssetRootsIgnoreOtherRelationshipTypes(t *testing.T) {
	api, ctx := hierarchyTestApi(t)
	seedAssets(t, api, ctx, "plant", "grouped")
	seedDeviceForHierarchy(t, api, ctx, "dozer-01")

	assignment, err := api.EnsureAssignmentType(ctx)
	require.NoError(t, err, "ensure assignment type")
	_, err = api.CreateEntityRelationship(ctx, &EntityRelationshipCreateRequest{
		Token:            uuid.New().String(),
		SourceType:       string(entity.TypeDevice),
		Source:           "dozer-01",
		TargetType:       string(entity.TypeAsset),
		Target:           "grouped",
		RelationshipType: assignment.Token,
	})
	require.NoError(t, err, "assign device to asset")

	roots, err := api.AssetChildren(ctx, nil, rdb.Pagination{PageNumber: 1, PageSize: 10})
	require.NoError(t, err, "read roots")
	require.ElementsMatch(t, []string{"plant", "grouped"}, tokensOf(assetPtrs(roots.Results)),
		"an asset that is a device-assignment target was dropped from the roots")
}

// 🔴 A CYCLE PLANTED AROUND THE API MAKES EVERY WALK REFUSE, NOT SPIN.
//
// This is the input class the write-time cycle check cannot produce and therefore
// cannot cover. admitContainmentEdge is what keeps the graph acyclic, so as long as
// it is the only writer no walk can loop — which means the bound inside
// assetAncestorIds is dead code against every other test in this file, and deleting
// it would leave them all green. The bound exists for the case where that premise
// stops holding: a hand-run UPDATE, a restored backup, a future path that forgets
// this file.
//
// The cycle is planted with a direct gorm Create, which is the one door that skips
// the admission gate — the same shape as somebody writing the row by hand.
func TestAssetAncestorWalkRefusesACyclePlantedOutsideTheApi(t *testing.T) {
	api, ctx := hierarchyTestApi(t)
	assets := seedAssets(t, api, ctx, "a", "b")

	// The legal half, through the API.
	_, err := api.SetAssetParent(ctx, "b", "a")
	require.NoError(t, err, "place b under a")

	// The illegal half, around it: a now also has b as its parent.
	containment, err := api.EnsureContainmentType(ctx)
	require.NoError(t, err, "ensure containment type")
	planted := &EntityRelationship{
		TokenReference:     rdb.TokenReference{Token: uuid.New().String()},
		SourceType:         string(entity.TypeAsset),
		SourceId:           assets["b"].ID,
		TargetType:         string(entity.TypeAsset),
		TargetId:           assets["a"].ID,
		TargetToken:        "a",
		RelationshipTypeId: containment.ID,
	}
	require.NoError(t, api.RDB.DB(ctx).Create(planted).Error, "plant the cycle")

	// The walk refuses rather than looping. A truncated answer would be worse than
	// the refusal: it is indistinguishable from a correct breadcrumb.
	_, err = api.AssetAncestors(ctx, "a")
	require.ErrorIs(t, err, ErrAssetHierarchyTooDeep,
		"the ancestor walk did not refuse a cyclic graph")

	// And every write that has to walk refuses too, rather than committing an edge
	// whose legality it could not establish.
	seedAssets(t, api, ctx, "c")
	_, err = api.SetAssetParent(ctx, "c", "a")
	require.ErrorIs(t, err, ErrAssetHierarchyTooDeep,
		"a placement under a cyclic branch was accepted")
}

// An ASSET→ASSET edge of a CUSTOM relationship type is not containment.
//
// 🔴 THIS IS THE INPUT CLASS TestAssetRootsIgnoreOtherRelationshipTypes COULD NOT
// REACH, and a surviving mutant is what named it. That test uses a device→asset
// assignment, which containmentEdges already excludes on `source_type` alone — so
// swapping the relationship-type predicate for a different token changed nothing and
// the predicate went untested. Only an edge that passes the BOTH-ENDS-ASSETS filter
// and must still be rejected can exercise it.
//
// A tenant modelling "feeds" or "adjacent-to" between assets is the ordinary case, not
// a contrived one, and reading those as parenthood would silently restructure the tree.
func TestCustomAssetToAssetEdgesAreNotContainment(t *testing.T) {
	api, ctx := hierarchyTestApi(t)
	seedAssets(t, api, ctx, "tank", "pump", "valve")

	feeds, err := api.CreateEntityRelationshipType(ctx, &EntityRelationshipTypeCreateRequest{
		Token: "feeds",
	})
	require.NoError(t, err, "create custom relationship type")

	// tank feeds pump: an asset→asset edge, which the containment predicate must not
	// match. It is created through the generic API, so it also proves the admission
	// gate leaves a non-containment type alone.
	_, err = api.CreateEntityRelationship(ctx, &EntityRelationshipCreateRequest{
		Token:            uuid.New().String(),
		SourceType:       string(entity.TypeAsset),
		Source:           "tank",
		TargetType:       string(entity.TypeAsset),
		Target:           "pump",
		RelationshipType: feeds.Token,
	})
	require.NoError(t, err, "a custom asset-to-asset edge was refused")

	// Neither end moved in the hierarchy.
	parent, err := api.AssetParent(ctx, "pump")
	require.NoError(t, err, "read parent")
	require.Nil(t, parent, "a \"feeds\" edge was read as parenthood")

	roots, err := api.AssetChildren(ctx, nil, rdb.Pagination{PageNumber: 1, PageSize: 10})
	require.NoError(t, err, "read roots")
	require.ElementsMatch(t, []string{"tank", "pump", "valve"}, tokensOf(assetPtrs(roots.Results)),
		"a custom asset-to-asset edge removed an asset from the roots")

	children, err := api.AssetChildren(ctx, strPtr("tank"), rdb.Pagination{PageNumber: 1, PageSize: 10})
	require.NoError(t, err, "read children")
	require.Empty(t, children.Results, "a \"feeds\" target was listed as a child")

	// The counterweight: a real containment edge between the same two assets IS seen,
	// so the predicate discriminates rather than matching nothing.
	_, err = api.SetAssetParent(ctx, "pump", "tank")
	require.NoError(t, err, "place pump under tank")
	parent, err = api.AssetParent(ctx, "pump")
	require.NoError(t, err, "read parent after placement")
	require.NotNil(t, parent, "a real containment edge was not seen")
	require.Equal(t, "tank", parent.Token, "wrong parent")
}

// Redeeming a device claim cannot smuggle a containment edge past the gate.
//
// 🔴 ClaimDevice IS THE THIRD WRITER OF RELATIONSHIP EDGES and it takes a
// CALLER-CHOSEN relationship type, so before the gate was added there
// claimDevice(relationshipType:"contains") wrote a device→customer containment edge
// that had met none of the structural checks. It was harmless to the tree only because
// the both-ends-assets predicate hides it on read — which is a second mechanism
// covering for a missing first one, exactly the arrangement that stops being true
// quietly.
func TestClaimDeviceCannotWriteAContainmentEdge(t *testing.T) {
	api, ctx := hierarchyTestApi(t)
	seedDeviceForHierarchy(t, api, ctx, "dozer-01")

	customerType := &CustomerType{}
	customerType.Token = "operator"
	require.NoError(t, api.RDB.DB(ctx).Create(customerType).Error, "seed customer type")
	customer, err := api.CreateCustomer(ctx, &CustomerCreateRequest{
		Token:             "acme-mining",
		CustomerTypeToken: customerType.Token,
	})
	require.NoError(t, err, "seed customer")
	require.NotNil(t, customer, "no customer created")

	_, err = api.EnsureContainmentType(ctx)
	require.NoError(t, err, "ensure containment type")

	secret := "s3cret"
	_, err = api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{
		DeviceToken: "dozer-01",
		ClaimSecret: secret,
	})
	require.NoError(t, err, "open claim")

	_, err = api.ClaimDevice(ctx, &DeviceClaimRequest{
		DeviceToken:      "dozer-01",
		ClaimSecret:      secret,
		CustomerToken:    "acme-mining",
		RelationshipType: ContainmentRelationshipType,
	}, time.Now())
	require.ErrorIs(t, err, ErrContainmentEndsMustBeAssets,
		"a claim redeemed as \"contains\" wrote a device-to-customer containment edge")

	// The counterweight: the SAME claim redeemed with the assignment type succeeds, so
	// the gate refuses one type rather than breaking claims.
	assignment, err := api.EnsureAssignmentType(ctx)
	require.NoError(t, err, "ensure assignment type")
	edge, err := api.ClaimDevice(ctx, &DeviceClaimRequest{
		DeviceToken:      "dozer-01",
		ClaimSecret:      secret,
		CustomerToken:    "acme-mining",
		RelationshipType: assignment.Token,
	}, time.Now())
	require.NoError(t, err, "a legitimate claim was refused")
	require.NotNil(t, edge, "no edge returned")
}

// seedDeviceForHierarchy creates one device, for the tests that need a non-asset
// entity to point an edge at.
func seedDeviceForHierarchy(t *testing.T, api *Api, ctx context.Context, token string) {
	t.Helper()

	deviceType := &DeviceType{}
	deviceType.Token = token + "-type"
	require.NoError(t, api.RDB.DB(ctx).Create(deviceType).Error, "seed device type")
	_, err := api.CreateDevice(ctx, &DeviceCreateRequest{
		Token: token, DeviceTypeToken: deviceType.Token,
	})
	require.NoError(t, err, "seed device")
}

// containsRequest builds a generic-edge request for a containment edge, with a
// freshly minted token so successive calls never collide.
func containsRequest(parentToken, childToken string) *EntityRelationshipCreateRequest {
	return &EntityRelationshipCreateRequest{
		Token:            uuid.New().String(),
		SourceType:       string(entity.TypeAsset),
		Source:           parentToken,
		TargetType:       string(entity.TypeAsset),
		Target:           childToken,
		RelationshipType: ContainmentRelationshipType,
	}
}

func strPtr(value string) *string { return &value }

// assetPtrs adapts a page of values to the pointer slice tokensOf reads.
func assetPtrs(assets []Asset) []*Asset {
	out := make([]*Asset, 0, len(assets))
	for i := range assets {
		out = append(out, &assets[i])
	}
	return out
}

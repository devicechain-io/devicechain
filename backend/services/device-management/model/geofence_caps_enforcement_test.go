// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"gorm.io/datatypes"
)

// ── the seams ────────────────────────────────────────────────────────────────────────────────

// stubCapsResolver answers whatever a test sets, and RECORDS whether it was asked at all.
//
// 🔴 THE CALL COUNT IS NOT BOOKKEEPING. Half the contract these caps carry is about which
// operations DO NOT need a cap — a delete, a rename, a shrink — and "it succeeded" cannot
// distinguish "never asked" from "asked and got a usable answer". Only the count can.
type stubCapsResolver struct {
	mu      sync.Mutex
	caps    governance.GeoFenceCaps
	err     error
	calls   int
	tenants []string
}

func (s *stubCapsResolver) Resolve(_ context.Context, tenant string) (governance.GeoFenceCaps, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.tenants = append(s.tenants, tenant)
	return s.caps, s.err
}

func (s *stubCapsResolver) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// countingRefusals records refusals by cap, so the label can be asserted rather than assumed.
type countingRefusals struct {
	mu sync.Mutex
	by map[string]int
}

func newCountingRefusals() *countingRefusals { return &countingRefusals{by: map[string]int{}} }

func (c *countingRefusals) CountGeoFenceCapRefusal(cap string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.by[cap]++
}

func (c *countingRefusals) count(cap string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.by[cap]
}

func (c *countingRefusals) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.by {
		n += v
	}
	return n
}

// cappedApi builds a geofence Api metered at the given caps, with a refusal counter attached.
func cappedApi(t *testing.T, caps governance.GeoFenceCaps) (*Api, *stubCapsResolver, *countingRefusals) {
	t.Helper()
	api := newGeoFenceTestApi(t)
	resolver := &stubCapsResolver{caps: caps}
	refusals := newCountingRefusals()
	api.GeoFenceCapsResolver = resolver
	api.GeoFenceCapRefusals = refusals
	return api, resolver, refusals
}

// ringOf builds a valid closed ring of exactly n positions — a sampled circle, so it passes
// the self-intersection scan rather than a relaxed path. The radius varies the SHAPE, which is
// what makes two rings of the same length hash differently.
func ringOf(n int, radius float64) string {
	coords := make([]float64, 0, n*2)
	for i := 0; i < n-1; i++ {
		theta := 2 * math.Pi * float64(i) / float64(n-1)
		coords = append(coords, -84.0+radius*math.Cos(theta), 33.0+radius*math.Sin(theta))
	}
	coords = append(coords, -84.0+radius, 33.0)
	return polygonGeometry(coords...)
}

// positionsOf is the count the caps are actually spent in, read back through the same
// function the enforcement uses — so a fixture cannot disagree with the thing it is testing.
func positionsOf(t *testing.T, geometry string) int {
	t.Helper()
	_, positions, err := validateGeoFenceGeometry(geometry)
	if err != nil {
		t.Fatalf("fixture geometry is invalid: %v", err)
	}
	return positions
}

// generousCaps is every cap at the platform maximum — a tenant nothing refuses, so a test
// that lowers ONE cap is measuring that cap and not an accident of the others.
func generousCaps() governance.GeoFenceCaps {
	return governance.GeoFenceCaps{
		PositionCeiling: governance.MaxGeoFencePositionCeiling,
		FenceCeiling:    governance.MaxGeoFenceCeiling,
		PositionBudget:  governance.MaxTenantGeometryPositions,
	}
}

// ── the per-fence position ceiling ───────────────────────────────────────────────────────────

// The ceiling refuses a create above it, and the refusal names the tenant's own number rather
// than the platform maximum — an operator reading it has to know which knob to turn.
func TestTheTenantPositionCeilingRefusesAnOversizedCreate(t *testing.T) {
	caps := generousCaps()
	caps.PositionCeiling = 64
	api, resolver, refusals := cappedApi(t, caps)
	ctx := core.WithTenant(context.Background(), "acme")

	// The counterweight FIRST: at the ceiling is accepted, so what follows is not a validator
	// that refuses everything.
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "at-ceiling", Geometry: ringOf(64, 0.01),
	}); err != nil {
		t.Fatalf("a fence at exactly the tenant's 64-position ceiling was refused: %v", err)
	}

	_, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "over", Geometry: ringOf(65, 0.01),
	})
	if err == nil {
		t.Fatal("a 65-position fence was accepted against a 64-position tenant ceiling")
	}
	if !strings.Contains(err.Error(), "64") {
		t.Errorf("the refusal does not name the tenant's ceiling: %v", err)
	}
	if !strings.Contains(err.Error(), governance.GeoFencePositionCeilingField) {
		t.Errorf("the refusal does not name the setting an operator would raise: %v", err)
	}
	if got := refusals.count(governance.GeoFencePositionCeilingField); got != 1 {
		t.Errorf("the position-ceiling refusal counter is %d, want 1", got)
	}

	// 🔴 THE CAPS WERE RESOLVED FOR THE RIGHT TENANT, WHICH NOTHING ELSE HERE CHECKS. Every
	// other assertion in this file would pass just as well if the resolver were asked about
	// some other tenant and happened to answer the same numbers — and on a multi-tenant
	// instance metering one tenant against another's plan is the failure that would matter
	// most and look least like a bug.
	resolver.mu.Lock()
	asked := append([]string(nil), resolver.tenants...)
	resolver.mu.Unlock()
	for _, tenant := range asked {
		if tenant != "acme" {
			t.Errorf("the caps were resolved for tenant %q; every resolve on this path must name "+
				"the tenant in context (%q)", tenant, "acme")
		}
	}
	if len(asked) == 0 {
		t.Error("no resolve happened at all, so the tenant assertion above proves nothing")
	}
}

// 🔴 A TENANT ALREADY OVER ITS CEILING KEEPS AUTHORING, AND THIS IS THE WHOLE GRANDFATHERING
// RULE. The ceiling is a bound on GROWTH, not on size: lowering a tier (or shipping a default
// below what a tenant already stored) must not strand that tenant's fences in a state where
// they cannot be renamed, described, shrunk or replaced.
//
// Without this, the caps would have been a trap for exactly the population they were written
// to meter — the tenants who used the feature most.
func TestATenantOverItsPositionCeilingCanStillEditDownwards(t *testing.T) {
	api, resolver, refusals := cappedApi(t, generousCaps())
	ctx := core.WithTenant(context.Background(), "acme")

	// Authored while the ceiling was generous.
	big := ringOf(300, 0.01)
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "big", Geometry: big}); err != nil {
		t.Fatalf("create at the generous ceiling: %v", err)
	}

	// The operator lowers the tier far below what is stored.
	resolver.caps.PositionCeiling = 64

	// A metadata-only edit: same geometry, so no growth, so no refusal.
	name := "renamed in place"
	if _, err := api.UpdateGeoFence(ctx, "big", &GeoFenceUpdateRequest{Name: dcgraphql.OptionalStringOf(name), Geometry: dcgraphql.OptionalStringOf(big)}); err != nil {
		t.Errorf("a metadata edit resubmitting identical geometry was refused after the ceiling "+
			"dropped below the stored fence: %v", err)
	}

	// A SHRINK that is still far over the new ceiling: allowed, because it is an improvement.
	if _, err := api.UpdateGeoFence(ctx, "big", geoFenceEdit(ringOf(200, 0.01))); err != nil {
		t.Errorf("shrinking 300 positions to 200 was refused because 200 is over the new "+
			"64-position ceiling; the ceiling bounds growth, not size: %v", err)
	}

	// A delete: allowed, and it must not even ask for a cap.
	before := resolver.callCount()
	if ok, err := api.DeleteGeoFence(ctx, "big"); err != nil || !ok {
		t.Errorf("deleting an over-ceiling fence failed: ok=%v err=%v", ok, err)
	}
	if after := resolver.callCount(); after != before {
		t.Errorf("DeleteGeoFence resolved the caps %d time(s); a delete cannot increase any "+
			"footprint, so it must not depend on user-management being reachable", after-before)
	}
	if refusals.total() != 0 {
		t.Errorf("%d refusals were counted for a sequence that must not refuse anything", refusals.total())
	}
}

// Growth past the ceiling is refused on UPDATE too, so the bound cannot be walked around by
// creating a legal fence and editing it into an illegal one — and the refusal says what the
// fence was, not only what it would become.
func TestGrowthPastThePositionCeilingIsRefusedOnUpdate(t *testing.T) {
	caps := generousCaps()
	caps.PositionCeiling = 64
	api, _, refusals := cappedApi(t, caps)
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "small", Geometry: ringOf(32, 0.01),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Growth that stays UNDER the ceiling is fine — the counterweight.
	if _, err := api.UpdateGeoFence(ctx, "small", geoFenceEdit(ringOf(48, 0.01))); err != nil {
		t.Fatalf("growing from 32 to 48 positions under a 64 ceiling was refused: %v", err)
	}

	_, err := api.UpdateGeoFence(ctx, "small", geoFenceEdit(ringOf(96, 0.01)))
	if err == nil {
		t.Fatal("an update from 48 to 96 positions was accepted against a 64-position ceiling")
	}
	if !strings.Contains(err.Error(), "up from 48") {
		t.Errorf("the refusal does not say what the fence was, so an author cannot tell a growth "+
			"refusal from a flat size refusal: %v", err)
	}
	if got := refusals.count(governance.GeoFencePositionCeilingField); got != 1 {
		t.Errorf("position-ceiling refusals = %d, want 1", got)
	}

	// The refused update must not have landed.
	after, err := api.GeoFencesByToken(ctx, []string{"small"})
	if err != nil || len(after) != 1 {
		t.Fatalf("re-read: %v (%d rows)", err, len(after))
	}
	if n, err := geoFencePositionsIn([]byte(after[0].Geometry)); err != nil || n != 48 {
		t.Errorf("the stored fence has %d positions (err %v); the rejected update changed it", n, err)
	}
}

// ── the fence count ──────────────────────────────────────────────────────────────────────────

// The fence ceiling refuses the create that would cross it, and only a create: a tenant at or
// over the ceiling can still edit and delete what it has.
func TestTheTenantFenceCeilingRefusesOnlyCreates(t *testing.T) {
	caps := generousCaps()
	caps.FenceCeiling = 3
	api, resolver, refusals := cappedApi(t, caps)
	ctx := core.WithTenant(context.Background(), "acme")

	for i := 0; i < 3; i++ {
		if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
			Token: fmt.Sprintf("f%d", i), Geometry: ringOf(8, 0.01+0.001*float64(i)),
		}); err != nil {
			t.Fatalf("fence %d of 3 was refused below the ceiling: %v", i+1, err)
		}
	}
	_, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "f3", Geometry: ringOf(8, 0.02)})
	if err == nil {
		t.Fatal("a fourth fence was accepted against a ceiling of 3")
	}
	if !strings.Contains(err.Error(), governance.GeoFenceCeilingField) {
		t.Errorf("the refusal does not name the setting to raise: %v", err)
	}
	if got := refusals.count(governance.GeoFenceCeilingField); got != 1 {
		t.Errorf("fence-ceiling refusals = %d, want 1", got)
	}

	// The operator lowers it further, below what the tenant holds. Editing and deleting still work.
	resolver.caps.FenceCeiling = 1
	if _, err := api.UpdateGeoFence(ctx, "f0", geoFenceEdit(ringOf(8, 0.05))); err != nil {
		t.Errorf("editing a fence while the tenant holds 3 against a ceiling of 1 was refused: %v", err)
	}
	if ok, err := api.DeleteGeoFence(ctx, "f1"); err != nil || !ok {
		t.Errorf("deleting a fence while over the ceiling failed: ok=%v err=%v", ok, err)
	}
}

// ── the whole-set position budget ────────────────────────────────────────────────────────────

// The budget refuses the change that raises the tenant's distinct-geometry total past it, and
// the refusal names both totals so an author can see the direction.
func TestTheTenantPositionBudgetRefusesGrowthPastIt(t *testing.T) {
	caps := generousCaps()
	caps.PositionBudget = 200
	api, _, refusals := cappedApi(t, caps)
	ctx := core.WithTenant(context.Background(), "acme")

	first := ringOf(100, 0.01)
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "a", Geometry: first}); err != nil {
		t.Fatalf("first fence (%d positions of a 200 budget): %v", positionsOf(t, first), err)
	}
	// Exactly to the budget: accepted. The counterweight.
	second := ringOf(100, 0.02)
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "b", Geometry: second}); err != nil {
		t.Fatalf("a second fence bringing the total to exactly the 200 budget was refused: %v", err)
	}

	_, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "c", Geometry: ringOf(8, 0.03)})
	if err == nil {
		t.Fatal("a third fence taking the total over the budget was accepted")
	}
	for _, want := range []string{"208", "200", governance.GeoFencePositionBudgetField} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
	if got := refusals.count(governance.GeoFencePositionBudgetField); got != 1 {
		t.Errorf("budget refusals = %d, want 1", got)
	}

	// The refusal rolled the whole transaction back — no orphan row, no minted version.
	if fences, err := api.GeoFencesByToken(ctx, []string{"c"}); err != nil || len(fences) != 0 {
		t.Errorf("the refused fence survived the rollback: %d rows, err %v", len(fences), err)
	}
}

// 🔴 THE BUDGET IS TWO-SIDED, AND THIS IS THE TEST THAT WOULD HAVE CAUGHT THE ONE-SIDED FORM.
// A tenant over its budget — because the tier was lowered, or because the platform default
// moved under a set authored before enforcement — must still be able to shrink and to DELETE.
// A plain `total > budget` refuses both, and refusing a delete for being over budget is worse
// than having no budget at all: it removes the only route back under.
func TestATenantOverItsBudgetCanStillShrinkAndDelete(t *testing.T) {
	api, resolver, refusals := cappedApi(t, generousCaps())
	ctx := core.WithTenant(context.Background(), "acme")

	for i, r := range []float64{0.01, 0.02, 0.03} {
		if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
			Token: fmt.Sprintf("f%d", i), Geometry: ringOf(100, r),
		}); err != nil {
			t.Fatalf("create f%d: %v", i, err)
		}
	}
	// The operator lowers the budget far below the 300 positions already stored.
	resolver.caps.PositionBudget = 50

	// A shrink: allowed, even though the result (250) is still five times the budget.
	if _, err := api.UpdateGeoFence(ctx, "f0", geoFenceEdit(ringOf(50, 0.01))); err != nil {
		t.Errorf("shrinking a fence while over budget was refused; the budget bounds growth, not "+
			"size, and refusing this leaves the tenant no way down: %v", err)
	}
	// A delete: allowed, and without asking for a cap at all.
	before := resolver.callCount()
	if ok, err := api.DeleteGeoFence(ctx, "f1"); err != nil || !ok {
		t.Errorf("deleting a fence while over budget failed: ok=%v err=%v", ok, err)
	}
	if after := resolver.callCount(); after != before {
		t.Errorf("DeleteGeoFence resolved the caps %d time(s) while over budget", after-before)
	}
	// And growth is still refused, so the two-sidedness has not simply disabled the budget.
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "new", Geometry: ringOf(8, 0.09),
	}); err == nil {
		t.Error("a create was accepted while the tenant is over budget; the two-sided rule must " +
			"still refuse the direction that makes things worse")
	}
	if got := refusals.count(governance.GeoFencePositionBudgetField); got != 1 {
		t.Errorf("budget refusals = %d, want exactly the one create", got)
	}
}

// 🔴 SHRINKING A FENCE CAN RAISE THE CHARGE, AND THE REFUSAL SAYS SO RATHER THAN LOOKING LIKE
// A BUG. The budget sums DISTINCT geometry, because that is what the archive stores and what
// event-processing's cache holds. Two fences sharing a shape cost one entry; making one of
// them different un-dedupes them, so the tenant's footprint grows even though the edited fence
// got smaller.
//
// Three places in this arc asserted that a shrinking edit always passes. It does not, and this
// is the case that proves it — kept as a test rather than a comment because it is the single
// most surprising consequence of deduplicating the sum.
func TestShrinkingOneOfTwoIdenticalFencesCanRaiseTheCharge(t *testing.T) {
	caps := generousCaps()
	caps.PositionBudget = 150
	api, _, refusals := cappedApi(t, caps)
	ctx := core.WithTenant(context.Background(), "acme")

	// Two fences, IDENTICAL geometry: one hash, counted once.
	shared := ringOf(100, 0.01)
	for _, token := range []string{"a", "b"} {
		if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: token, Geometry: shared}); err != nil {
			t.Fatalf("create %s: %v", token, err)
		}
	}
	// 100, not 200 — the dedup is the premise of the whole test.
	_, stored, err := api.fenceSetVersionRow(ctx, 0)
	if err != nil {
		t.Fatalf("read the current version: %v", err)
	}
	if stored.PositionSum == nil || *stored.PositionSum != 100 {
		t.Fatalf("two identical 100-position fences summed to %v; the budget is not deduplicating "+
			"by content address and this test proves nothing", stored.PositionSum)
	}

	// Now shrink ONE of them to 60. Distinct total goes 100 -> 160, over the 150 budget.
	_, err = api.UpdateGeoFence(ctx, "a", geoFenceEdit(ringOf(60, 0.02)))
	if err == nil {
		t.Fatal("shrinking one of two identical fences past the budget was accepted; the sum is " +
			"over DISTINCT geometry, so un-deduplicating must be charged")
	}
	if !strings.Contains(err.Error(), "DISTINCT") {
		t.Errorf("the refusal does not explain why a smaller fence cost more, which is the one "+
			"thing an author cannot work out from the numbers: %v", err)
	}
	if got := refusals.count(governance.GeoFencePositionBudgetField); got != 1 {
		t.Errorf("budget refusals = %d, want 1", got)
	}
}

// ── an unresolvable cap ──────────────────────────────────────────────────────────────────────

// 🔴 AN UNREACHABLE user-management REFUSES GROWTH AND BLOCKS NOTHING ELSE. This is the
// property the resolver's own contract states and that a naive "resolve up front, fail the
// mutation" would have broken: during an outage a tenant would lose renames, description
// edits, shrinks AND deletes — including the deletes an over-budget tenant needs.
func TestAnUnresolvableCapRefusesGrowthAndNothingElse(t *testing.T) {
	api, resolver, refusals := cappedApi(t, generousCaps())
	ctx := core.WithTenant(context.Background(), "acme")

	big := ringOf(200, 0.01)
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "f", Geometry: big}); err != nil {
		t.Fatalf("create while resolvable: %v", err)
	}

	// user-management goes away.
	resolver.err = fmt.Errorf("user-management unreachable")

	// A create is growth: refused, and the error says why rather than reading as a database fault.
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "another", Geometry: ringOf(8, 0.05),
	}); err == nil {
		t.Error("a create was accepted while the tenant's caps could not be resolved; a tier may " +
			"sit below the platform default, so there is no safe number to assume")
	} else {
		// 🔴 IT MUST NOT READ AS A CAP REFUSAL. An operator seeing this during an outage has
		// to be able to tell "we could not check your limit" from "you hit your limit" —
		// the second sends them to raise a tier that was never the problem.
		if !strings.Contains(err.Error(), "cannot check") {
			t.Errorf("the refusal does not say a CHECK could not be made: %v", err)
		}
		if !strings.Contains(err.Error(), "could not be resolved") {
			t.Errorf("the refusal does not name the cause an operator can act on: %v", err)
		}
		if strings.Contains(err.Error(), governance.GeoFencePositionCeilingBecause) {
			t.Errorf("the refusal quotes a cap's rationale, so it reads as a cap refusal "+
				"rather than an outage: %v", err)
		}
	}

	// A metadata-only edit, resubmitting identical geometry: allowed.
	name := "still editable"
	if _, err := api.UpdateGeoFence(ctx, "f", &GeoFenceUpdateRequest{Name: dcgraphql.OptionalStringOf(name), Geometry: dcgraphql.OptionalStringOf(big)}); err != nil {
		t.Errorf("a metadata edit was refused during a user-management outage: %v", err)
	}
	// A shrink: allowed.
	if _, err := api.UpdateGeoFence(ctx, "f", geoFenceEdit(ringOf(100, 0.01))); err != nil {
		t.Errorf("a shrink was refused during a user-management outage: %v", err)
	}
	// A delete: allowed.
	if ok, err := api.DeleteGeoFence(ctx, "f"); err != nil || !ok {
		t.Errorf("a delete was refused during a user-management outage: ok=%v err=%v", ok, err)
	}

	// 🔴 An unresolvable cap is NOT a refusal by a cap. Counting it would make the metric read
	// as a tenant hitting its packaging limit when what happened was an outage.
	if refusals.total() != 0 {
		t.Errorf("%d cap refusals were counted for an outage; the counter reports tenants at their "+
			"caps, and an unreadable cap is a different event", refusals.total())
	}
}

// ── the nil resolver ─────────────────────────────────────────────────────────────────────────

// 🔴 NO RESOLVER MEANS THE PLATFORM DEFAULTS, NEVER "UNBOUNDED". An instance with no
// user-management coordinate — and every unit test in this package — still meters every
// tenant, at exactly the numbers this service enforced as hard constants before the caps
// became tier-driven. An absent authority that reads as "no bound" is how a governance ceiling
// silently stops governing, and it stops precisely when the authority is unreachable.
func TestNoResolverMetersAtThePlatformDefaults(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	if api.GeoFenceCapsResolver != nil {
		t.Fatal("fixture: this test is about the nil-resolver path")
	}
	// At the default ceiling: accepted.
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "at-default", Geometry: ringOf(governance.DefaultGeoFencePositionCeiling, 0.01),
	}); err != nil {
		t.Fatalf("a fence at the default position ceiling was refused with no resolver: %v", err)
	}
	// One over: refused, by the DEFAULT and not by the platform maximum.
	_, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "over-default", Geometry: ringOf(governance.DefaultGeoFencePositionCeiling+1, 0.01),
	})
	if err == nil {
		t.Fatal("a fence over the DEFAULT position ceiling was accepted with no resolver wired; " +
			"an unconfigured instance is metered, not ungoverned")
	}
	if !strings.Contains(err.Error(), fmt.Sprint(governance.DefaultGeoFencePositionCeiling)) {
		t.Errorf("with no resolver the refusal should name the platform default; it says: %v", err)
	}
}

// A nil refusal counter changes no verdict — the metric is reporting, never enforcement.
func TestANilRefusalCounterDoesNotChangeAVerdict(t *testing.T) {
	caps := generousCaps()
	caps.PositionCeiling = 16
	api, _, _ := cappedApi(t, caps)
	api.GeoFenceCapRefusals = nil
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "over", Geometry: ringOf(64, 0.01),
	}); err == nil {
		t.Error("the refusal disappeared when the counter was nil; counting must not gate anything")
	}
}

// ── the stored sum ───────────────────────────────────────────────────────────────────────────

// 🔴 A SNAPSHOT WITH NO PositionSum FAILS OPEN FOR EXACTLY ONE MINT, AND THEN HEALS. Versions
// written before the field existed — or by a pod mid-rolling-update — carry no sum, and there
// is nothing to compare against. Refusing would strand every such tenant on their next edit;
// charging them as if the whole set were new would refuse anyone already over. So the budget
// stands aside once, and the mint it stands aside for writes the field.
func TestAnAbsentStoredSumFailsOpenOnceAndThenHeals(t *testing.T) {
	caps := generousCaps()
	caps.PositionBudget = 150
	api, _, _ := cappedApi(t, caps)
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "a", Geometry: ringOf(100, 0.01),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Rewrite the current snapshot as a pre-field version would have been written: same
	// fences, no positionSum. Through the same struct, with the field cleared — so this
	// fixture cannot drift away from the shape it is imitating.
	row, stored, err := api.fenceSetVersionRow(ctx, 0)
	if err != nil {
		t.Fatalf("read version: %v", err)
	}
	if stored.PositionSum == nil {
		t.Fatal("fixture: the mint did not write a position sum, so there is nothing to clear")
	}
	stored.PositionSum = nil
	legacy, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal legacy snapshot: %v", err)
	}
	if err := api.RDB.DB(ctx).Model(&GeoFenceSetVersion{}).
		Where("version = ?", row.Version).Update("snapshot", datatypes.JSON(legacy)).Error; err != nil {
		t.Fatalf("write legacy snapshot: %v", err)
	}

	// The next edit is growth well past the budget, and it is ALLOWED — there was no sum to
	// compare against, and inventing one would refuse a tenant for a field they never had.
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "b", Geometry: ringOf(300, 0.02),
	}); err != nil {
		t.Fatalf("the mint after a sum-less snapshot was refused; an absent sum has to fail open "+
			"or every pre-enforcement tenant is stranded: %v", err)
	}

	// And it HEALED: the version just minted carries a sum, so the next edit is metered.
	_, after, err := api.fenceSetVersionRow(ctx, 0)
	if err != nil {
		t.Fatalf("read healed version: %v", err)
	}
	if after.PositionSum == nil {
		t.Fatal("the fail-open mint did not write a position sum, so the tenant fails open forever")
	}
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "c", Geometry: ringOf(8, 0.03),
	}); err == nil {
		t.Error("the tenant is still unmetered after a mint that wrote a sum; the fail-open is " +
			"permanent rather than one-shot")
	}
}

// A mint that changes nothing about the fence SET does not reach the budget at all — so a
// rename or a metadata edit costs no resolve even when the tenant is far over.
func TestAnUnchangedFenceSetNeverReachesTheBudget(t *testing.T) {
	api, resolver, _ := cappedApi(t, generousCaps())
	ctx := core.WithTenant(context.Background(), "acme")

	geometry := ringOf(100, 0.01)
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "f", Geometry: geometry}); err != nil {
		t.Fatalf("create: %v", err)
	}
	resolver.caps.PositionBudget = 1
	resolver.err = fmt.Errorf("user-management unreachable")

	description := "edited"
	if _, err := api.UpdateGeoFence(ctx, "f", &GeoFenceUpdateRequest{Description: dcgraphql.OptionalStringOf(description), Geometry: dcgraphql.OptionalStringOf(geometry)}); err != nil {
		t.Errorf("a description edit resubmitting identical geometry was refused by a budget of 1 "+
			"during an outage; it changes no shape and mints no version: %v", err)
	}

	// 🔴 IT DID RESOLVE, THOUGH, AND THAT IS NOT A DEFECT TO FIX HERE. An update cannot know
	// in advance that it needs no cap — a shrink can still raise the whole-set total by
	// un-deduplicating shared geometry — so it resolves and carries the error, where a
	// delete passes an attempt that cannot answer at all. Asserted rather than left implicit
	// because the two paths look interchangeable and are not; see the comment in
	// UpdateGeoFence, which is what this pins.
	if resolver.callCount() == 0 {
		t.Error("UpdateGeoFence resolved nothing. If that is now deliberate, the asymmetry with " +
			"DeleteGeoFence documented in UpdateGeoFence is stale and must be rewritten — an " +
			"update that skips the resolve has to prove it cannot raise the distinct-geometry sum")
	}
}

// 🔴 A STORED DOCUMENT THAT CANNOT BE COUNTED STANDS THE BUDGET ASIDE — IT DOES NOT FAIL THE
// MUTATION. `documents` is the whole POST-mutation set, so a corrupt row belongs to a fence the
// caller may not be touching at all: returning the count error would let one bad row block every
// later mutation the tenant makes, including the delete of that very fence. That is the same
// trap the undecodable-snapshot path refuses, arriving through a different door.
//
// And the version it mints records NO sum rather than the partial one. A partial sum is a
// number that looks authoritative and is too small, which would meter the next edit against a
// total below what the tenant holds and refuse a change that made nothing worse.
func TestAnUncountableStoredDocumentStandsTheBudgetAsideRatherThanFailing(t *testing.T) {
	caps := generousCaps()
	caps.PositionBudget = 150
	api, _, refusals := cappedApi(t, caps)
	ctx := core.WithTenant(context.Background(), "acme")

	for _, f := range []struct{ token, geometry string }{
		{"keep", ringOf(100, 0.01)},
		{"corrupt", ringOf(20, 0.02)},
	} {
		if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: f.token, Geometry: f.geometry}); err != nil {
			t.Fatalf("create %s: %v", f.token, err)
		}
	}

	// Corrupt the fence's LIVE geometry column — the bytes the mint reads back, hashes, and
	// tries to count when it rebuilds the document set. Written straight to the column, since
	// nothing in the API can produce this state (every write is canonicalized first).
	//
	// Not the archive row: the mint builds `documents` from the geo_fences rows it reads, so
	// corrupting the archive would leave the count intact and test nothing. An earlier version
	// of this comment claimed the archive, which would have sent a reader looking at the wrong
	// table for the mechanism under test.
	var blobs []GeoFenceGeometryBlob
	if err := api.RDB.DB(ctx).Find(&blobs).Error; err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if len(blobs) != 2 {
		t.Fatalf("fixture: expected 2 archived geometries, found %d", len(blobs))
	}
	if err := api.RDB.DB(ctx).Model(&GeoFence{}).Where("token = ?", "corrupt").
		Update("geometry", `{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":"not a ring"}}`).
		Error; err != nil {
		t.Fatalf("corrupt the stored geometry: %v", err)
	}

	// A DELETE of the OTHER fence still works: the corrupt document is in the post-mutation
	// set, gets counted, fails, and stands the budget aside instead of failing the delete.
	if ok, err := api.DeleteGeoFence(ctx, "keep"); err != nil || !ok {
		t.Fatalf("a delete failed because an UNRELATED fence's stored geometry could not be "+
			"counted; one corrupt row must not block the tenant's every mutation: ok=%v err=%v", ok, err)
	}
	if refusals.total() != 0 {
		t.Errorf("%d cap refusals were counted; an uncountable document is not a tenant at its cap",
			refusals.total())
	}

	// And the minted version carries NO sum, so the next mint compares nothing rather than
	// comparing against an under-count.
	_, stored, err := api.fenceSetVersionRow(ctx, 0)
	if err != nil {
		t.Fatalf("read the minted version: %v", err)
	}
	if stored.PositionSum != nil {
		t.Errorf("the mint recorded a position sum of %d from a count that did not finish; a "+
			"partial sum reads as authoritative and would refuse a later change that made "+
			"nothing worse", *stored.PositionSum)
	}
}

// 🔴 THE DELETE PATH'S "CAPS NOT NEEDED" ATTEMPT IS A GUARD, NOT A CONVENIENCE, SO IT HAS TO
// REFUSE IF ANYTHING EVER ASKS IT. The property it buys — a delete never depends on
// user-management being reachable — holds only while no check on that path demands a number.
// If a future edit made one demand it, the right outcome is a loud failure in a test rather
// than a delete that quietly acquired a network dependency.
//
// Tested directly because the whole point is that it is UNREACHABLE through DeleteGeoFence:
// there is no input that drives a real delete into it, which is exactly why the guard cannot
// be observed any other way.
func TestTheDeletePathsCapAttemptRefusesIfAnythingAsksIt(t *testing.T) {
	_, err := geoFenceCapsNotNeeded().require("something")
	if err == nil {
		t.Fatal("the delete path's caps attempt answered a require(). It must not: a check that " +
			"can demand a number on the delete path is a delete that fails when user-management " +
			"is down, and the attempt is what makes that impossible rather than merely unlikely")
	}
	if !errors.Is(err, errGeoFenceCapsNotNeeded) {
		t.Errorf("the refusal does not carry errGeoFenceCapsNotNeeded, so a caller cannot tell "+
			"this internal bug from a real user-management outage: %v", err)
	}
}

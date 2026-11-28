// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestGate(cap int) *previewGate { return &previewGate{inUse: map[string]int{}, cap: cap} }

// Distinct tenants (one slot each, so the per-tenant limit never binds) fill the cap; the next
// is refused as an INSTANCE refusal, and a freed slot lets the next one in.
func TestPreviewGateInstanceCapRefusesThenAdmitsAfterRelease(t *testing.T) {
	g := newTestGate(4)
	for i := 0; i < 4; i++ {
		require.Equal(t, previewAdmitted, g.tryAcquire(fmt.Sprintf("t%d", i)))
	}
	require.Equal(t, previewRefusedInstance, g.tryAcquire("t4"))
	require.False(t, g.acquire("t4"))

	g.release("t0")
	require.Equal(t, previewAdmitted, g.tryAcquire("t4"))
	require.Equal(t, previewRefusedInstance, g.tryAcquire("t5"))
	assert.Equal(t, 4, g.total)
}

// The per-tenant allowance still binds first and is reported as the tenant's own.
func TestPreviewGateTenantLimitIsReportedAsTheTenants(t *testing.T) {
	g := newTestGate(8)
	require.Equal(t, previewAdmitted, g.tryAcquire("a"))
	require.Equal(t, previewAdmitted, g.tryAcquire("a"))
	require.Equal(t, previewRefusedTenant, g.tryAcquire("a"))
}

// Fairness: tenants that double up cannot lock out a tenant holding nothing. With a cap of 8,
// second (burst) slots stop at half the cap, so the other half stays reachable to new tenants.
func TestPreviewGateDoublingTenantsCannotStarveANewTenant(t *testing.T) {
	g := newTestGate(8)
	// Four tenants each try for two slots; their burst slots stop once half the cap is in use.
	for i := 0; i < 4; i++ {
		g.tryAcquire(fmt.Sprintf("hog%d", i))
	}
	for i := 0; i < 4; i++ {
		g.tryAcquire(fmt.Sprintf("hog%d", i))
	}
	require.LessOrEqual(t, g.total, 8)
	// Every tenant that holds nothing is still admitted until the instance is truly full.
	admitted := 0
	for i := 0; i < 8; i++ {
		if g.tryAcquire(fmt.Sprintf("new%d", i)) == previewAdmitted {
			admitted++
		}
	}
	require.GreaterOrEqual(t, admitted, 4, "at least half the cap must stay reachable by tenants holding none")
	require.Equal(t, 8, g.total)
}

// A tenant holding a slot is refused its burst slot above half the cap even though slots are free,
// while a tenant holding none is admitted into the same free slot.
func TestPreviewGateBurstSlotIsRefusedAboveHalfButFirstSlotIsNot(t *testing.T) {
	g := newTestGate(8)
	for i := 0; i < 4; i++ {
		require.Equal(t, previewAdmitted, g.tryAcquire(fmt.Sprintf("t%d", i)))
	}
	require.Equal(t, previewRefusedBurst, g.tryAcquire("t0"), "a second slot above half the cap")
	require.Equal(t, previewAdmitted, g.tryAcquire("fresh"), "a first slot while the instance has room")
}

// The gate never over-admits under contention and ends empty.
func TestPreviewGateConcurrentNeverExceedsCap(t *testing.T) {
	g := newTestGate(5)
	var wg sync.WaitGroup
	var mu sync.Mutex
	cur, peak := 0, 0
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tenant := fmt.Sprintf("t%d", i%9)
			if !g.acquire(tenant) {
				return
			}
			mu.Lock()
			cur++
			peak = max(peak, cur)
			mu.Unlock()
			time.Sleep(2 * time.Millisecond)
			mu.Lock()
			cur--
			mu.Unlock()
			g.release(tenant)
		}(i)
	}
	wg.Wait()
	require.LessOrEqual(t, peak, 5)
	require.Equal(t, 0, g.total)
	require.Empty(t, g.inUse)
}

func TestPreviewGateSetCapIgnoresNonPositive(t *testing.T) {
	g := newTestGate(0)
	require.Equal(t, 8, g.limit())
	g.setCap(0)
	g.setCap(-3)
	require.Equal(t, 8, g.limit(), "a cap can never be switched off")
	g.setCap(2)
	require.Equal(t, 2, g.limit())
	g.setCap(0)
	require.Equal(t, 2, g.limit(), "a later non-positive cap leaves the installed one alone")
}

// previewRule at the instance cap is a typed degraded answer (never an empty success), and the
// same request runs once a slot is free.
func TestPreviewRuleRefusedAtInstanceCapThenRunsAfterRelease(t *testing.T) {
	old := globalPreviewGate.cap
	globalPreviewGate.setCap(2)
	t.Cleanup(func() { globalPreviewGate.cap = old })
	require.Equal(t, previewAdmitted, globalPreviewGate.tryAcquire("other1"))
	require.Equal(t, previewAdmitted, globalPreviewGate.tryAcquire("other2"))
	released := false
	release := func() {
		if !released {
			released = true
			globalPreviewGate.release("other1")
			globalPreviewGate.release("other2")
		}
	}
	t.Cleanup(release)

	r := &SchemaResolver{Profiles: publishedProfiles(t, true), Replay: emptyReplay{first: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)}}
	res := previewWith(t, r, previewFallbackRule)
	require.NotNil(t, res.Degraded(), "a refused preview must say so, not read as an empty success")
	assert.Contains(t, *res.Degraded(), "on this replica")

	release()
	res = previewWith(t, r, previewFallbackRule)
	require.True(t, res.Ok())
	assert.Nil(t, res.Degraded())
	globalPreviewGate.mu.Lock()
	total := globalPreviewGate.total
	globalPreviewGate.mu.Unlock()
	assert.Equal(t, 0, total, "the slot is given back")
}

// An evaluation at the instance cap is a BUSY refusal (ok false), never an empty success, and the
// per-tenant slot it briefly held is given back.
func TestEvaluateRefusedAtInstanceCap(t *testing.T) {
	old := globalPreviewGate.cap
	globalPreviewGate.setCap(2)
	t.Cleanup(func() { globalPreviewGate.cap = old })
	require.Equal(t, previewAdmitted, globalPreviewGate.tryAcquire("other1"))
	require.Equal(t, previewAdmitted, globalPreviewGate.tryAcquire("other2"))
	t.Cleanup(func() { globalPreviewGate.release("other1"); globalPreviewGate.release("other2") })

	s := evalSchema(t, &SchemaResolver{})
	got, errs := evExec(t, s, previewCtx(auth.DeviceRead), evDraft(hotDefinition, evEvent("d1", evT0, "temperature", 90)))
	require.Nil(t, errs)
	require.False(t, got.Ok)
	require.NotNil(t, got.Refusal)
	assert.Equal(t, "BUSY", *got.Refusal)
	assert.Contains(t, got.Diagnostics[0].Message, "on this replica")
	assert.Empty(t, got.Edges)
	globalPreviewGate.mu.Lock()
	defer globalPreviewGate.mu.Unlock()
	assert.Equal(t, 2, globalPreviewGate.total)
}

// A comparison at the instance cap is a BUSY refusal naming the instance.
func TestCompareRefusedAtInstanceCap(t *testing.T) {
	old := globalPreviewGate.cap
	globalPreviewGate.setCap(2)
	t.Cleanup(func() { globalPreviewGate.cap = old })
	require.Equal(t, previewAdmitted, globalPreviewGate.tryAcquire("other1"))
	require.Equal(t, previewAdmitted, globalPreviewGate.tryAcquire("other2"))
	t.Cleanup(func() { globalPreviewGate.release("other1"); globalPreviewGate.release("other2") })
	r := &SchemaResolver{}
	out, err := r.CompareRuleVersions(compareContext(compareServiceClaims()), struct{ Input compareRuleVersionsInput }{compareRuleVersionsInput{}})
	require.NoError(t, err)
	require.False(t, out.Ok())
	require.Equal(t, refusalBusy, out.Refusal().Reason())
	assert.Contains(t, out.Refusal().Message(), "on this replica")
}

// A burst refusal must neither run a replay nor release the tenant's existing slot.
func TestEvaluateBurstRefusedAndHoldsNoSlot(t *testing.T) {
	holdPreviewBurstSlots(t)
	s := evalSchema(t, &SchemaResolver{})
	got, errs := evExec(t, s, previewCtx(auth.DeviceRead), evDraft(hotDefinition, evEvent("d1", evT0, "temperature", 90)))
	require.Nil(t, errs)
	assert.False(t, got.Ok)
	require.NotNil(t, got.Refusal)
	assert.Equal(t, "BUSY", *got.Refusal)
	require.NotEmpty(t, got.Diagnostics)
	assert.Contains(t, got.Diagnostics[0].Message, "already has a preview running")
	assert.Empty(t, got.Edges)
	assertPreviewBurstSlotsHeld(t)
}

func TestCompareBurstRefusedAndHoldsNoSlot(t *testing.T) {
	holdPreviewBurstSlots(t)
	r := &SchemaResolver{}
	ctx := core.WithTenant(compareContext(compareServiceClaims()), "acme")
	out, err := r.CompareRuleVersions(ctx, struct{ Input compareRuleVersionsInput }{compareRuleVersionsInput{}})
	require.NoError(t, err)
	assert.False(t, out.Ok())
	require.NotNil(t, out.Refusal())
	assert.Equal(t, refusalBusy, out.Refusal().Reason())
	assert.Contains(t, out.Refusal().Message(), "already has a preview running")
	assertPreviewBurstSlotsHeld(t)
}

func holdPreviewBurstSlots(t *testing.T) {
	t.Helper()
	old := globalPreviewGate
	globalPreviewGate = newTestGate(4)
	t.Cleanup(func() { globalPreviewGate = old })
	require.Equal(t, previewAdmitted, globalPreviewGate.tryAcquire("acme"))
	require.Equal(t, previewAdmitted, globalPreviewGate.tryAcquire("other"))
}

func assertPreviewBurstSlotsHeld(t *testing.T) {
	t.Helper()
	globalPreviewGate.mu.Lock()
	defer globalPreviewGate.mu.Unlock()
	assert.Equal(t, 2, globalPreviewGate.total)
	assert.Equal(t, map[string]int{"acme": 1, "other": 1}, globalPreviewGate.inUse)
}

// A refused burst slot says the tenant already has one running, not that the replica is full; the
// two refusals carry different wording on every door.
func TestPreviewRuleBurstRefusalWordingDiffersFromInstanceFull(t *testing.T) {
	old := globalPreviewGate.cap
	globalPreviewGate.setCap(4)
	t.Cleanup(func() { globalPreviewGate.cap = old })
	// acme holds one, another tenant holds one: total 2 = half of 4, so acme's second is a burst refusal.
	require.Equal(t, previewAdmitted, globalPreviewGate.tryAcquire("acme"))
	require.Equal(t, previewAdmitted, globalPreviewGate.tryAcquire("other"))
	r := &SchemaResolver{Profiles: publishedProfiles(t, true), Replay: emptyReplay{first: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)}}
	res := previewWith(t, r, previewFallbackRule)
	require.NotNil(t, res.Degraded())
	assert.Contains(t, *res.Degraded(), "already has a preview running")
	assert.NotContains(t, *res.Degraded(), "already running on this replica")
	globalPreviewGate.release("other")
	globalPreviewGate.release("acme")

	// Full replica: the instance wording.
	globalPreviewGate.setCap(1)
	require.Equal(t, previewAdmitted, globalPreviewGate.tryAcquire("other"))
	t.Cleanup(func() { globalPreviewGate.release("other") })
	res = previewWith(t, r, previewFallbackRule)
	require.NotNil(t, res.Degraded())
	assert.Contains(t, *res.Degraded(), "already running on this replica")
}

// Half-full rounds the way the prose says: a second slot needs strictly fewer than half in use.
func TestPreviewGateBurstThresholdIsStrictlyHalf(t *testing.T) {
	g := newTestGate(3)
	require.Equal(t, previewAdmitted, g.tryAcquire("a"))
	// total 1 of 3: 1 < 1.5, so a's second slot is granted.
	require.Equal(t, previewAdmitted, g.tryAcquire("a"))
	// total 2 of 3: not fewer than half, so b's second is a burst refusal while its first is fine.
	require.Equal(t, previewAdmitted, g.tryAcquire("b"))
	require.Equal(t, previewRefusedTenant, g.tryAcquire("a"))
	require.Equal(t, previewRefusedInstance, g.tryAcquire("c"))
}

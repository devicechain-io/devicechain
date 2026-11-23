// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"sync"

	"github.com/devicechain-io/dc-event-processing/config"
)

// maxConcurrentPreviewsPerTenant bounds how many replay previews one tenant may run at once (ADR-023):
// a preview is a bounded but non-trivial replay job, so "replay 24h per keystroke" must not be a
// self-inflicted DoS. Excess previews are refused with a degraded result rather than queued.
const maxConcurrentPreviewsPerTenant = 2

// previewInstanceBusy is the refusal text when the instance-wide cap is reached. It is a typed
// busy/degraded answer, never an empty success: the caller must retry, not read it as "no firings".
const previewInstanceBusy = "the maximum number of previews are already running on this replica; try again in a moment"

// previewBurstBusy is the refusal text for a tenant's second concurrent slot when the replica is
// past half full: the replica still has room for tenants holding none, but not for a second.
const previewBurstBusy = "this tenant already has a preview running and the replica is busy; try again when it finishes"

// previewAdmission is the gate's answer to one request.
type previewAdmission int

const (
	previewAdmitted previewAdmission = iota
	// previewRefusedTenant: this tenant already holds its own allowance.
	previewRefusedTenant
	// previewRefusedInstance: the process-wide cap leaves no slot for this request.
	previewRefusedInstance
	// previewRefusedBurst: the tenant holds a slot and the process is at least half full, so its
	// second (burst) slot is withheld for tenants holding none.
	previewRefusedBurst
)

// previewGate limits the replays one process runs: per tenant, and across every tenant together.
// Previews, comparisons and evaluations share ONE allowance.
//
// The instance cap exists because a replay reads the resolved-events stream and, on an instance
// that is not sharded by tenant, that is every tenant's traffic: the per-tenant limit alone lets
// N tenants run 2N whole-stream replays at once.
//
// FAIRNESS. A tenant's FIRST concurrent slot may take any free slot, so a tenant with nothing
// running is only ever refused when the instance is truly full. Its SECOND slot is a burst slot,
// granted only while fewer than half the cap is in use, so tenants that double up can never hold
// more than half the instance between them and the other half stays reachable by tenants holding
// none. Requests are refused at once, never queued, so a burst cannot pile goroutines up.
type previewGate struct {
	mu    sync.Mutex
	inUse map[string]int
	total int
	// cap is the instance-wide limit; zero is config.DefaultPreviewMaxConcurrent.
	cap int
}

func (g *previewGate) limit() int {
	if g.cap <= 0 {
		return config.DefaultPreviewMaxConcurrent
	}
	return g.cap
}

// tryAcquire takes a slot for tenant, or says why not.
func (g *previewGate) tryAcquire(tenant string) previewAdmission {
	g.mu.Lock()
	defer g.mu.Unlock()
	held := g.inUse[tenant]
	if held >= maxConcurrentPreviewsPerTenant {
		return previewRefusedTenant
	}
	limit := g.limit()
	if g.total >= limit {
		return previewRefusedInstance
	}
	if held >= 1 && 2*g.total >= limit {
		return previewRefusedBurst
	}
	g.inUse[tenant]++
	g.total++
	return previewAdmitted
}

func (g *previewGate) acquire(tenant string) bool {
	return g.tryAcquire(tenant) == previewAdmitted
}

func (g *previewGate) release(tenant string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inUse[tenant] > 0 {
		g.inUse[tenant]--
		g.total--
		if g.inUse[tenant] == 0 {
			delete(g.inUse, tenant)
		}
	}
}

// setCap installs the instance-wide cap. A value below one is ignored (the default stays), so a
// caller can never switch the cap off.
func (g *previewGate) setCap(n int) {
	if n < 1 {
		return
	}
	g.mu.Lock()
	g.cap = n
	g.mu.Unlock()
}

var globalPreviewGate = &previewGate{inUse: map[string]int{}}

// SetPreviewInstanceCap installs event-processing's instance-wide limit on concurrent replays
// (preview.maxConcurrent). Called once at startup, before the GraphQL surface serves.
func SetPreviewInstanceCap(n int) { globalPreviewGate.setCap(n) }

// PreviewInstanceCap is the instance-wide cap in effect.
func PreviewInstanceCap() int {
	globalPreviewGate.mu.Lock()
	defer globalPreviewGate.mu.Unlock()
	return globalPreviewGate.limit()
}

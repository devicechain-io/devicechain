// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"sync"

	"github.com/devicechain-io/dc-microservice/core"
)

// maxConcurrentCacheReads bounds how many membership cache reads one event has in flight at
// once. A device's tracked set has no fixed size, and the goroutines one event starts must
// not grow with it.
const maxConcurrentCacheReads = 8

// MembershipTarget is one entity whose rule-scoped group memberships contribute to an
// event's scope stamp: the reporting device, or one of its tracked anchors.
type MembershipTarget struct {
	Type string
	Id   uint
}

// ReadAheadForEvent returns the api to resolve one event through. Given the CachedApi, it
// first reads the three caches the event will need after its device: the device type's
// published profile, the device's tracked relationships, and whether the tenant has any
// rule-scoped group. What process memory holds is read at once; the rest is asked of the
// key-value bucket at the same time, so an event that misses memory for all three waits
// for one round trip rather than three (see readAll). The api it returns answers each of those three lookups
// ONCE from what was read ahead, and everything else as the CachedApi does. Given anything
// else, or a context with no tenant, it returns api unchanged and reads nothing.
//
// 🔑 ONLY THE CACHES ARE READ AHEAD; THE DATABASE IS NOT. A lookup the caches could not
// answer (a miss, or any cache error, as before) still goes to the database when the
// resolver asks for it, at the point it always asked, one lookup after another. So:
//
//   - A resolver still holds at most one database connection at a time, which is what
//     resolution.workers is bounded by (config.ResolutionConfiguration).
//   - Every failure is reported where it was before. A measurement that fails validation
//     never reads relationships or scoped groups from the database, as before; it now costs
//     their cache reads, which were already under way.
//   - The pay-nothing rule holds: no membership is read, from the cache or the database,
//     until the scoped-groups answer says the tenant has a scoped group
//     (ReadMembershipsAhead is called after it).
//
// Each cache read counts in the cache's own metrics exactly as the same read made by the
// lookup itself would, so the hit and miss counts an operator reads do not move.
//
// The reads use the caller's context, so the tenant in every key is the caller's. Nothing is
// cancelled early: every read is joined before this returns.
//
// 🔴 IT DISPATCHES ON THE CONCRETE *CachedApi, not on an interface a wrapper or a test double
// could satisfy by embedding. A double that embeds DeviceManagementApi and overrides one
// method to add latency would otherwise be read through its embedded methods, and the
// latency it models would silently vanish. ReadsAheadConcurrently lets the service's wiring
// test check that production still hands the resolver the *CachedApi itself.
func ReadAheadForEvent(ctx context.Context, api DeviceManagementApi, device *Device) DeviceManagementApi {
	capi, ok := api.(*CachedApi)
	if !ok || capi == nil {
		return api
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return api
	}
	r := &eventReads{DeviceManagementApi: capi, capi: capi, held: map[heldKey]heldRead{}}
	r.readAll(ctx, []heldKey{
		{readProfile, profileResolutionByTypeKey(tenant, device.DeviceTypeId)},
		{readRelationships, relationshipsBySourceKey(tenant, device.ID)},
		{readScopedGroups, tenant},
	})
	return r
}

// ReadMembershipsAhead reads the membership cache for every target at the same time, at
// most maxConcurrentCacheReads at once, when api was returned by ReadAheadForEvent. The
// api then answers each target's MembershipsForEntity once from what was read, and the
// database is read, one target after another, only for the targets the cache could not
// answer, when they are asked for. It does nothing for any other api.
//
// Call it only once the tenant is known to have a rule-scoped group: it reads the cache
// for every target it is given.
func ReadMembershipsAhead(ctx context.Context, api DeviceManagementApi, targets []MembershipTarget) {
	r, ok := api.(*eventReads)
	if !ok {
		return
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return
	}
	keys := make([]heldKey, 0, len(targets))
	seen := map[string]bool{}
	for _, t := range targets {
		key := membershipsByEntityKey(tenant, t.Type, t.Id)
		if !seen[key] {
			seen[key] = true
			keys = append(keys, heldKey{readMemberships, key})
		}
	}
	r.readAll(ctx, keys)
}

// readAll reads each key's cache and holds what it got. It asks process memory for every
// key first, on this goroutine: a key held there costs no goroutine and no wait, and an
// event whose reads memory answers costs what it did when they were made one after another.
// Handing all three to goroutines regardless made such an event take about 100 µs instead
// of about 27 (BenchmarkWarmResolve, tier=local): waking them cost more than the event's own
// work. Only the keys memory could not answer go to the bucket, at the same time, at most
// maxConcurrentCacheReads at once, one of them on this goroutine; a single one is read on
// this goroutine alone.
func (r *eventReads) readAll(ctx context.Context, keys []heldKey) {
	var bucket []heldKey
	for _, k := range keys {
		if !r.read(ctx, k, true) {
			bucket = append(bucket, k)
		}
	}
	if len(bucket) == 0 {
		return
	}
	slots := make(chan struct{}, maxConcurrentCacheReads)
	var wg sync.WaitGroup
	for _, k := range bucket[1:] {
		wg.Add(1)
		slots <- struct{}{}
		go func(k heldKey) {
			defer wg.Done()
			defer func() { <-slots }()
			r.read(ctx, k, false)
		}(k)
	}
	slots <- struct{}{}
	r.read(ctx, bucket[0], false)
	<-slots
	wg.Wait()
}

// read makes one cache read through the CachedApi's own cache half for k's kind, and holds
// the result once it is settled. With memoryOnly it reports false, holding nothing, when
// process memory could not decide.
func (r *eventReads) read(ctx context.Context, k heldKey, memoryOnly bool) bool {
	var (
		value             any
		answered, settled bool
	)
	switch k.kind {
	case readProfile:
		value, answered, settled = r.capi.cachedProfileResolution(ctx, k.key, memoryOnly)
	case readRelationships:
		value, answered, settled = r.capi.cachedRelationships(ctx, k.key, memoryOnly)
	case readScopedGroups:
		value, answered, settled = r.capi.cachedAnyScopedGroups(ctx, k.key, memoryOnly)
	case readMemberships:
		value, answered, settled = r.capi.cachedMemberships(ctx, k.key, memoryOnly)
	default:
		panic(fmt.Sprintf("model: no cache read for read kind %d", k.kind))
	}
	if !settled {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.held[k] = heldRead{value: value, answered: answered}
	return true
}

// ReadsAheadConcurrently reports whether ReadAheadForEvent reads ahead for api: whether it
// is the *CachedApi itself.
func ReadsAheadConcurrently(api DeviceManagementApi) bool {
	capi, ok := api.(*CachedApi)
	return ok && capi != nil
}

type readKind int

const (
	readProfile readKind = iota
	readRelationships
	readScopedGroups
	readMemberships
)

type heldKey struct {
	kind readKind
	key  string // the cache key, which carries the tenant
}

// heldRead is one cache read made ahead. answered is false for a miss or a cache error:
// the lookup then goes straight to the database, without asking the cache a second time.
type heldRead struct {
	value    any
	answered bool
}

// eventReads is the api ReadAheadForEvent returns: the CachedApi, with four lookups
// answered from what was read ahead for one event.
type eventReads struct {
	DeviceManagementApi
	capi *CachedApi

	mu   sync.Mutex
	held map[heldKey]heldRead
}

// take removes and returns what was read ahead for k. Each is used once: a second lookup of
// the same key in the same event asks the CachedApi, exactly as it did before anything was
// read ahead, so a read made ahead answers no more lookups than the one it stands in for.
func (r *eventReads) take(k heldKey) (heldRead, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.held[k]
	if ok {
		delete(r.held, k)
	}
	return h, ok
}

func (r *eventReads) ProfileResolutionByDeviceType(ctx context.Context, deviceTypeId uint) (*ProfileResolution, error) {
	if tenant, ok := core.TenantFromContext(ctx); ok {
		key := profileResolutionByTypeKey(tenant, deviceTypeId)
		if h, ok := r.take(heldKey{readProfile, key}); ok {
			if h.answered {
				return h.value.(*ProfileResolution), nil
			}
			return r.capi.loadProfileResolution(ctx, key, deviceTypeId)
		}
	}
	return r.capi.ProfileResolutionByDeviceType(ctx, deviceTypeId)
}

func (r *eventReads) TrackedRelationshipsForDevice(ctx context.Context, deviceId uint) (*EntityRelationshipSearchResults, error) {
	if tenant, ok := core.TenantFromContext(ctx); ok {
		key := relationshipsBySourceKey(tenant, deviceId)
		if h, ok := r.take(heldKey{readRelationships, key}); ok {
			if h.answered {
				return h.value.(*EntityRelationshipSearchResults), nil
			}
			return r.capi.loadRelationships(ctx, key, deviceId)
		}
	}
	return r.capi.TrackedRelationshipsForDevice(ctx, deviceId)
}

func (r *eventReads) AnyScopedGroups(ctx context.Context) (bool, error) {
	if tenant, ok := core.TenantFromContext(ctx); ok {
		if h, ok := r.take(heldKey{readScopedGroups, tenant}); ok {
			if h.answered {
				return h.value.(bool), nil
			}
			return r.capi.loadAnyScopedGroups(ctx, tenant)
		}
	}
	return r.capi.AnyScopedGroups(ctx)
}

func (r *eventReads) MembershipsForEntity(ctx context.Context, entityType string, entityId uint) ([]GroupMembership, error) {
	if tenant, ok := core.TenantFromContext(ctx); ok {
		key := membershipsByEntityKey(tenant, entityType, entityId)
		if h, ok := r.take(heldKey{readMemberships, key}); ok {
			if h.answered {
				return h.value.([]GroupMembership), nil
			}
			return r.capi.loadMemberships(ctx, key, entityType, entityId)
		}
	}
	return r.capi.MembershipsForEntity(ctx, entityType, entityId)
}

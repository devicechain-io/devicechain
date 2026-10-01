// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// CachedApi is a caching decorator over *Api implementing the ADR-022 review B2
// finding: the hot inbound-event resolution path repeats the same lookups for a small
// set of devices, so they are cached here. It embeds *Api so every method of
// DeviceManagementApi is promoted, and overrides only the methods on (and the
// mutations that invalidate) the hot lookups, among them:
//
//   - DevicesByToken: device-token -> *Device (positive hits only).
//   - TrackedRelationshipsForDevice: a device's tracked relationships, the full set
//     the resolver denormalizes onto every event. It is one named method rather than
//     a recognized shape of the general relationship search; the search itself is not
//     cached and goes to the DB by promotion.
//   - ProfileResolutionByDeviceType: a device type's published profile — metric
//     definitions, rule scope and fence-set version — as one entry read once per event.
//   - AuthenticateDevice: a credential that has just verified, with its device, for five
//     seconds (Caches.Credentials). Only a success is kept, every hit is checked again
//     exactly as a database row is (expiry, then the constant-time secret compare), and
//     every write that changes a credential or its device evicts it by owning device, on
//     this replica before the write returns and on every other one by broadcast.
//
// AuthenticateDeviceConnect and ResolveDeviceCredential, the MQTT connect checks, are
// deliberately NOT cached and go straight to the DB via method promotion. A connect is
// granted a session that lasts hours, so a connect answered from a copy a revocation had
// not reached would outlive that copy by hours; and the password connect compares through
// credential.Checker under a backoff, from a row (deviceCredentialForConnect) that must
// never reach evaluateCredential.
//
// Tenant scoping: every cache key includes the tenant derived from the context, so
// one tenant can never read another tenant's device or relationships (a
// cross-tenant cache hit would be a tenant-isolation breach). When no tenant is in
// context the cache is bypassed entirely (fail-open to the DB, never cross-tenant).
type CachedApi struct {
	*Api
	caches *Caches
}

// Create a new cached API instance wrapping api with the given caches.
func NewCachedApi(api *Api, caches *Caches) *CachedApi {
	return &CachedApi{
		Api:    api,
		caches: caches,
	}
}

// AuthenticateDevice answers a credential this replica verified within the last
// CredentialCacheTTL from memory, and otherwise reads it from the database and keeps it
// if it verifies. Either way the same checks decide: evaluateCredential (expiry, then the
// constant-time secret compare) and credentialDevice. A failure is never kept, so a
// corrected credential works on the next event.
//
// With no cache, no tenant in context, or nothing usable presented it is the plain Api's
// method, which returns the same sentinels as before.
func (capi *CachedApi) AuthenticateDevice(ctx context.Context, presented *PresentedCredential, now time.Time) (*Device, error) {
	creds := capi.caches.Credentials
	tenant, hasTenant := core.TenantFromContext(ctx)
	if creds == nil || !hasTenant || presented == nil || presented.CredentialId == "" ||
		!CredentialType(presented.CredentialType).Valid() {
		return capi.Api.AuthenticateDevice(ctx, presented, now)
	}
	key := credentialCacheKey(tenant, presented.CredentialType, presented.CredentialId)
	if cred, ok := creds.lookup(tenant, key); ok {
		// A refusal here leaves the entry where it is: what is held is still the stored
		// row, and only the presented secret or the time was wrong.
		if err := evaluateCredential(&cred, presented, now); err != nil {
			return nil, err
		}
		return credentialDevice(&cred)
	}
	gen, readAt := creds.readStarted()
	cred, device, err := capi.Api.authenticateCredential(ctx, presented, now)
	if err != nil {
		return nil, err
	}
	creds.fill(key, presented.CredentialType, cred, gen, readAt)
	return device, nil
}

// EvictDeviceCredentials satisfies model.CacheEvictor: it drops the cached credentials of
// each given device of tenant, here first, before the write that called it returns, and
// then, by broadcast, on every other replica. The tenant is the ROW's, passed by the
// caller, not the context's: a write made under a system context still evicts what was
// filed under the credential's own tenant. An empty tenant evicts nothing and is logged
// as the defect it is.
func (capi *CachedApi) EvictDeviceCredentials(_ context.Context, tenant string, deviceIds []uint) {
	creds := capi.caches.Credentials
	if creds == nil || len(deviceIds) == 0 {
		return
	}
	if tenant == "" {
		log.Error().Uints("deviceIds", deviceIds).
			Msg("A credential cache eviction named no tenant, so it evicted nothing; the entries expire within 5s.")
		return
	}
	creds.EvictDevices(tenant, deviceIds)
	creds.publishEviction(tenant, deviceIds)
}

// EvictEntityDelete satisfies model.CacheEvictor (ADR-044 F2): it drops the caches
// a delete invalidated. A deleted device loses its by-token entry and its own
// tracked-relationship entry; every device that tracked the deleted entity as a
// target loses its relationship entry (the cached set still lists the gone edge).
// No tenant in context means the caches were bypassed on write, so nothing to evict.
func (capi *CachedApi) EvictEntityDelete(ctx context.Context, etype entity.Type, id uint, token string, trackingSourceDeviceIds []uint) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return
	}
	if etype == entity.TypeDevice {
		_ = capi.caches.DeviceByToken.Delete(ctx, deviceByTokenKey(tenant, token))
		_ = capi.caches.RelationshipsBySource.Delete(ctx, relationshipsBySourceKey(tenant, id))
	}
	capi.evictRelationshipSources(ctx, tenant, trackingSourceDeviceIds)
}

// EvictRelationshipSources drops the cached tracked-relationship set of each given
// source device (ADR-044 F2): after an edge is removed the set still lists the gone
// edge. No tenant in context means the caches were bypassed on write, nothing to
// evict.
func (capi *CachedApi) EvictRelationshipSources(ctx context.Context, sourceDeviceIds []uint) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return
	}
	capi.evictRelationshipSources(ctx, tenant, sourceDeviceIds)
}

func (capi *CachedApi) evictRelationshipSources(ctx context.Context, tenant string, sourceDeviceIds []uint) {
	for _, sid := range sourceDeviceIds {
		_ = capi.caches.RelationshipsBySource.Delete(ctx, relationshipsBySourceKey(tenant, sid))
	}
}

// EvictMemberships satisfies model.CacheEvictor (ADR-062): it drops the cached
// membership entry of each given entity of a family, so a mutated membership is not
// served stale from the negative cache. No tenant in context means the cache was
// bypassed on write, so nothing to evict.
func (capi *CachedApi) EvictMemberships(ctx context.Context, entityType string, entityIds []uint) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return
	}
	for _, id := range entityIds {
		_ = capi.caches.MembershipsByEntity.Delete(ctx, membershipsByEntityKey(tenant, entityType, id))
	}
}

// membershipsByEntityKey builds the tenant-scoped cache key for an entity's group
// memberships, keyed by family + row id (row ids are per-table, so the family must be
// part of the key to avoid a device/area id collision).
func membershipsByEntityKey(tenant, entityType string, entityId uint) string {
	return fmt.Sprintf("%s|%s|%d", tenant, entityType, entityId)
}

// EvictScopedGroupsExist satisfies model.CacheEvictor (ADR-062 Decision 7): it drops the
// tenant's cached scoped-groups-exist flag so the resolver's pay-nothing gate re-evaluates.
func (capi *CachedApi) EvictScopedGroupsExist(ctx context.Context) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return
	}
	_ = capi.caches.ScopedGroupsExist.Delete(ctx, tenant)
}

// EvictFenceSetVersion satisfies model.CacheEvictor (ADR-078): a geofence mutation minted
// a new tenant fence-set version, and that version rides in the per-type cached
// ProfileResolution, so every device type of the tenant holds a stale copy.
//
// It fires only when a version was actually minted — an edit that leaves the fence set as
// it was mints nothing and reaches neither this nor the fan-out below, because the cached
// version is still exactly right. See announceMintedGeoFenceSet, the one caller.
//
// 🔴 THIS FANS OUT ACROSS THE TENANT'S DEVICE TYPES BECAUSE THE VERSION IS TENANT-WIDE
// WHILE ITS CACHE KEY IS PER-TYPE. That mismatch is the deliberate cost of reusing the
// resolve path's existing lookup instead of adding a second cache: the fan-out runs once
// per authoring action over a set measured in tens, while the alternative would add a
// per-event cache read and a second bucket to keep coherent. Because the version shares
// its entry with the type's metric definitions, the eviction costs the next event of each
// type a FULL resolution miss (type, profile and version snapshot re-read), not a
// scope-only one — still once per authoring action per type. Missing a type is bounded
// by the ProfileResolution cache TTL and is stale-but-coherent (see the interface comment),
// which is why a read failure here is swallowed rather than surfaced — the fence write
// has already committed and must not be reported as failed over a cache sweep.
func (capi *CachedApi) EvictFenceSetVersion(ctx context.Context) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return
	}
	typeIds, err := capi.Api.deviceTypeIdsForTenant(ctx)
	if err != nil {
		return
	}
	for _, typeId := range typeIds {
		_ = capi.caches.ProfileResolutionByType.Delete(ctx, profileResolutionByTypeKey(tenant, typeId))
	}
}

// AnyScopedGroups serves the resolver's pay-nothing gate (ADR-062 Decision 7) from a
// per-tenant cache: whether the tenant has any rule-scoped group. A lookup without a
// tenant in context bypasses the cache and goes straight to the DB.
func (capi *CachedApi) AnyScopedGroups(ctx context.Context) (bool, error) {
	tenant, hasTenant := core.TenantFromContext(ctx)
	if !hasTenant {
		return capi.Api.AnyScopedGroups(ctx)
	}
	if cached, answered, _ := capi.cachedAnyScopedGroups(ctx, tenant, false); answered {
		return cached, nil
	}
	return capi.loadAnyScopedGroups(ctx, tenant)
}

// cachedAnyScopedGroups is the cache half of AnyScopedGroups (see readCache for the three
// results).
func (capi *CachedApi) cachedAnyScopedGroups(ctx context.Context, tenant string, memoryOnly bool) (value, answered, settled bool) {
	answered, settled = readCache(ctx, capi.caches.ScopedGroupsExist, tenant, &value, memoryOnly)
	return value, answered, settled
}

// readCache reads key from c into dest: the cache half of every cached read here, so all of
// them treat a miss and a cache error alike, as not answered, and go on to the database.
//
// With memoryOnly it asks only the in-process copy, and settled reports whether that was
// enough to decide: false when memory held nothing live, and the caller must ask again
// without memoryOnly. Without it, settled is always true.
func readCache(ctx context.Context, c *messaging.Cache, key string, dest any, memoryOnly bool) (answered, settled bool) {
	if memoryOnly {
		found, err := c.GetFromMemory(key, dest)
		if err != nil {
			// Held, but it would not decode: as Get's decode error does, go to the database.
			return false, true
		}
		return found, found
	}
	found, err := c.Get(ctx, key, dest)
	return err == nil && found, true
}

// loadAnyScopedGroups is the database half of AnyScopedGroups: it reads the database and
// stores the answer in the cache.
func (capi *CachedApi) loadAnyScopedGroups(ctx context.Context, tenant string) (bool, error) {
	exists, err := capi.Api.AnyScopedGroups(ctx)
	if err != nil {
		return false, err
	}
	_ = capi.caches.ScopedGroupsExist.Set(ctx, tenant, exists)
	return exists, nil
}

// MembershipsForEntity serves the resolve path's per-entity group-membership lookup
// (ADR-062) from cache, including empty results (a non-member is the common case and
// must not query on every event). A lookup without a tenant in context bypasses the
// cache and goes straight to the DB.
//
// Like the sibling read-through cache here (ProfileResolutionByType), this is
// cache-aside: a mutation evicts post-commit, but a read that missed and is repopulating
// across that commit can re-store the pre-commit value, so worst-case staleness is TTL-
// bounded, not the eviction instant. On top of that, another replica keeps what it read in
// process memory for up to 5 s after the eviction (see InitializeCaches). That is the
// accepted posture for these caches. ADR-062's arming invariant must therefore not depend
// on sub-TTL visibility of a just-registered group@v. Nothing in the tree provides a
// margin for it today: a rule scoped to a new group can miss that group's members for up
// to the TTL on the events of a replica still holding the old membership.
func (capi *CachedApi) MembershipsForEntity(ctx context.Context, entityType string, entityId uint) ([]GroupMembership, error) {
	tenant, hasTenant := core.TenantFromContext(ctx)
	if !hasTenant {
		return capi.Api.MembershipsForEntity(ctx, entityType, entityId)
	}

	key := membershipsByEntityKey(tenant, entityType, entityId)
	if cached, answered, _ := capi.cachedMemberships(ctx, key, false); answered {
		return cached, nil
	}
	return capi.loadMemberships(ctx, key, entityType, entityId)
}

// cachedMemberships is the cache half of MembershipsForEntity (see readCache).
func (capi *CachedApi) cachedMemberships(ctx context.Context, key string, memoryOnly bool) (value []GroupMembership, answered, settled bool) {
	answered, settled = readCache(ctx, capi.caches.MembershipsByEntity, key, &value, memoryOnly)
	return value, answered, settled
}

// loadMemberships is the database half of MembershipsForEntity.
func (capi *CachedApi) loadMemberships(ctx context.Context, key, entityType string, entityId uint) ([]GroupMembership, error) {
	memberships, err := capi.Api.MembershipsForEntity(ctx, entityType, entityId)
	if err != nil {
		return nil, err
	}
	_ = capi.caches.MembershipsByEntity.Set(ctx, key, memberships)
	return memberships, nil
}

// deviceByTokenKey builds the tenant-scoped cache key for a single device token.
// The tenant is part of the key so a hit can never cross tenant boundaries.
func deviceByTokenKey(tenant string, token string) string {
	return tenant + "|" + token
}

// relationshipsBySourceKey builds the tenant-scoped cache key for a device's
// tracked relationships, keyed by the source device row id.
func relationshipsBySourceKey(tenant string, sourceId uint) string {
	return fmt.Sprintf("%s|%d", tenant, sourceId)
}

// DevicesByToken serves single-token lookups from the device-by-token cache,
// caching positive hits only so a newly-registered device resolves on its very
// next event rather than waiting out the TTL. Multi-token lookups and lookups
// without a tenant in context bypass the cache and go straight to the DB.
func (capi *CachedApi) DevicesByToken(ctx context.Context, tokens []string) ([]*Device, error) {
	tenant, hasTenant := core.TenantFromContext(ctx)
	if !hasTenant || len(tokens) != 1 {
		return capi.Api.DevicesByToken(ctx, tokens)
	}

	key := deviceByTokenKey(tenant, tokens[0])
	if device := capi.getDevice(ctx, key); device != nil {
		return []*Device{device}, nil
	}

	matches, err := capi.Api.DevicesByToken(ctx, tokens)
	if err != nil {
		return nil, err
	}
	// Cache positive hits only; never cache a miss/not-found.
	if len(matches) == 1 && matches[0] != nil {
		_ = capi.caches.DeviceByToken.Set(ctx, key, matches[0])
	}
	return matches, nil
}

// getDevice returns the cached device for key, or nil on a miss (or any cache
// error, which degrades to a DB lookup by the caller).
func (capi *CachedApi) getDevice(ctx context.Context, key string) *Device {
	var device Device
	if found, err := capi.caches.DeviceByToken.Get(ctx, key, &device); err == nil && found {
		return &device
	}
	return nil
}

// TrackedRelationshipsForDevice serves the resolver's tracked-relationship lookup from
// cache (positive results only, keyed by tenant + source device id), falling through to
// the DB when no tenant is in context.
//
// 🔑 THIS USED TO BE A SHAPE-SNIFFING OVERRIDE OF THE GENERIC SEARCH, and deleting that
// predicate is half the reason the read was given a name. Because the resolver expressed
// its lookup as an ordinary EntityRelationshipSearchCriteria, the only way to recognize
// the one query worth caching was to compare five criteria fields against the shape the
// resolver happened to send (isTrackedSourceDeviceShape). That predicate was a second,
// informal definition of this method, living in a different package from the caller it
// described and kept in step with it by hand.
//
// 🔴 AND IT DID NOT COMPARE THE PAGINATION. The cache key is (tenant, source device) with
// no room for a page, so any caller issuing that field shape with a page size would have
// had its PAGE written to the entry the resolver reads as the COMPLETE tracked set —
// silently dropping anchors from every event until the entry expired. Nothing does that
// today: GraphQL reads deliberately go through the uncached Api (see GetApi; only the
// profile publish/rollback MUTATIONS take GetCachedApi, for its eviction), so
// this was one caller away rather than broken. A read that must be complete and a read
// that may be paged are now different methods, so there is no shape left to confuse.
func (capi *CachedApi) TrackedRelationshipsForDevice(ctx context.Context,
	deviceId uint) (*EntityRelationshipSearchResults, error) {
	tenant, hasTenant := core.TenantFromContext(ctx)
	if !hasTenant {
		return capi.Api.TrackedRelationshipsForDevice(ctx, deviceId)
	}

	key := relationshipsBySourceKey(tenant, deviceId)
	if results, answered, _ := capi.cachedRelationships(ctx, key, false); answered {
		return results, nil
	}
	return capi.loadRelationships(ctx, key, deviceId)
}

// loadRelationships is the database half of TrackedRelationshipsForDevice.
func (capi *CachedApi) loadRelationships(ctx context.Context, key string,
	deviceId uint) (*EntityRelationshipSearchResults, error) {
	results, err := capi.Api.TrackedRelationshipsForDevice(ctx, deviceId)
	if err != nil {
		return nil, err
	}
	// Cache positive results only.
	if results != nil {
		_ = capi.caches.RelationshipsBySource.Set(ctx, key, results)
	}
	return results, nil
}

// cachedRelationships is the cache half of TrackedRelationshipsForDevice (see readCache).
func (capi *CachedApi) cachedRelationships(ctx context.Context, key string,
	memoryOnly bool) (*EntityRelationshipSearchResults, bool, bool) {
	var results EntityRelationshipSearchResults
	answered, settled := readCache(ctx, capi.caches.RelationshipsBySource, key, &results, memoryOnly)
	if !answered {
		return nil, false, settled
	}
	return &results, true, settled
}

// UpdateDevice forwards to the DB then evicts the device's by-token entry so a
// device-type change is not served stale (bounded further by the TTL).
//
// The second eviction this used to perform — of `updated.Token` when it differed
// from the argument — is gone with the token field it defended against. The update
// input carries no token, so the row's token cannot move and the argument is the
// only key the cached entry can be filed under.
func (capi *CachedApi) UpdateDevice(ctx context.Context, token string, request *DeviceUpdateRequest) (*Device, error) {
	updated, err := capi.Api.UpdateDevice(ctx, token, request)
	if err != nil {
		return nil, err
	}
	if tenant, ok := core.TenantFromContext(ctx); ok {
		_ = capi.caches.DeviceByToken.Delete(ctx, deviceByTokenKey(tenant, token))
	}
	return updated, nil
}

// CreateEntityRelationship forwards to the DB then, when the new edge originates
// from a device, evicts that source device's tracked-relationships entry so a
// newly tracked relationship is not hidden by a stale cached set.
func (capi *CachedApi) CreateEntityRelationship(ctx context.Context,
	request *EntityRelationshipCreateRequest) (*EntityRelationship, error) {
	created, err := capi.Api.CreateEntityRelationship(ctx, request)
	if err != nil {
		return nil, err
	}
	if created != nil && created.SourceType == string(entity.TypeDevice) {
		if tenant, ok := core.TenantFromContext(ctx); ok {
			_ = capi.caches.RelationshipsBySource.Delete(ctx, relationshipsBySourceKey(tenant, created.SourceId))
		}
	}
	return created, nil
}

// UpdateDeviceType forwards to the DB then evicts the type's cached profile
// resolution. Attaching, changing, or detaching the type's profile (ADR-045)
// changes what the ingest path resolves for this type — the same class of
// resolution change as a publish/rollback on the profile — so the type's cached
// entry must be dropped. Bounded further by the cache TTL if eviction fails.
func (capi *CachedApi) UpdateDeviceType(ctx context.Context, token string,
	request *DeviceTypeUpdateRequest) (*DeviceType, error) {
	updated, err := capi.Api.UpdateDeviceType(ctx, token, request)
	if err != nil {
		return nil, err
	}
	if updated != nil {
		if tenant, ok := core.TenantFromContext(ctx); ok {
			_ = capi.caches.ProfileResolutionByType.Delete(ctx, profileResolutionByTypeKey(tenant, updated.ID))
		}
	}
	return updated, nil
}

// profileResolutionByTypeKey builds the tenant-scoped cache key for a device type's
// ProfileResolution, keyed by the device type row id.
func profileResolutionByTypeKey(tenant string, deviceTypeId uint) string {
	return fmt.Sprintf("%s|%d", tenant, deviceTypeId)
}

// ProfileResolutionByDeviceType serves the resolve path's per-device-type profile read
// from cache, including empty resolutions (an untyped or unpublished device type is
// common and must not query on every event). A lookup without a tenant in context
// bypasses the cache and goes straight to the DB.
func (capi *CachedApi) ProfileResolutionByDeviceType(ctx context.Context, deviceTypeId uint) (*ProfileResolution, error) {
	tenant, hasTenant := core.TenantFromContext(ctx)
	if !hasTenant {
		return capi.Api.ProfileResolutionByDeviceType(ctx, deviceTypeId)
	}

	key := profileResolutionByTypeKey(tenant, deviceTypeId)
	if cached, answered, _ := capi.cachedProfileResolution(ctx, key, false); answered {
		return cached, nil
	}
	return capi.loadProfileResolution(ctx, key, deviceTypeId)
}

// cachedProfileResolution is the cache half of ProfileResolutionByDeviceType (see
// readCache).
func (capi *CachedApi) cachedProfileResolution(ctx context.Context, key string,
	memoryOnly bool) (*ProfileResolution, bool, bool) {
	var cached ProfileResolution
	answered, settled := readCache(ctx, capi.caches.ProfileResolutionByType, key, &cached, memoryOnly)
	if !answered {
		return nil, false, settled
	}
	return &cached, true, settled
}

// loadProfileResolution is the database half of ProfileResolutionByDeviceType.
func (capi *CachedApi) loadProfileResolution(ctx context.Context, key string, deviceTypeId uint) (*ProfileResolution, error) {
	res, err := capi.Api.ProfileResolutionByDeviceType(ctx, deviceTypeId)
	if err != nil {
		return nil, err
	}
	_ = capi.caches.ProfileResolutionByType.Set(ctx, key, res)
	return res, nil
}

// PublishDeviceProfile forwards to the DB then evicts the cached resolution of
// every device type adopting the profile: resolution serves the active PUBLISHED
// version (ADR-045 slice c), so a publish is exactly when the cached set changes and
// must be dropped (a draft edit does not change resolution, so def create/update no
// longer evict). Bounded further by the cache TTL if the eviction fan-out fails.
func (capi *CachedApi) PublishDeviceProfile(ctx context.Context, token string,
	label, description *string, publishedBy string) (*DeviceProfileVersion, error) {
	version, err := capi.Api.PublishDeviceProfile(ctx, token, label, description, publishedBy)
	if err != nil {
		return nil, err
	}
	capi.evictProfileResolution(ctx, version.DeviceProfileId)
	return version, nil
}

// RollbackDeviceProfile forwards to the DB then evicts the cached resolution of
// every device type adopting the profile, since the active version pointer (what
// resolution reads) just moved.
func (capi *CachedApi) RollbackDeviceProfile(ctx context.Context, token string, version int32) (*DeviceProfile, error) {
	profile, err := capi.Api.RollbackDeviceProfile(ctx, token, version)
	if err != nil {
		return nil, err
	}
	if profile != nil {
		capi.evictProfileResolution(ctx, profile.ID)
	}
	return profile, nil
}

// 🔴 THERE IS DELIBERATELY NO UpdateDeviceProfile OR RenameDeviceProfile OVERRIDE HERE,
// AND ITS ABSENCE IS AN INVARIANT RATHER THAN AN OMISSION.
//
// One used to sit here: updateDeviceProfile carried the rename in its payload token, and
// a rename changes the denormalized ProfileVersionToken "{profileToken}@{version}"
// (ADR-051) that resolution stamps onto every event — a dependency the metric definitions
// alone did not have — so the override dropped the cached scope of every device type adopting
// the profile.
//
// Two things ended that. The rename moved to its own mutation (Api.RenameDeviceProfile),
// so an update can no longer move a token at all; and a rename is REFUSED once the
// profile has any published version or any adopting device type. evictProfileResolution
// fans out across the ADOPTING TYPES, so on the only path that can still move a token
// there are provably none to evict.
//
// If that guard is ever relaxed — if a published or adopted profile becomes renameable —
// this override has to come back, because the stamped scope token would then be stale in
// every ingest cache. The guard and this absence are one decision, and the guard's
// comment in api_profiles.go is where it is argued.

// evictProfileResolution drops the cached resolution of every device type adopting
// the profile whose active version changed. The ingest cache is keyed by device
// type (what the hot path has), but versioning lives on the profile (ADR-045), so
// eviction fans back out across the adopting types. A shared profile is rare and
// this is off the hot path; the cache TTL bounds any miss.
func (capi *CachedApi) evictProfileResolution(ctx context.Context, profileId uint) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return
	}
	typeIds, err := capi.Api.deviceTypeIdsForProfile(ctx, profileId)
	if err != nil {
		return
	}
	for _, typeId := range typeIds {
		_ = capi.caches.ProfileResolutionByType.Delete(ctx, profileResolutionByTypeKey(tenant, typeId))
	}
}

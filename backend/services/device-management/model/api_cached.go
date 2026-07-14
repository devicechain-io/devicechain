// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
)

// CachedApi is a caching decorator over *Api implementing the ADR-022 review B2
// finding: the hot inbound-event resolution path repeats two lookups for a small
// set of devices, so they are cached here. It embeds *Api so every method of
// DeviceManagementApi is promoted, and overrides only the methods on (and the
// mutations that invalidate) the two hot lookups:
//
//   - DevicesByToken: device-token -> *Device (positive hits only).
//   - EntityRelationships: a device's tracked relationships, only for the
//     specific (SourceType=device, SourceId set, Tracked=true) shape the resolver
//     issues; every other query shape falls through to the DB.
//
// AuthenticateDevice is deliberately NOT cached: credential validation is
// security-sensitive (caching would delay the effect of revocation/expiry), so it
// always goes straight to the DB via method promotion.
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

// AnyScopedGroups serves the resolver's pay-nothing gate (ADR-062 Decision 7) from a
// per-tenant cache: whether the tenant has any rule-scoped group. A lookup without a
// tenant in context bypasses the cache and goes straight to the DB.
func (capi *CachedApi) AnyScopedGroups(ctx context.Context) (bool, error) {
	tenant, hasTenant := core.TenantFromContext(ctx)
	if !hasTenant {
		return capi.Api.AnyScopedGroups(ctx)
	}
	var cached bool
	if found, err := capi.caches.ScopedGroupsExist.Get(ctx, tenant, &cached); err == nil && found {
		return cached, nil
	}
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
// Like the sibling read-through caches here (MetricDefsByType, ProfileScopeByType), this is
// cache-aside: a mutation evicts post-commit, but a read that missed and is repopulating
// across that commit can re-store the pre-commit value, so worst-case staleness is TTL-
// bounded, not the eviction instant. That is the accepted posture for these caches. ADR-062's
// arming invariant must therefore not depend on sub-TTL visibility of a just-registered
// group@v — S4's rule arming owns that guarantee (e.g. arming a safety margin after Register).
func (capi *CachedApi) MembershipsForEntity(ctx context.Context, entityType string, entityId uint) ([]GroupMembership, error) {
	tenant, hasTenant := core.TenantFromContext(ctx)
	if !hasTenant {
		return capi.Api.MembershipsForEntity(ctx, entityType, entityId)
	}

	key := membershipsByEntityKey(tenant, entityType, entityId)
	var cached []GroupMembership
	if found, err := capi.caches.MembershipsByEntity.Get(ctx, key, &cached); err == nil && found {
		return cached, nil
	}

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

// isTrackedSourceDeviceShape reports whether criteria is exactly the shape the
// resolver issues for a device's tracked relationships
// (SourceType=device, SourceId set, Tracked=true). Only this shape is served from
// or written to the relationships cache; any other shape falls through to the DB
// so unrelated query shapes are never mis-cached.
func isTrackedSourceDeviceShape(criteria EntityRelationshipSearchCriteria) bool {
	return criteria.SourceId != nil &&
		criteria.Tracked != nil && *criteria.Tracked &&
		criteria.SourceType != nil && *criteria.SourceType == string(entity.TypeDevice) &&
		criteria.TargetType == nil &&
		criteria.RelationshipType == nil
}

// EntityRelationships serves the resolver's tracked-source-device relationship
// lookup from cache (positive results only, keyed by tenant + source device id),
// and falls through to the DB for any other query shape or when no tenant is in
// context.
func (capi *CachedApi) EntityRelationships(ctx context.Context,
	criteria EntityRelationshipSearchCriteria) (*EntityRelationshipSearchResults, error) {
	tenant, hasTenant := core.TenantFromContext(ctx)
	if !hasTenant || !isTrackedSourceDeviceShape(criteria) {
		return capi.Api.EntityRelationships(ctx, criteria)
	}

	key := relationshipsBySourceKey(tenant, *criteria.SourceId)
	if results := capi.getRelationships(ctx, key); results != nil {
		return results, nil
	}

	results, err := capi.Api.EntityRelationships(ctx, criteria)
	if err != nil {
		return nil, err
	}
	// Cache positive results only.
	if results != nil {
		_ = capi.caches.RelationshipsBySource.Set(ctx, key, results)
	}
	return results, nil
}

// getRelationships returns the cached relationship results for key, or nil on a
// miss (or any cache error, which degrades to a DB lookup by the caller).
func (capi *CachedApi) getRelationships(ctx context.Context, key string) *EntityRelationshipSearchResults {
	var results EntityRelationshipSearchResults
	if found, err := capi.caches.RelationshipsBySource.Get(ctx, key, &results); err == nil && found {
		return &results
	}
	return nil
}

// UpdateDevice forwards to the DB then evicts the device's by-token entry so a
// rename or device-type change is not served stale (bounded further by the TTL).
func (capi *CachedApi) UpdateDevice(ctx context.Context, token string, request *DeviceCreateRequest) (*Device, error) {
	updated, err := capi.Api.UpdateDevice(ctx, token, request)
	if err != nil {
		return nil, err
	}
	if tenant, ok := core.TenantFromContext(ctx); ok {
		// Evict both the lookup token and the (possibly changed) stored token.
		_ = capi.caches.DeviceByToken.Delete(ctx, deviceByTokenKey(tenant, token))
		if updated != nil && updated.Token != token {
			_ = capi.caches.DeviceByToken.Delete(ctx, deviceByTokenKey(tenant, updated.Token))
		}
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

// UpdateDeviceType forwards to the DB then evicts the type's cached metric
// definitions. Attaching, changing, or detaching the type's profile (ADR-045)
// changes what the ingest path resolves for this type — the same class of
// resolution change as a publish/rollback on the profile — so the type's cached
// def set must be dropped. Bounded further by the cache TTL if eviction fails.
func (capi *CachedApi) UpdateDeviceType(ctx context.Context, token string,
	request *DeviceTypeCreateRequest) (*DeviceType, error) {
	updated, err := capi.Api.UpdateDeviceType(ctx, token, request)
	if err != nil {
		return nil, err
	}
	if updated != nil {
		if tenant, ok := core.TenantFromContext(ctx); ok {
			_ = capi.caches.MetricDefsByType.Delete(ctx, metricDefsByTypeKey(tenant, updated.ID))
			_ = capi.caches.ProfileScopeByType.Delete(ctx, profileScopeByTypeKey(tenant, updated.ID))
		}
	}
	return updated, nil
}

// metricDefsByTypeKey builds the tenant-scoped cache key for a device type's
// declared metric definitions, keyed by the device type row id.
func metricDefsByTypeKey(tenant string, deviceTypeId uint) string {
	return fmt.Sprintf("%s|%d", tenant, deviceTypeId)
}

// MetricDefinitionsByDeviceType serves the ingest validation path's per-device-type
// metric-definition lookup from cache, including empty results (an untyped device
// type is the common case and must not query on every measurement event). A lookup
// without a tenant in context bypasses the cache and goes straight to the DB.
func (capi *CachedApi) MetricDefinitionsByDeviceType(ctx context.Context, deviceTypeId uint) ([]*MetricDefinition, error) {
	tenant, hasTenant := core.TenantFromContext(ctx)
	if !hasTenant {
		return capi.Api.MetricDefinitionsByDeviceType(ctx, deviceTypeId)
	}

	key := metricDefsByTypeKey(tenant, deviceTypeId)
	var cached []*MetricDefinition
	if found, err := capi.caches.MetricDefsByType.Get(ctx, key, &cached); err == nil && found {
		return cached, nil
	}

	defs, err := capi.Api.MetricDefinitionsByDeviceType(ctx, deviceTypeId)
	if err != nil {
		return nil, err
	}
	_ = capi.caches.MetricDefsByType.Set(ctx, key, defs)
	return defs, nil
}

// profileScopeByTypeKey builds the tenant-scoped cache key for a device type's
// denormalized rule-scoping identity (ADR-051), keyed by the device type row id.
func profileScopeByTypeKey(tenant string, deviceTypeId uint) string {
	return fmt.Sprintf("%s|%d", tenant, deviceTypeId)
}

// ProfileScopeByDeviceType serves the resolve path's per-device-type scope lookup
// (ADR-051) from cache, including empty scopes (an untyped or unpublished device
// type is common and must not query on every event). A lookup without a tenant in
// context bypasses the cache and goes straight to the DB.
func (capi *CachedApi) ProfileScopeByDeviceType(ctx context.Context, deviceTypeId uint) (*ProfileScope, error) {
	tenant, hasTenant := core.TenantFromContext(ctx)
	if !hasTenant {
		return capi.Api.ProfileScopeByDeviceType(ctx, deviceTypeId)
	}

	key := profileScopeByTypeKey(tenant, deviceTypeId)
	var cached ProfileScope
	if found, err := capi.caches.ProfileScopeByType.Get(ctx, key, &cached); err == nil && found {
		return &cached, nil
	}

	scope, err := capi.Api.ProfileScopeByDeviceType(ctx, deviceTypeId)
	if err != nil {
		return nil, err
	}
	_ = capi.caches.ProfileScopeByType.Set(ctx, key, scope)
	return scope, nil
}

// PublishDeviceProfile forwards to the DB then evicts the cached definitions of
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

// RollbackDeviceProfile forwards to the DB then evicts the cached definitions of
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

// UpdateDeviceProfile forwards to the DB then, on a profile-TOKEN rename, evicts
// the cached scope of every device type adopting the profile. The denormalized
// ProfileVersionToken is "{profileToken}@{version}" (ADR-051), so a rename changes
// what resolution stamps onto events even though the version and the metric/command/
// alarm definitions are unchanged — a dependency the metric-def cache does not have.
// evictProfileResolution drops both caches; the metric-def eviction is a harmless
// over-eviction (it simply repopulates). Bounded further by the cache TTL.
func (capi *CachedApi) UpdateDeviceProfile(ctx context.Context, token string,
	request *DeviceProfileCreateRequest) (*DeviceProfile, error) {
	updated, err := capi.Api.UpdateDeviceProfile(ctx, token, request)
	if err != nil {
		return nil, err
	}
	if updated != nil && token != request.Token {
		capi.evictProfileResolution(ctx, updated.ID)
	}
	return updated, nil
}

// evictProfileResolution drops the cached definitions of every device type adopting
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
		_ = capi.caches.MetricDefsByType.Delete(ctx, metricDefsByTypeKey(tenant, typeId))
		_ = capi.caches.ProfileScopeByType.Delete(ctx, profileScopeByTypeKey(tenant, typeId))
	}
}

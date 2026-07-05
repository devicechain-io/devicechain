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

// CreateMetricDefinition forwards to the DB then evicts the affected device type's
// cached definitions so a newly declared metric is enforced on the next event.
func (capi *CachedApi) CreateMetricDefinition(ctx context.Context, request *MetricDefinitionCreateRequest) (*MetricDefinition, error) {
	created, err := capi.Api.CreateMetricDefinition(ctx, request)
	if err != nil {
		return nil, err
	}
	capi.evictMetricDefs(ctx, created)
	return created, nil
}

// UpdateMetricDefinition forwards to the DB then evicts the cached definitions of
// every device type adopting the affected profile so a changed bound/type/enum
// takes effect on the next event (bounded further by the cache TTL if a def is
// retargeted to another profile).
func (capi *CachedApi) UpdateMetricDefinition(ctx context.Context, token string, request *MetricDefinitionCreateRequest) (*MetricDefinition, error) {
	updated, err := capi.Api.UpdateMetricDefinition(ctx, token, request)
	if err != nil {
		return nil, err
	}
	capi.evictMetricDefs(ctx, updated)
	return updated, nil
}

// evictMetricDefs drops the cached definitions for a changed metric definition.
// The ingest cache is keyed by device type (what the hot path has), but a
// definition now lives on the profile (ADR-045), so eviction fans back out to
// every device type that adopts the definition's profile. A shared profile is
// rare and this is off the hot path; the cache TTL bounds any miss.
func (capi *CachedApi) evictMetricDefs(ctx context.Context, def *MetricDefinition) {
	if def == nil {
		return
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return
	}
	typeIds, err := capi.Api.deviceTypeIdsForProfile(ctx, def.DeviceProfileId)
	if err != nil {
		return
	}
	for _, typeId := range typeIds {
		_ = capi.caches.MetricDefsByType.Delete(ctx, metricDefsByTypeKey(tenant, typeId))
	}
}

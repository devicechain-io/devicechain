// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"strconv"

	"github.com/devicechain-io/dc-device-management/model"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
)

//go:embed schema.graphql
var SchemaContent string

// ContextCachedApiKey carries the caching *model.CachedApi decorator into the
// GraphQL context. The plain *model.Api backs reads/writes (ContextApiKey); only
// the mutations that must invalidate the shared ingest cache — profile publish and
// rollback — reach for the cached decorator so their eviction actually runs, without
// making every console read cache-stale (ADR-045 slice c; the resolution cache is
// keyed by device type and lives on the processor's CachedApi).
const ContextCachedApiKey = gqlcore.ContextKey("cached-api")

type SchemaResolver struct{}

// Get rdb manager from context.
func (s *SchemaResolver) GetRdbManager(ctx context.Context) *rdb.RdbManager {
	return ctx.Value(gqlcore.ContextRdbKey).(*rdb.RdbManager)
}

// Get api from context.
func (s *SchemaResolver) GetApi(ctx context.Context) *model.Api {
	return ctx.Value(gqlcore.ContextApiKey).(*model.Api)
}

// GetCachedApi returns the caching decorator over the api, used by the profile
// publish/rollback mutations so their resolution-cache eviction runs (ADR-045 slice
// c). Reads and every other mutation deliberately use GetApi (uncached) instead.
func (s *SchemaResolver) GetCachedApi(ctx context.Context) *model.CachedApi {
	return ctx.Value(ContextCachedApiKey).(*model.CachedApi)
}

// Get nats manager from context. Backs the live subscription resolvers
// (SubscribeLive); injected as a provider in main.go once the manager is connected.
func (s *SchemaResolver) GetNats(ctx context.Context) *messaging.NatsManager {
	return ctx.Value(gqlcore.ContextNatsKey).(*messaging.NatsManager)
}

// Convert string ids to uint ids.
func (r *SchemaResolver) asUintIds(val []string) ([]uint, error) {
	ids := make([]uint, 0)
	for _, sid := range val {
		id, err := strconv.ParseUint(sid, 0, 64)
		if err != nil {
			return nil, err
		}
		ids = append(ids, uint(id))
	}
	return ids, nil
}

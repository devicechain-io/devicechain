// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"errors"

	"github.com/devicechain-io/dc-event-management/model"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
)

//go:embed schema.graphql
var SchemaContent string

type SchemaResolver struct{}

// ErrNoNatsManager reports a missing or invalid subscription dependency in the context.
var ErrNoNatsManager = errors.New("no NATS manager in context")

// Get rdb manager from context.
func (s *SchemaResolver) GetRdbManager(ctx context.Context) *rdb.RdbManager {
	return ctx.Value(gqlcore.ContextRdbKey).(*rdb.RdbManager)
}

// Get api from context.
func (s *SchemaResolver) GetApi(ctx context.Context) *model.Api {
	return ctx.Value(gqlcore.ContextApiKey).(*model.Api)
}

// Get nats manager from context (used by subscription resolvers to open a live
// tenant-scoped feed).
func (s *SchemaResolver) GetNats(ctx context.Context) (*messaging.NatsManager, error) {
	manager, ok := ctx.Value(gqlcore.ContextNatsKey).(*messaging.NatsManager)
	if !ok || manager == nil {
		return nil, ErrNoNatsManager
	}
	return manager, nil
}

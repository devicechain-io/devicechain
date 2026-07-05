// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"strconv"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-notification-management/model"
)

//go:embed schema.graphql
var SchemaContent string

// SchemaResolver is the root resolver for the notification-management schema. It
// reaches its dependencies (the rdb manager and the notification Api) through the
// GraphQL request context, which main.go populates as providers — the same
// pattern the other services use.
type SchemaResolver struct{}

// GetRdbManager returns the rdb manager from the request context.
func (s *SchemaResolver) GetRdbManager(ctx context.Context) *rdb.RdbManager {
	return ctx.Value(gqlcore.ContextRdbKey).(*rdb.RdbManager)
}

// GetApi returns the notification Api from the request context.
func (s *SchemaResolver) GetApi(ctx context.Context) *model.Api {
	return ctx.Value(gqlcore.ContextApiKey).(*model.Api)
}

// asUintIds converts string ids to uint ids.
func (s *SchemaResolver) asUintIds(val []string) ([]uint, error) {
	ids := make([]uint, 0, len(val))
	for _, sid := range val {
		id, err := strconv.ParseUint(sid, 0, 64)
		if err != nil {
			return nil, err
		}
		ids = append(ids, uint(id))
	}
	return ids, nil
}

// NotificationChannelTypes returns the static catalog of channel-adapter types the
// service can deliver through (ADR-017).
func (s *SchemaResolver) NotificationChannelTypes() []*NotificationChannelTypeResolver {
	out := make([]*NotificationChannelTypeResolver, 0, len(model.SupportedChannelTypes))
	for i := range model.SupportedChannelTypes {
		out = append(out, &NotificationChannelTypeResolver{M: model.SupportedChannelTypes[i]})
	}
	return out
}

// NotificationChannelTypeResolver resolves a single supported channel type.
type NotificationChannelTypeResolver struct {
	M model.ChannelType
}

func (r *NotificationChannelTypeResolver) Id() string          { return r.M.Id }
func (r *NotificationChannelTypeResolver) Label() string       { return r.M.Label }
func (r *NotificationChannelTypeResolver) Description() string { return r.M.Description }
func (r *NotificationChannelTypeResolver) Available() bool     { return r.M.Available }

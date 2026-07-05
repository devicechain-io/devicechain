// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	_ "embed"

	"github.com/devicechain-io/dc-notification-management/model"
)

//go:embed schema.graphql
var SchemaContent string

// SchemaResolver is the root resolver for the notification-management schema. It
// holds no dependencies yet: this slice serves only the static channel-type
// capability list. The policy-CRUD slice will give it an Api handle (via the
// GraphQL context, as the other services do) when it needs one.
type SchemaResolver struct{}

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

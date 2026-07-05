// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-notification-management/model"
	gql "github.com/graph-gophers/graphql-go"
)

// NotificationChannelResolver resolves a configured delivery channel.
type NotificationChannelResolver struct {
	M model.NotificationChannel
	S *SchemaResolver
	C context.Context
}

func (r *NotificationChannelResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *NotificationChannelResolver) CreatedAt() *string { return util.FormatTime(r.M.CreatedAt) }

func (r *NotificationChannelResolver) UpdatedAt() *string { return util.FormatTime(r.M.UpdatedAt) }

func (r *NotificationChannelResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *NotificationChannelResolver) Token() string { return r.M.Token }

func (r *NotificationChannelResolver) Name() *string { return util.NullStr(r.M.Name) }

func (r *NotificationChannelResolver) Description() *string { return util.NullStr(r.M.Description) }

func (r *NotificationChannelResolver) ChannelType() string { return r.M.ChannelType }

func (r *NotificationChannelResolver) Config() *string { return util.MetadataStr(r.M.Config) }

// HasSecret reports whether a delivery secret is configured, without exposing it.
// The secret itself is write-only (accepted on create/update, never returned), so
// a notification:read holder cannot exfiltrate the SMTP password / bearer token —
// this boolean is all the read API reveals about it.
func (r *NotificationChannelResolver) HasSecret() bool { return r.M.Secret.Valid }

func (r *NotificationChannelResolver) Enabled() bool { return r.M.Enabled }

func (r *NotificationChannelResolver) Metadata() *string { return util.MetadataStr(r.M.Metadata) }

// NotificationChannelSearchResultsResolver resolves a page of channels.
type NotificationChannelSearchResultsResolver struct {
	M model.NotificationChannelSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *NotificationChannelSearchResultsResolver) Results() []*NotificationChannelResolver {
	resolvers := make([]*NotificationChannelResolver, 0, len(r.M.Results))
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &NotificationChannelResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *NotificationChannelSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}

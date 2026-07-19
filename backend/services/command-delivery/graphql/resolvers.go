// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/devicechain-io/dc-command-delivery/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// ----------------
// Command resolver
// ----------------

type CommandResolver struct {
	M model.Command
	S *SchemaResolver
	C context.Context
}

func (r *CommandResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *CommandResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *CommandResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *CommandResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *CommandResolver) Token() string {
	return r.M.Token
}

func (r *CommandResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *CommandResolver) DeviceToken() string {
	return r.M.DeviceToken
}

func (r *CommandResolver) Name() string {
	return r.M.Name
}

func (r *CommandResolver) Payload() *string {
	return util.MetadataStr(r.M.Payload)
}

func (r *CommandResolver) Status() string {
	return r.M.Status
}

func (r *CommandResolver) QueuedTime() *string {
	return util.FormatTime(r.M.QueuedTime)
}

func (r *CommandResolver) SentTime() *string {
	if r.M.SentTime.Valid {
		return util.FormatTime(r.M.SentTime.Time)
	}
	return nil
}

func (r *CommandResolver) RespondedTime() *string {
	if r.M.RespondedTime.Valid {
		return util.FormatTime(r.M.RespondedTime.Time)
	}
	return nil
}

func (r *CommandResolver) ExpiresAt() *string {
	if r.M.ExpiresAt.Valid {
		return util.FormatTime(r.M.ExpiresAt.Time)
	}
	return nil
}

func (r *CommandResolver) ResponsePayload() *string {
	return util.MetadataStr(r.M.ResponsePayload)
}

func (r *CommandResolver) Error() *string {
	return util.NullStr(r.M.Error)
}

// -------------------------------
// Command search results resolver
// -------------------------------

type CommandSearchResultsResolver struct {
	M model.CommandSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *CommandSearchResultsResolver) Results() []*CommandResolver {
	resolvers := make([]*CommandResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&CommandResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *CommandSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

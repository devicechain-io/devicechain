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

func (r *CommandResolver) DispatchAfter() *string {
	if r.M.DispatchAfter.Valid {
		return util.FormatTime(r.M.DispatchAfter.Time)
	}
	return nil
}

func (r *CommandResolver) MaintenanceWindowToken() *string {
	if r.M.WindowToken.Valid {
		return &r.M.WindowToken.String
	}
	return nil
}

func (r *CommandResolver) ResponsePayload() *string {
	return util.MetadataStr(r.M.ResponsePayload)
}

func (r *CommandResolver) Error() *string {
	return util.NullStr(r.M.Error)
}

// BatchToken resolves the denormalized link, not the BatchId foreign key: the column is
// already on the row, so this costs no join and no second query per command in a page.
func (r *CommandResolver) BatchToken() *string {
	return util.NullStr(r.M.BatchToken)
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

// Outcome is the stored class of a finished command; null while it is live.
func (r *CommandResolver) Outcome() *string {
	return util.NullStr(r.M.Outcome)
}

// DeliveredTime is when the first piece of delivery evidence was recorded; null until then.
func (r *CommandResolver) DeliveredTime() *string {
	if r.M.DeliveredTime.Valid {
		return util.FormatTime(r.M.DeliveredTime.Time)
	}
	return nil
}

// DeliveryEvidence is what DeliveredTime rests on; null until there is any.
func (r *CommandResolver) DeliveryEvidence() *string {
	return util.NullStr(r.M.DeliveryEvidence)
}

// DeliveryMode is ONEWAY for a command the device only acknowledges; null otherwise.
func (r *CommandResolver) DeliveryMode() *string {
	return util.NullStr(r.M.DeliveryMode)
}

// MaxAttempts is the pull channel's bound on hand-outs; null for the platform default.
func (r *CommandResolver) MaxAttempts() *int32 {
	if r.M.MaxAttempts.Valid {
		return &r.M.MaxAttempts.Int32
	}
	return nil
}

// DeliveryAttempts is how many times the pull channel has handed the command out.
func (r *CommandResolver) DeliveryAttempts() *int32 {
	if r.M.DeliveryAttempts.Valid {
		return &r.M.DeliveryAttempts.Int32
	}
	return nil
}

// PulledCommandResolver exposes one command a pull handed to a device.
type PulledCommandResolver struct{ M *model.PulledCommand }

func (r *PulledCommandResolver) Token() string { return r.M.Command.Token }
func (r *PulledCommandResolver) Name() string  { return r.M.Command.Name }
func (r *PulledCommandResolver) Payload() *string {
	return util.MetadataStr(r.M.Command.Payload)
}
func (r *PulledCommandResolver) DeliveryMode() *string {
	return util.NullStr(r.M.Command.DeliveryMode)
}
func (r *PulledCommandResolver) Attempt() int32 { return int32(r.M.Attempt) }
func (r *PulledCommandResolver) ExpiresAt() *string {
	if r.M.Command.ExpiresAt.Valid {
		return util.FormatTime(r.M.Command.ExpiresAt.Time)
	}
	return nil
}

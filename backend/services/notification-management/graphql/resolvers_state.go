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

// NotificationStateResolver resolves the per-alarm notification/escalation state.
// This is a read-only surface in this slice (N.B): the dispatcher (N.C) writes the
// notified timestamps and the escalation scheduler (N.D) the escalation fields.
type NotificationStateResolver struct {
	M model.NotificationState
	S *SchemaResolver
	C context.Context
}

func (r *NotificationStateResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *NotificationStateResolver) AlarmToken() string { return r.M.AlarmToken }

func (r *NotificationStateResolver) AlarmKey() string { return r.M.AlarmKey }

func (r *NotificationStateResolver) Severity() string { return r.M.Severity }

func (r *NotificationStateResolver) FirstNotifiedAt() *string {
	return util.FormatTime(r.M.FirstNotifiedAt.Time)
}

func (r *NotificationStateResolver) LastNotifiedAt() *string {
	return util.FormatTime(r.M.LastNotifiedAt.Time)
}

func (r *NotificationStateResolver) NotifyCount() int32 { return int32(r.M.NotifyCount) }

func (r *NotificationStateResolver) AcknowledgedAt() *string {
	return util.FormatTime(r.M.AcknowledgedAt.Time)
}

func (r *NotificationStateResolver) ClearedAt() *string {
	return util.FormatTime(r.M.ClearedAt.Time)
}

func (r *NotificationStateResolver) EscalationLevel() int32 { return int32(r.M.EscalationLevel) }

func (r *NotificationStateResolver) LastEscalatedAt() *string {
	return util.FormatTime(r.M.LastEscalatedAt.Time)
}

// NotificationStateSearchResultsResolver resolves a page of notification states.
type NotificationStateSearchResultsResolver struct {
	M model.NotificationStateSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *NotificationStateSearchResultsResolver) Results() []*NotificationStateResolver {
	resolvers := make([]*NotificationStateResolver, 0, len(r.M.Results))
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &NotificationStateResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *NotificationStateSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}

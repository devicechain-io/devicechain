// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-notification-management/model"
	gql "github.com/graph-gophers/graphql-go"
)

// NotificationAttemptResolver resolves one delivery-ledger row.
type NotificationAttemptResolver struct {
	M model.NotificationAttempt
	S *SchemaResolver
	C context.Context
}

func (r *NotificationAttemptResolver) Id() gql.ID { return gql.ID(fmt.Sprint(r.M.ID)) }

func (r *NotificationAttemptResolver) AttemptedAt() string {
	return r.M.CreatedAt.UTC().Format(time.RFC3339Nano)
}

func (r *NotificationAttemptResolver) AlarmToken() string { return r.M.AlarmToken }

func (r *NotificationAttemptResolver) Trigger() string { return r.M.Trigger }

func (r *NotificationAttemptResolver) PolicyToken() *string { return util.NullStr(r.M.PolicyToken) }

func (r *NotificationAttemptResolver) ChannelToken() string { return r.M.ChannelToken }

func (r *NotificationAttemptResolver) ChannelType() string { return r.M.ChannelType }

// Recipients decodes the stored JSON array. A row whose recipients could not be encoded
// reads as an empty list rather than failing the whole page.
func (r *NotificationAttemptResolver) Recipients() []string {
	out := make([]string, 0)
	if len(r.M.Recipients) > 0 {
		if err := json.Unmarshal(r.M.Recipients, &out); err != nil {
			return []string{}
		}
	}
	return out
}

func (r *NotificationAttemptResolver) SubjectHash() *string {
	if r.M.SubjectHash == "" {
		return nil
	}
	v := r.M.SubjectHash
	return &v
}

func (r *NotificationAttemptResolver) Status() string { return r.M.Status }

func (r *NotificationAttemptResolver) Attempt() int32 { return clampInt32(int64(r.M.Attempt)) }

func (r *NotificationAttemptResolver) MaxAttempts() int32 { return clampInt32(int64(r.M.MaxAttempts)) }

func (r *NotificationAttemptResolver) LatencyMillis() *int32 {
	if !r.M.LatencyMillis.Valid {
		return nil
	}
	v := clampInt32(r.M.LatencyMillis.Int64)
	return &v
}

func (r *NotificationAttemptResolver) ErrorCode() *string { return util.NullStr(r.M.ErrorCode) }

func (r *NotificationAttemptResolver) ErrorMessage() *string { return util.NullStr(r.M.ErrorMessage) }

// clampInt32 narrows a stored count to GraphQL's Int without wrapping.
func clampInt32(v int64) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}

// NotificationAttemptSearchResultsResolver resolves a page of ledger rows.
type NotificationAttemptSearchResultsResolver struct {
	M model.NotificationAttemptSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *NotificationAttemptSearchResultsResolver) Results() []*NotificationAttemptResolver {
	out := make([]*NotificationAttemptResolver, 0, len(r.M.Results))
	for _, current := range r.M.Results {
		out = append(out, &NotificationAttemptResolver{M: current, S: r.S, C: r.C})
	}
	return out
}

func (r *NotificationAttemptSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}

// NotificationChannelHealthResolver resolves a channel's recent delivery record.
type NotificationChannelHealthResolver struct {
	M model.ChannelHealth
}

func (r *NotificationChannelHealthResolver) LastSuccessAt() *string {
	return util.FormatTime(r.M.LastSuccessAt.Time)
}

func (r *NotificationChannelHealthResolver) LastFailureAt() *string {
	return util.FormatTime(r.M.LastFailureAt.Time)
}

func (r *NotificationChannelHealthResolver) ConsecutiveFailures() int32 {
	return clampInt32(r.M.ConsecutiveFailures)
}

// DeliveryHealth reads the channel's recent delivery record from the ledger. It checks
// notification:read itself rather than relying on the operation that returned the
// channel, because a channel is also returned by mutations, which require a different
// authority.
func (r *NotificationChannelResolver) DeliveryHealth() (*NotificationChannelHealthResolver, error) {
	if err := auth.Authorize(r.C, auth.NotificationRead); err != nil {
		return nil, err
	}
	health, err := r.S.GetApi(r.C).ChannelHealthByToken(r.C, r.M.Token)
	if err != nil {
		return nil, err
	}
	return &NotificationChannelHealthResolver{M: *health}, nil
}

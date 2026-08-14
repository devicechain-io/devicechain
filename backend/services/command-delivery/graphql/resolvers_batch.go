// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/devicechain-io/dc-command-delivery/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
	"gorm.io/datatypes"
)

// ---------------------------------
// Batch refusal + count resolvers
// ---------------------------------

// BatchDeviceRefusalResolver names one device a batch did not enqueue to, and why.
type BatchDeviceRefusalResolver struct {
	M model.BatchDeviceRefusal
}

func (r *BatchDeviceRefusalResolver) DeviceToken() string { return r.M.DeviceToken }

func (r *BatchDeviceRefusalResolver) Code() string { return string(r.M.Code) }

func (r *BatchDeviceRefusalResolver) Reason() string { return r.M.Reason }

// BatchRefusalCountResolver is the COMPLETE total for one refusal code.
type BatchRefusalCountResolver struct {
	M model.RefusalCount
}

func (r *BatchRefusalCountResolver) Code() string { return string(r.M.Code) }

func (r *BatchRefusalCountResolver) Count() int32 { return int32(r.M.Count) }

// refusalResolvers wraps a decoded refusal sample.
func refusalResolvers(refusals []model.BatchDeviceRefusal) []*BatchDeviceRefusalResolver {
	resolved := make([]*BatchDeviceRefusalResolver, 0, len(refusals))
	for _, refusal := range refusals {
		resolved = append(resolved, &BatchDeviceRefusalResolver{M: refusal})
	}
	return resolved
}

// refusalCountResolvers wraps decoded per-code totals.
func refusalCountResolvers(counts []model.RefusalCount) []*BatchRefusalCountResolver {
	resolved := make([]*BatchRefusalCountResolver, 0, len(counts))
	for _, count := range counts {
		resolved = append(resolved, &BatchRefusalCountResolver{M: count})
	}
	return resolved
}

// decodeStoredJSON reads one of the batch record's JSON columns back into a typed value.
//
// 🔴 A DECODE FAILURE IS RETURNED AS AN ERROR, NOT AS AN EMPTY LIST. These columns are
// written by summarizeRefusals from plain structs, so malformed content means the row was
// corrupted or hand-edited — and answering that with `[]` would report "nothing was
// refused", which is the single most misleading answer available: it is indistinguishable
// from a clean fleet write, and it breaks the `resolved = accepted + refusals` arithmetic
// that makes the record self-auditing. A field error leaves the rest of the batch readable
// and says plainly that this part could not be.
func decodeStoredJSON[T any](raw *datatypes.JSON, field string) ([]T, error) {
	decoded := make([]T, 0)
	if raw == nil {
		return decoded, nil
	}
	if err := json.Unmarshal(*raw, &decoded); err != nil {
		return nil, fmt.Errorf("command batch %s could not be read back", field)
	}
	return decoded, nil
}

// ----------------------
// Command batch resolver
// ----------------------

type CommandBatchResolver struct {
	M model.CommandBatch
	S *SchemaResolver
	C context.Context
}

func (r *CommandBatchResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *CommandBatchResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *CommandBatchResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *CommandBatchResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *CommandBatchResolver) Token() string {
	return r.M.Token
}

func (r *CommandBatchResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *CommandBatchResolver) Name() string {
	return r.M.Name
}

func (r *CommandBatchResolver) Payload() *string {
	return util.MetadataStr(r.M.Payload)
}

func (r *CommandBatchResolver) TargetKind() string {
	return r.M.TargetKind
}

func (r *CommandBatchResolver) GroupToken() *string {
	return util.NullStr(r.M.GroupToken)
}

// The FROZEN group version this batch resolved against; null for a device-list batch or
// a static group. It is what lets an audit answer what the group meant when this fired.
func (r *CommandBatchResolver) GroupVersion() *int32 {
	if !r.M.GroupVersion.Valid {
		return nil
	}
	version := r.M.GroupVersion.Int32
	return &version
}

func (r *CommandBatchResolver) AllowPartial() bool {
	return r.M.AllowPartial
}

func (r *CommandBatchResolver) Resolved() int32 {
	return int32(r.M.Resolved)
}

func (r *CommandBatchResolver) Accepted() int32 {
	return int32(r.M.Accepted)
}

// When this batch was called off, if it was.
func (r *CommandBatchResolver) CancelledAt() *string {
	if !r.M.CancelledAt.Valid {
		return nil
	}
	return util.FormatTime(r.M.CancelledAt.Time)
}

// How many commands the cancelling call caught. Deliberately NOT a live count of the
// batch's cancelled commands — see the field's own documentation in the schema.
func (r *CommandBatchResolver) CancelledCount() *int32 {
	if !r.M.CancelledCount.Valid {
		return nil
	}
	count := r.M.CancelledCount.Int32
	return &count
}

func (r *CommandBatchResolver) Refusals() ([]*BatchDeviceRefusalResolver, error) {
	refusals, err := decodeStoredJSON[model.BatchDeviceRefusal](r.M.Refusals, "refusals")
	if err != nil {
		return nil, err
	}
	return refusalResolvers(refusals), nil
}

func (r *CommandBatchResolver) RefusalCounts() ([]*BatchRefusalCountResolver, error) {
	counts, err := decodeStoredJSON[model.RefusalCount](r.M.RefusalCounts, "refusalCounts")
	if err != nil {
		return nil, err
	}
	return refusalCountResolvers(counts), nil
}

// -------------------------------------
// Command batch search results resolver
// -------------------------------------

type CommandBatchSearchResultsResolver struct {
	M model.CommandBatchSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *CommandBatchSearchResultsResolver) Results() []*CommandBatchResolver {
	resolvers := make([]*CommandBatchResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &CommandBatchResolver{
			M: current,
			S: r.S,
			C: r.C,
		})
	}
	return resolvers
}

func (r *CommandBatchSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

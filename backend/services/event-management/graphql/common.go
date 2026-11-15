// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/rdb"
)

type SearchResultsPaginationResolver struct {
	M rdb.SearchResultsPagination
	S *SchemaResolver
	C context.Context
}

func (r *SearchResultsPaginationResolver) PageStart() *int32 {
	return &r.M.PageStart
}

func (r *SearchResultsPaginationResolver) PageEnd() *int32 {
	return &r.M.PageEnd
}

func (r *SearchResultsPaginationResolver) TotalRecords() *int32 {
	if r.M.TotalRecordsAbsent {
		return nil
	}
	return &r.M.TotalRecords
}

// HasMore also supplies pagination for non-event lists using their exact total.
func (r *SearchResultsPaginationResolver) HasMore() *bool {
	if r.M.HasMore != nil {
		return r.M.HasMore
	}
	if r.M.TotalRecordsAbsent {
		return nil
	}
	// A wrapped (negative) pageEnd from a huge page number is past the end, not before it.
	more := r.M.PageEnd >= 0 && r.M.PageEnd < r.M.TotalRecords
	return &more
}

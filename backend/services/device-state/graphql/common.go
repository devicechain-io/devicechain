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
	return &r.M.TotalRecords
}

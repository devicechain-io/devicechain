// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// CommandBatchesById gets command batches by id.
//
// 🔴 THE EMPTY-SLICE GUARD IS NOT DEFENSIVE PADDING — WITHOUT IT THIS RETURNS THE WHOLE
// TABLE. gorm's inline-condition form drops an empty id slice entirely rather than
// rendering an impossible predicate, so `Find(&found, []uint{})` is an unfiltered SELECT
// with no pagination: asking for NO batches answers with every batch the tenant has.
// Measured, not assumed — the same is true of CommandsById, which this mirrors.
//
// Zero ids means zero rows. That is both the honest answer and the safe one.
func (api *Api) CommandBatchesById(ctx context.Context, ids []uint) ([]*CommandBatch, error) {
	found := make([]*CommandBatch, 0)
	if len(ids) == 0 {
		return found, nil
	}
	result := api.RDB.DB(ctx).Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// CommandBatchesByToken gets command batches by token.
//
// The empty case is guarded here too, though `token in ?` with an empty slice already
// renders a predicate that matches nothing. The guard is for the READER: the two lookups
// sit side by side and behave oppositely on the same input, and the one that is safe is
// safe by accident of how gorm renders IN rather than by anything visible here.
func (api *Api) CommandBatchesByToken(ctx context.Context, tokens []string) ([]*CommandBatch, error) {
	found := make([]*CommandBatch, 0)
	if len(tokens) == 0 {
		return found, nil
	}
	result := api.RDB.DB(ctx).Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// CommandBatchSearchCriteria is the search criteria for locating command batches.
//
// The filters are the three questions an operator actually arrives with after a fleet
// write: what did we send (Name), where did we send it (GroupToken), and how was the
// target named (TargetKind). There is deliberately no filter on the OUTCOME — "show me
// the batches that were partially refused" — because that is a derived question the
// stored counts already answer, and a column for it would be a fourth place the
// resolved/accepted arithmetic has to stay consistent with.
type CommandBatchSearchCriteria struct {
	rdb.Pagination
	// Name matches the command key the batch fanned out, exactly.
	Name *string
	// GroupToken matches batches fired at one entity group. It matches nothing for
	// device-list batches, which carry no group.
	GroupToken *string
	// TargetKind is DEVICE_LIST or GROUP. An unrecognized value matches nothing rather
	// than being rejected: the column is a stored string, and a search that errors on an
	// unknown filter value is harder to use than one that returns no rows.
	TargetKind *string
}

// CommandBatchSearchResults wraps a page of command batches.
type CommandBatchSearchResults struct {
	Results    []CommandBatch
	Pagination rdb.SearchResultsPagination
}

// CommandBatches searches for command batches matching the given criteria.
func (api *Api) CommandBatches(ctx context.Context, criteria CommandBatchSearchCriteria) (*CommandBatchSearchResults, error) {
	results := make([]CommandBatch, 0)
	db, pag := api.RDB.ListOf(ctx, &CommandBatch{}, func(db *gorm.DB) *gorm.DB {
		if criteria.Name != nil {
			db = db.Where("name = ?", *criteria.Name)
		}
		if criteria.GroupToken != nil {
			db = db.Where("group_token = ?", *criteria.GroupToken)
		}
		if criteria.TargetKind != nil {
			db = db.Where("target_kind = ?", *criteria.TargetKind)
		}
		return db
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &CommandBatchSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// maxDefinitionBytes caps a stored dashboard definition. A definition is a layout
// document (schemaVersion + canvas + widget list), not a blob — image/background
// widgets carry URLs, not embedded data — so 1 MiB is already generous. The cap
// keeps a single tenant from exhausting shared storage with an oversized document.
const maxDefinitionBytes = 1 << 20

// ErrInvalidDefinition is returned when a create/update carries a Definition that
// is not a well-formed JSON object. The document is otherwise stored opaquely.
var ErrInvalidDefinition = errors.New("dashboard definition must be a JSON object")

// ErrDefinitionTooLarge is returned when a Definition exceeds maxDefinitionBytes.
var ErrDefinitionTooLarge = errors.New("dashboard definition exceeds the maximum size")

type Api struct {
	RDB *rdb.RdbManager
}

// NewApi creates a new API instance around the given rdb manager.
func NewApi(rdb *rdb.RdbManager) *Api {
	return &Api{RDB: rdb}
}

// definitionJSON validates that raw is a well-formed, size-bounded JSON object
// and returns it as a datatypes.JSON column value. Unlike the registry Metadata
// helper (which drops invalid input silently) a bad definition is rejected — a
// dashboard with a corrupt document is a client bug, not a value to swallow. The
// object requirement rejects well-formed-but-nonsense scalars ("42", true, an
// array) that would only fail later at render time. The backend still treats the
// document's *contents* opaquely; the @devicechain/dashboards types own the shape.
func definitionJSON(raw string) (datatypes.JSON, error) {
	b := []byte(raw)
	// Length-check before parsing so an oversized payload can't cost a full scan.
	if len(b) > maxDefinitionBytes {
		return nil, ErrDefinitionTooLarge
	}
	if !json.Valid(b) {
		return nil, ErrInvalidDefinition
	}
	if trimmed := bytes.TrimSpace(b); len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, ErrInvalidDefinition
	}
	return datatypes.JSON(b), nil
}

// CreateDashboard inserts a new dashboard definition.
func (api *Api) CreateDashboard(ctx context.Context, request *DashboardCreateRequest) (*Dashboard, error) {
	def, err := definitionJSON(request.Definition)
	if err != nil {
		return nil, err
	}

	created := &Dashboard{
		TokenReference: rdb.TokenReference{Token: request.Token},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		Definition: def,
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// UpdateDashboard updates the dashboard with the given (current) token.
func (api *Api) UpdateDashboard(ctx context.Context, token string, request *DashboardCreateRequest) (*Dashboard, error) {
	def, err := definitionJSON(request.Definition)
	if err != nil {
		return nil, err
	}

	matches, err := api.DashboardsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	updated := matches[0]
	updated.Token = request.Token
	updated.Name = rdb.NullStrOf(request.Name)
	updated.Description = rdb.NullStrOf(request.Description)
	updated.Definition = def

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// DashboardsByToken looks up dashboards by their current tokens.
func (api *Api) DashboardsByToken(ctx context.Context, tokens []string) ([]*Dashboard, error) {
	found := make([]*Dashboard, 0)
	result := api.RDB.DB(ctx).Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Dashboards searches dashboards by criteria (name substring + pagination).
func (api *Api) Dashboards(ctx context.Context, criteria DashboardSearchCriteria) (*DashboardSearchResults, error) {
	results := make([]Dashboard, 0)
	db, pag := api.RDB.ListOf(ctx, &Dashboard{}, func(result *gorm.DB) *gorm.DB {
		if criteria.Name != nil {
			result = result.Where("name LIKE ?", "%"+*criteria.Name+"%")
		}
		return result
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	return &DashboardSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// DeleteDashboard hard-deletes the dashboard with the given token. It reports
// whether a row was deleted (false when no dashboard has that token). The
// tenant-scope callback confines the delete to the caller's tenant.
//
// The delete is Unscoped (a real DELETE, not a soft-delete). A dashboard has no
// trash/restore semantics, and — critically — the token unique index does not
// exclude soft-deleted rows, so a soft-delete would lock the token forever and a
// later create of the same token would fail with a duplicate-key error. Hard
// delete frees the token immediately. (The platform-wide fix — a per-tenant
// partial unique index that ignores soft-deleted rows — is tracked separately in
// the "entity addressing & token policy" ADR.)
func (api *Api) DeleteDashboard(ctx context.Context, token string) (bool, error) {
	result := api.RDB.DB(ctx).Unscoped().Where("token = ?", token).Delete(&Dashboard{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

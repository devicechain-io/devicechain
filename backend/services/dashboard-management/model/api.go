// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
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

// ErrConflict is returned by UpdateDashboard when the caller passes the version it
// edited (expectedUpdatedAt) and the row has moved on since — a concurrent edit
// (a second tab / another writer). The caller should reload and re-apply.
var ErrConflict = errors.New("dashboard was modified by another writer; reload and try again")

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

// UpdateDashboard applies a PARTIAL update to the dashboard (the mutable draft) with
// the given token: a field the request omits is left alone, a field it sends is written,
// and an explicit null clears it — except definition, which refuses one (see below).
//
// When expectedUpdatedAt is non-nil it is an optimistic-concurrency precondition: the
// save is rejected with ErrConflict if the row's current UpdatedAt no longer matches,
// i.e. another writer changed it since the caller loaded it.
func (api *Api) UpdateDashboard(ctx context.Context, token string, request *DashboardUpdateRequest, expectedUpdatedAt *string) (*Dashboard, error) {
	matches, err := api.DashboardsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	current := matches[0]

	// 🔴 EVERY FOLD AND EVERY VALIDATION HAPPENS BEFORE THE FIRST WRITE, and that
	// ordering is the contract rather than a tidiness preference. A request carrying a
	// rename AND a malformed definition must be refused WHOLE: applying the name and
	// then failing on the document would leave a caller who retries having already
	// half-applied their first attempt, with nothing anywhere to say so.
	//
	// The assignment map is built from the fields the request actually NAMED, which is
	// what makes this a partial update at the SQL level and not merely at the caller's:
	// a column nobody mentioned is not in the statement at all, so it cannot be written
	// from a stale read.
	//
	// The folds run BEFORE the precondition check on purpose. A malformed request is
	// malformed whoever else is writing, so reporting it as a conflict would send the
	// caller off to reload and retry a request that can never succeed.
	assignments := map[string]any{}
	if request.Name.Set {
		current.Name = rdb.NullStrOf(request.Name.ApplyTo(dcgraphql.NullStr(current.Name)))
		assignments["name"] = current.Name
	}
	if request.Description.Set {
		current.Description = rdb.NullStrOf(request.Description.ApplyTo(dcgraphql.NullStr(current.Description)))
		assignments["description"] = current.Description
	}
	// 🔴 definition TAKES ApplyToRequired, SO AN EXPLICIT NULL IS REFUSED. The column is
	// NOT NULL and the document is a dashboard's entire content (opaque versioned JSON),
	// so there is no reading of "clear it" that leaves a dashboard behind — folding the
	// null to "" would store a document nothing can render, and report success.
	//
	// Validation is gated on Set for the same reason the fold is: definitionJSON is a
	// check on what the CALLER SENT. Re-running it over the stored document on every
	// update would make a dashboard that predates a tightening of that check unsavable —
	// its owner could no longer even rename it.
	rawDefinition, err := request.Definition.ApplyToRequired("definition", string(current.Definition))
	if err != nil {
		return nil, err
	}
	if request.Definition.Set {
		def, derr := definitionJSON(rawDefinition)
		if derr != nil {
			return nil, derr
		}
		current.Definition = def
		assignments["definition"] = def
	}

	// 🔴 AN UPDATE THAT NAMES NOTHING WRITES NOTHING — INCLUDING updated_at.
	//
	// It is not a degenerate case to shrug at, because that timestamp is a SHARED
	// precondition: bumping it here would invalidate every other editor's baseline on
	// behalf of a caller who asked for no change at all, and the second tab would be
	// told it had a conflict with a write that wrote nothing.
	//
	// Under a precondition the check below still runs first, so a STALE one is a
	// conflict whether or not any field was named — an empty update is not a way to be
	// told "success" about a row that has moved on. What is returned is the row AS READ,
	// whose UpdatedAt equals the precondition the caller supplied; reloading instead
	// would hand a caller who wrote nothing a baseline advanced past a concurrent
	// writer's content they have never seen.
	if expectedUpdatedAt != nil {
		// The clean early-out against the caller's stated version — the exact string the
		// caller was handed by core/graphql.FormatTime, so the layout must match it.
		//
		// 🔴 This used to say "RFC3339 second precision" as though the coarseness were
		// part of the contract. It was not — the guarded write re-reads updated_at, so
		// this comparison is the only enforcement of the CALLER's version, and
		// truncating it to the second let a client whose view was stale by under a
		// second publish over a change it had never seen.
		if current.UpdatedAt.Format(time.RFC3339Nano) != *expectedUpdatedAt {
			return nil, ErrConflict
		}
	}
	if len(assignments) == 0 {
		return current, nil
	}

	// The write. With a precondition it is ATOMIC and guarded: UPDATE ... WHERE
	// updated_at = <the value just read>, so a concurrent save slipping in between the
	// read and this write moves updated_at and matches zero rows (RowsAffected == 0)
	// instead of being silently clobbered. Without one it is unconditional
	// last-write-wins (backward-compatible; used by non-interactive callers that don't
	// track a version), and zero rows can then only mean the row was deleted since the
	// read.
	write := api.RDB.DB(ctx).Model(&Dashboard{}).Where("id = ?", current.ID)
	if expectedUpdatedAt != nil {
		write = write.Where("updated_at = ?", current.UpdatedAt)
	}
	res := write.Updates(assignments)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		if expectedUpdatedAt != nil {
			return nil, ErrConflict
		}
		return nil, gorm.ErrRecordNotFound
	}

	// Reload for the freshly-bumped updated_at — the caller advances its precondition
	// baseline from the returned value. Reloaded by the ARGUMENT, which is the only
	// thing that names the row: the update input carries no token at all.
	reloaded, err := api.DashboardsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(reloaded) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return reloaded[0], nil
}

// PublishDashboard freezes the dashboard's current draft into a new immutable
// version (the next monotonic integer for that dashboard) and returns it. label
// and description are optional user annotations; publishedBy is the caller's
// identity. Concurrent publishes are safe: the unique (dashboard_id, version)
// index rejects a duplicate version number.
func (api *Api) PublishDashboard(ctx context.Context, token string, label *string, description *string, publishedBy string, expectedUpdatedAt *string) (*DashboardVersion, error) {
	matches, err := api.DashboardsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	dash := matches[0]

	// Optimistic precondition (same contract as UpdateDashboard): refuse to freeze a
	// draft that moved on since the caller loaded it — otherwise publish could snapshot
	// another writer's content while the author believes they froze their own view.
	// Same layout coupling as UpdateDashboard — see the comment there.
	if expectedUpdatedAt != nil && dash.UpdatedAt.Format(time.RFC3339Nano) != *expectedUpdatedAt {
		return nil, ErrConflict
	}

	// Next version = max existing + 1 for this dashboard (tenant-confined already,
	// both because dash was loaded tenant-scoped and via the scope callback here).
	var maxVersion int32
	if err := api.RDB.DB(ctx).Model(&DashboardVersion{}).
		Where("dashboard_id = ?", dash.ID).
		Select("COALESCE(MAX(version), 0)").Scan(&maxVersion).Error; err != nil {
		return nil, err
	}

	version := &DashboardVersion{
		DashboardID: dash.ID,
		Version:     maxVersion + 1,
		Label:       rdb.NullStrOf(label),
		Description: rdb.NullStrOf(description),
		Definition:  dash.Definition,
		PublishedBy: publishedBy,
	}
	if err := api.RDB.DB(ctx).Create(version).Error; err != nil {
		return nil, err
	}
	return version, nil
}

// RollbackDashboard copies a published version's definition back into the draft
// (the parent Dashboard row), returning the updated dashboard. History is
// append-only — no version is deleted; the caller may edit and re-publish. Returns
// gorm.ErrRecordNotFound if the dashboard or the version does not exist.
func (api *Api) RollbackDashboard(ctx context.Context, token string, version int32) (*Dashboard, error) {
	matches, err := api.DashboardsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	dash := matches[0]

	var snapshot DashboardVersion
	if err := api.RDB.DB(ctx).
		Where("dashboard_id = ? AND version = ?", dash.ID, version).
		First(&snapshot).Error; err != nil {
		return nil, err
	}

	dash.Definition = snapshot.Definition
	if err := api.RDB.DB(ctx).Save(dash).Error; err != nil {
		return nil, err
	}
	return dash, nil
}

// DashboardVersions lists a dashboard's published versions, newest first. Returns
// gorm.ErrRecordNotFound if the dashboard does not exist.
func (api *Api) DashboardVersions(ctx context.Context, token string) ([]*DashboardVersion, error) {
	matches, err := api.DashboardsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	dash := matches[0]

	versions := make([]*DashboardVersion, 0)
	if err := api.RDB.DB(ctx).
		Where("dashboard_id = ?", dash.ID).
		Order("version DESC").Find(&versions).Error; err != nil {
		return nil, err
	}
	return versions, nil
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
	// Resolve the dashboard first (tenant-scoped) so we can drop its version history
	// too — DashboardVersion.DashboardID is a plain column with no FK cascade, so a
	// bare dashboard delete would orphan every snapshot (up to 1 MiB each) forever.
	matches, err := api.DashboardsByToken(ctx, []string{token})
	if err != nil {
		return false, err
	}
	if len(matches) == 0 {
		return false, nil
	}
	dashboardID := matches[0].ID

	// Delete the versions and the dashboard atomically so a delete can't half-succeed
	// and orphan rows. Hard deletes (Unscoped): a dashboard has no trash/restore, and
	// the token unique index counts soft-deleted rows (see the rationale above); the
	// tenant-scope callback still confines both statements to the caller's tenant.
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Where("dashboard_id = ?", dashboardID).Delete(&DashboardVersion{}).Error; err != nil {
			return err
		}
		return tx.Unscoped().Where("token = ?", token).Delete(&Dashboard{}).Error
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

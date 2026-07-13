// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// maxConfigBytes caps a stored connector config. A config is a small connection
// document (broker URL, topic, region, …), not a blob, so 64 KiB is already generous.
// The cap keeps a single tenant from exhausting shared storage with an oversized
// document and bounds the JSON validation cost.
const maxConfigBytes = 64 << 10

// ErrInvalidConfig is returned when a create/update carries a Config that is not a
// well-formed JSON object. The document is otherwise stored opaquely (per-type field
// validation lives with each output generator, slices C4b/C4c).
var ErrInvalidConfig = errors.New("connector config must be a JSON object")

// ErrConfigTooLarge is returned when a Config exceeds maxConfigBytes.
var ErrConfigTooLarge = errors.New("connector config exceeds the maximum size")

// ErrConflict is returned by UpdateConnector/PublishConnector when the caller passes
// the version it edited (expectedUpdatedAt) and the row has moved on since — a
// concurrent edit (a second tab / another writer). The caller should reload and retry.
var ErrConflict = errors.New("connector was modified by another writer; reload and try again")

// Api is the outbound-connectors persistence surface: the versioned Connector entity
// plus its write-only credential (sealed in the ADR-059 secret store). Secrets is
// required — a connector's credential is never a column.
type Api struct {
	RDB     *rdb.RdbManager
	Secrets secrets.SecretStore
}

// NewApi creates a new API instance around the rdb manager and secret store.
func NewApi(rdb *rdb.RdbManager, store secrets.SecretStore) *Api {
	return &Api{RDB: rdb, Secrets: store}
}

// configJSON validates that raw is a well-formed, size-bounded JSON object and returns
// it as a datatypes.JSON column value. A bad config is rejected (not swallowed) — a
// connector with a corrupt document is a client bug. The object requirement rejects
// well-formed-but-nonsense scalars ("42", true, an array) that would only fail later
// at send time. The backend still treats the contents opaquely at this layer.
func configJSON(raw string) (datatypes.JSON, error) {
	b := []byte(raw)
	// Length-check before parsing so an oversized payload can't cost a full scan.
	if len(b) > maxConfigBytes {
		return nil, ErrConfigTooLarge
	}
	if !json.Valid(b) {
		return nil, ErrInvalidConfig
	}
	if trimmed := bytes.TrimSpace(b); len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, ErrInvalidConfig
	}
	return datatypes.JSON(b), nil
}

// validateRequest validates a create/update request's type + config and returns the config
// as a column value. The type must be in the registered vocabulary and the config a
// well-formed JSON object; additionally, for a type whose Bento generator has shipped, the
// per-type field shape is validated here (fail early at write, not only at dispatch). A
// vocabulary type without a shipped generator yet is accepted as JSON-object-only and
// dead-letters at dispatch until its generator lands (slice C4c) — never silently.
func (api *Api) validateRequest(request *ConnectorCreateRequest) (datatypes.JSON, error) {
	if err := validateConnectorType(request.Type); err != nil {
		return nil, err
	}
	cfg, err := configJSON(request.Config)
	if err != nil {
		return nil, err
	}
	if connectorspec.Supported(request.Type) {
		if err := connectorspec.ValidateConfig(request.Type, cfg); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// CreateConnector inserts a new connector (draft). The type must be in the registered
// vocabulary and the config a well-formed JSON object; a non-empty request.Secret is
// sealed into the secret store under the connector's handle (never a column).
func (api *Api) CreateConnector(ctx context.Context, request *ConnectorCreateRequest) (*Connector, error) {
	cfg, err := api.validateRequest(request)
	if err != nil {
		return nil, err
	}

	created := &Connector{
		TokenReference: rdb.TokenReference{Token: request.Token},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		Type:   request.Type,
		Config: cfg,
	}
	if err := api.RDB.DB(ctx).Create(created).Error; err != nil {
		return nil, err
	}
	// Seal the credential under the connector's handle. The row is written first so
	// its immutable id (the secret's stable key) exists; the secret is a separate
	// write to the store (same DB, not one transaction).
	if err := api.applyConnectorSecret(ctx, created.ID, request.Secret); err != nil {
		// The row committed but sealing the secret failed. Roll the row back (best
		// effort) so the create is atomic from the caller's view — otherwise a retry
		// would collide on the now-existing token. A cleanup failure is logged, not
		// masked; the original secret error is what the caller needs.
		if delErr := api.RDB.DB(ctx).Unscoped().Delete(created).Error; delErr != nil {
			log.Warn().Err(delErr).Str("token", request.Token).
				Msg("Failed to roll back connector row after secret write failure; connector may exist without a secret")
		}
		return nil, err
	}
	return created, nil
}

// UpdateConnector updates the connector (draft) with the given (current) token. The
// secret is write-only, so a nil request.Secret preserves the stored secret (the
// caller cannot read it back to resend it); a non-nil value replaces it, and an
// explicit empty string clears it. Every other field is fully replaced.
//
// When expectedUpdatedAt is non-nil it is an optimistic-concurrency precondition (same
// contract as the dashboard precedent): the save is rejected with ErrConflict if the
// row's current UpdatedAt no longer matches — another writer changed it since the
// caller loaded it. The comparison uses RFC3339 (second precision), the exact string
// the caller was handed by the `updatedAt` query field, so a value that round-trips
// unchanged always matches.
func (api *Api) UpdateConnector(ctx context.Context, token string, request *ConnectorCreateRequest, expectedUpdatedAt *string) (*Connector, error) {
	cfg, err := api.validateRequest(request)
	if err != nil {
		return nil, err
	}

	matches, err := api.ConnectorsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	current := matches[0]

	// No precondition → unconditional last-write-wins (non-interactive callers that
	// don't track a version).
	if expectedUpdatedAt == nil {
		current.Token = request.Token
		current.Name = rdb.NullStrOf(request.Name)
		current.Description = rdb.NullStrOf(request.Description)
		current.Type = request.Type
		current.Config = cfg
		if err := api.RDB.DB(ctx).Save(current).Error; err != nil {
			return nil, err
		}
		return api.applyUpdatedSecret(ctx, current, request.Secret)
	}

	// Optimistic concurrency: a clean early-out against the caller's stated version,
	// then an ATOMIC guarded write (UPDATE ... WHERE updated_at = <the value just read>)
	// so a concurrent save slipping in between the read and this write moves updated_at
	// and matches zero rows instead of being silently clobbered.
	if current.UpdatedAt.Format(time.RFC3339) != *expectedUpdatedAt {
		return nil, ErrConflict
	}
	res := api.RDB.DB(ctx).Model(&Connector{}).
		Where("id = ? AND updated_at = ?", current.ID, current.UpdatedAt).
		Updates(map[string]any{
			"token":       request.Token,
			"name":        rdb.NullStrOf(request.Name),
			"description": rdb.NullStrOf(request.Description),
			"type":        request.Type,
			"config":      cfg,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrConflict
	}

	// Reload for the freshly-bumped updated_at — the caller advances its precondition
	// baseline from the returned value.
	reloaded, err := api.ConnectorsByToken(ctx, []string{request.Token})
	if err != nil {
		return nil, err
	}
	if len(reloaded) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return api.applyUpdatedSecret(ctx, reloaded[0], request.Secret)
}

// applyUpdatedSecret applies the write-only secret to conn per the request (preserve on
// nil), keyed by the connector's immutable id (so a token rename in the same update
// keeps the existing secret bound), and returns conn for the caller.
func (api *Api) applyUpdatedSecret(ctx context.Context, conn *Connector, secret *string) (*Connector, error) {
	if secret != nil {
		if err := api.applyConnectorSecret(ctx, conn.ID, secret); err != nil {
			return nil, err
		}
	}
	return conn, nil
}

// applyConnectorSecret writes the connector's credential to the store to match the
// request: a non-empty value is sealed (Put), an explicit empty string clears it
// (Delete, idempotent). A nil secret is a caller decision made above (preserve) and
// never reaches here. Keyed by the connector's immutable id.
func (api *Api) applyConnectorSecret(ctx context.Context, id uint, secret *string) error {
	if secret == nil {
		return nil
	}
	ref, err := ConnectorSecretRef(ctx, id)
	if err != nil {
		return err
	}
	if *secret == "" {
		return api.Secrets.Delete(ctx, ref)
	}
	return api.Secrets.Put(ctx, ref, []byte(*secret))
}

// PublishConnector freezes the connector's current draft {type, config} into a new
// immutable version (the next monotonic integer for that connector) and returns it.
// label and description are optional user annotations; publishedBy is the caller's
// identity. Concurrent publishes are safe: the unique (connector_id, version) index
// rejects a duplicate version number. Returns gorm.ErrRecordNotFound if the connector
// does not exist.
//
// expectedUpdatedAt is the same optional optimistic-concurrency precondition as
// UpdateConnector: publish is refused with ErrConflict if the draft moved on since the
// caller loaded it — otherwise publish could snapshot another writer's content while
// the author believes they froze their own view.
func (api *Api) PublishConnector(ctx context.Context, token string, label, description *string, publishedBy string, expectedUpdatedAt *string) (*ConnectorVersion, error) {
	matches, err := api.ConnectorsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	conn := matches[0]

	if expectedUpdatedAt != nil && conn.UpdatedAt.Format(time.RFC3339) != *expectedUpdatedAt {
		return nil, ErrConflict
	}

	// Next version = max existing + 1 for this connector (tenant-confined already, both
	// because conn was loaded tenant-scoped and via the scope callback here).
	var maxVersion int32
	if err := api.RDB.DB(ctx).Model(&ConnectorVersion{}).
		Where("connector_id = ?", conn.ID).
		Select("COALESCE(MAX(version), 0)").Scan(&maxVersion).Error; err != nil {
		return nil, err
	}

	version := &ConnectorVersion{
		ConnectorID: conn.ID,
		Version:     maxVersion + 1,
		Type:        conn.Type,
		Config:      conn.Config,
		Label:       rdb.NullStrOf(label),
		Description: rdb.NullStrOf(description),
		PublishedBy: publishedBy,
	}
	if err := api.RDB.DB(ctx).Create(version).Error; err != nil {
		return nil, err
	}
	return version, nil
}

// RollbackConnector copies a published version's {type, config} back into the draft
// (the parent Connector row), returning the updated connector. History is append-only
// — no version is deleted; the caller may edit and re-publish. Returns
// gorm.ErrRecordNotFound if the connector or the version does not exist.
func (api *Api) RollbackConnector(ctx context.Context, token string, version int32) (*Connector, error) {
	matches, err := api.ConnectorsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	conn := matches[0]

	var snapshot ConnectorVersion
	if err := api.RDB.DB(ctx).
		Where("connector_id = ? AND version = ?", conn.ID, version).
		First(&snapshot).Error; err != nil {
		return nil, err
	}

	conn.Type = snapshot.Type
	conn.Config = snapshot.Config
	if err := api.RDB.DB(ctx).Save(conn).Error; err != nil {
		return nil, err
	}
	return conn, nil
}

// ErrNotPublished is returned by LatestPublishedConnector when the connector exists but
// has no published version yet — a rule may reference a draft-only connector. Dispatch
// treats it as terminal (a redelivery cannot make a draft published); the author must
// publish the connector.
var ErrNotPublished = errors.New("connector has no published version")

// LatestPublishedConnector resolves a connector by token to its most recent PUBLISHED
// version (the draft is work-in-progress and never dispatched). It is the dispatch-side
// read: given a rule's ConnectorRef, return the {type, config} + the parent connector id
// (the secret's key). Returns gorm.ErrRecordNotFound if the connector does not exist, or
// ErrNotPublished if it exists but was never published. Tenant-confined via the scope
// callback on both reads.
func (api *Api) LatestPublishedConnector(ctx context.Context, token string) (*ConnectorVersion, error) {
	matches, err := api.ConnectorsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	conn := matches[0]

	var version ConnectorVersion
	err = api.RDB.DB(ctx).
		Where("connector_id = ?", conn.ID).
		Order("version DESC").First(&version).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotPublished
	}
	if err != nil {
		return nil, err
	}
	return &version, nil
}

// ConnectorVersions lists a connector's published versions, newest first. Returns
// gorm.ErrRecordNotFound if the connector does not exist.
func (api *Api) ConnectorVersions(ctx context.Context, token string) ([]*ConnectorVersion, error) {
	matches, err := api.ConnectorsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	conn := matches[0]

	versions := make([]*ConnectorVersion, 0)
	if err := api.RDB.DB(ctx).
		Where("connector_id = ?", conn.ID).
		Order("version DESC").Find(&versions).Error; err != nil {
		return nil, err
	}
	return versions, nil
}

// ConnectorsByToken looks up connectors by their current tokens.
func (api *Api) ConnectorsByToken(ctx context.Context, tokens []string) ([]*Connector, error) {
	found := make([]*Connector, 0)
	result := api.RDB.DB(ctx).Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Connectors searches connectors by criteria (type filter + pagination).
func (api *Api) Connectors(ctx context.Context, criteria ConnectorSearchCriteria) (*ConnectorSearchResults, error) {
	results := make([]Connector, 0)
	db, pag := api.RDB.ListOf(ctx, &Connector{}, func(result *gorm.DB) *gorm.DB {
		if criteria.Type != nil {
			result = result.Where("type = ?", *criteria.Type)
		}
		return result
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &ConnectorSearchResults{Results: results, Pagination: pag}, nil
}

// DeleteConnector hard-deletes the connector with the given token, its version
// history, and its stored credential. It reports whether a row was deleted (false when
// no connector has that token). The tenant-scope callback confines the delete to the
// caller's tenant.
//
// The delete is Unscoped (a real DELETE, not a soft-delete): a connector has no
// trash/restore semantics, and the token unique index does not exclude soft-deleted
// rows, so a soft-delete would lock the token forever (mirrors the dashboard rationale).
//
// A rule referencing a since-deleted connector fails to publish (the C4b executor
// dead-letters a dangling ConnectorRef, and C4d's publish-time existence check rejects
// authoring a new one) rather than silently mis-delivering — the delete does not check
// for referencing rules here (they live in a different service), matching the
// notification "dangling reference renders as nothing" precedent.
func (api *Api) DeleteConnector(ctx context.Context, token string) (bool, error) {
	// Resolve first (tenant-scoped) so we can drop the version history and the secret
	// too — ConnectorVersion.ConnectorID is a plain column with no FK cascade, so a bare
	// connector delete would orphan every snapshot forever.
	matches, err := api.ConnectorsByToken(ctx, []string{token})
	if err != nil {
		return false, err
	}
	if len(matches) == 0 {
		return false, nil
	}
	connectorID := matches[0].ID

	// Delete the versions and the connector atomically so a delete can't half-succeed
	// and orphan rows. Hard deletes (Unscoped); the tenant-scope callback still confines
	// both statements to the caller's tenant.
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Where("connector_id = ?", connectorID).Delete(&ConnectorVersion{}).Error; err != nil {
			return err
		}
		return tx.Unscoped().Where("token = ?", token).Delete(&Connector{}).Error
	})
	if err != nil {
		return false, err
	}

	// Remove the connector's credential so a deleted connector leaves no orphaned secret
	// (Delete is idempotent, so a connector that never had one is a no-op). The rows are
	// already hard-deleted, so a failure to remove the (now unreachable) secret must not
	// report the connector as undeleted: log and continue. The orphaned ciphertext is
	// benign — ids are never recycled, so it can never be resolved by a future connector.
	ref, err := ConnectorSecretRef(ctx, connectorID)
	if err != nil {
		return false, err
	}
	if err := api.Secrets.Delete(ctx, ref); err != nil {
		log.Warn().Err(err).Str("token", token).
			Msg("Deleted connector but failed to remove its stored secret (orphaned ciphertext)")
	}
	return true, nil
}

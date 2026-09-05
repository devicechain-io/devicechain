// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
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
	return api.validateTypeAndConfig(request.Type, request.Config)
}

// validateTypeAndConfig is validateRequest's body, taking the pair directly so the
// UPDATE path can validate what a partial request FOLDS ONTO — which is not what the
// request carries. An update naming only `type` still has to be checked against the
// STORED config, because the per-type field shape is a property of the two together:
// re-pointing an mqtt connector at kafka without touching its config would otherwise
// store a document the new type cannot dispatch, and the failure would surface at send
// time instead of at the write.
func (api *Api) validateTypeAndConfig(connectorType, config string) (datatypes.JSON, error) {
	if err := validateConnectorType(connectorType); err != nil {
		return nil, err
	}
	cfg, err := configJSON(config)
	if err != nil {
		return nil, err
	}
	if connectorspec.Supported(connectorType) {
		if err := connectorspec.ValidateConfig(connectorType, cfg); err != nil {
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

// UpdateConnector updates the connector (draft) with the given (current) token, which
// is the ONLY thing that names the row — the request carries no token, so retargeting a
// second connector is unrepresentable. Renaming is renameConnector's job.
//
// It is a PARTIAL update: an omitted field leaves the stored value alone, an explicit
// null clears it, and a value sets it. `type` and `config` sit on NOT NULL columns and
// refuse a null; `secret` is write-only, so omitting it preserves the stored credential
// (the caller cannot read it back to resend it), a value rotates it, and a null — or an
// empty string — deletes it.
//
// When expectedUpdatedAt is non-nil it is an optimistic-concurrency precondition (same
// contract as the dashboard precedent): the save is rejected with ErrConflict if the
// row's current UpdatedAt no longer matches — another writer changed it since the
// caller loaded it. The comparison uses RFC3339 (second precision), the exact string
// the caller was handed by the `updatedAt` query field, so a value that round-trips
// unchanged always matches.
func (api *Api) UpdateConnector(ctx context.Context, token string, request *ConnectorUpdateRequest, expectedUpdatedAt *string) (*Connector, error) {
	matches, err := api.ConnectorsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	current := matches[0]

	// 🔴 THE FOLD RUNS — AND VALIDATES — BEFORE ANYTHING IS WRITTEN, so a refused null
	// on a required column or a config the new type cannot take rejects the WHOLE
	// update. A caller who retries has not half-applied the first attempt.
	//
	// The pair is validated as a pair, against the values the row will HOLD rather than
	// the ones the request carried: an update naming only `type` re-checks the stored
	// config against it, and one naming only `config` checks it against the stored type.
	connectorType, err := request.Type.ApplyToRequired("type", current.Type)
	if err != nil {
		return nil, err
	}
	configRaw, err := request.Config.ApplyToRequired("config", string(current.Config))
	if err != nil {
		return nil, err
	}
	cfg, err := api.validateTypeAndConfig(connectorType, configRaw)
	if err != nil {
		return nil, err
	}
	name := rdb.NullStrOf(request.Name.ApplyTo(dcgraphql.NullStr(current.Name)))
	description := rdb.NullStrOf(request.Description.ApplyTo(dcgraphql.NullStr(current.Description)))
	secret := updatedSecret(request.Secret)

	// No precondition → unconditional last-write-wins (non-interactive callers that
	// don't track a version).
	if expectedUpdatedAt == nil {
		current.Name = name
		current.Description = description
		current.Type = connectorType
		current.Config = cfg
		if err := api.RDB.DB(ctx).Save(current).Error; err != nil {
			return nil, err
		}
		return api.applyUpdatedSecret(ctx, current, secret)
	}

	// Optimistic concurrency: a clean early-out against the caller's stated version,
	// then an ATOMIC guarded write (UPDATE ... WHERE updated_at = <the value just read>)
	// so a concurrent save slipping in between the read and this write moves updated_at
	// and matches zero rows instead of being silently clobbered.
	//
	// 🔴 The layout must match core/graphql.FormatTime, which produced the string the
	// caller is echoing back. It does NOT merely need to be self-consistent: the guarded
	// write below re-reads updated_at, so this comparison is the ONLY thing enforcing the
	// CALLER's version, and at RFC3339 it enforced it to the whole second — a client whose
	// view was stale by less than a second passed the precondition and overwrote a change
	// it had never seen.
	if current.UpdatedAt.Format(time.RFC3339Nano) != *expectedUpdatedAt {
		return nil, ErrConflict
	}
	res := api.RDB.DB(ctx).Model(&Connector{}).
		Where("id = ? AND updated_at = ?", current.ID, current.UpdatedAt).
		Updates(map[string]any{
			"name":        name,
			"description": description,
			"type":        connectorType,
			"config":      cfg,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrConflict
	}

	// Reload for the freshly-bumped updated_at — the caller advances its precondition
	// baseline from the returned value. By the token ARGUMENT, which is the only thing
	// that ever named this row: the payload used to carry one, and reloading by it was
	// how a blank payload token made the success response read a row it could no longer
	// name.
	reloaded, err := api.ConnectorsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(reloaded) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return api.applyUpdatedSecret(ctx, reloaded[0], secret)
}

// RenameConnector changes a connector's token, and changes nothing else.
//
// 🔴 IT EXISTS BECAUSE THE RENAME WAS A REAL CAPABILITY THE UPDATE PAYLOAD CARRIED. A
// connector's credential is keyed by its IMMUTABLE ID (ConnectorSecretRef), so a rename
// keeps it bound — TestSecretSurvivesTokenRename is what pins that, and it is now
// pointed here. Dispatch resolves a connector by token to its latest published version,
// so a rename does move what a rule's ConnectorRef must name; that is a property of the
// rename being a real, deliberate operation rather than a reason to forbid it.
//
// The rules, in the order they are applied:
//
//  1. A BLANK new token — empty or WHITESPACE-ONLY — is refused. `token: String!`
//     admits "", and that used to be written straight onto the row, leaving a connector
//     REACT still dispatches to addressable by nothing, with the mutation returning
//     success.
//  2. newToken == token is an idempotent NO-OP SUCCESS returning the connector, so the
//     retry of a rename that half-failed is safe.
//  3. A token another connector in the tenant already holds is refused BY NAME, from
//     inside the transaction that does the write.
//
// The new token is stored VERBATIM, never trimmed: trimming would silently accept
// " pager " as naming "pager", while the token grammar refuses it plainly.
func (api *Api) RenameConnector(ctx context.Context, token string, newToken string) (*Connector, error) {
	if strings.TrimSpace(newToken) == "" {
		return nil, fmt.Errorf("cannot rename connector %q: the new token is blank, and a "+
			"connector named by nothing can never be dispatched to again", token)
	}

	matches, err := api.ConnectorsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	current := matches[0]

	if newToken == current.Token {
		return current, nil
	}

	// 🔴 THE LOOKUP IS THE FAST PATH; THE UNIQUE INDEX IS THE AUTHORITY. Both are made to
	// say the same sentence, and the reason is that the transaction does NOT close the
	// race. At READ COMMITTED a Count that matches nothing takes no lock — there is no
	// row to lock — so two concurrent renames onto one free token both see it free and
	// the loser is stopped by the index instead. Without the translation below it is
	// handed `SQLSTATE 23505` and an index name, which is not what this API promises and
	// not something a caller can act on: they cannot write two handlers for one condition
	// that differ only by which of them got there first.
	//
	// The tenant predicate on the Count is the scoping callback's, so it counts within
	// the caller's tenant; the index carries the same predicate plus `deleted_at IS NULL`,
	// which is exactly the set the lookup queries.
	if err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		var taken int64
		if err := tx.Model(&Connector{}).Where("token = ?", newToken).Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return ErrConnectorTokenTaken(token, newToken)
		}
		// 🔴 ONE COLUMN, NOT THE WHOLE ROW. Saving `current` would rewrite every field
		// from a copy loaded a moment ago, so a concurrent edit of the draft's config
		// landing in that window would be silently reverted by a mutation that changes
		// only a name. This mutation takes no expectedUpdatedAt precondition, so the
		// narrow write is what bounds it. It still passes through the token-grammar
		// callback (a map destination is checked the same way a struct is) and the
		// tenant-scope callback.
		if err := tx.Model(current).Update("token", newToken).Error; err != nil {
			// THE LOSING RACER ARRIVES HERE rather than through the Count above, and it
			// must read exactly as the uncontended refusal does.
			if rdb.IsUniqueViolation(err, connectorTokenIndexName, "connectors.token") {
				return ErrConnectorTokenTaken(token, newToken)
			}
			return err
		}
		current.Token = newToken
		return nil
	}); err != nil {
		return nil, err
	}
	return current, nil
}

// ErrConnectorTokenTaken is the ONE sentence a caller gets when the token they asked for
// belongs to another connector — whether the pre-write lookup found it or the unique index
// did. Both paths are made to say this, because a client cannot be asked to write two
// handlers for one condition that differ only by timing.
func ErrConnectorTokenTaken(token, newToken string) error {
	return fmt.Errorf("cannot rename connector %q to %q: that token is already in use "+
		"by another connector in this tenant", token, newToken)
}

// connectorTokenIndexName is the per-tenant partial unique index the baseline creates on
// connectors (tenant_id, token) among live rows. Postgres names it in the text of a unique
// violation, and that name is what distinguishes "this token is taken" from any other write
// failure.
//
// It mirrors schema/baseline.go's createTenantTokenIndex naming rule, "uix_" + the bare
// table name + "_tenant_token". The rule is spelled in two places because that helper is a
// deliberate copy inside the migration and is unexported;
// TestConnectorTokenIndexNameMatchesTheMigration is what keeps the two from drifting.
const connectorTokenIndexName = "uix_connectors_tenant_token"

// updatedSecret folds the three states of the write-only `secret` field onto the
// *string applyConnectorSecret takes, which has only two: nil means PRESERVE, and a
// pointer is applied (empty clears, non-empty seals).
//
// 🔴 AN EXPLICIT NULL CLEARS, AND THAT IS A NEW OPERATION SPELLED THE PLATFORM'S WAY.
// Under the old pointer the clear was spelled as the empty STRING — a value standing in
// for an absence, because a pointer had no third state to give it. Now null means what
// it means on every other field: remove the stored value. The empty string keeps
// clearing too; a form's "" and a null are the same intent, and refusing one of them
// would only surprise a caller who had cleared a credential the other way yesterday.
func updatedSecret(field dcgraphql.OptionalString) *string {
	if !field.Set {
		return nil
	}
	if field.Value == nil {
		cleared := ""
		return &cleared
	}
	return field.Value
}

// applyUpdatedSecret applies the write-only secret to conn per the request (preserve on
// nil), keyed by the connector's immutable id (so a rename never orphans it), and
// returns conn for the caller.
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

	// Same layout coupling as UpdateConnector — see the comment there.
	if expectedUpdatedAt != nil && conn.UpdatedAt.Format(time.RFC3339Nano) != *expectedUpdatedAt {
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

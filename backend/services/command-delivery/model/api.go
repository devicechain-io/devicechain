// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DeviceVerifier confirms a target device exists (in the request's tenant) before
// a command is enqueued against it (ADR-044 amendment / W1.1b). It is
// dependency-inverted — the model depends only on this narrow interface, never on
// the sync-call machinery (svcclient) — mirroring device-management's
// AlarmEventPublisher seam. A nil verifier (unconfigured service secret) skips the
// check, preserving the prior enqueue-anything behavior; command-delivery logs the
// disabled mode loudly at startup.
type DeviceVerifier interface {
	DeviceExists(ctx context.Context, deviceToken string) (bool, error)
}

type Api struct {
	RDB *rdb.RdbManager
	// DeviceVerifier, when set, gates CreateCommand on the target device existing.
	DeviceVerifier DeviceVerifier
}

// NewApi creates a new API instance.
func NewApi(rdb *rdb.RdbManager) *Api {
	api := &Api{}
	api.RDB = rdb
	return api
}

// CommandDeliveryApi is the interface for the command delivery API (used for
// mocking and dependency injection into the processor).
type CommandDeliveryApi interface {
	CreateCommand(ctx context.Context, request *CommandCreateRequest) (*Command, error)

	MarkSent(ctx context.Context, id uint) (*Command, error)
	MarkResponse(ctx context.Context, commandToken string, success bool, payload *string, errMsg *string) (*Command, error)
	CancelCommand(ctx context.Context, token string) (*Command, error)
	ExpireStale(ctx context.Context, now time.Time) (int64, error)

	CommandsById(ctx context.Context, ids []uint) ([]*Command, error)
	CommandsByToken(ctx context.Context, tokens []string) ([]*Command, error)
	Commands(ctx context.Context, criteria CommandSearchCriteria) (*CommandSearchResults, error)
	PendingCommands(ctx context.Context) ([]*Command, error)
}

// canTransition reports whether a command in state `from` may transition to
// state `to`. A transition out of a terminal state is never permitted; the
// allowed forward edges follow the lifecycle QUEUED -> SENT -> {SUCCESSFUL,FAILED}
// with expiry/timeout edges to EXPIRED/TIMEOUT.
//
// DELIVERED is deliberately NOT an edge here: there is no device
// delivery-acknowledgment transport today, so nothing can emit it (see the note
// on CommandDelivered in model.go). It is reserved for when such an ack exists.
func canTransition(from, to CommandStatus) bool {
	if from.Terminal() {
		return false
	}
	switch to {
	case CommandSent:
		return from == CommandQueued
	case CommandSuccessful, CommandFailed:
		// A response can arrive after SENT (and tolerate QUEUED in races where
		// the response beats the SENT write).
		return true
	case CommandExpired, CommandTimeout:
		// Sweep / cancellation may terminate any non-terminal command.
		return true
	}
	return false
}

// CreateCommand persists a new command in the QUEUED state.
func (api *Api) CreateCommand(ctx context.Context, request *CommandCreateRequest) (*Command, error) {
	// Reject a malformed JSON payload rather than silently persisting NULL (the
	// metadata helper swallows the parse error), which would deliver a command
	// stripped of its arguments. Same for the metadata blob.
	if request.Payload != nil && !json.Valid([]byte(*request.Payload)) {
		return nil, fmt.Errorf("command payload is not valid JSON")
	}
	if request.Metadata != nil && !json.Valid([]byte(*request.Metadata)) {
		return nil, fmt.Errorf("command metadata is not valid JSON")
	}

	// Parse the optional TTL (RFC3339) — a cheap local check done before the remote
	// verification below, so a malformed request fails without a wasted round trip.
	var expiresAt sql.NullTime
	if request.ExpiresAt != nil {
		parsed, err := time.Parse(time.RFC3339, *request.ExpiresAt)
		if err != nil {
			return nil, err
		}
		expiresAt = sql.NullTime{Time: parsed, Valid: true}
	}

	// Verify the target device exists before enqueuing (W1.1b) — a synchronous
	// read against device-management, the authoritative owner. This is a read-time
	// invariant check (does it exist *now*?), the case the async projection can't
	// answer, so it is the sanctioned sync-call use (ADR-044 decision rule). When no
	// verifier is wired (service secret unconfigured) the check is skipped. A
	// verification *failure* (device-management unreachable) fails closed — a ghost
	// command is never persisted — but the detail is logged, not returned, so the
	// tenant API client does not learn the in-cluster topology.
	if api.DeviceVerifier != nil {
		exists, err := api.DeviceVerifier.DeviceExists(ctx, request.DeviceToken)
		if err != nil {
			log.Error().Err(err).Str("deviceToken", request.DeviceToken).Msg("Device existence verification failed; refusing enqueue.")
			return nil, fmt.Errorf("cannot enqueue command: device verification is unavailable")
		}
		if !exists {
			return nil, fmt.Errorf("cannot enqueue command: device %q does not exist", request.DeviceToken)
		}
	}

	created := &Command{
		TokenReference: rdb.TokenReference{
			Token: request.Token,
		},
		MetadataEntity: rdb.MetadataEntity{
			Metadata: rdb.MetadataStrOf(request.Metadata),
		},
		DeviceToken: request.DeviceToken,
		Name:        request.Name,
		Payload:     rdb.MetadataStrOf(request.Payload),
		Status:      CommandQueued.String(),
		QueuedTime:  time.Now(),
		ExpiresAt:   expiresAt,
	}

	// Idempotent on the client-supplied token (ADR-042 per-tenant unique). ON CONFLICT DO NOTHING
	// (no target ⇒ any unique violation, so it matches the partial (tenant_id, token) index) turns a
	// repeat with an already-live token into a no-op instead of a unique-violation error; the caller
	// then reads back and receives the ORIGINAL command unchanged. This makes createCommand a safe
	// idempotency-key operation: a client — or the REACT dispatcher's at-least-once redelivery
	// (ADR-051 slice 5b), which derives a deterministic token per (detection, action) — can retry
	// with the same token without ever enqueuing a second physical command.
	result := api.RDB.DB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		// The token already names a live command in this tenant: return the existing row (idempotent
		// replay), not a fresh one. The request's other fields are intentionally ignored on replay —
		// the token IS the identity, so the first write wins and a differing re-request does not
		// mutate or duplicate it.
		return api.commandByToken(ctx, request.Token)
	}
	return created, nil
}

// commandByToken loads the single live command with the given token in the request's tenant (tenant
// scoping is applied transparently by the tenant-scoped DB callback). It backs the idempotent
// createCommand replay path; a missing row after an ON CONFLICT DO NOTHING no-op would mean the
// conflicting row was concurrently soft-deleted, which surfaces as a not-found error to the caller.
func (api *Api) commandByToken(ctx context.Context, token string) (*Command, error) {
	found := &Command{}
	if err := api.RDB.DB(ctx).Where("token = ?", token).First(found).Error; err != nil {
		return nil, err
	}
	return found, nil
}

// loadCommand loads a single command by id.
func (api *Api) loadCommand(ctx context.Context, id uint) (*Command, error) {
	found := &Command{}
	result := api.RDB.DB(ctx).First(found, id)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// MarkSent transitions a command QUEUED -> SENT.
func (api *Api) MarkSent(ctx context.Context, id uint) (*Command, error) {
	found, err := api.loadCommand(ctx, id)
	if err != nil {
		return nil, err
	}
	if !canTransition(CommandStatus(found.Status), CommandSent) {
		return nil, fmt.Errorf("command %d can not transition from %s to %s", id, found.Status, CommandSent)
	}
	found.Status = CommandSent.String()
	found.SentTime = sql.NullTime{Time: time.Now(), Valid: true}
	if result := api.RDB.DB(ctx).Save(found); result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// MarkResponse records a device response against a command, looked up by its
// token. If the command is already terminal the response is ignored (the
// current command is returned). On success the command becomes SUCCESSFUL,
// otherwise FAILED with the error message recorded.
func (api *Api) MarkResponse(ctx context.Context, commandToken string, success bool,
	payload *string, errMsg *string) (*Command, error) {
	matches, err := api.CommandsByToken(ctx, []string{commandToken})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	found := matches[0]

	// Ignore responses to already-terminal commands (idempotent / late).
	if CommandStatus(found.Status).Terminal() {
		return found, nil
	}

	found.RespondedTime = sql.NullTime{Time: time.Now(), Valid: true}
	found.ResponsePayload = rdb.MetadataStrOf(payload)
	if success {
		found.Status = CommandSuccessful.String()
	} else {
		found.Status = CommandFailed.String()
		found.Error = rdb.NullStrOf(errMsg)
	}
	if result := api.RDB.DB(ctx).Save(found); result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// CancelCommand cancels a non-terminal command by token, moving it to EXPIRED
// (QUEUED/SENT/DELIVERED -> EXPIRED). A terminal command is returned unchanged.
func (api *Api) CancelCommand(ctx context.Context, token string) (*Command, error) {
	matches, err := api.CommandsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	found := matches[0]
	if CommandStatus(found.Status).Terminal() {
		return found, nil
	}
	found.Status = CommandExpired.String()
	if result := api.RDB.DB(ctx).Save(found); result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// ExpireStale times out every non-terminal command whose TTL has elapsed. A
// QUEUED command that never went out becomes EXPIRED; a SENT/DELIVERED command
// that was never answered becomes TIMEOUT. The caller MUST pass a system
// context (core.WithSystemContext) so the sweep spans all tenants. Returns the
// number of commands expired.
func (api *Api) ExpireStale(ctx context.Context, now time.Time) (int64, error) {
	stale := make([]*Command, 0)
	terminal := []string{
		CommandSuccessful.String(), CommandTimeout.String(),
		CommandExpired.String(), CommandFailed.String(),
	}
	result := api.RDB.DB(ctx).
		Where("status NOT IN ?", terminal).
		Where("expires_at IS NOT NULL AND expires_at < ?", now).
		Find(&stale)
	if result.Error != nil {
		return 0, result.Error
	}

	var count int64
	for _, cmd := range stale {
		next := CommandTimeout.String()
		if cmd.Status == CommandQueued.String() {
			next = CommandExpired.String()
		}
		// Conditional update: only expire a command that is STILL non-terminal, and
		// touch only the status column — never a full-row Save of the pre-response
		// snapshot. A device response (MarkResponse) that landed since the scan made
		// the command terminal, so this WHERE misses and the response is preserved
		// instead of being overwritten back to TIMEOUT/EXPIRED.
		res := api.RDB.DB(ctx).Model(&Command{}).
			Where("id = ? AND status NOT IN ?", cmd.ID, terminal).
			Update("status", next)
		if res.Error != nil {
			return count, res.Error
		}
		count += res.RowsAffected
	}
	return count, nil
}

// CommandsById gets commands by id.
func (api *Api) CommandsById(ctx context.Context, ids []uint) ([]*Command, error) {
	found := make([]*Command, 0)
	result := api.RDB.DB(ctx).Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// CommandsByToken gets commands by token.
func (api *Api) CommandsByToken(ctx context.Context, tokens []string) ([]*Command, error) {
	found := make([]*Command, 0)
	result := api.RDB.DB(ctx).Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Commands searches for commands matching the given criteria.
func (api *Api) Commands(ctx context.Context, criteria CommandSearchCriteria) (*CommandSearchResults, error) {
	results := make([]Command, 0)
	db, pag := api.RDB.ListOf(ctx, &Command{}, func(db *gorm.DB) *gorm.DB {
		if criteria.DeviceToken != nil {
			db = db.Where("device_token = ?", *criteria.DeviceToken)
		}
		if criteria.Status != nil {
			db = db.Where("status = ?", *criteria.Status)
		}
		return db
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &CommandSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// PendingCommands returns every still-QUEUED command. It is the redelivery
// worker's source; the caller passes a system context for the cross-tenant sweep.
func (api *Api) PendingCommands(ctx context.Context) ([]*Command, error) {
	found := make([]*Command, 0)
	result := api.RDB.DB(ctx).Where("status = ?", CommandQueued.String()).Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

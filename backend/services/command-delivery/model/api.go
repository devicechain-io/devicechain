// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

type Api struct {
	RDB *rdb.RdbManager
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
	MarkDelivered(ctx context.Context, id uint) (*Command, error)
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
// allowed forward edges follow the lifecycle QUEUED -> SENT -> DELIVERED ->
// {SUCCESSFUL,FAILED} with expiry/timeout edges to EXPIRED/TIMEOUT.
func canTransition(from, to CommandStatus) bool {
	if from.Terminal() {
		return false
	}
	switch to {
	case CommandSent:
		return from == CommandQueued
	case CommandDelivered:
		return from == CommandSent
	case CommandSuccessful, CommandFailed:
		// A response can arrive after SENT or DELIVERED (and tolerate QUEUED in
		// races where the response beats the SENT write).
		return true
	case CommandExpired, CommandTimeout:
		// Sweep / cancellation may terminate any non-terminal command.
		return true
	}
	return false
}

// CreateCommand persists a new command in the QUEUED state.
func (api *Api) CreateCommand(ctx context.Context, request *CommandCreateRequest) (*Command, error) {
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
	}

	// Parse optional TTL (RFC3339).
	if request.ExpiresAt != nil {
		parsed, err := time.Parse(time.RFC3339, *request.ExpiresAt)
		if err != nil {
			return nil, err
		}
		created.ExpiresAt = sql.NullTime{Time: parsed, Valid: true}
	}

	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
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

// MarkDelivered transitions a command SENT -> DELIVERED.
func (api *Api) MarkDelivered(ctx context.Context, id uint) (*Command, error) {
	found, err := api.loadCommand(ctx, id)
	if err != nil {
		return nil, err
	}
	if !canTransition(CommandStatus(found.Status), CommandDelivered) {
		return nil, fmt.Errorf("command %d can not transition from %s to %s", id, found.Status, CommandDelivered)
	}
	found.Status = CommandDelivered.String()
	found.DeliveredTime = sql.NullTime{Time: time.Now(), Valid: true}
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

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EnqueueRejected reports that a command may not be enqueued, and why. It is a
// distinct type from a transport failure so CreateCommand can relay a rejection to
// the API client verbatim (it names only tenant-visible things — the device token,
// the command key, the offending parameter) while a failure to *perform* the check
// stays sanitized.
type EnqueueRejected struct {
	Reason string
}

func (e *EnqueueRejected) Error() string { return e.Reason }

// payloadBytes renders an optional payload for validation. A nil payload and an
// explicit JSON null are the same thing to the validator (a command carrying no
// arguments), which is why the absent case is nil rather than an empty-but-present
// document — the schema validator treats both as "no arguments supplied" and
// rejects only if a required parameter is declared.
func payloadBytes(payload *string) []byte {
	if payload == nil {
		return nil
	}
	return []byte(*payload)
}

// CommandEnqueueValidator gates CreateCommand on device-management's answer to
// "may this command be enqueued to this device?" — the single ADR-043 decision 3
// enqueue gate. It resolves device → its profile's active PUBLISHED command
// vocabulary → the definition for the command key, and validates the payload
// against that definition's parameter schema, so it answers all three of decision
// 3's rejections at once: a non-existent device, an unknown command key, and a
// payload that violates the schema.
//
// It is dependency-inverted — the model depends only on this narrow interface,
// never on the sync-call machinery (svcclient) — mirroring device-management's
// DetectionRuleValidator seam. Validation lives at the OWNER of the vocabulary
// rather than here so the parameter-schema validator has exactly one
// implementation; shipping the schema across the module boundary to re-validate it
// here would guarantee the two copies drift.
//
// A nil validator (unconfigured service secret) skips the gate, preserving the
// prior enqueue-anything behavior; command-delivery logs the disabled mode loudly
// at startup.
//
// Returns *EnqueueRejected when the command is invalid, a plain error when the
// check could not be performed (which the caller must fail closed on).
type CommandEnqueueValidator interface {
	ValidateEnqueue(ctx context.Context, deviceToken string, commandKey string, payload []byte) error
}

type Api struct {
	RDB *rdb.RdbManager
	// EnqueueValidator, when set, gates CreateCommand on the target device existing
	// and on the command matching the device profile's published vocabulary.
	EnqueueValidator CommandEnqueueValidator
	// DefaultCommandTTL, when positive, is stamped as expires_at on a command whose
	// creator supplies no explicit ExpiresAt (a caller value always wins). It gives
	// every command a terminal horizon: a command a device never receives reaches
	// TIMEOUT via ExpireStale instead of sitting in SENT forever, and it bounds the
	// LwM2M queue-mode hold (ADR-075 L4b). Zero disables stamping — the pre-config
	// behavior, used by tests that construct the Api directly; production always sets
	// it from CommandDeliveryConfiguration (floored positive in ApplyDefaults).
	DefaultCommandTTL time.Duration
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
	// TrySweepLock serializes the expiry + redelivery sweep across replicas.
	TrySweepLock(ctx context.Context, fn func() error) (bool, error)
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
	} else if api.DefaultCommandTTL > 0 {
		// No explicit TTL: stamp the platform default so the command still reaches a
		// terminal state (ExpireStale → TIMEOUT) instead of sitting in SENT forever. A
		// caller-supplied ExpiresAt above always wins; this only fills the absent case.
		expiresAt = sql.NullTime{Time: time.Now().Add(api.DefaultCommandTTL), Valid: true}
	}

	// Gate the enqueue on device-management, the authoritative owner of both the
	// device and the command vocabulary (ADR-043 decision 3): the target device must
	// exist, the command must be one its profile's PUBLISHED version declares, and
	// the payload must satisfy that command's parameter schema. This is a read-time
	// invariant check (is it valid *now*?), the case the async projection can't
	// answer, so it is the sanctioned sync-call use (ADR-044 decision rule). When no
	// validator is wired (service secret unconfigured) the gate is skipped.
	//
	// The two outcomes are deliberately NOT collapsed. A rejection is the client's
	// fault and is relayed verbatim so the caller can fix the command. A failure to
	// perform the check (device-management unreachable) fails closed — a ghost or
	// unvalidated command is never persisted — but the detail is logged, not
	// returned, so the tenant API client does not learn the in-cluster topology.
	// Were these collapsed, an outage would read to the client as "your command is
	// invalid" and send them chasing a correct payload.
	if api.EnqueueValidator != nil {
		err := api.EnqueueValidator.ValidateEnqueue(ctx, request.DeviceToken, request.Name, payloadBytes(request.Payload))
		var rejected *EnqueueRejected
		switch {
		case errors.As(err, &rejected):
			return nil, fmt.Errorf("cannot enqueue command: %s", rejected.Reason)
		case err != nil:
			log.Error().Err(err).Str("deviceToken", request.DeviceToken).Str("command", request.Name).
				Msg("Command enqueue validation failed; refusing enqueue.")
			return nil, fmt.Errorf("cannot enqueue command: validation is unavailable")
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

// terminalStatusStrings is the wire form of the four terminal states, for a
// "status NOT IN (…)" guard. One definition shared by every from-state-predicated
// update below and by ExpireStale, so the set the sweep skips and the set a
// transition guards against can never drift.
func terminalStatusStrings() []string {
	return []string{
		CommandSuccessful.String(), CommandTimeout.String(),
		CommandExpired.String(), CommandFailed.String(),
	}
}

// MarkSent transitions a command QUEUED -> SENT.
//
// It is a from-state-predicated conditional UPDATE, not a load-modify-Save: only a
// still-QUEUED row advances, and only the status/sent_time columns are touched. A
// full-row Save would LOSE-UPDATE a response that raced in between the load and the
// write — the sweep publishes BEFORE marking SENT, so a device answering in
// milliseconds can drive the row to SUCCESSFUL (MarkResponse) while this write is
// delayed under load; a Save of the stale QUEUED snapshot would then clobber it back
// to SENT, wiping RespondedTime/ResponsePayload. That is permanent: PendingCommands
// redelivers only QUEUED rows, the response was already consumed, and a REACT command
// carries no TTL so it never times out. RowsAffected==0 means the row already left
// QUEUED (a fast response, a concurrent sweep) — a benign race, not an error; the
// current row is returned. (A deleted row surfaces as loadCommand's not-found.)
func (api *Api) MarkSent(ctx context.Context, id uint) (*Command, error) {
	res := api.RDB.DB(ctx).Model(&Command{}).
		Where("id = ? AND status = ?", id, CommandQueued.String()).
		Updates(map[string]any{
			"status":    CommandSent.String(),
			"sent_time": sql.NullTime{Time: time.Now(), Valid: true},
		})
	if res.Error != nil {
		return nil, res.Error
	}
	return api.loadCommand(ctx, id)
}

// MarkResponse records a device response against a command, looked up by its
// token. If the command is already terminal the response is ignored (the
// current command is returned). On success the command becomes SUCCESSFUL,
// otherwise FAILED with the error message recorded.
//
// The write is a from-state-predicated conditional UPDATE guarded on the row still
// being non-terminal (the same shape ExpireStale uses), touching only the response
// columns — so a response and a racing MarkSent / expire / cancel never clobber each
// other via a stale full-row Save. RowsAffected==0 means the row went terminal
// between the read and the write (a late/duplicate response); the current row is
// returned unchanged.
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

	// Fast-path: ignore responses to already-terminal commands (idempotent / late).
	// The conditional WHERE below is the authoritative guard if it races.
	if CommandStatus(found.Status).Terminal() {
		return found, nil
	}

	updates := map[string]any{
		"responded_time":   sql.NullTime{Time: time.Now(), Valid: true},
		"response_payload": rdb.MetadataStrOf(payload),
	}
	if success {
		updates["status"] = CommandSuccessful.String()
	} else {
		updates["status"] = CommandFailed.String()
		updates["error"] = rdb.NullStrOf(errMsg)
	}
	if res := api.RDB.DB(ctx).Model(&Command{}).
		Where("id = ? AND status NOT IN ?", found.ID, terminalStatusStrings()).
		Updates(updates); res.Error != nil {
		return nil, res.Error
	}
	return api.loadCommand(ctx, found.ID)
}

// CancelCommand cancels a non-terminal command by token, moving it to EXPIRED
// (QUEUED/SENT -> EXPIRED). A terminal command is returned unchanged. Like the other
// transitions it is a from-state-predicated conditional UPDATE so a cancel racing a
// device response does not clobber the response.
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
	if res := api.RDB.DB(ctx).Model(&Command{}).
		Where("id = ? AND status NOT IN ?", found.ID, terminalStatusStrings()).
		Updates(map[string]any{"status": CommandExpired.String()}); res.Error != nil {
		return nil, res.Error
	}
	return api.loadCommand(ctx, found.ID)
}

// ExpireStale times out every non-terminal command whose TTL has elapsed. A
// QUEUED command that never went out becomes EXPIRED; a SENT command
// that was never answered becomes TIMEOUT. The caller MUST pass a system
// context (core.WithSystemContext) so the sweep spans all tenants. Returns the
// number of commands expired.
func (api *Api) ExpireStale(ctx context.Context, now time.Time) (int64, error) {
	stale := make([]*Command, 0)
	terminal := terminalStatusStrings()
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

// sweepLockName namespaces the advisory lock that makes the expiry + redelivery
// sweep single-writer across replicas (distinct from the migration lock, so the two
// never contend).
const sweepLockName = "command-delivery-sweep"

// TrySweepLock runs fn while holding the cross-replica sweep lock, reporting whether
// it ran. It does NOT wait: a replica whose peer is already sweeping skips this pass.
//
// The lock lives here rather than in the processor because the invariant it protects
// is a model-level one — exactly one writer walking the QUEUED set — and because the
// processor talks to this API through an interface, so a lock reached around it could
// not be exercised in a test.
//
// Why a LOCK here when notification-management's escalation scheduler solves the same
// N-replica problem with a per-row CAS claim (ClaimEscalation) and no lock at all:
// escalation can claim because the claim IS the record it protects — one atomic UPDATE
// both wins the right to notify and marks the tier consumed. Command delivery cannot,
// because it must PUBLISH before it can mark. A claim that set SENT up front would lose
// the command outright whenever the publish then failed, and a claim that set it
// afterwards would not have prevented the duplicate. Claiming would therefore need a new
// intermediate state on the command lifecycle; a lock buys the same safety with no model
// change. Revisit if the sweep ever becomes a throughput bottleneck — the claim approach
// parallelizes across replicas where this serializes onto one.
func (api *Api) TrySweepLock(ctx context.Context, fn func() error) (bool, error) {
	return api.RDB.TryAdvisoryLock(ctx, rdb.AdvisoryLockKey(sweepLockName), fn)
}

// PendingCommands returns every still-QUEUED command, oldest first. It is the
// redelivery worker's source; the caller passes a system context for the
// cross-tenant sweep.
//
// The ORDER BY is a strict improvement over the previous unordered read: delivery
// now follows enqueue order instead of whatever the planner returned.
//
// It is deliberately NOT capped, though the unbounded read is a real memory hazard
// — a fleet that goes offline queues commands without bound, and this loads the
// whole backlog into the pod at once. A naive `LIMIT n` makes that worse, not
// better, and the failure is not obvious: combined with oldest-first ordering, any
// command that can never be delivered (an oversized payload, a tenant whose stream
// is gone) keeps the smallest id and therefore occupies a slot in EVERY subsequent
// batch. Accumulate n of them and delivery stops platform-wide, with nothing in the
// data model to break the tie — expiry only touches rows with an explicit
// expires_at, and ExpiresAt is optional. A cap also silently ceilings throughput at
// n per sweep interval for the whole instance, and global-id ordering lets one
// tenant's backlog delay every other tenant behind it.
//
// A correct bound therefore needs three things this model does not yet have: an
// attempt count so a poison command can reach a terminal FAILED state, ordering
// that de-prioritizes what was just tried, and per-tenant fairness so one backlog
// cannot monopolize a pass. That is its own change; until then an unbounded read
// that always makes progress beats a bounded one that can wedge.
func (api *Api) PendingCommands(ctx context.Context) ([]*Command, error) {
	found := make([]*Command, 0)
	result := api.RDB.DB(ctx).Where("status = ?", CommandQueued.String()).
		Order("id ASC").Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

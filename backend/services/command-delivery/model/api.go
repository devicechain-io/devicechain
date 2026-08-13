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

	"github.com/devicechain-io/dc-command-delivery/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RejectionCode is the stable, machine-readable classification of an enqueue
// rejection. It is the field a caller BRANCHES on; Reason is prose for a person and
// its wording is free to change without the meaning changing.
//
// 🔴 THE SET IS NOT CLOSED HERE. The codes below are the ones command-delivery
// itself produces; a rejection from device-management's enqueue gate (the owner of
// the device and the command vocabulary) is relayed with ITS code unchanged —
// DEVICE_NOT_FOUND, COMMAND_NOT_IN_VOCABULARY, PAYLOAD_SCHEMA_VIOLATION today. They
// are deliberately NOT re-declared here: two constant sets naming one enum drift the
// day the owner adds a case, and the drift is silent — a relayed code would keep
// working while the copy claiming to enumerate it quietly went stale.
type RejectionCode string

const (
	// RejectPayloadNotJSON: the payload is not well-formed JSON. Caught locally,
	// before the remote gate, because a malformed body would otherwise be persisted
	// as NULL and delivered as a command stripped of its arguments.
	RejectPayloadNotJSON RejectionCode = "PAYLOAD_NOT_JSON"
	// RejectMetadataNotJSON: the metadata blob is not well-formed JSON.
	RejectMetadataNotJSON RejectionCode = "METADATA_NOT_JSON"
	// RejectExpiresAtInvalid: expiresAt is not an RFC3339 timestamp.
	RejectExpiresAtInvalid RejectionCode = "EXPIRES_AT_INVALID"
	// RejectHeldCeilingExceeded: the tenant already holds its ceiling of withheld
	// (HELD) commands, so this enqueue would grow a backlog that is already at its
	// bound.
	//
	// 🔴 IT IS THE ONE REJECTION HERE THAT IS TEMPORARY. Every other code says the
	// request is wrong and will still be wrong on the next attempt; this one says
	// the tenant is full RIGHT NOW, and it clears itself as the fleet comes back
	// online and the holds drain. A caller that retries this is doing the correct
	// thing, which is exactly why it must be distinguishable from the others.
	RejectHeldCeilingExceeded RejectionCode = "HELD_CEILING_EXCEEDED"
	// RejectUnclassified: a rejection arrived from the enqueue gate carrying no
	// code. It is a real rejection (the verdict said no) that this build cannot
	// classify, so it is passed on as such rather than being dressed up as one of
	// the codes above.
	RejectUnclassified RejectionCode = "COMMAND_REJECTED"
)

// EnqueueRejected reports that a command may not be enqueued, and why. It is a
// distinct type from a transport failure so CreateCommand can relay a rejection to
// the API client verbatim (it names only tenant-visible things — the device token,
// the command key, the offending parameter) while a failure to *perform* the check
// stays sanitized.
type EnqueueRejected struct {
	// Code classifies the rejection for a machine (see RejectionCode).
	Code RejectionCode
	// Reason explains it for a person, in client-safe terms.
	Reason string
}

func (e *EnqueueRejected) Error() string { return e.Reason }

// rejected builds a rejection with a client-safe reason. The code is positional so
// that adding a rejection forces a decision about how a caller classifies it.
func rejected(code RejectionCode, format string, args ...any) *EnqueueRejected {
	return &EnqueueRejected{Code: code, Reason: fmt.Sprintf(format, args...)}
}

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

// HeldCommandCeilingResolver reads a tenant's RESOLVED ceiling on withheld (HELD)
// commands — the per-tenant override, else the tenant's tier, else the platform
// default, folded into one number by whoever implements this.
//
// It is a narrow READ seam, mirroring how event-sources consumes
// governance.ShedPriorityResolver: this package depends on the question, never on
// the machinery that answers it (a service-token client, a cache, user-management's
// governance query), so the ceiling can be exercised here with a two-line stub.
//
// Resolve returns the ceiling in force for the tenant, without blocking. It carries no
// "was this actually fetched?" bool — deliberately, and unlike the shed-priority
// resolver it otherwise mirrors: a shed BAND served from a cold cache is a wrong action
// waiting to happen (it sheds a premium tenant), whereas a ceiling served from a cold
// cache is a live bound in its own right. There is nothing here for a caller to hold
// off on, and holding off would mean admitting an UNBOUNDED backlog while the cache
// warms — the one outcome this must never produce.
//
// 🔴 A non-positive answer — or no resolver at all — means the PLATFORM DEFAULT, and
// never unlimited. An absent bound that reads as "no bound" is how a governance ceiling
// silently stops governing, and it stops precisely when the authority carrying it is
// unreachable.
type HeldCommandCeilingResolver interface {
	Resolve(tenant string) int
}

type Api struct {
	RDB *rdb.RdbManager
	// EnqueueValidator, when set, gates CreateCommand on the target device existing
	// and on the command matching the device profile's published vocabulary.
	EnqueueValidator CommandEnqueueValidator
	// HeldCeilingResolver, when set, supplies the per-tenant held-command ceiling.
	// Nil (the wiring not yet configured, or a unit test) falls back to
	// DefaultHeldCommandCeiling below — never to "unbounded".
	HeldCeilingResolver HeldCommandCeilingResolver
	// DefaultHeldCommandCeiling is the service-configured fallback ceiling, used when
	// no resolver is wired or the tenant is not yet resolved. Zero (an Api built
	// directly by a test) falls back to config.DefaultHeldCommandCeiling, so the
	// bound holds in every configuration — see heldCommandCeiling.
	DefaultHeldCommandCeiling int
	// DefaultCommandTTL, when positive, is stamped as expires_at on a command whose
	// creator supplies no explicit ExpiresAt (a caller value always wins). It gives
	// every command a terminal horizon instead of leaving it in flight forever, and
	// it bounds the LwM2M queue-mode hold (ADR-075 L4b). Which terminal it reaches
	// depends on how far the command got: one that was never dispatched (QUEUED or
	// HELD) becomes EXPIRED, one that went out unanswered becomes TIMEOUT — see
	// expiredTerminalFor. Zero disables stamping — the pre-config
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
	MarkSentByToken(ctx context.Context, token string) (bool, error)
	MarkResponse(ctx context.Context, commandToken string, success bool, payload *string, errMsg *string) (*Command, error)
	CancelCommand(ctx context.Context, token string) (*Command, error)
	ExpireStale(ctx context.Context, now time.Time) (int64, map[string]int64, error)

	CommandsById(ctx context.Context, ids []uint) ([]*Command, error)
	CommandsByToken(ctx context.Context, tokens []string) ([]*Command, error)
	Commands(ctx context.Context, criteria CommandSearchCriteria) (*CommandSearchResults, error)
	PendingCommands(ctx context.Context) ([]*Command, error)
	// TrySweepLock serializes the expiry + redelivery sweep across replicas.
	TrySweepLock(ctx context.Context, fn func() error) (bool, error)
}

// CreateCommand persists a new command in the QUEUED state.
//
// Rejections are returned as *EnqueueRejected (a typed verdict the GraphQL layer
// turns into a rejection payload); a plain error means the enqueue could not be
// DECIDED — a DB failure, or an enqueue gate that could not be reached. The two are
// never collapsed: a rejection is the caller's fault and is relayed verbatim, while
// an availability failure is sanitized, because the alternative tells a tenant
// client "your command is invalid" during an outage and sends them chasing a correct
// payload, and leaks in-cluster topology while doing it.
func (api *Api) CreateCommand(ctx context.Context, request *CommandCreateRequest) (*Command, error) {
	// 🔴 THE REPLAY LOOKUP COMES FIRST — before the ceiling, before the enqueue gate,
	// before every local check. A token that already names a live command in this
	// tenant is a REPLAY: the row exists, this call will create nothing, and the
	// answer is that row.
	//
	// The ordering is not an optimization, it is a correctness requirement. REACT's
	// send-command derives a DETERMINISTIC token per (detection, action) precisely so
	// its at-least-once redelivery collapses here, which makes replays a NORMAL path
	// rather than an edge case. Anything that can say no in front of this lookup says
	// no to a call that would have created no row at all: at the held-command ceiling,
	// every REACT retry for a tenant would be refused for a reason that is not true of
	// it, and the tenant's already-enqueued commands would look like new pressure they
	// are not.
	//
	// It needs no transaction and no lock. The insert below is ON CONFLICT DO NOTHING
	// on the partial unique (tenant_id, token) index, so a row created between this
	// read and that write still collapses to the same replay answer via
	// RowsAffected==0. That same index serves this lookup, so the cost is one indexed
	// read on a path that already does several.
	existing, replay, err := api.liveCommandByToken(ctx, request.Token)
	if err != nil {
		// A real read failure. Fail rather than fall through: falling through would
		// re-attempt the insert blind and turn a transient DB blip into a rejection.
		return nil, err
	}
	if replay {
		return existing, nil
	}

	// Reject a malformed JSON payload rather than silently persisting NULL (the
	// metadata helper swallows the parse error), which would deliver a command
	// stripped of its arguments. Same for the metadata blob.
	if request.Payload != nil && !json.Valid([]byte(*request.Payload)) {
		return nil, rejected(RejectPayloadNotJSON, "command payload is not valid JSON")
	}
	if request.Metadata != nil && !json.Valid([]byte(*request.Metadata)) {
		return nil, rejected(RejectMetadataNotJSON, "command metadata is not valid JSON")
	}

	// Parse the optional TTL (RFC3339) — a cheap local check done before the remote
	// verification below, so a malformed request fails without a wasted round trip.
	var expiresAt sql.NullTime
	if request.ExpiresAt != nil {
		parsed, err := time.Parse(time.RFC3339, *request.ExpiresAt)
		if err != nil {
			// The offending value is the caller's own input, so echoing it is
			// client-safe and is the only way they can see what was wrong with it.
			return nil, rejected(RejectExpiresAtInvalid,
				"expiresAt %q is not an RFC3339 timestamp", *request.ExpiresAt)
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
	// fault and is relayed verbatim — now WITH the gate's own code, so a machine
	// caller can classify it — so the caller can fix the command. A failure to
	// perform the check (device-management unreachable) fails closed — a ghost or
	// unvalidated command is never persisted — but the detail is logged, not
	// returned, and it stays a plain ERROR rather than becoming a coded rejection,
	// so the tenant API client learns neither the in-cluster topology nor a verdict
	// nobody actually reached. Were these collapsed, an outage would read to the
	// client as "your command is invalid" and send them chasing a correct payload.
	if api.EnqueueValidator != nil {
		err := api.EnqueueValidator.ValidateEnqueue(ctx, request.DeviceToken, request.Name, payloadBytes(request.Payload))
		var verdict *EnqueueRejected
		switch {
		case errors.As(err, &verdict):
			// Relay the OWNER's code rather than re-deriving one here: this service
			// does not know why the vocabulary said no, and a second classification
			// built from the reason string would be a parse of prose. An uncoded
			// rejection is still a rejection, marked as unclassified rather than
			// guessed at.
			code := verdict.Code
			if code == "" {
				code = RejectUnclassified
			}
			return nil, rejected(code, "cannot enqueue command: %s", verdict.Reason)
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

	// The held-command ceiling and the insert run in ONE transaction, so the count
	// reads the same state the insert lands in (see heldCommandCeiling for where the
	// number comes from).
	//
	// Idempotent on the client-supplied token (ADR-042 per-tenant unique). ON CONFLICT DO NOTHING
	// (no target ⇒ any unique violation, so it matches the partial (tenant_id, token) index) turns a
	// repeat with an already-live token into a no-op instead of a unique-violation error; the caller
	// then reads back and receives the ORIGINAL command unchanged. This makes createCommand a safe
	// idempotency-key operation: a client — or the REACT dispatcher's at-least-once redelivery
	// (ADR-051 slice 5b), which derives a deterministic token per (detection, action) — can retry
	// with the same token without ever enqueuing a second physical command.
	var conflicted bool
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := api.checkHeldCeiling(ctx, tx); err != nil {
			return err
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(created)
		if result.Error != nil {
			return result.Error
		}
		conflicted = result.RowsAffected == 0
		return nil
	})
	if err != nil {
		return nil, err
	}
	if conflicted {
		// The token was claimed between this call's replay lookup and its insert — a
		// concurrent create with the same token. Return the existing row (idempotent
		// replay), not a fresh one. The request's other fields are intentionally ignored on replay —
		// the token IS the identity, so the first write wins and a differing re-request does not
		// mutate or duplicate it.
		return api.commandByToken(ctx, request.Token)
	}
	return created, nil
}

// checkHeldCeiling refuses an enqueue for a tenant already holding its ceiling of
// WITHHELD commands. It runs inside CreateCommand's insert transaction so the count
// reads the state the insert lands in.
//
// 🔴 IT COUNTS ONLY HELD, and that is the whole point of the state existing
// separately. HELD is where an absent fleet's backlog accumulates and where it can
// sit for days; QUEUED is transient (every row leaves it at the next sweep tick) and
// SENT is in flight. Counting those too would throttle a BUSY HEALTHY tenant — one
// whose commands are being delivered as fast as they are issued — for a backlog it
// does not have. Terminal rows are history and count for nothing.
//
// It is a count-then-insert, deliberately NOT serialized with a lock, following the
// same reasoning as device-management's MaxGeoFencesPerTenant: two simultaneous
// enqueues at the limit can both pass, leaving the tenant one over. That is
// tolerated. The ceiling exists to keep an offline fleet's backlog — and the
// unbounded read the delivery sweep performs over it — from growing without end;
// being one row over it for the instant between two concurrent calls does not
// threaten that, while a lock would serialize the hot enqueue path (every REACT
// send-command, every console issue) against a per-tenant row to buy a precision the
// bound does not need.
func (api *Api) checkHeldCeiling(ctx context.Context, tx *gorm.DB) error {
	ceiling := api.heldCommandCeiling(ctx)
	var held int64
	if err := tx.Model(&Command{}).Where("status = ?", CommandHeld.String()).Count(&held).Error; err != nil {
		// A failure to COUNT is a failure to decide, not a rejection: it stays a plain
		// error so the caller fails closed without telling the client its command is
		// invalid.
		return err
	}
	if held >= int64(ceiling) {
		return rejected(RejectHeldCeilingExceeded,
			"the tenant is already holding %d commands for absent devices; the limit is %d "+
				"(commands withheld for offline devices are released as the devices return)",
			held, ceiling)
	}
	return nil
}

// heldCommandCeiling resolves the ceiling in force for the calling tenant: the
// resolver's answer (per-tenant override → tier → platform default, folded upstream)
// when one is wired and the tenant is known, else this service's configured default,
// else the compiled-in platform default.
//
// 🔴 EVERY FALLBACK LANDS ON A NUMBER. A missing resolver, an unresolved tenant, a
// zero from either of them: all mean the PLATFORM DEFAULT, never unlimited. A
// governance ceiling whose absent value reads as "no ceiling" stops governing exactly
// when its authority is unreachable — the moment it is most needed.
func (api *Api) heldCommandCeiling(ctx context.Context) int {
	if api.HeldCeilingResolver != nil {
		if tenant, ok := core.TenantFromContext(ctx); ok {
			// A non-positive answer is not honoured. Zero would mean "this tenant may hold
			// nothing", which is an outage for every tenant on the instance rather than a
			// tighter bound — the inversion of fail-open, and the reverse of the mistake
			// the unlimited reading makes.
			if resolved := api.HeldCeilingResolver.Resolve(tenant); resolved > 0 {
				return resolved
			}
		}
	}
	if api.DefaultHeldCommandCeiling > 0 {
		return api.DefaultHeldCommandCeiling
	}
	return config.DefaultHeldCommandCeiling
}

// liveCommandByToken is the REPLAY PROBE: it reports whether this tenant already holds a live
// command under the token, without treating "no" as a failure.
//
// It is a Find with a limit rather than a First, and the difference is not stylistic. First returns
// gorm.ErrRecordNotFound for a miss, which gorm LOGS at error level — and on this path a miss is
// the common case (every first-time enqueue), so First would emit an error log line per command
// issued, teaching an operator to ignore the one that matters. Find reports the miss as
// RowsAffected==0, which is what it is: an answer, not a fault.
func (api *Api) liveCommandByToken(ctx context.Context, token string) (*Command, bool, error) {
	found := &Command{}
	result := api.RDB.DB(ctx).Where("token = ?", token).Limit(1).Find(found)
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, false, nil
	}
	return found, true, nil
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

// terminalStatusStrings is the wire form of the terminal states, for a
// "status NOT IN (…)" guard. One definition shared by every from-state-predicated
// update below and by ExpireStale, so the set the sweep skips and the set a
// transition guards against can never drift.
//
// 🔴 It is DERIVED from model.go's terminalStatuses, not typed out again.
// It used to be a second hand-written list beside CommandStatus.Terminal(), and
// two lists of the same set drift on the day a state is added: MarkResponse's
// fast path would treat a row as finished while this guard still let a sweep
// overwrite it, or the reverse. Deriving makes "add a terminal state" a
// one-place edit.
func terminalStatusStrings() []string {
	out := make([]string, 0, len(terminalStatuses))
	for _, s := range terminalStatuses {
		out = append(out, s.String())
	}
	return out
}

// dispatchableStatusStrings is the wire form of the states from which a command
// may still be dispatched: QUEUED (never yet considered) and HELD (considered,
// and deliberately withheld because the device was absent).
//
// It is the from-state guard for MarkSent and the selection predicate for
// PendingCommands, and those two MUST agree: a state the sweep hands out but
// cannot then mark sent is republished on every tick forever, and a state
// MarkSent accepts but the sweep never selects is a hold nothing drains.
func dispatchableStatusStrings() []string {
	return []string{CommandQueued.String(), CommandHeld.String()}
}

// MarkSent transitions a command QUEUED/HELD -> SENT.
//
// HELD is accepted alongside QUEUED because a hold is released by DISPATCHING it,
// not by first returning it to QUEUED: the release is the same publish the sweep
// performs for a fresh command, and routing it through an extra state would open
// a window in which the row is indistinguishable from one that had never been
// considered. This is also what stops a released hold from being published twice
// — the row leaves the dispatchable set at the moment it is sent.
//
// It is a from-state-predicated conditional UPDATE, not a load-modify-Save: only a
// still-dispatchable row advances, and only the status/sent_time columns are touched. A
// full-row Save would LOSE-UPDATE a response that raced in between the load and the
// write — the sweep publishes BEFORE marking SENT, so a device answering in
// milliseconds can drive the row to SUCCESSFUL (MarkResponse) while this write is
// delayed under load; a Save of the stale QUEUED snapshot would then clobber it back
// to SENT, wiping RespondedTime/ResponsePayload. Nothing recovers it: PendingCommands
// redelivers only QUEUED rows and the response was already consumed, so the row sits in
// SENT until its TTL drags it to TIMEOUT — a week, by default, reporting a failure that
// already succeeded. (It used to be genuinely permanent; the platform default TTL now
// stamps every command that does not carry its own, which bounds the lie without making
// it less of one.) RowsAffected==0 means the row already left
// the dispatchable set (a fast response, a concurrent sweep, a cancel) — a benign
// race, not an error; the current row is returned. (A deleted row surfaces as
// loadCommand's not-found.)
func (api *Api) MarkSent(ctx context.Context, id uint) (*Command, error) {
	res := api.RDB.DB(ctx).Model(&Command{}).
		Where("id = ? AND status IN ?", id, dispatchableStatusStrings()).
		Updates(map[string]any{
			"status":    CommandSent.String(),
			"sent_time": sql.NullTime{Time: time.Now(), Valid: true},
		})
	if res.Error != nil {
		return nil, res.Error
	}
	return api.loadCommand(ctx, id)
}

// MarkSentByToken is MarkSent addressed by the command's token rather than its
// primary key, for a dispatcher that holds the token and not the row.
//
// It exists for the LwM2M wake drain, which is a DISPATCHER the sweep does not
// perform: when a sleeping device registers, the drain reads its withheld
// commands and issues them over the live CoAP session directly. Without this the
// drain would leave those rows HELD, and the next sweep tick — by then seeing the
// device as present — would publish them a second time. A command is a physical
// actuation, so that second publish is a second movement of real hardware.
//
// Claim-THEN-dispatch is the ordering: the caller marks the row sent first and
// issues the op only if it won the claim, so a losing racer declines instead of
// re-actuating. The dispatcher also carries an in-memory recently-dispatched
// cache, but that is defence in depth — it is per-pod and TTL-bounded, so it
// cannot be the thing standing between a leadership change and a duplicate
// actuation.
//
// 🔴 BE PRECISE ABOUT WHAT THE CLAIM DOES AND DOES NOT CLOSE. It is structural
// against a LATER sweep tick: once the row leaves the dispatchable set, no
// subsequent PendingCommands read can return it. It is NOT structural against the
// tick already in flight. The sweep SELECTs its batch and then publishes each row
// in a loop, re-checking nothing in between, so a claim that lands after that
// SELECT does not stop the publish that follows it — and the sweep's own MarkSent
// then matches zero rows and is treated as a benign race. In that window the only
// thing left between the two dispatches is the per-pod dedupe cache, which is
// exactly the guarantee this comment must not overstate.
//
// It is harmless today because nothing writes HELD and the drain never fetches a
// QUEUED row, so the two paths select disjoint sets. It stops being harmless the
// moment the presence gate produces held rows. Closing it means the sweep claims
// before publishing too — which needs a way to release a claim whose publish then
// failed, since a marked-but-unpublished row is invisible to redelivery until its
// TTL. That decision belongs with the gate, and is recorded rather than left to be
// rediscovered as a bug.
//
// It reports whether THIS call performed the transition. RowsAffected==0 means
// the row was not dispatchable — already sent by the sweep, already answered, or
// cancelled — which is a benign race and not an error.
func (api *Api) MarkSentByToken(ctx context.Context, token string) (bool, error) {
	res := api.RDB.DB(ctx).Model(&Command{}).
		Where("token = ? AND status IN ?", token, dispatchableStatusStrings()).
		Updates(map[string]any{
			"status":    CommandSent.String(),
			"sent_time": sql.NullTime{Time: time.Now(), Valid: true},
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
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
		"responded_time": sql.NullTime{Time: time.Now(), Valid: true},
		// JSONTextOf, not MetadataStrOf: this value comes from the DEVICE, and the wire
		// contract for it is a plain string (responseEnvelope.Payload is *string). A device
		// answering "bucket raised" is answering correctly, so its text is encoded as a JSON
		// string rather than dropped — which is what MetadataStrOf would now do, and what
		// previously produced a JSON column write Postgres refused, stranding the command in
		// SENT while the sweep retried the same doomed UPDATE every minute.
		"response_payload": rdb.JSONTextOf(payload),
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

// CancelCommand cancels a non-terminal command by token, moving it to CANCELLED
// (QUEUED/HELD/SENT -> CANCELLED). A terminal command is returned unchanged. Like the
// other transitions it is a from-state-predicated conditional UPDATE so a cancel racing a
// device response does not clobber the response.
//
// It writes CANCELLED, not EXPIRED. The two were one value, which forced the docs
// to carry the line "cancelling a command also records EXPIRED" — an apology for a
// model that could not distinguish "the platform ran out of time" from "someone
// called it off". They are different ACTORS and an operator auditing a fleet needs
// to tell them apart: a run of EXPIRED means the platform is failing to deliver,
// while a run of CANCELLED means people keep changing their minds.
//
// 🔴 Rows cancelled before this change stay EXPIRED. There is no backfill and one
// would be a guess — nothing recorded which EXPIRED rows came from a cancel, which
// is precisely the information that was being lost.
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
		Updates(map[string]any{"status": CommandCancelled.String()}); res.Error != nil {
		return nil, res.Error
	}
	return api.loadCommand(ctx, found.ID)
}

// expiredTerminalFor maps the state a lapsed command was IN to the terminal it
// deserves. The distinction is the whole reason the platform tracks a hold
// separately from a dispatch:
//
//   - QUEUED / HELD -> EXPIRED. The command never went out. Its TTL elapsed while
//     the platform still had it — because the device was absent the entire time,
//     or because it was enqueued too close to its own horizon.
//   - SENT -> TIMEOUT. The command DID go out and the device never answered.
//
// Reporting the first case as TIMEOUT — which is what a single "not finished"
// state forces — blames the device for a delivery that was never attempted. For a
// fleet that is switched off overnight or over a weekend that is not an edge case,
// it is the common case, and it sends an operator looking for a fault in hardware
// that was behaving correctly by being off.
//
// An unrecognised status maps to TIMEOUT, preserving the pre-existing default. It
// should be unreachable: the only non-terminal states are the three above.
func expiredTerminalFor(status string) string {
	switch CommandStatus(status) {
	case CommandQueued, CommandHeld:
		return CommandExpired.String()
	default:
		return CommandTimeout.String()
	}
}

// ExpireStale times out every non-terminal command whose TTL has elapsed. A
// QUEUED or HELD command that never went out becomes EXPIRED; a SENT command
// that was never answered becomes TIMEOUT (see expiredTerminalFor). The caller
// MUST pass a system context (core.WithSystemContext) so the sweep spans all
// tenants.
//
// It returns the number of commands expired AND a breakdown keyed by the state
// each command lapsed FROM, because those counts mean opposite things
// operationally: rows dying out of HELD say the fleet is absent and the platform
// never got to try, while rows dying out of SENT say devices are being reached
// and are not answering. One total reports both as "expiry is happening", which
// points an operator at the wrong half of the system.
func (api *Api) ExpireStale(ctx context.Context, now time.Time) (int64, map[string]int64, error) {
	stale := make([]*Command, 0)
	terminal := terminalStatusStrings()
	byFromStatus := make(map[string]int64)
	result := api.RDB.DB(ctx).
		Where("status NOT IN ?", terminal).
		Where("expires_at IS NOT NULL AND expires_at < ?", now).
		Find(&stale)
	if result.Error != nil {
		return 0, byFromStatus, result.Error
	}

	var count int64
	for _, cmd := range stale {
		next := expiredTerminalFor(cmd.Status)
		affected, err := api.expireOne(ctx, cmd.ID, cmd.Status, next)
		if err != nil {
			return count, byFromStatus, err
		}
		// Count against the state it lapsed FROM, and only when the conditional update
		// actually landed — a row that moved on between the scan and the write was
		// not expired by us and must not be reported as though it were.
		if affected > 0 {
			byFromStatus[cmd.Status] += affected
		}
		count += affected
	}
	return count, byFromStatus, nil
}

// expireOne applies one expiry, from the exact state the scan observed.
//
// Split out so the from-state predicate is directly testable: the race it guards
// against — the row moving on between ExpireStale's SELECT and its UPDATE — cannot
// be staged through the public API, but it can be staged here by writing a
// different status to the row and then asking this to expire the stale one.
//
// Returns the number of rows it actually transitioned, which is 0 when it lost.
func (api *Api) expireOne(ctx context.Context, id uint, fromStatus, next string) (int64, error) {
	// Conditional update: only expire a command that is still in the EXACT state
	// the scan saw, and touch only the status column — never a full-row Save of
	// the pre-response snapshot. A device response (MarkResponse) that landed
	// since the scan made the command terminal, so this WHERE misses and the
	// response is preserved instead of being overwritten back to TIMEOUT/EXPIRED.
	//
	// 🔴 It pins the SCANNED status, not merely "still non-terminal", and the
	// difference is load-bearing now that the terminal depends on how far the
	// command got. A row that moved HELD→SENT between the scan and this write — a
	// wake drain claiming it, or the delivery sweep publishing it — would otherwise
	// be stamped EXPIRED ("never dispatched") microseconds after it was physically
	// dispatched, and the device's imminent answer would then be dropped by
	// MarkResponse's terminal guard: a lost response AND a mis-attributed count.
	// Pinning the from-state makes this write lose the race instead, leaving the row
	// live to expire on a later pass with the terminal its new state deserves.
	res := api.RDB.DB(ctx).Model(&Command{}).
		Where("id = ? AND status = ?", id, fromStatus).
		Update("status", next)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
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
		// ANDed with Status above, per CommandSearchCriteria's contract. An empty
		// slice is "no filter", not "match nothing" — gorm renders an empty IN as a
		// NULL comparison, so honouring it literally would make the result depend on
		// the driver rather than on the query.
		if criteria.Statuses != nil && len(*criteria.Statuses) > 0 {
			db = db.Where("status IN ?", *criteria.Statuses)
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

// PendingCommands returns every still-dispatchable command — QUEUED or HELD —
// oldest first. It is the redelivery worker's source; the caller passes a system
// context for the cross-tenant sweep.
//
// HELD rows are included because the sweep is what NOTICES that a held command can
// now go out: it is the recurring pass that re-reads presence, so a hold placed
// while a device was absent is reconsidered on the next tick after it returns. A
// hold the sweep could not see would be a hold nothing drains — it would sit until
// its TTL regardless of the device coming back. The decision to publish or keep
// holding belongs to the caller; this read only says which rows are still in play.
// (dispatchableStatusStrings is shared with MarkSent so the set handed out and the
// set that can be marked sent cannot drift apart.)
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
	result := api.RDB.DB(ctx).Where("status IN ?", dispatchableStatusStrings()).
		Order("id ASC").Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

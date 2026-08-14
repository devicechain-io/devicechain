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
	// BatchValidator is EnqueueValidator's fleet counterpart, gating CreateCommandBatch.
	// It is a separate interface rather than a method on the same one because the two
	// answer differently shaped questions — one verdict versus a refusal list — and
	// because a batch that fell back to looping the single validator would issue one
	// network round trip per device, which is the cost the batch gate exists to remove.
	BatchValidator BatchEnqueueValidator
	// GroupTargetResolver, when set, resolves a group-targeted batch to its devices.
	// Nil makes a group target FAIL rather than resolve to nothing: an empty batch would
	// report success for a fleet write that reached nobody.
	GroupTargetResolver GroupTargetResolver
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

	// MarkSent claims a command for dispatch, reporting whether THIS caller won it.
	// Claim before dispatching: a command is a physical actuation.
	MarkSent(ctx context.Context, id uint) (bool, error)
	// ReleaseClaim undoes a claim whose dispatch then failed, returning the row to
	// QUEUED and clearing sent_time.
	ReleaseClaim(ctx context.Context, id uint) (bool, error)
	// HoldCommand withholds a queued command whose device is authoritatively absent.
	HoldCommand(ctx context.Context, id uint) (bool, error)
	// MarkUndeliverable fails a queued command whose transport carries no command path.
	MarkUndeliverable(ctx context.Context, id uint, reason string) (bool, error)
	// ReleaseHold returns a withheld command to QUEUED for the sweep to reconsider.
	ReleaseHold(ctx context.Context, id uint) (bool, error)
	// ReleaseHeldForDevice returns one device's withheld commands to QUEUED — the wake.
	ReleaseHeldForDevice(ctx context.Context, deviceToken string) (int64, error)
	// HeldCommands walks the withheld set a bounded page at a time.
	HeldCommands(ctx context.Context, afterId uint, limit int) ([]*Command, uint, error)
	// TryReconcileLock serializes the hold-reconcile pass across replicas.
	TryReconcileLock(ctx context.Context, fn func() error) (bool, error)
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
		if err := api.checkUndeliveredCeiling(ctx, tx); err != nil {
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

// checkUndeliveredCeiling refuses an enqueue for a tenant that already has its ceiling
// of commands waiting to go out. It runs inside CreateCommand's insert transaction so
// the count reads the state the insert lands in.
//
// SENT is excluded: it is in flight, the platform has done its part, and a device that
// never answers is bounded by the command's own TTL rather than by this. Terminal rows
// are history and count for nothing.
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
//
// 🔑 IT COUNTS EVERY UNDELIVERED COMMAND, NOT ONLY THE HELD ONES, AND THAT IS WHAT
// MAKES ONE ENFORCEMENT POINT SUFFICIENT. Counting HELD alone was a bound on the
// wrong set: a row does not enter HELD at enqueue — it is inserted QUEUED and the
// presence gate promotes it on a later sweep tick — so the counted set did not grow
// as a tenant enqueued. A tenant whose fleet was absent could enqueue without limit,
// every check passing, and a SINGLE sweep tick could convert the whole backlog at
// once. That was a hole in the bound, not a rounding error.
//
// Counting QUEUED + HELD closes it by making the counted set INVARIANT UNDER
// PROMOTION: QUEUED→HELD moves a row between two counted states, so it cannot change
// the count, so no sweep tick can overshoot for any backlog size. The residual is
// back to the tolerated one-over race described above — which this may now cite
// honestly, because the two are finally the same kind of thing.
//
// The consequence to know rather than discover: a tenant whose fleet is PRESENT now
// counts its in-flight QUEUED rows too. Those drain within a tick, so the
// steady-state contribution is one tick of enqueue volume — small, but not zero, and
// a tenant enqueueing near its ceiling at a high rate will feel it. That is the
// correct behaviour: the bound is on undelivered work, and undelivered is undelivered.
func (api *Api) checkUndeliveredCeiling(ctx context.Context, tx *gorm.DB) error {
	ceiling := api.heldCommandCeiling(ctx)
	var undelivered int64
	if err := tx.Model(&Command{}).
		Where("status IN ?", undeliveredStatusStrings()).Count(&undelivered).Error; err != nil {
		// A failure to COUNT is a failure to decide, not a rejection: it stays a plain
		// error so the caller fails closed without telling the client its command is
		// invalid.
		return err
	}
	if undelivered >= int64(ceiling) {
		return rejected(RejectHeldCeilingExceeded,
			"the tenant already has %d commands waiting to be delivered; the limit is %d "+
				"(commands are released as their devices become reachable, and expire if they do not)",
			undelivered, ceiling)
	}
	return nil
}

// undeliveredStatusStrings is the set the tenant ceiling bounds: commands accepted by
// the platform that have not gone out yet. QUEUED is "not yet gated"; HELD is "gated,
// waiting for the device". Both are the tenant holding work open, which is the thing
// being limited.
//
// 🔴 THIS IS DELIBERATELY A SEPARATE LIST FROM claimableStatusStrings(), even though
// the two hold the same values today. They answer different questions — "what does the
// tenant have outstanding" versus "what may a dispatcher claim" — and the day a state
// belongs to one and not the other, a shared helper would silently change both.
func undeliveredStatusStrings() []string {
	return []string{CommandQueued.String(), CommandHeld.String()}
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

// claimableStatusStrings is the wire form of the states a DISPATCHER may claim a
// command from: QUEUED (never yet considered) and HELD (considered, and deliberately
// withheld because the device was absent). It is the from-state guard for MarkSent.
//
// 🔴 IT IS NO LONGER THE SWEEP'S SELECTION PREDICATE, AND THE SPLIT IS THE POINT.
// These were one helper while the two sets genuinely coincided, and collapsing them
// then was correct. They have now come apart: the sweep stops scanning HELD (see
// sweepableStatusStrings), while a claim must still accept HELD because the paths that
// release a hold — the LwM2M wake drain, and the wake handler — claim held rows
// directly.
//
// Do NOT re-merge these because they happen to list the same values today. They answer
// different questions — "what may a dispatcher claim" versus "what does the sweep hand
// out" — and a shared helper would silently move both the day one of them changes.
func claimableStatusStrings() []string {
	return []string{CommandQueued.String(), CommandHeld.String()}
}

// sweepableStatusStrings is the wire form of what the periodic delivery sweep selects:
// QUEUED only.
//
// 🔑 HELD IS DELIBERATELY ABSENT, AND THAT IS THE WHOLE POINT OF THE GATE. Polling for
// "has this device come back yet" asks a question the transports already answer by
// telling us; the sweep only ever scanned held rows because nothing was listening. A
// held backlog is released by the device's return, not by re-reading it every 30
// seconds — which, for a multi-day offline fleet, is the entire backlog loaded and
// iterated by the leader forever.
//
// This is also what keeps the sweep's unbounded read defensible: its selection is once
// again one tick of QUEUED churn, which is what that argument was written for.
func sweepableStatusStrings() []string {
	return []string{CommandQueued.String()}
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
// to SENT, wiping RespondedTime/ResponsePayload. Nothing recovers it: the sweep selects
// QUEUED rows and nothing returns a SENT row to QUEUED except ReleaseClaim, which the
// dispatcher calls only on its own failed publish — and the response was
// already consumed, so the row sits in SENT until its TTL drags it to TIMEOUT — a week, by
// default, reporting a failure that already succeeded. (It used to be genuinely permanent;
// the platform default TTL now stamps every command that does not carry its own, which
// bounds the lie without making it less of one.) RowsAffected==0 means the row already left
// the dispatchable set (a fast response, a concurrent sweep, a cancel) — a benign
// race, not an error; the current row is returned. (A deleted row surfaces as
// loadCommand's not-found.)
func (api *Api) MarkSent(ctx context.Context, id uint) (bool, error) {
	res := api.RDB.DB(ctx).Model(&Command{}).
		Where("id = ? AND status IN ?", id, claimableStatusStrings()).
		Updates(map[string]any{
			"status":    CommandSent.String(),
			"sent_time": sql.NullTime{Time: time.Now(), Valid: true},
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// ReleaseClaim returns a claimed-but-undispatched command to QUEUED, clearing the
// sent_time the claim stamped. It reports whether this caller's release landed.
//
// 🔴 IT IS THE OTHER HALF OF CLAIM-BEFORE-PUBLISH, AND WITHOUT IT THE CLAIM IS A LEAK.
// A dispatcher that claims a row and then fails to publish has produced a row that is
// SENT with a sent_time, invisible to every dispatcher (SENT is not claimable and the
// sweep does not select it), which dies TIMEOUT — "delivered, and the device never
// answered" — for a command that was never sent. That is precisely the lie the whole
// command-state vocabulary exists to stop telling.
//
// 🔑 THE TARGET IS QUEUED, NOT HELD, and the distinction matters. A failed publish says
// nothing about presence: the device was present enough a moment ago for this row to be
// dispatched at all. QUEUED sends it back through the presence gate on the next tick,
// which re-evaluates and either dispatches or holds it. HELD would skip that evaluation
// and park a deliverable command behind a wake that may never come, because the device
// never left.
//
// The from-state predicate is what makes this safe to run after a failure whose outcome
// is uncertain. If the publish actually landed and only its acknowledgement was lost,
// the device may already have answered and moved the row to a terminal state — the
// release then matches nothing and correctly does nothing.
func (api *Api) ReleaseClaim(ctx context.Context, id uint) (bool, error) {
	res := api.RDB.DB(ctx).Model(&Command{}).
		Where("id = ? AND status = ?", id, CommandSent.String()).
		Updates(map[string]any{
			"status":    CommandQueued.String(),
			"sent_time": sql.NullTime{},
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// HoldCommand withholds a queued command because its device is authoritatively absent,
// reporting whether THIS caller performed the transition.
//
// 🔴 THE FROM-STATE PREDICATE IS NOT DEFENSIVE POLISH — WITHOUT IT THIS RE-OPENS THE
// DOUBLE-ACTUATION HOLE claim-before-publish was built to close. The sweep SELECTs its
// batch and then walks it, so a row it observed as QUEUED can be claimed by another
// dispatcher — the LwM2M wake drain — while the walk is still in progress. An
// unconditional write would then stamp HELD over a row that was claimed SENT
// microseconds earlier and physically dispatched: the command becomes claimable again,
// the next tick or drain sends it a second time, and a device dispenses, unlocks or
// reboots twice. Pinning QUEUED makes this write LOSE that race instead.
//
// It pins QUEUED specifically rather than "still non-terminal" for the same reason
// expireOne pins its scanned status: HELD is already held (a no-op worth distinguishing
// from a win), and SENT is a command in flight, which must never be dragged back into
// the withheld set.
//
// A false return is a benign race, not an error — the row left QUEUED between the scan
// and this write, and whoever moved it is welcome to it.
func (api *Api) HoldCommand(ctx context.Context, id uint) (bool, error) {
	res := api.RDB.DB(ctx).Model(&Command{}).
		Where("id = ? AND status = ?", id, CommandQueued.String()).
		Update("status", CommandHeld.String())
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// MarkUndeliverable drives a queued command to FAILED because its transport cannot carry
// a command at all, recording the reason. It reports whether THIS caller performed the
// transition.
//
// 🔑 THE ALTERNATIVE — HOLDING — WOULD BE A DIFFERENT LIE, NOT A SAFER ONE. HELD means
// "waiting for the device to come back", and for a transport with no command path that is
// false in a way that never resolves: the device does come back, and delivery still cannot
// happen. The row would then sit until its TTL dragged it to EXPIRED ("the platform ran
// out of time"), occupying the tenant's undelivered ceiling the whole time and letting one
// such fleet crowd out commands to devices that CAN receive them. FAILED with a reason is
// the honest terminal, and it is reached in seconds rather than days.
//
// The reason is written to the same Error column a device's own failure response uses.
// That is deliberate: from the caller's side both mean "this command will not happen, and
// here is why", and a second column for platform-side failures would need every reader to
// know which one to look at.
//
// From-state predicated on QUEUED, for exactly the reason HoldCommand is.
func (api *Api) MarkUndeliverable(ctx context.Context, id uint, reason string) (bool, error) {
	res := api.RDB.DB(ctx).Model(&Command{}).
		Where("id = ? AND status = ?", id, CommandQueued.String()).
		Updates(map[string]any{
			"status": CommandFailed.String(),
			"error":  sql.NullString{String: reason, Valid: true},
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// ReleaseHold returns a withheld command to QUEUED so the next delivery sweep
// reconsiders it, reporting whether THIS caller performed the transition.
//
// It is the inverse of HoldCommand and predicated on HELD for the mirror-image reason:
// a row that has since been claimed, answered or cancelled must not be dragged back into
// the dispatchable set.
//
// 🔑 IT RELEASES TO QUEUED RATHER THAN DISPATCHING DIRECTLY, so there stays exactly ONE
// place that decides whether a command goes out. The releaser's question is only "is this
// device still absent?"; everything else — the device turning out to be on a transport
// that cannot carry commands, the tenant having been deleted, the ceiling, the claim — is
// the sweep's, and it re-asks all of it with a fresh presence read on the next tick. A
// release path that published would be a second dispatcher with its own copy of those
// rules, which is how the two answers drift apart.
func (api *Api) ReleaseHold(ctx context.Context, id uint) (bool, error) {
	res := api.RDB.DB(ctx).Model(&Command{}).
		Where("id = ? AND status = ?", id, CommandHeld.String()).
		Update("status", CommandQueued.String())
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// ReleaseHeldForDevice returns every command withheld for one device to QUEUED, reporting
// how many it released. It is the wake: the transport that owns the device's connection
// telling us the wait is over.
//
// 🔑 ONE STATEMENT, NOT A WALK. The reconciler pages because it scans a whole instance's
// withheld set on a timer; this one already knows the device, so the set is small, bounded
// by that device's share of its tenant's ceiling, and addressable by an indexed predicate.
// A read-then-release loop here would add a round trip and a race for nothing.
//
// Predicated on HELD for the same reason every other gate write is predicated: a row that
// has since been claimed, answered or cancelled must not be dragged back into the
// dispatchable set. Rows the caller does not own are excluded by the tenant scope the
// callbacks add, so a wake for one tenant's device cannot release another's.
//
// It releases to QUEUED rather than dispatching, which keeps ONE place deciding whether a
// command goes out — the sweep, which re-reads presence and re-applies the ADR-077
// lifecycle gate and the transport verdict. So the wake is an optimisation of WHEN the
// question is asked, never of what the answer is.
func (api *Api) ReleaseHeldForDevice(ctx context.Context, deviceToken string) (int64, error) {
	res := api.RDB.DB(ctx).Model(&Command{}).
		Where("device_token = ? AND status = ?", deviceToken, CommandHeld.String()).
		Update("status", CommandQueued.String())
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// HeldCommands returns a bounded page of withheld commands with an id above afterId,
// oldest first, together with the id to resume from. A returned cursor of 0 means the
// walk reached the end and the next pass should start over.
//
// 🔴 THIS ONE MUST BE PAGED WHERE PendingCommands MUST NOT BE, and the difference is the
// size of the set rather than a difference of opinion about caps. QUEUED is transient —
// every row leaves it at the next tick — so an uncapped read is one tick of churn. HELD
// is where an absent fleet's backlog ACCUMULATES, bounded only by each tenant's ceiling
// times the number of tenants, and it can sit for days.
//
// 🔑 A PLAIN `LIMIT n` WOULD WEDGE, which is the trap PendingCommands' own comment
// describes and the reason this takes a cursor instead. With oldest-first ordering, the
// commands of a device that is never coming back hold the smallest ids and would
// therefore fill EVERY page forever — so the devices that DID return, holding higher ids,
// would never be looked at, and the net would silently stop being a net. Walking from a
// cursor visits every row within a few passes regardless of what is stuck at the front.
//
// The cursor is the caller's to hold, deliberately: it is a position in a walk, not a
// fact about the data, and a per-pod one that resets on restart merely restarts the walk.
func (api *Api) HeldCommands(ctx context.Context, afterId uint, limit int) ([]*Command, uint, error) {
	found := make([]*Command, 0)
	result := api.RDB.DB(ctx).
		Where("status = ? AND id > ?", CommandHeld.String(), afterId).
		Order("id ASC").Limit(limit).Find(&found)
	if result.Error != nil {
		return nil, 0, result.Error
	}
	// A short page means the walk is done; answering 0 restarts it. Answering the last
	// id instead would leave the cursor parked past the end, and every subsequent pass
	// would read nothing at all — a reconciler that quietly stops reconciling.
	if len(found) < limit {
		return found, 0, nil
	}
	return found, found[len(found)-1].ID, nil
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
// 🔑 THE SWEEP NOW CLAIMS BEFORE IT PUBLISHES, which is what closes the LATER-tick
// half of this properly rather than by the two paths happening to select disjoint
// sets — they no longer do, since the presence gate produces held rows and the
// reconciler returns them to QUEUED. What remains open is only the in-flight tick
// described above, and it is narrow: both dispatchers claim first, so the loser
// declines; the residue is a publish already in progress when the claim lands.
//
// It reports whether THIS call performed the transition. RowsAffected==0 means
// the row was not dispatchable — already sent by the sweep, already answered, or
// cancelled — which is a benign race and not an error.
func (api *Api) MarkSentByToken(ctx context.Context, token string) (bool, error) {
	res := api.RDB.DB(ctx).Model(&Command{}).
		Where("token = ? AND status IN ?", token, claimableStatusStrings()).
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

	for _, cmd := range stale {
		expired, err := api.expireOne(ctx, cmd.ID, cmd.Status, expiredTerminalFor(cmd.Status))
		if err != nil {
			return sumCounts(byFromStatus), byFromStatus, err
		}
		// Count against the state it lapsed FROM, and only when the conditional update
		// actually landed — a row that moved on between the scan and the write was
		// not expired by us and must not be reported as though it were. The total is
		// DERIVED from this breakdown rather than accumulated beside it: two counters
		// with separately-written rules can disagree, and one that has drifted from
		// the other is indistinguishable from a correct one at a glance.
		if expired {
			byFromStatus[cmd.Status]++
		}
	}
	return sumCounts(byFromStatus), byFromStatus, nil
}

// sumCounts totals an expiry breakdown. ExpireStale reports both because they answer
// different operational questions, but only the breakdown is measured — the total is
// whatever it adds up to, by construction.
func sumCounts(byFromStatus map[string]int64) int64 {
	var total int64
	for _, n := range byFromStatus {
		total += n
	}
	return total
}

// expireOne applies one expiry, from the exact state the scan observed.
//
// Split out so the from-state predicate is directly testable: the race it guards
// against — the row moving on between ExpireStale's SELECT and its UPDATE — cannot
// be staged through the public API, but it can be staged here by writing a
// different status to the row and then asking this to expire the stale one.
//
// It reports whether THIS call performed the transition — false when it lost the race
// to a row that moved on after the scan, which is benign and not an error.
func (api *Api) expireOne(ctx context.Context, id uint, fromStatus, next string) (bool, error) {
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
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
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

// reconcileLockName namespaces the hold-reconcile pass's own advisory lock.
//
// 🔑 ITS OWN LOCK, NOT THE SWEEP'S, and the two are safe to run at once. Sharing would
// serialize a slow minutes-cadence walk of the withheld set against the 30-second
// delivery sweep, so a long reconcile pass would starve delivery — the fast path losing
// to the slow one, which is the wrong way round. They can overlap because every write
// on both paths is predicated on the exact state its scan observed: a release that
// races a claim, or a hold that races a release, matches zero rows and does nothing.
// The lock is here only to stop N replicas doing the SAME walk, which would be wasted
// reads rather than duplicate actuations — the reconcile publishes nothing.
const reconcileLockName = "command-delivery-hold-reconcile"

// TryReconcileLock runs fn while holding the cross-replica reconcile lock, reporting
// whether it ran. Like TrySweepLock it does not wait.
func (api *Api) TryReconcileLock(ctx context.Context, fn func() error) (bool, error) {
	return api.RDB.TryAdvisoryLock(ctx, rdb.AdvisoryLockKey(reconcileLockName), fn)
}

// PendingCommands returns every QUEUED command, oldest first. It is the delivery
// sweep's source; the caller passes a system context for the cross-tenant read.
//
// 🔑 HELD ROWS ARE DELIBERATELY EXCLUDED, and it is the presence gate that makes that
// safe. A held command is one the platform has decided not to send yet, so re-reading
// the whole withheld set every 30 seconds would be asking "has this device come back?"
// on a schedule, when the transports already answer that by telling us. For a fleet
// offline over a weekend it means an entire multi-day backlog re-read on every tick.
// Held rows leave HELD by a different door: the reconcile net's slower pass, and — once
// it exists — a wake on the device's return.
//
// (The set selected here is a SUBSET of what MarkSent will claim from — the sweep hands
// out QUEUED, while a claim also accepts HELD for the release paths. Subset, not
// equality: a state the sweep hands out but cannot then mark sent would be republished
// on every tick forever, which is what must never happen.)
//
// The ORDER BY is a strict improvement over the previous unordered read: delivery
// now follows enqueue order instead of whatever the planner returned.
//
// It is deliberately NOT capped. 🔑 THAT ARGUMENT SURVIVES ONLY BECAUSE THIS SELECTS
// QUEUED ALONE — see above. What is left is one tick of enqueue churn, which is the
// thing the reasoning below was written about. (HeldCommands reads the set that DOES
// accumulate, which is why that one is paged.)
//
// A naive `LIMIT n` makes that worse, not
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
	result := api.RDB.DB(ctx).Where("status IN ?", sweepableStatusStrings()).
		Order("id ASC").Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

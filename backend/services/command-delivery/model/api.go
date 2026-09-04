// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-command-delivery/config"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/google/uuid"
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
	// DeliveryMachineryReserve is the share of the ceiling in force that only a platform
	// service token may draw on, so a fleet write cannot consume the whole ceiling and
	// leave every automated send-command for that tenant refused. Zero (an Api built
	// directly by a test) means the platform reserve, never "no reserve" — see
	// governance.RestrictedCommandLimit, which owns that rule.
	DeliveryMachineryReserve float64
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
	// BatchMetrics, when set, counts fleet-write outcomes. Nil is a fully supported
	// state and is what every test gets: promauto registers globally and panics on a
	// duplicate, so an Api built by literal must not register anything. Every recorder
	// tolerates a nil receiver — see NewBatchMetrics.
	BatchMetrics *BatchMetrics
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

	// MarkSent claims a command for dispatch, reporting whether THIS caller won it and
	// the dispatch nonce it stamped. Claim before dispatching: a command is a physical
	// actuation. The nonce must be carried in the published envelope so a transport can
	// park THIS dispatch and no other.
	MarkSent(ctx context.Context, id uint) (string, bool, error)
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
	// TryStrandedLock serializes the stranded-SENT reconcile pass across replicas.
	TryStrandedLock(ctx context.Context, fn func() error) (bool, error)
	// ParkClaim retires a claimed command whose transport could not reach the device,
	// naming the dispatch it observed so it cannot retire a newer one. It reports the
	// status the row landed on as well as whether it moved.
	ParkClaim(ctx context.Context, token, nonce string) (CommandStatus, bool, error)
	// StrandedSentCommands walks the commands stuck in SENT past the grace horizon.
	StrandedSentCommands(ctx context.Context, cursor StrandedCursor, olderThan time.Time,
		limit int) ([]*Command, StrandedCursor, error)
	MarkSentByToken(ctx context.Context, token string) (bool, error)
	MarkResponse(ctx context.Context, commandToken, responder string, success bool, payload *string, errMsg *string) (*Command, error)
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

	// Reject a malformed JSON payload here, with a code, rather than letting it reach the
	// column helper — which would deliver a command stripped of its arguments. Same for the
	// metadata blob. This guard predates rdb.JSONInputOf returning an error and is no longer
	// the only thing standing between a bad payload and the column, but it is still the
	// layer that names WHICH rejection it is, and it runs before the remote verification
	// below rather than after it.
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

	// 🔴 CODED, NOT BARE, EVEN THOUGH THE GUARD ABOVE MAKES THESE UNREACHABLE TODAY. Every
	// client-fault refusal in this service carries a RejectionCode so rejectionCodeOf and
	// recordBatchOutcome can classify it and the documented rejection table stays true. A
	// bare error here would be a trap rather than a bug: the day somebody reads the two
	// checks as duplication and deletes the earlier one, malformed payloads would start
	// producing uncoded errors, PAYLOAD_NOT_JSON would quietly vanish from the metrics, and
	// nothing would fail. Agreeing with the layer above costs one line each.
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, rejected(RejectMetadataNotJSON, "command metadata is not valid JSON")
	}
	payloadJSON, err := rdb.JSONInputOf("payload", request.Payload)
	if err != nil {
		return nil, rejected(RejectPayloadNotJSON, "command payload is not valid JSON")
	}
	created := &Command{
		TokenReference: rdb.TokenReference{
			Token: request.Token,
		},
		MetadataEntity: rdb.MetadataEntity{
			Metadata: metadataJSON,
		},
		DeviceToken: request.DeviceToken,
		Name:        request.Name,
		Payload:     payloadJSON,
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
		// The token was claimed between this call's replay lookup and its insert. Re-probe
		// through the SAME provenance-aware lookup rather than reading the row back
		// blindly: if a client's concurrent create claimed it, this is an ordinary
		// idempotent replay and the existing row is the answer. The request's other fields
		// are intentionally ignored — the token IS the identity, so the first write wins
		// and a differing re-request does not mutate or duplicate it.
		claimed, found, err := api.liveCommandByToken(ctx, request.Token)
		if err != nil {
			return nil, err
		}
		if found {
			return claimed, nil
		}
		// Nothing a client owns holds the token, yet the insert conflicted — so the row
		// occupying it is one the platform minted for a batch. Refusing is the only honest
		// answer: returning that command would hand this caller a different device's
		// actuation as though it were their own, and silently succeeding would report a
		// command that was never created.
		return nil, rejected(RejectTokenInUse,
			"command token %q is already in use", request.Token)
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
// same reasoning as device-management's geofence-count ceiling: two simultaneous
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
// Counting every state in which the platform still holds the command — QUEUED, HELD and
// PARKED — closes it by making the counted set INVARIANT UNDER
// PROMOTION: QUEUED→HELD moves a row between two counted states, so it cannot change
// the count, and so does the promotion PARKED closed — QUEUED→SENT→PARKED, which for a
// queue-mode fleet completes within a tick and would otherwise have drained the counted
// set on every one. No sweep tick can overshoot for any backlog size. The residual is
// back to the tolerated one-over race described above — which this may now cite
// honestly, because the two are finally the same kind of thing.
//
// The consequence to know rather than discover: a tenant whose fleet is PRESENT now
// counts its in-flight QUEUED rows too. Those drain within a tick, so the
// steady-state contribution is one tick of enqueue volume — small, but not zero, and
// a tenant enqueueing near its ceiling at a high rate will feel it. That is the
// correct behaviour: the bound is on undelivered work, and undelivered is undelivered.
func (api *Api) checkUndeliveredCeiling(ctx context.Context, tx *gorm.DB) error {
	limit, ceiling := api.effectiveUndeliveredLimit(ctx)
	var undelivered int64
	if err := tx.Model(&Command{}).
		Where("status IN ?", undeliveredStatusStrings()).Count(&undelivered).Error; err != nil {
		// A failure to COUNT is a failure to decide, not a rejection: it stays a plain
		// error so the caller fails closed without telling the client its command is
		// invalid.
		return err
	}
	if undelivered >= int64(limit) {
		return rejected(RejectHeldCeilingExceeded, "%s", undeliveredRefusalReason(undelivered, limit, ceiling))
	}
	return nil
}

// undeliveredRefusalReason explains a ceiling refusal in terms of the limit that ACTUALLY
// applied to this caller.
//
// 🔴 QUOTING THE CEILING WHEN THE RESERVE IS WHAT BOUND YOU IS A LIE THE CALLER CANNOT
// DEBUG: it reports 8,000 outstanding against a limit of 10,000 and refuses anyway. So the
// reserved share is named whenever it is the thing in force — and only then, because for a
// service-token caller no reserve applied and mentioning one would be equally misleading.
func undeliveredRefusalReason(undelivered int64, limit, ceiling int) string {
	const tail = "(commands are released as their devices become reachable, and expire if they do not)"
	if limit < ceiling {
		return fmt.Sprintf("the tenant already has %d commands waiting to be delivered; the limit "+
			"for this caller is %d, because %d of the tenant's ceiling of %d is reserved for command "+
			"delivery %s", undelivered, limit, ceiling-limit, ceiling, tail)
	}
	return fmt.Sprintf("the tenant already has %d commands waiting to be delivered; the limit is %d %s",
		undelivered, limit, tail)
}

// undeliveredStatusStrings is the set the tenant ceiling bounds: commands accepted by
// the platform that have not gone out yet. QUEUED is "not yet gated"; HELD is "gated,
// waiting for the device"; PARKED is "dispatched, found the device unreachable, still
// held". All three are the tenant holding work open, which is the thing being limited.
//
// 🔴 PARKED IS COUNTED FOR THE REASON QUEUED IS COUNTED, AND LEAVING IT OUT WOULD HAVE
// RE-OPENED THE HOLE checkUndeliveredCeiling DESCRIBES ABOVE. That comment's argument is
// that the counted set must be INVARIANT UNDER PROMOTION, or a tenant enqueues freely
// while rows drain out of the count. QUEUED→SENT→PARKED is exactly such a promotion, and
// it completes within a sweep tick for a queue-mode fleet: publish, find the device
// asleep, park. Uncounted, a tenant with a sleeping fleet could fill its ceiling, watch
// the count fall back to zero, and refill — every tick, with nothing but the command TTL
// bounding the backlog.
//
// It was tempting to leave PARKED out on the grounds that SENT is out and PARKED is where
// those rows used to sit. That is the wrong comparator: SENT is excluded because the work
// is no longer the platform's to hold — it is at the device. A parked command is the
// platform's to hold, by definition. Undelivered is undelivered.
//
// 🔴 THIS IS DELIBERATELY A SEPARATE LIST FROM claimableStatusStrings() and
// cancellableStatusStrings(), even where the values overlap. They answer different
// questions — "what does the tenant have outstanding", "what may a dispatcher claim",
// "what may be called off" — and the day a state belongs to one and not the others, a
// shared helper would silently change all of them.
func undeliveredStatusStrings() []string {
	return []string{CommandQueued.String(), CommandHeld.String(), CommandParked.String()}
}

// effectiveUndeliveredLimit is how many undelivered commands THIS CALLER may hold, and
// the ceiling it was derived from. Both paths that bound enqueue — the single-command
// check and the batch headroom — resolve through here, which is the only reason one
// reserve can hold: a reserve enforced on the batch path alone is walked around by a
// client looping createCommand, which is precisely the fleet write it exists to bound.
//
// The split is by TOKEN CLASS, and the distinction already exists in the token rather
// than being invented here: a platform service token (ADR-044's machine tier — REACT's
// send-command sink is the one that matters) may use the whole ceiling. Everything else
// — console, /dash, the SDKs, dcctl, the simulator, and a tenant's own integration — is
// bounded by the ceiling less the reserve.
//
// 🔴 NO CLAIMS MEANS RESTRICTED. An unauthenticated or claims-less context is not a
// machine caller; treating "I could not tell" as machinery would make the reserve
// vanish exactly where identity is least established. On the data plane the handler
// admits only access and service tokens and refuses the rest, and CommandWrite is
// checked before this is ever reached — so the claims-less branch is defence in depth
// rather than a live path, which is exactly why it needs a test of its own.
//
// ⚠️ The single-command path counts without a lock (see checkUndeliveredCeiling), so
// concurrent restricted enqueues at the boundary can each pass and nibble a few rows
// into the reserved share — the same one-over race the ceiling has always tolerated,
// now also present at this boundary. Batches cannot: they take the per-tenant advisory
// lock first.
func (api *Api) effectiveUndeliveredLimit(ctx context.Context) (limit int, ceiling int) {
	ceiling = api.heldCommandCeiling(ctx)
	if claims, ok := auth.ClaimsFromContext(ctx); ok && claims.TokenType == auth.TokenTypeService {
		return ceiling, ceiling
	}
	return governance.RestrictedCommandLimit(ceiling, api.DeliveryMachineryReserve), ceiling
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
//
// 🔴 IT DELIBERATELY IGNORES BATCH-CREATED COMMANDS, and that is a correctness requirement
// rather than a filter. A command token is a CLIENT-CHOSEN idempotency key, but a batch's
// commands carry tokens the PLATFORM minted — two namespaces in one column. Without this
// predicate, a client issuing a command under a token the batch minter happened to produce
// got back somebody else's command, for a different device and a different command name,
// reported as a successful idempotent replay with nothing created and no error. The minter's
// output is predictable, so that was reachable by accident and not only by an attacker.
//
// Excluding them here means such a token instead falls through to the insert, where the
// (tenant_id, token) unique index refuses it — a refusal the caller can see and act on,
// which is the honest answer to "that name is taken".
func (api *Api) liveCommandByToken(ctx context.Context, token string) (*Command, bool, error) {
	found := &Command{}
	result := api.RDB.DB(ctx).Where("token = ? AND batch_id IS NULL", token).Limit(1).Find(found)
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
// "status NOT IN (…)" guard. One definition shared by ExpireStale's writes and by the
// stranded-SENT reconciler, so the set the sweep skips and the set a transition guards
// against can never drift.
//
// 🔑 MarkResponse AND CancelCommand NO LONGER USE IT, and neither should a new
// transition without arguing for it. Both were written as "not terminal" and both were
// wrong for the same reason: a negative guard admits every state nobody thought about,
// so SENT was cancellable and a QUEUED command was answerable purely by default. Each
// now names its own from-set positively (cancellableStatusStrings,
// answerableStatusStrings). This one survives where the question really is "has this row
// finished" rather than "may this particular transition run".
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

// expiredWhere and liveWhere are the TWO HALVES OF ONE PARTITION over a command's
// expiry horizon, and they are declared together so that they can only be changed
// together. For any row at any instant EXACTLY ONE of them matches — that is the
// property TestExpiryPredicatesPartitionEveryRow asserts, and it is the whole reason
// they live side by side rather than being written out at each call site.
//
// 🔴 THEY WERE TWO DEFINITIONS THAT DISAGREED BY ONE INSTANT. The expiry sweep asked
// `expires_at < now` while the LwM2M wake drain dropped a row on `expires_at <= now`,
// so a command sitting exactly on its horizon was in neither set: the drain refused to
// actuate it because it read as expired, and the sweep refused to expire it because it
// read as live. The row was invisible to both until the clock moved past it — which is
// a one-instant window, and therefore the kind of thing that is never reproduced and
// never explained. Making them complements removes the gap by construction.
//
// 🔑 THE BOUNDARY BELONGS TO `live`, NOT TO `expired`: at `expires_at == now` the
// command is still deliverable. That direction is the safe one — a command actuates one
// instant early rather than a horizon being enforced one instant late — and it matches
// the sweep, which is the writer that makes expiry FACT. The drain must not be stricter
// than the thing that does the expiring, or it declines to deliver rows nothing will
// ever expire.
//
// ExpiresAt is nullable (sql.NullTime) and optional: a caller may supply none, in which
// case the configured default TTL is stamped at create — but a zero DefaultCommandTTL
// leaves it NULL, so NULL is a real, reachable state and not just a schema artifact. A
// NULL horizon means "no horizon": never expired, always live. Both predicates spell
// that out rather than leaning on SQL's three-valued logic, since `expires_at < ?`
// against NULL is neither true nor false and would silently drop the row from BOTH
// sides — the exact hole this pair exists to close.
//
// They return (sql, args) rather than taking a *gorm.DB so they compose with the
// conditional-update builders as easily as with the selects, and so a test can assert on
// the predicate itself.
func expiredWhere(now time.Time) (string, []any) {
	return "expires_at IS NOT NULL AND expires_at < ?", []any{now}
}

// liveWhere is expiredWhere's exact complement — see there for the boundary rule and the
// NULL rule, which are the two places this pair is easy to get subtly wrong.
func liveWhere(now time.Time) (string, []any) {
	return "(expires_at IS NULL OR expires_at >= ?)", []any{now}
}

// claimableStatusStrings is the wire form of the states a DISPATCHER may claim a
// command from: QUEUED (never yet considered), HELD (considered, and deliberately
// withheld because the device was absent) and PARKED (dispatched once, and the
// transport found the device unreachable). It is the from-state guard for MarkSent.
//
// 🔴 IT IS NO LONGER THE SWEEP'S SELECTION PREDICATE, AND THE SPLIT IS THE POINT.
// These were one helper while the two sets genuinely coincided, and collapsing them
// then was correct. They have now come apart: the sweep stops scanning HELD (see
// sweepableStatusStrings), while a claim must still accept HELD because the paths that
// release a hold — the LwM2M wake drain, and the wake handler — claim held rows
// directly.
//
// Do NOT re-merge these because they happen to overlap. They answer different questions
// — "what may a dispatcher claim" versus "what does the sweep hand out" — and a shared
// helper would silently move both the day one of them changes. PARKED is the worked
// example: it belongs here, because the wake drain claims a parked row before actuating
// it, and must NEVER reach the sweep, because a sweep that republished parked rows would
// re-publish an offline fleet's whole backlog every tick.
//
// SENT is absent, and that absence is load-bearing in both directions: it makes a claim
// of an already-claimed row lose (RowsAffected 0), which is how two dispatchers racing
// one command settle it, and it means the wake drain can no longer dispatch out of SENT
// without claiming — which was the platform's only unclaimed dispatch.
func claimableStatusStrings() []string {
	return []string{CommandQueued.String(), CommandHeld.String(), CommandParked.String()}
}

// ErrResponderNotCommandOwner is returned when a device publishes a response for a
// command belonging to a DIFFERENT device.
//
// It is a distinct sentinel rather than a generic failure because the two need opposite
// handling and produce opposite conclusions. An ordinary persist failure is transient:
// leave the message unacked and let it retry. This is not retryable at all — the same
// message will be refused forever — so its consumer must ack it and stop.
//
// 🔑 THE SENTINEL IS ALSO WHAT MAKES THE EVENT COUNTABLE. The consumer's shared result
// vocabulary can only call this message "invalid", which is the same label an undecodable
// payload gets — a broken device and a device answering for its neighbours reported as one
// number. Recognising the sentinel is what lets the consumer raise its own
// ResponsesRefused counter alongside, so the two stay tellable apart.
var ErrResponderNotCommandOwner = errors.New("command response came from a device that does not own the command")

// answerableStatusStrings is the wire form of the states in which a DEVICE RESPONSE may
// settle a command: the states a dispatcher has held the row in for that device.
//
// 🔴 IT REPLACED A NEGATIVE GUARD ("status NOT IN (terminal)"), and the difference is
// QUEUED and HELD. Those fell through the old guard by default, so a device could report
// success for a command no transport had ever handed it — closing out an actuation that
// then never happened, with the record saying it did. Naming the set positively means a
// state is answerable because someone decided it is, which is the same correction
// CancelCommand's from-set already received (see cancellableStatusStrings).
//
// 🔑 PARKED IS IN THE SET DELIBERATELY, AND NOT BECAUSE IT IS A DISPATCHED STATE — it is
// not; ParkClaim records that the transport found the device UNREACHABLE, which is why
// expiredTerminalFor maps it to EXPIRED rather than TIMEOUT. It is here because a park
// can land on a row that WAS delivered and actuated: ParkClaim's own note describes a
// park request arriving as a redelivery for a command since claimed and run. Excluding
// PARKED would discard that device's true answer and let a command it really executed
// expire. Admitting it costs nothing now that the responder must own the row.
//
// 🔴 DELIBERATELY ITS OWN LIST, like its three siblings above. It happens to be the
// complement of neither of them, and a shared helper would silently move this set on the
// day a state is added for one of the other questions.
func answerableStatusStrings() []string {
	return []string{CommandSent.String(), CommandParked.String()}
}

// cancellableStatusStrings is the wire form of the states a CANCEL takes to CANCELLED:
// QUEUED, HELD and PARKED — the three that mean "the platform still has it, and nothing
// has reached the device". It is shared by CancelCommand and CancelCommandBatch, which
// now answer the same question.
//
// 🔴 SENT IS ABSENT, AND ITS ABSENCE IS THE DESIGN. A sent command has been published
// toward a device believed live. Driving it to CANCELLED stops no actuation — the
// hardware acts either way — and MarkResponse's terminal fast path then DROPS the
// device's real answer when it arrives. So a "cancel" over sent rows would reboot the
// fleet, discard what each device reported, and leave a record saying the operation was
// called off. The cancel reports those rows as alreadySent, which is true.
//
// 🔑 THIS USED TO BE TWO LISTS, AND THE COMMENT HERE ARGUED AT LENGTH THAT THEY MUST STAY
// APART BECAUSE THEY ANSWERED DIFFERENT QUESTIONS. They did not. The batch cancel took
// QUEUED+HELD while CancelCommand took everything non-terminal, and that divergence was
// not two questions — it was the bug. CancelCommand fell through to SENT because it was
// written as a NEGATIVE guard (status NOT IN terminal), so SENT was included by default
// rather than by decision, and nobody had decided. Merging them is what makes "may this
// be called off?" have one answer.
//
// The earlier version of this comment also claimed the SENT argument covered every sent
// row. It did not: SENT then carried a second meaning — "we published while the device
// was asleep and it went nowhere" — and those rows WERE recallable, so a cancelled fleet
// write still actuated them on the next wake. That second meaning is now PARKED, which is
// why PARKED is in this list. See CommandStatus in model.go.
//
// Do NOT fold undeliveredStatusStrings or claimableStatusStrings in here just because the
// three sets overlap today. Those genuinely do answer different questions — "what counts
// against the backlog ceiling" and "what may a dispatcher claim" — and SENT is the proof:
// it is in none of them for three unrelated reasons.
func cancellableStatusStrings() []string {
	return []string{CommandQueued.String(), CommandHeld.String(), CommandParked.String()}
}

// cancellable is the in-process form of the same question, for a fast path that skips the
// UPDATE when the loaded row is already out of reach.
//
// 🔴 It is DERIVED from cancellableStatusStrings rather than written out again. The fast
// path and the SQL guard answering differently is not a hypothetical: it is the failure the
// terminal-set comment in model.go describes, where one of the pair treats a row as
// finished while the other still lets a write through.
func cancellable(status string) bool {
	for _, s := range cancellableStatusStrings() {
		if status == s {
			return true
		}
	}
	return false
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

// drainableStatusStrings is the wire form of what a WAKING DEVICE'S BACKLOG lives in:
// HELD (the presence gate withheld it because the device was absent) and PARKED (a
// transport published it and found nobody there). Those are the two ways a command ends
// up waiting on a device to come back, and the wake drain reads exactly their union.
//
// 🔴 QUEUED IS ABSENT, AND ITS ABSENCE IS THE DESIGN. A queued command has not yet been
// through the presence gate, so the sweep still owns it — it will be evaluated on the
// next tick and dispatched over whichever transport the gate picks. Draining it here
// would mean the drain and the sweep both handed the same row to a dispatcher, from two
// different transports, with only the claim to settle it. The drain's job is the rows
// the sweep has deliberately STOPPED looking at.
//
// SENT is absent for the ordinary reason: it is at a device already.
//
// Do NOT fold this into claimableStatusStrings just because a drained row is claimed
// immediately afterwards. That list is QUEUED ∪ HELD ∪ PARKED and answers "what may a
// dispatcher claim"; this one answers "what is waiting for THIS device to return". The
// difference IS the QUEUED row above, and merging them would silently add it.
func drainableStatusStrings() []string {
	return []string{CommandHeld.String(), CommandParked.String()}
}

// MarkSent transitions a command QUEUED/HELD/PARKED -> SENT.
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
//
// It returns the DISPATCH NONCE it stamped, which the caller must carry in the envelope it
// publishes. That value is what lets a transport park THIS dispatch and no other; see
// Command.DispatchNonce and ParkClaim. On a lost claim there is no dispatch, so there is no
// nonce, and the caller must not publish.
func (api *Api) MarkSent(ctx context.Context, id uint) (string, bool, error) {
	nonce := newDispatchNonce()
	res := api.RDB.DB(ctx).Model(&Command{}).
		Where("id = ? AND status IN ?", id, claimableStatusStrings()).
		Updates(map[string]any{
			"status":         CommandSent.String(),
			"sent_time":      sql.NullTime{Time: time.Now(), Valid: true},
			"dispatch_nonce": sql.NullString{String: nonce, Valid: true},
		})
	if res.Error != nil {
		return "", false, res.Error
	}
	if res.RowsAffected != 1 {
		return "", false, nil
	}
	return nonce, true, nil
}

// newDispatchNonce mints the identifier for ONE dispatch attempt.
//
// A UUID rather than a counter on purpose: nothing may order two of these. The only
// question a nonce ever answers is "is this the dispatch I was handed?", and a value that
// looked like a sequence would invite a comparison the semantics do not support.
func newDispatchNonce() string {
	return uuid.NewString()
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
// 🔴 A COMMAND WHOSE BATCH HAS BEEN CANCELLED IS RELEASED TO CANCELLED, NOT TO QUEUED,
// AND WITHOUT THAT THE BATCH CANCEL IS NOT A BRAKE. The sequence is entirely ordinary:
// the sweep claims a batch's command QUEUED->SENT; the cancel arrives, correctly skips
// the row (it is SENT — the platform has handed it over) and reports it as alreadySent;
// the publish then FAILS, and this release returns the row to QUEUED. The next tick
// dispatches it. The device actuates minutes after an operator was told the fleet write
// was stopped — and that row, unlike the ones this design legitimately cannot recall, had
// never gone anywhere.
//
// 🔴 IT IS NO LONGER THE ONLY PATH WITH THAT PROPERTY, AND AN EARLIER VERSION OF THIS COMMENT
// SAID IT WAS. ParkClaim is a second route out of SENT and back into the dispatchable set —
// it shares retireClaim with this one for exactly that reason, so the cancelled-batch check is
// written once and cannot be present on one route and missing on the other. Every OTHER route
// still predicates on HELD or on QUEUED/HELD/PARKED, and a cancelled row is terminal, so the
// hold reconciler and the wake drain no-op correctly.
//
// The two updates are ORDERED, cancelled-batch first, and both are predicated on SENT so
// exactly one of them can move the row. That is what makes it safe without a transaction.
//
// ⚠️ IT IS NOT AIRTIGHT, AND AN EARLIER VERSION OF THIS COMMENT CLAIMED IT WAS. One
// interleaving survives: the first statement evaluates its subquery and sees no committed
// stamp, the cancel commits, and the second statement then requeues a command whose batch
// is now called off. Neither the sweep nor MarkSent carries a cancelled-batch predicate,
// so the next tick delivers it. Closing that would mean testing the batch at CLAIM time,
// on the hot path, to buy a window that needs a publish failure and a cancel to land in
// the same microsecond. Documented and accepted rather than silently assumed away — if it
// ever needs closing, close it at the claim, not here.
// It collapses retireClaim's landing status: this caller is returning a command to the
// dispatchable set after a failed publish, and both landings — QUEUED, or CANCELLED if the
// batch was called off meanwhile — mean the same thing to it, that the row is no longer its
// responsibility.
func (api *Api) ReleaseClaim(ctx context.Context, id uint) (bool, error) {
	_, released, err := api.retireClaim(ctx, "id = ?", []any{id}, CommandQueued)
	return released, err
}

// ParkClaim retires a claimed command whose transport found the device UNREACHABLE, taking
// it SENT -> PARKED so the platform's record says it went nowhere. It reports whether this
// caller's park landed.
//
// 🔴 IT IS PREDICATED ON THE DISPATCH NONCE, AND WITHOUT THAT IT WOULD RE-ARM AN
// ALREADY-ACTUATED COMMAND. The full sequence is in Command.DispatchNonce; the short form is
// that a park request can be a redelivery of a message whose command has since been claimed
// and run, and a park matching on status alone would drag that row back into the
// dispatchable set to be actuated a second time. Matching the nonce means a stale request
// names a dispatch that no longer exists.
//
// 🔑 RowsAffected == 0 IS A BENIGN SUCCESS, NOT A FAILURE, and the caller must treat it as
// settled. It means the row moved on under this park — answered, cancelled, expired, or
// re-claimed by a wake drain. A caller that read it as failure and retried would burn every
// redelivery on a row that is already where it should be.
// It returns the status the row LANDED ON as well as whether it moved. A parked command
// normally lands on PARKED, but a command whose batch has been called off lands on
// CANCELLED instead — and only this write can tell which, because the batch can be
// cancelled between a caller's scan and this update. When nothing moved the status is
// empty; it pairs with moved=false rather than naming a third outcome.
func (api *Api) ParkClaim(ctx context.Context, token, nonce string) (CommandStatus, bool, error) {
	return api.retireClaim(ctx, "token = ? AND dispatch_nonce = ?", []any{token, nonce}, CommandParked)
}

// retireClaim is the shape both ReleaseClaim and ParkClaim need: take a SENT row out of the
// dispatcher's hands, to CANCELLED if its batch has been called off and to the caller's
// target otherwise. Callers differ only in how they name the row and where it lands.
//
// Both writes are predicated on SENT, so exactly one of them can move the row — which is
// what makes this safe without a transaction — and both clear sent_time, because a retired
// claim did not send anything.
//
// 🔑 IT REPORTS WHICH STATUS THE ROW LANDED ON, NOT ONLY THAT IT MOVED, and the distinction
// is only knowable HERE. Which branch fires depends on whether the batch was called off,
// which can change between a caller's scan and this write — so a caller that tried to
// derive the landing from anything it read earlier would be guessing at a race. Callers
// that do not care collapse it at the call site; the one that meters recoveries does not
// have to invent a label it cannot observe.
func (api *Api) retireClaim(ctx context.Context, where string, args []any,
	target CommandStatus) (CommandStatus, bool, error) {
	cancelledBatches := api.RDB.DB(ctx).Model(&CommandBatch{}).
		Select("id").Where("cancelled_at IS NOT NULL")
	stopped := api.RDB.DB(ctx).Model(&Command{}).
		Where(where, args...).
		Where("status = ? AND batch_id IN (?)", CommandSent.String(), cancelledBatches).
		Updates(map[string]any{
			"status":    CommandCancelled.String(),
			"sent_time": sql.NullTime{},
		})
	if stopped.Error != nil {
		return "", false, stopped.Error
	}
	if stopped.RowsAffected == 1 {
		return CommandCancelled, true, nil
	}

	res := api.RDB.DB(ctx).Model(&Command{}).
		Where(where, args...).
		Where("status = ?", CommandSent.String()).
		Updates(map[string]any{
			"status":    target.String(),
			"sent_time": sql.NullTime{},
		})
	if res.Error != nil {
		return "", false, res.Error
	}
	if res.RowsAffected != 1 {
		// Nothing moved, so nothing landed anywhere. The empty status is not a third
		// outcome: it is the absence of one, and it pairs with moved=false.
		return "", false, nil
	}
	return target, true, nil
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

// ResponseLostReason is the fixed sentence written into a command's Error column when
// the device's answer to it was dead-lettered — the platform accepted the response, could
// not record it against the command, and gave up.
//
// 🔴 IT IS FIXED, NOT INTERPOLATED, and the dead letter's own Detail is deliberately not
// spliced in. Detail carries the underlying error text, which core/deadletter warns is
// not always safe to show: this column is read by the TENANT on its own command, and a
// persistence failure's text can name hosts, drivers and schema. The dead-letter store is
// where an operator reads the cause; this column says the part the tenant needs, which is
// that the outcome the device reported is not recoverable.
//
// 🔑 IT SAYS NOTHING ABOUT *WHY* THE PLATFORM GAVE UP, AND THAT IS WHAT LETS THE WRITE-BACK
// FILTER ON KIND ALONE. The consumer settles any command-response dead letter, whatever
// Reason it carries; today the only producer writes ReasonExhausted, but an unprocessable
// one would settle the same command through the same call. A sentence claiming the answer
// was retried "after every attempt" would then be a statement about a retry that never
// happened — so the claim is left out rather than guarded by a Reason check that a second
// producer would have to remember to keep in step with.
const ResponseLostReason = "the device answered this command and the platform could not " +
	"record the answer; the outcome it reported is not recoverable"

// MarkResponseLost drives a command to FAILED because the device's answer to it was
// dead-lettered, reporting whether THIS call performed the transition.
//
// 🔑 FAILED, NOT TIMEOUT, AND NOT A NEW STATE. The vocabulary already has the shape this
// needs: MarkUndeliverable drives a command the platform cannot deliver to FAILED and
// writes WHY into the same Error column a device's own failure response uses, on the
// argument that from the caller's side both mean "this command will not happen, and here
// is why". A lost response is that same statement one step later in the lifecycle.
// TIMEOUT is the natural-looking choice and is the wrong one for exactly the reason
// PARKED exists: it means "dispatched, and never answered", which blames hardware that in
// fact answered. EXPIRED claims the platform never sent it, which is false. SUCCESSFUL —
// and FAILED read as the device's own verdict — would claim to know an outcome nobody
// recorded. So the status says what the platform can say (this command did not complete)
// and the Error column carries the part only this path knows (an answer existed, and the
// platform lost it).
//
// 🔴 IT CANNOT CLOBBER A REAL TERMINAL. The from-set is answerableStatusStrings() — the
// same two states in which a response could have settled this command — so a row that has
// since reached SUCCESSFUL, FAILED, TIMEOUT, EXPIRED or CANCELLED some other way matches
// nothing and is left exactly as it is. A late dead letter for a command that a
// redelivery answered, the sweep expired, or a human cancelled is a no-op reporting false,
// never a rewrite. The same predicate declines a row that has gone BACK to QUEUED
// (ReleaseClaim) or HELD: that is a live command the platform still intends to deliver,
// and it must not be failed on the strength of an answer to an earlier dispatch.
//
// Keyed by token rather than id because the token is what the dead letter carries — it is
// the client-facing key the response envelope names — and the write is confined to one
// tenant by the scope callbacks, which is what makes that token unambiguous.
func (api *Api) MarkResponseLost(ctx context.Context, token string) (bool, error) {
	if strings.TrimSpace(token) == "" {
		return false, errors.New("cannot record a lost response without a command token")
	}
	res := api.RDB.DB(ctx).Model(&Command{}).
		Where("token = ? AND status IN ?", token, answerableStatusStrings()).
		Updates(map[string]any{
			"status": CommandFailed.String(),
			"error":  sql.NullString{String: ResponseLostReason, Valid: true},
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

// DefaultDrainLimit is how many backlogged commands one wake drains when the caller
// names no limit. It is a bound on ONE wake, not on the backlog: a device with more
// waiting takes the rest on its next registration, which for a queue-mode device is
// its next wake window.
//
// 32 is the LwM2M drain's own figure, moved here because the bound is now the server's
// to enforce — a client that asked for none used to get 1000 rows and throw away 968.
const DefaultDrainLimit = 32

// DrainableCommands returns the head of a device's still-waiting backlog: HELD ∪ PARKED
// commands that have not passed their expiry horizon, OLDEST FIRST, bounded.
//
// It exists because the drain's read used to be assembled on the client. The LwM2M
// downlink drain asked the generic command search for a 1000-row page, sorted it by id
// in memory and truncated to 32 — three steps of which only the truncation was visible
// in the contract. The generic search cannot serve this: its order is newest-first (a
// PRODUCT requirement of the console list, see Command.DefaultOrder), it has no notion
// of an expiry horizon, and it pages a set the caller wants only the front of.
//
// 🔴 OLDEST-FIRST IS A DELIVERY GUARANTEE, NOT A DISPLAY CHOICE. A firmware update is a
// sequence of commands whose order IS its meaning, and the backlog is where that order
// is preserved across an outage. Newest-first would hand a waking device the chunks of
// an image backwards; a bounded newest-first read would hand it the TAIL and silently
// drop the beginning. id ASC is enqueue order — monotonic, unique, and unlike created_at
// not tied within a batch.
//
// 🔴 THIS DOES NOT GO THROUGH rdb.ListOf, and the reasons are specific to this shape
// rather than a preference. ListOf appends the model's DefaultOrder AFTER any order the
// filter closure set, so the SQL would end `ORDER BY id ASC, created_at DESC, token ASC`
// — three keys, of which Postgres cannot prove the first is already total, so it plans a
// Sort node and the partial index below (idx_commands_drainable) buys nothing. ListOf
// also runs an unconditional COUNT before the data query, which is a second round trip
// per device wake to compute a total this caller never reads. Neither is a defect in
// ListOf; they are the cost of a paged list, and this is not a page — it is a bounded
// head with no next.
//
// It builds on api.RDB.DB(ctx) exactly as HeldCommands and PendingCommands do. That is
// the same binding ListOf uses (Database.WithContext), so the dc:tenant_query scope
// callback still injects the tenant predicate. 🔴 It stays on the gorm builder for that
// reason — raw SQL would leave the tenant fence behind, and deviceToken is a filter
// INSIDE the fence, never a substitute for it: device tokens are unique per tenant, not
// per instance.
//
// limit: absent, zero or negative means DefaultDrainLimit; anything above
// rdb.MaxPageSize is clamped to it. A caller cannot ask for an unbounded drain, which is
// the point — the backlog of a fleet that was offline for a weekend is exactly the set
// that must not arrive in one response.
func (api *Api) DrainableCommands(ctx context.Context, deviceToken string, limit int) ([]*Command, error) {
	if limit <= 0 {
		limit = DefaultDrainLimit
	}
	if limit > rdb.MaxPageSize {
		limit = rdb.MaxPageSize
	}
	// The horizon is read ONCE, here, rather than per row: a drain that evaluated
	// `now` while walking would be comparing rows against different instants.
	liveSQL, liveArgs := liveWhere(time.Now())
	found := make([]*Command, 0, limit)
	result := api.RDB.DB(ctx).
		Where("device_token = ?", deviceToken).
		Where("status IN ?", drainableStatusStrings()).
		Where(liveSQL, liveArgs...).
		Order("id ASC").
		Limit(limit).
		Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
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
//
// 🔴 IT STAMPS A FRESH DISPATCH NONCE, AND THAT IS WHAT INVALIDATES AN IN-FLIGHT PARK.
// This claim's caller does not publish an envelope — it dispatches over its own live
// session — so it never reads the value back. The stamp exists for the other direction:
// a park request still in JetStream redelivery names the PREVIOUS dispatch, and once this
// write lands that name matches nothing, so the row this caller is about to actuate
// cannot be dragged back under it. Omitting the stamp here would leave exactly the
// re-arm window the nonce was introduced to close.
func (api *Api) MarkSentByToken(ctx context.Context, token string) (bool, error) {
	res := api.RDB.DB(ctx).Model(&Command{}).
		Where("token = ? AND status IN ?", token, claimableStatusStrings()).
		Updates(map[string]any{
			"status":         CommandSent.String(),
			"sent_time":      sql.NullTime{Time: time.Now(), Valid: true},
			"dispatch_nonce": sql.NullString{String: newDispatchNonce(), Valid: true},
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// MarkResponse records a device response against a command, looked up by its token.
// If the command is already terminal the response is ignored (the current command is
// returned). On success the command becomes SUCCESSFUL, otherwise FAILED with the
// error message recorded.
//
// responder is the token of the device that ACTUALLY published the response, taken
// from the delivered subject rather than the payload, and a response from anyone but
// the command's own device is refused with ErrResponderNotCommandOwner.
//
// 🔴 THAT CHECK IS THE WHOLE POINT OF THE PARAMETER, AND ITS ABSENCE WAS A FORGERY.
// The device grant used to permit publishing on the TENANT-WIDE response subject while
// the response body carried no identity, so this function marked whatever token it was
// handed: any device could settle any other device's command, and batch tokens
// (dcb{id}-{index}) are enumerable, so an entire fleet write could be reported complete
// with no device acting. The grant is device-scoped now, which is what makes responder
// trustworthy — the broker refuses a publish to another device's subject — and this is
// where that authenticated fact is finally USED. A grant nobody checks against would
// only have moved the hole.
//
// The write is a from-state-predicated conditional UPDATE (the same shape ExpireStale
// uses), touching only the response columns — so a response and a racing MarkSent /
// expire / cancel never clobber each other via a stale full-row Save. RowsAffected==0
// means the row left the answerable set between the read and the write (a late or
// duplicate response); the current row is returned unchanged.
func (api *Api) MarkResponse(ctx context.Context, commandToken, responder string, success bool,
	payload *string, errMsg *string) (*Command, error) {
	matches, err := api.CommandsByToken(ctx, []string{commandToken})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	found := matches[0]

	// 🔴 IDENTITY BEFORE ANYTHING ELSE, INCLUDING BEFORE THE TERMINAL FAST PATH. A
	// forged response to an already-terminal command changes no row, but it is still a
	// device claiming to be another device, and returning it the command's current state
	// would answer a question it had no right to ask. Refusing first also means the one
	// place that reports this event cannot be skipped by a lucky race.
	if found.DeviceToken != responder {
		return nil, fmt.Errorf("%w: device %q answered for command %q, which belongs to device %q",
			ErrResponderNotCommandOwner, responder, commandToken, found.DeviceToken)
	}

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
	// ⚠️ `device_token = ?` HERE IS NOT INDEPENDENTLY VERIFIED, AND IT IS RECORDED RATHER
	// THAN QUIETLY LEFT TO READ AS A TESTED GUARD. Deleting it breaks no test, because the
	// Go check above returns before this runs and nothing in the platform ever WRITES
	// device_token — it is set once at creation (CreateCommand / the batch enqueue) and
	// never updated — so `found.DeviceToken` and the row cannot disagree. That makes it a
	// genuinely equivalent mutation, not a gap a test could close.
	//
	// It is kept because this file's convention is that the conditional WHERE is the
	// AUTHORITATIVE guard and the in-process check is the fast path (see the terminal
	// fast-path note above), and because the day a device_token update path does appear,
	// the authority is already where it belongs instead of being remembered.
	if res := api.RDB.DB(ctx).Model(&Command{}).
		Where("id = ? AND device_token = ? AND status IN ?", found.ID, responder, answerableStatusStrings()).
		Updates(updates); res.Error != nil {
		return nil, res.Error
	}
	return api.loadCommand(ctx, found.ID)
}

// CancelCommand cancels a command the platform still holds, by token, moving it to
// CANCELLED (QUEUED/HELD/PARKED -> CANCELLED). A command in any other state is returned
// unchanged. Like the other transitions it is a from-state-predicated conditional UPDATE so
// a cancel racing a device response does not clobber the response.
//
// 🔴 IT USED TO CANCEL FROM SENT, AND NOT BECAUSE ANYONE DECIDED IT SHOULD. This was
// written as a NEGATIVE guard — "status NOT IN (terminal)" — so every non-terminal state
// fell through it by default, SENT included. A sent command is at a device: cancelling it
// stops no actuation, and MarkResponse's terminal guard then DROPS the answer the device
// sends back. The hardware acts, the response vanishes, and the record says an operator
// called it off. The set is now POSITIVE, so a state is cancellable because someone decided
// it is (see cancellableStatusStrings).
//
// 🔑 A CANCEL ON A SENT COMMAND RETURNS IT UNCHANGED RATHER THAN ERRORING, which is the
// same shape as cancelling an already-terminal one. Callers already handle "the status you
// get back is the truth"; making this the one cancel that raises would push a new failure
// mode into every one of them to describe a race they cannot avoid — the command may have
// been claimed a millisecond before the request arrived.
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
	// An OPTIMIZATION, not the guard. The conditional UPDATE below is authoritative — swap this
	// for the old Terminal() test and behaviour is identical, because a SENT row then reaches
	// the UPDATE, matches nothing, and is reloaded unchanged. What it buys is skipping a write
	// that was never going to match. Keep the two in agreement anyway: a fast path that admits
	// rows the SQL guard refuses is how a "cancelled" command silently stays live.
	if !cancellable(found.Status) {
		return found, nil
	}
	if res := api.RDB.DB(ctx).Model(&Command{}).
		Where("id = ? AND status IN ?", found.ID, cancellableStatusStrings()).
		Updates(map[string]any{"status": CommandCancelled.String()}); res.Error != nil {
		return nil, res.Error
	}
	return api.loadCommand(ctx, found.ID)
}

// expiredTerminalFor maps the state a lapsed command was IN to the terminal it
// deserves. The distinction is the whole reason the platform tracks a hold
// separately from a dispatch:
//
//   - QUEUED / HELD / PARKED -> EXPIRED. The command never went out. Its TTL elapsed
//     while the platform still had it — because the device was absent the entire time,
//     because a transport found it unreachable and handed the command back, or because
//     it was enqueued too close to its own horizon.
//   - SENT -> TIMEOUT. The command DID go out and the device never answered.
//
// Reporting the first case as TIMEOUT — which is what a single "not finished"
// state forces — blames the device for a delivery that was never attempted. For a
// fleet that is switched off overnight or over a weekend that is not an edge case,
// it is the common case, and it sends an operator looking for a fault in hardware
// that was behaving correctly by being off.
//
// 🔴 PARKED IS HERE BECAUSE THIS FUNCTION WAS TELLING THAT EXACT LIE ON THE ORDINARY
// EXPIRY PATH. A queue-mode device's command was published, found unreachable, and left in
// SENT — so it lapsed as TIMEOUT, "the device was reached and never answered", for a
// command that reached nothing. Nothing about that involved a fleet write or a cancel; it
// fired for a single command to a single sleeping device, silently, every time.
//
// Note what SENT -> TIMEOUT still does not promise. SENT means "dispatched toward a device
// believed live", so a device that drops between the presence read and the publish also
// lands in TIMEOUT for a command it never received. That window is narrow and no state
// distinguishes it, unlike the queue-mode case, which was systematic.
//
// An unrecognised status maps to TIMEOUT, preserving the pre-existing default. It
// should be unreachable: the only non-terminal states are the four above.
func expiredTerminalFor(status string) string {
	switch CommandStatus(status) {
	case CommandQueued, CommandHeld, CommandParked:
		return CommandExpired.String()
	default:
		return CommandTimeout.String()
	}
}

// expirePageSize bounds how many stale commands one page of the expiry walk loads.
// It is not a limit on how many expire — the walk continues to the end of the set —
// only on how many are held in memory at once.
const expirePageSize = 500

// ExpireStale times out every non-terminal command whose TTL has elapsed. A
// QUEUED, HELD or PARKED command that never reached a device becomes EXPIRED; a
// SENT command that was never answered becomes TIMEOUT (see expiredTerminalFor). The caller
// MUST pass a system context (core.WithSystemContext) so the sweep spans all
// tenants.
//
// It returns the number of commands expired AND a breakdown keyed by the state
// each command lapsed FROM, because those counts mean opposite things
// operationally: rows dying out of HELD say the fleet is absent and the platform
// never got to try; rows dying out of PARKED say it tried and found nobody there;
// rows dying out of SENT say devices are being reached and are not answering. One
// total reports all three as "expiry is happening", which points an operator at
// the wrong part of the system.
//
// 🔑 SENT is the only one of the three that implicates the DEVICE, and even it is not
// proof: SENT means "dispatched toward a device believed live", so a device dropping
// between the presence read and the publish also lands there. PARKED exists because that
// used to be true of a whole systematic class — every queue-mode command to a sleeping
// device — and those rows all lapsed as TIMEOUT, blaming hardware that was never reached.
// 🔑 IT WALKS THE STALE SET IN PAGES, from a cursor. The set is every expired
// non-terminal row across every tenant, so its size is a property of the fleet and
// of how long an outage lasted — an instance coming back after a weekend with a
// held backlog per tenant can have a very large one, and loading it whole puts all
// of it in the pod at once. The cursor is the row id, which is stable and ascending
// (the same walk HeldCommands uses); expiry is a conditional update per row, so a
// row that moves on between pages is simply not expired by us, exactly as before.
func (api *Api) ExpireStale(ctx context.Context, now time.Time) (int64, map[string]int64, error) {
	terminal := terminalStatusStrings()
	// The sweep is the writer that makes expiry fact, so it defines the horizon for
	// every reader — via the shared predicate rather than by being copied. Its
	// complement (liveWhere) is what DrainableCommands asks for, which is what keeps
	// "expirable" and "still deliverable" from overlapping or leaving a gap.
	expiredSQL, expiredArgs := expiredWhere(now)
	byFromStatus := make(map[string]int64)
	var after uint
	for {
		stale := make([]*Command, 0, expirePageSize)
		result := api.RDB.DB(ctx).
			Where("status NOT IN ?", terminal).
			Where(expiredSQL, expiredArgs...).
			Where("id > ?", after).
			Order("id ASC").
			Limit(expirePageSize).
			Find(&stale)
		if result.Error != nil {
			return sumCounts(byFromStatus), byFromStatus, result.Error
		}
		if len(stale) == 0 {
			return sumCounts(byFromStatus), byFromStatus, nil
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
		// 🔴 THE CURSOR ADVANCES PAST THE LAST ROW SEEN, NOT PAST THE LAST ROW EXPIRED.
		// A row whose conditional update lost its race is still behind us; re-reading
		// from its id would hand the next page the same row forever, and the walk would
		// never end. The predicate is not what bounds this loop — the cursor is.
		after = stale[len(stale)-1].ID
	}
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

// CommandsById gets commands by id. The empty-id case is a live hazard rather than an
// edge case — rdb.FindByIds carries the guard and the reason it has to exist.
func (api *Api) CommandsById(ctx context.Context, ids []uint) ([]*Command, error) {
	return rdb.FindByIds[Command](api.RDB.DB(ctx), ids)
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
		// Filtered on the denormalized token rather than joining the batch, matching
		// DeviceToken above: an empty string is a filter that matches nothing (no
		// command carries an empty batch token), not a filter that is skipped. Only
		// Statuses gets the empty-means-unfiltered treatment, and that is a deliberate
		// API choice about a LIST argument, not something gorm forces.
		if criteria.BatchToken != nil {
			db = db.Where("batch_token = ?", *criteria.BatchToken)
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

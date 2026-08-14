// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// batchInsertChunk is how many command rows go into one INSERT.
//
// 🔴 IT IS NOT A STYLE CHOICE — A SINGLE STATEMENT CANNOT CARRY A FULL-CEILING BATCH. The
// Postgres extended protocol caps a statement at 65,535 bind parameters; a command row
// binds well over a dozen columns, so ten thousand rows in one statement is roughly
// 160,000 binds and fails outright. The chunks all run INSIDE the one transaction, so
// atomicity is untouched — this is about statement size, not about commit scope.
const batchInsertChunk = 1000

// BatchEnqueueValidator is the batch counterpart of CommandEnqueueValidator: it asks the
// owner of the devices and the command vocabulary which of MANY devices may receive one
// command.
//
// It answers with refusals only — a device that may receive the command produces no entry —
// so a healthy fleet costs an empty response rather than one verdict per device. An error
// means the check could not be PERFORMED and the caller must fail closed.
type BatchEnqueueValidator interface {
	ValidateEnqueueBatch(ctx context.Context, deviceTokens []string, commandKey string,
		payload []byte) ([]BatchDeviceRefusal, error)
}

// CreateCommandBatch issues one command to many devices as a single, recorded operation.
//
// The ORDER of the steps below is the design, not an implementation detail, and two of them
// were wrong in the first draft of this feature:
//
//  1. REPLAY PROBE FIRST — before target resolution, before remote validation, before the
//     ceiling. This mirrors CreateCommand and is a correctness requirement for the same
//     reason: anything that can say no in front of the probe says no to a call that would
//     have created nothing. A client whose request timed out and retried with the same
//     token must get its original batch back, not a ceiling refusal — and must not
//     re-resolve a ten-thousand-member group to find that out.
//  2. RESOLVE and VALIDATE, both outside the transaction. Remote calls do not belong
//     inside a transaction holding a per-tenant lock, and a device-management outage must
//     fail the whole batch cleanly with the token unspent.
//  3. ONE TRANSACTION for the lock, the count, the batch row and every command row.
//
// 🔴 STEP 3 IS ALL-OR-NOTHING, AND THE IDEMPOTENCY OF THIS WHOLE MUTATION DEPENDS ON IT.
// The natural implementation writes the batch row first to obtain its id, then opens a
// transaction for the commands. A crash between those two commits a batch row with zero
// commands — and because the token now exists, EVERY later replay matches it and returns
// resolved=N, accepted=0, forever. The idempotency key would have converted a transient
// crash into a permanently dead fleet write that reports success-shaped data. The batch row
// is therefore inserted inside the transaction, where its id is available to the same
// transaction.
//
// 🔴 IT RETURNS TWO REJECTION TYPES, and whatever wires this up must handle BOTH.
// *EnqueueRejected covers the request-shaped refusals (ambiguous target, too large, bad
// JSON, unusable group); *BatchRejected covers the two that need to name devices
// (partial-refused, ceiling) and is the more operationally common of the pair. A GraphQL
// layer that only errors.As the first would sanitize the most frequent refusals into opaque
// server errors — telling a tenant the platform is broken when it has just declined their
// request for a stated, fixable reason. A plain error means the batch could not be DECIDED.
func (api *Api) CreateCommandBatch(ctx context.Context, request *CommandBatchCreateRequest) (*CommandBatch, error) {
	existing, replay, err := api.liveBatchByToken(ctx, request.Token)
	if err != nil {
		return nil, err
	}
	if replay {
		// The token IS the identity: the first write wins and a differing re-request does
		// not mutate it. A partially-admitted batch is NOT topped up on replay — it is the
		// same batch, and admitting more devices under the same token would make `accepted`
		// a moving number and the record un-auditable.
		return existing, nil
	}

	if err := validateBatchRequest(request); err != nil {
		return nil, err
	}
	payload, expiresAt, err := api.batchPayloadAndTTL(request)
	if err != nil {
		return nil, err
	}

	targets, err := api.resolveBatchTargets(ctx, request)
	if err != nil {
		return nil, err
	}

	refusals, err := api.validateBatchTargets(ctx, targets.deviceTokens, request.Name, payload)
	if err != nil {
		return nil, err
	}
	admissible := withoutRefused(targets.deviceTokens, refusals)

	// 🔴 A per-device refusal fails the WHOLE batch unless the caller opted in. allowPartial
	// means "I accept that some devices may not get this command", one meaning applied to
	// every refusal reason — a flag that covered ceiling refusals but silently tolerated
	// vocabulary refusals would make the safe default unsafe in exactly the case an
	// operator least anticipates.
	if len(refusals) > 0 && !request.AllowPartial {
		return nil, batchRejection(RejectBatchPartialRefused, refusals, len(targets.deviceTokens),
			"%d of %d devices cannot receive this command; re-issue with allowPartial to "+
				"command the rest", len(refusals), len(targets.deviceTokens))
	}

	created := &CommandBatch{
		TokenReference: rdb.TokenReference{Token: request.Token},
		MetadataEntity: rdb.MetadataEntity{Metadata: rdb.MetadataStrOf(request.Metadata)},
		Name:           request.Name,
		Payload:        rdb.MetadataStrOf(request.Payload),
		TargetKind:     targets.kind.String(),
		GroupToken:     targets.groupToken,
		GroupVersion:   targets.groupVersion,
		AllowPartial:   request.AllowPartial,
		Resolved:       len(targets.deviceTokens),
	}

	// Named `verdict` rather than `rejected` because `rejected` is this package's rejection
	// CONSTRUCTOR — a variable of that name shadows it for the rest of the function, so the
	// next person to add a rejection inside the transaction would get a confusing type error
	// instead of a helper.
	var verdict error
	var conflicted bool
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// 🔴 THE LOCK IS WHY A BATCH CANNOT OVERSHOOT THE CEILING BY ITS OWN SIZE. The
		// single-enqueue path deliberately runs lock-free and tolerates being one row over
		// between two concurrent calls. That tolerance does NOT survive being scaled: two
		// concurrent 8,000-device batches under a 10,000 ceiling would both count ~0, both
		// admit 8,000, and leave the tenant at 16,000 — over the bound the ceiling exists
		// to hold, and far enough over that every subsequent service enqueue is refused
		// until the backlog drains. Humans would starve automation through the very
		// mechanism the reserve provides. Serializing BATCHES per tenant costs nothing
		// (they are rare, and the expensive remote work already happened above) and leaves
		// the hot single-enqueue path untouched.
		if err := lockTenantForBatch(ctx, tx); err != nil {
			return err
		}

		headroom, err := api.undeliveredHeadroom(ctx, tx)
		if err != nil {
			return err
		}
		admitted := admissible
		if len(admitted) > headroom {
			if !request.AllowPartial {
				verdict = batchRejection(RejectHeldCeilingExceeded, nil, len(targets.deviceTokens),
					"this batch needs room for %d commands but the tenant has room for %d "+
						"(commands are released as their devices become reachable, and expire "+
						"if they do not); re-issue with allowPartial to send what fits",
					len(admitted), headroom)
				return verdict
			}
			// Best effort against the remaining headroom, in the order the target
			// resolved: the caller's own order for a list (so it can express priority),
			// id order for a group. Never map order — an operator must be able to predict
			// and explain which subset went out.
			for _, over := range admitted[headroom:] {
				refusals = append(refusals, BatchDeviceRefusal{
					DeviceToken: over,
					Code:        RejectHeldCeilingExceeded,
					Reason:      "the tenant had no remaining room for undelivered commands",
				})
			}
			admitted = admitted[:headroom]
		}

		created.Accepted = len(admitted)
		created.Refusals, created.RefusalCounts = summarizeRefusals(refusals)
		// ON CONFLICT DO NOTHING for the same reason CreateCommand uses it: the replay
		// probe runs OUTSIDE this transaction, so two concurrent issues of the same batch
		// token can both pass it and race here. Without this the loser hits the
		// (tenant_id, token) unique index, which aborts its transaction and surfaces as a
		// sanitized server error — an outage-shaped answer to what is really an ordinary
		// idempotent retry. RowsAffected==0 means the other one won, and the answer is its
		// batch, resolved after the transaction closes.
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(created)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			conflicted = true
			return nil
		}
		return insertBatchCommands(tx, created, admitted, payload, expiresAt)
	})
	if verdict != nil {
		return nil, verdict
	}
	if err != nil {
		return nil, err
	}
	if conflicted {
		// A concurrent issue of the same batch token won the race. That batch is the
		// answer — same rule as the replay path above, just discovered a moment later.
		existing, found, err := api.liveBatchByToken(ctx, request.Token)
		if err != nil {
			return nil, err
		}
		if !found {
			// The winner was soft-deleted between its commit and this read. Rare enough to
			// be worth reporting honestly rather than papering over with a retry loop.
			return nil, fmt.Errorf("command batch %q was removed while being created", request.Token)
		}
		return existing, nil
	}
	return created, nil
}

// batchTargets is a resolved target set plus how it was named.
type batchTargets struct {
	kind         BatchTargetKind
	deviceTokens []string
	groupToken   sql.NullString
	groupVersion sql.NullInt32
}

// validateBatchRequest checks what can be decided without touching another service.
func validateBatchRequest(request *CommandBatchCreateRequest) error {
	named := request.NamedDevices()
	hasList := len(named) > 0
	hasGroup := request.GroupToken != nil && *request.GroupToken != ""
	if hasList == hasGroup {
		// Both or neither. Not a precedence rule: a caller that sent both does not know
		// what it asked for, and picking one would fan a physical actuation across
		// whichever set the implementation happened to favour.
		return rejected(RejectBatchTargetAmbiguous,
			"a batch must name exactly one of deviceTokens or groupToken")
	}
	if hasList && len(named) > MaxBatchDeviceTokens {
		return rejected(RejectBatchTooLarge,
			"a batch may name at most %d devices in one request; this one names %d",
			MaxBatchDeviceTokens, len(named))
	}
	if request.GroupVersion != nil && !hasGroup {
		return rejected(RejectBatchTargetAmbiguous,
			"a group version was named without a group")
	}
	if request.Payload != nil && !json.Valid([]byte(*request.Payload)) {
		return rejected(RejectPayloadNotJSON, "command payload is not valid JSON")
	}
	if request.Metadata != nil && !json.Valid([]byte(*request.Metadata)) {
		return rejected(RejectMetadataNotJSON, "command metadata is not valid JSON")
	}
	return nil
}

// batchPayloadAndTTL parses the request's payload bytes and resolves the TTL every command
// in the batch carries, exactly as CreateCommand does for one.
func (api *Api) batchPayloadAndTTL(request *CommandBatchCreateRequest) ([]byte, sql.NullTime, error) {
	var expiresAt sql.NullTime
	if request.ExpiresAt != nil {
		parsed, err := time.Parse(time.RFC3339, *request.ExpiresAt)
		if err != nil {
			return nil, expiresAt, rejected(RejectExpiresAtInvalid,
				"expiresAt %q is not an RFC3339 timestamp", *request.ExpiresAt)
		}
		expiresAt = sql.NullTime{Time: parsed, Valid: true}
	} else if api.DefaultCommandTTL > 0 {
		expiresAt = sql.NullTime{Time: time.Now().Add(api.DefaultCommandTTL), Valid: true}
	}
	return payloadBytes(request.Payload), expiresAt, nil
}

// resolveBatchTargets turns the request's target into an ordered, deduplicated device list.
func (api *Api) resolveBatchTargets(ctx context.Context, request *CommandBatchCreateRequest) (*batchTargets, error) {
	if named := request.NamedDevices(); len(named) > 0 {
		return &batchTargets{
			kind:         BatchTargetDeviceList,
			deviceTokens: dedupeBatchTokens(named),
		}, nil
	}
	if api.GroupTargetResolver == nil {
		// Fail closed. A group target with nothing able to resolve it must not silently
		// become an empty batch, which would report success for a fleet write that
		// reached nobody.
		return nil, fmt.Errorf("cannot resolve a group target: group resolution is unavailable")
	}
	return api.resolveGroupTargets(ctx, request)
}

// dedupeBatchTokens returns the distinct tokens in first-seen order.
//
// First-seen order is preserved because it is the order a partially-admitted batch admits
// in, so the caller's own ordering can express priority. Deduplication is not cosmetic: a
// token named twice would otherwise be counted twice against the headroom and collide on
// the intra-batch unique index, turning a caller's copy-paste into either a sanitized
// server error or a silently short batch.
func dedupeBatchTokens(tokens []string) []string {
	seen := make(map[string]struct{}, len(tokens))
	ordered := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, already := seen[token]; already {
			continue
		}
		seen[token] = struct{}{}
		ordered = append(ordered, token)
	}
	return ordered
}

// validateBatchTargets runs the fleet enqueue gate, chunked to the validator's request
// bound. A failure to REACH the owner fails the whole batch with nothing persisted and the
// token unspent, so a clean retry is always available.
func (api *Api) validateBatchTargets(ctx context.Context, tokens []string, name string,
	payload []byte) ([]BatchDeviceRefusal, error) {
	refusals := make([]BatchDeviceRefusal, 0)
	if api.BatchValidator == nil {
		// Matches CreateCommand: when no validator is wired (service secret
		// unconfigured), the gate is skipped rather than failing every enqueue.
		return refusals, nil
	}
	for start := 0; start < len(tokens); start += batchValidationChunk {
		end := start + batchValidationChunk
		if end > len(tokens) {
			end = len(tokens)
		}
		chunk, err := api.BatchValidator.ValidateEnqueueBatch(ctx, tokens[start:end], name, payload)
		if err != nil {
			return nil, fmt.Errorf("cannot enqueue command batch: validation is unavailable")
		}
		refusals = append(refusals, chunk...)
	}
	return refusals, nil
}

// withoutRefused returns the tokens not named in refusals, preserving order.
func withoutRefused(tokens []string, refusals []BatchDeviceRefusal) []string {
	if len(refusals) == 0 {
		return tokens
	}
	refused := make(map[string]struct{}, len(refusals))
	for _, r := range refusals {
		refused[r.DeviceToken] = struct{}{}
	}
	kept := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, no := refused[token]; no {
			continue
		}
		kept = append(kept, token)
	}
	return kept
}

// undeliveredHeadroom is how many more undelivered commands this tenant may hold: the
// ceiling in force minus what it already has. Never negative.
func (api *Api) undeliveredHeadroom(ctx context.Context, tx *gorm.DB) (int, error) {
	ceiling := api.heldCommandCeiling(ctx)
	var undelivered int64
	if err := tx.Model(&Command{}).
		Where("status IN ?", undeliveredStatusStrings()).Count(&undelivered).Error; err != nil {
		return 0, err
	}
	headroom := ceiling - int(undelivered)
	if headroom < 0 {
		return 0, nil
	}
	return headroom, nil
}

// lockTenantForBatch serializes batch admission per tenant for the life of the transaction.
//
// ⚠️ IT IS A NO-OP ON SQLITE, which is what the unit tests run on — pg_advisory_xact_lock
// does not exist there. So no unit test in this package exercises the lock, and none should
// be written that appears to: the overshoot it prevents needs a Postgres-backed test to
// observe at all. Stated plainly here because a lock nobody has seen hold is exactly the
// kind of guard that turns out never to have been wired.
func lockTenantForBatch(ctx context.Context, tx *gorm.DB) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		// The tenant-scope callback already refuses a tenant-scoped query with no tenant,
		// so this is unreachable in practice — but a lock keyed on an empty string would
		// serialize every tenant against every other, which is worse than not locking.
		return fmt.Errorf("cannot lock for batch admission: no tenant in context")
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte("command-batch:" + tenant))
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(hash.Sum64())).Error
}

// insertBatchCommands writes one QUEUED command per admitted device, in chunks.
//
// Every row carries the batch's id AND its token: the id links them for counting and
// cancelling, the token lets the single-table command search filter by batch without a
// join. The command token is platform-generated rather than derived from the batch and
// device tokens — that derivation is impossible here, because the token grammar's only
// legal separators are also legal inside tokens (so `{batch}-{device}` is ambiguous and two
// different pairs can collide onto one string) and two full-length tokens exceed the
// 128-character bound.
func insertBatchCommands(tx *gorm.DB, batch *CommandBatch, deviceTokens []string,
	payload []byte, expiresAt sql.NullTime) error {
	if len(deviceTokens) == 0 {
		return nil
	}
	queuedTime := time.Now()
	rows := make([]*Command, 0, len(deviceTokens))
	for i, deviceToken := range deviceTokens {
		cmd := &Command{
			DeviceToken: deviceToken,
			Name:        batch.Name,
			Status:      CommandQueued.String(),
			QueuedTime:  queuedTime,
			ExpiresAt:   expiresAt,
			BatchId:     sql.NullInt64{Int64: int64(batch.ID), Valid: true},
			BatchToken:  sql.NullString{String: batch.Token, Valid: true},
		}
		cmd.Token = batchCommandToken(batch.ID, i)
		if len(payload) > 0 {
			raw := datatypes.JSON(payload)
			cmd.Payload = &raw
		}
		rows = append(rows, cmd)
	}
	return tx.CreateInBatches(rows, batchInsertChunk).Error
}

// batchCommandToken mints a command's token from the batch's ROW ID and the device's
// position in the admitted order.
//
// 🔴 IT USED THE BATCH TOKEN, TRUNCATED TO 112 CHARACTERS, AND THAT WAS INJECTIVE ONLY BY
// LUCK. Two legal batch tokens sharing a 112-character prefix minted byte-identical command
// tokens, so the second batch died on the (tenant_id, token) unique index — a sanitized
// server error, and every retry failed identically forever, because the input that caused
// it is the input. The comment above it claimed the derivation had been chosen to avoid
// exactly that class of collision.
//
// The row id is injective per tenant by construction and needs no truncation, so the
// property is now structural rather than probabilistic. It is available because the batch
// row is inserted inside the same transaction as its commands.
//
// The `dcb` prefix is a PROVENANCE MARK, not decoration: it is what liveCommandByToken
// keys on to keep a client's own token namespace and this generated one from being
// confused for each other (see batchMintedTokenPrefix).
func batchCommandToken(batchId uint, index int) string {
	return fmt.Sprintf("%s%d-%06d", batchMintedTokenPrefix, batchId, index)
}

// batchMintedTokenPrefix marks a command token as PLATFORM-MINTED for a batch rather than
// chosen by a client.
//
// 🔴 IT EXISTS BECAUSE THE TWO NAMESPACES OVERLAP AND THE COLLISION IS SILENT. A command
// token is a client-chosen idempotency key, and CreateCommand answers a token that already
// names a live command by returning THAT command as a replay. A client issuing a
// single command under a token the batch minter happened to produce therefore got back
// somebody else's command — a different device, a different command name — reported as a
// successful idempotent replay, with nothing created and no error. Since the minter's
// output is predictable, that was reachable by accident, not only by an attacker.
//
// It is a grammar-legal prefix (the token grammar requires an alphanumeric first
// character, so a sigil like "_" is not available) and is deliberately not documented as
// reserved-for-clients-to-avoid — the fix is that the replay probe distinguishes
// provenance, not that clients are asked to stay out of the way.
const batchMintedTokenPrefix = "dcb"

// summarizeRefusals renders refusals for storage: a bounded per-code sample and the
// COMPLETE per-code totals, so `resolved = accepted + sum(counts)` holds however large the
// refusal set was.
//
// It shares boundRefusals with the rejection path deliberately. The persisted record and
// the rejection payload are two views of the same event, and if they truncated differently
// a caller comparing what it was told with what was stored would find them disagreeing.
func summarizeRefusals(refusals []BatchDeviceRefusal) (*datatypes.JSON, *datatypes.JSON) {
	if len(refusals) == 0 {
		return nil, nil
	}
	kept, counts := boundRefusals(refusals)
	return jsonOrNil(kept), jsonOrNil(counts)
}

// jsonOrNil marshals a value, returning nil rather than an error-shaped column on failure.
// Both inputs are plain structs of strings and ints, so a marshal failure is not reachable;
// nil keeps a summarization problem from failing an otherwise-good fleet write.
func jsonOrNil(v any) *datatypes.JSON {
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	raw := datatypes.JSON(encoded)
	return &raw
}

// liveBatchByToken is the batch replay probe. Like liveCommandByToken it is a Find with a
// limit rather than a First, so a miss — the common case on a first issue — is reported as
// RowsAffected==0 rather than logged as an error on every batch created.
func (api *Api) liveBatchByToken(ctx context.Context, token string) (*CommandBatch, bool, error) {
	found := &CommandBatch{}
	result := api.RDB.DB(ctx).Where("token = ?", token).Limit(1).Find(found)
	if result.Error != nil {
		return nil, false, result.Error
	}
	return found, result.RowsAffected == 1, nil
}

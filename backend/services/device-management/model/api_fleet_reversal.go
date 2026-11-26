// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Reversal. A reversal is a new operation of kind config.set with reversal_of set: one line per selected
// line of the original, each restoring the before-image the original recorded when it applied that line.
// It goes through the same two-step create, the same start (a person approves its own contract) and the
// same posting transaction as any operation. What is particular to it:
//
//   - the original must have finished dispatching (DISPATCHED or ABORTED). Neither state returns to
//     RUNNING, so no line of the original can post after the reversal is planned;
//   - a reversal is never itself reversed;
//   - the selection and the override set are in the request digest and in the contract, so start approves them;
//   - any override needs a fresh second factor at create;
//   - at post, under the device lock, the original line is read again: a redacted or purged original
//     refuses PRIOR_REDACTED, an original another reversal already restored refuses ALREADY_REVERSED, and
//     a key that changed since the original was applied refuses CHANGED_SINCE unless the line is overridden.
//     Changed-since is ABA-proof: it walks every configuration revision after the original's, and it
//     compares the key's generation, which every attribute write advances, minted revision or not;
//   - the partial unique index on (reverses_line_id) WHERE posting = 'POSTED' is the backstop: at most
//     one applied reversal per original line, while refused attempts never consume the original;
//   - retention skips an original while a reversal of it is live, and a purged original refuses EVIDENCE_EXPIRED.

// Reversal refusals. They ride extensions.code.
const (
	FleetCodeEvidenceExpired          = "EVIDENCE_EXPIRED"
	FleetCodeNothingToReverse         = "NOTHING_TO_REVERSE"
	FleetCodeOriginalStillDispatching = "ORIGINAL_STILL_DISPATCHING"
	FleetCodeReversalNotReversible    = "REVERSAL_NOT_REVERSIBLE"
)

// Codes a reversal line carries.
const (
	// FleetLineNothingToReverse: the original line was never applied. Stored SKIPPED at plan time.
	FleetLineNothingToReverse = "NOTHING_TO_REVERSE"
	// FleetLinePriorRedacted: the original's before-image was erased with its device, or purged.
	FleetLinePriorRedacted = "PRIOR_REDACTED"
	// FleetLineAlreadyReversed: another reversal already restored the original line.
	FleetLineAlreadyReversed = "ALREADY_REVERSED"
	// FleetLineChangedSince: the key changed after the original was applied, and the line is not overridden.
	FleetLineChangedSince = "CHANGED_SINCE"

	fleetEventOverrideChanged = "OVERRIDE_CHANGED"
)

// FleetReversalRequest asks to reverse an earlier operation.
type FleetReversalRequest struct {
	// Original is the token of the operation to reverse.
	Original string
	Token    string
	Name     string
	// Ordinals selects the original's lines by ordinal. Nil reverses every applied line; an empty list
	// reverses nothing and is refused.
	Ordinals *[]int32
	// OverrideChanged applies every selected line over a change made since the original.
	OverrideChanged bool
	// OverrideOrdinals applies only these lines over such a change.
	OverrideOrdinals []int32
	Safety           FleetSafetyRequest
}

func (r *FleetReversalRequest) wantsOverride() bool {
	return r.OverrideChanged || len(r.OverrideOrdinals) > 0
}

// fleetReversalNormalized is the part of a normalized request only a reversal has.
type fleetReversalNormalized struct {
	original    *FleetOperation
	key         string
	selection   []int32 // nil: every applied line; otherwise sorted and unique
	overrideAll bool
	overrides   map[int32]bool
}

// FleetReversalPreview is what a reversal would do, computed without writing anything.
type FleetReversalPreview struct {
	// ReversibleCount is the lines that would be applied.
	ReversibleCount int
	// ChangedSinceCount is how many of those show a change made since the original, as of this read.
	ChangedSinceCount    int
	RedactedCount        int
	AlreadyReversedCount int
	NotPostedCount       int
	RequestDigest        string
	PlanDigest           string
	ContractDigest       string
}

// fleetReversibleDispatch reports whether an original has finished dispatching. RUNNING and PAUSED have
// not: a paused original can be resumed, and a line it posts after the reversal was planned would never
// be reversed.
func fleetReversibleDispatch(dispatch string) bool {
	return dispatch == FleetDispatchDispatched || dispatch == FleetDispatchAborted
}

// fleetReversalAdmissible is every refusal that depends only on the original's header.
func fleetReversalAdmissible(orig *FleetOperation) error {
	if !ledger.Drivable(ledger.DriverProtocol(orig.DriverProtocol)) {
		return fleetErr(FleetCodeNewerThanServer, "bulk change %q was created by a newer release", orig.Token)
	}
	if orig.ReversalOf.Valid {
		return fleetErr(FleetCodeReversalNotReversible, "bulk change %q is itself a reversal and cannot be reversed", orig.Token)
	}
	if !fleetReversibleDispatch(orig.Dispatch) {
		return fleetErr(FleetCodeOriginalStillDispatching, "bulk change %q is %s; only a DISPATCHED or ABORTED one can be reversed", orig.Token, orig.Dispatch)
	}
	if orig.LinesPurgedAt.Valid || orig.PauseReason == fleetRetentionExpired {
		return fleetErr(FleetCodeEvidenceExpired, "the lines of bulk change %q are past retention and cannot be reversed", orig.Token)
	}
	return nil
}

// sortedOrdinals validates, sorts and deduplicates a list of ordinals.
func sortedOrdinals(in []int32, what string) ([]int32, error) {
	if len(in) > ledger.MaxLinesPerOperation {
		return nil, fleetErr(FleetCodeOperationTooLarge, "%s may name at most %d lines", what, ledger.MaxLinesPerOperation)
	}
	out := slices.Clone(in)
	for _, o := range out {
		if o < 1 {
			return nil, fleetErr(FleetCodeInvalidRequest, "%s start at 1", what)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

func ordinals64(in []int32) []int64 {
	if in == nil {
		return nil
	}
	out := make([]int64, len(in))
	for i, o := range in {
		out[i] = int64(o)
	}
	return out
}

// normalizeFleetReversal validates a reversal request against its original's header and computes its
// request digest. It reads nothing, so the replay probe can run on its answer.
func normalizeFleetReversal(req *FleetReversalRequest, orig *FleetOperation) (*fleetNormalized, error) {
	if orig.Kind != FleetKindConfigSet {
		return nil, fleetErr(FleetCodeKindNotExecutable, "kind %q cannot be reversed by this build", orig.Kind)
	}
	origIntent, err := fleetIntentFrom(orig.Intent)
	if err != nil {
		return nil, err
	}
	r := &fleetReversalNormalized{original: orig, key: origIntent.Key, overrideAll: req.OverrideChanged, overrides: map[int32]bool{}}
	if req.Ordinals != nil {
		if len(*req.Ordinals) == 0 {
			return nil, fleetErr(FleetCodeNothingToReverse, "an empty selection reverses nothing; omit ordinals to reverse every applied line")
		}
		if r.selection, err = sortedOrdinals(*req.Ordinals, "ordinals"); err != nil {
			return nil, err
		}
	}
	if req.OverrideChanged && len(req.OverrideOrdinals) > 0 {
		return nil, fleetErr(FleetCodeInvalidRequest, "give overrideChanged or overrideOrdinals, not both")
	}
	overrides, err := sortedOrdinals(req.OverrideOrdinals, "overrideOrdinals")
	if err != nil {
		return nil, err
	}
	for _, o := range overrides {
		if r.selection != nil {
			if _, found := slices.BinarySearch(r.selection, o); !found {
				return nil, fleetErr(FleetCodeInvalidRequest, "override ordinal %d is not in the selection", o)
			}
		}
		r.overrides[o] = true
	}
	safety, err := normalizeFleetSafety(req.Safety)
	if err != nil {
		return nil, err
	}
	intent := FleetConfigSetIntent{Key: origIntent.Key, Reverse: true, OverrideAll: req.OverrideChanged}
	intentJSON, err := ledger.Canonical(intent)
	if err != nil {
		return nil, err
	}
	origID := int64(orig.ID)
	digest, err := ledger.RequestDigest(ledger.RequestDigestInput{Kind: FleetKindConfigSet, Intent: intent, Safety: safety,
		Overrides: ledger.Overrides{ReversalOf: &origID, LineSelection: ordinals64(r.selection), OverrideOrdinals: ordinals64(overrides)}})
	if err != nil {
		return nil, err
	}
	return &fleetNormalized{kind: FleetKindConfigSet, intent: intent, intentJSON: string(intentJSON), safety: safety,
		requestDigest: digest, reversal: r}, nil
}

// fleetReversalCounts is how a reversal plan classified the selected lines.
type fleetReversalCounts struct {
	reversible, redacted, alreadyReversed, notPosted int
	// admitted holds the original lines that would be applied, for the changed-since preview.
	admitted []FleetOperationLine
}

// planFleetReversal reads the original's lines and freezes the reversal's lines and digests. It writes
// nothing and takes no lock: what it reads is re-checked where it matters, under the original's fence at
// create and under the device lock at post.
func (api *Api) planFleetReversal(ctx context.Context, n *fleetNormalized) (*FleetPlan, *fleetReversalCounts, error) {
	r := n.reversal
	orig := r.original
	db := api.RDB.DB(ctx)
	var all []FleetOperationLine
	if err := db.Select("id", "ordinal", "target_id", "target_key", "posting", "prior", "posted_value", "posted_revision", "redacted_at").
		Where("operation_id = ?", orig.ID).Order("ordinal").Find(&all).Error; err != nil {
		return nil, nil, err
	}
	if len(all) != int(orig.TargetCount) {
		// Lines leave an operation only through retention: a partial set is evidence already expiring.
		return nil, nil, fleetErr(FleetCodeEvidenceExpired, "the lines of bulk change %q are past retention and cannot be reversed", orig.Token)
	}
	var selected []FleetOperationLine
	if r.selection == nil {
		for _, l := range all {
			if l.Posting == FleetPostingPosted {
				selected = append(selected, l)
			}
		}
	} else {
		byOrdinal := make(map[int32]FleetOperationLine, len(all))
		for _, l := range all {
			byOrdinal[l.Ordinal] = l
		}
		for _, o := range r.selection {
			l, ok := byOrdinal[o]
			if !ok {
				return nil, nil, fleetErr(FleetCodeInvalidRequest, "ordinal %d is not a line of bulk change %q", o, orig.Token)
			}
			selected = append(selected, l)
		}
	}
	inSelection := make(map[int32]bool, len(selected))
	for _, l := range selected {
		inSelection[l.Ordinal] = true
	}
	for o := range r.overrides {
		if !inSelection[o] {
			return nil, nil, fleetErr(FleetCodeInvalidRequest, "override ordinal %d is not a line being reversed", o)
		}
	}
	reversed, err := fleetReversedLines(db, selected)
	if err != nil {
		return nil, nil, err
	}

	plan := &FleetPlan{Kind: n.kind, Intent: n.intentJSON, Safety: n.safety, RefusalCounts: map[string]int{}}
	counts := &fleetReversalCounts{}
	digestLines := make([]ledger.PlanLine, 0, len(selected))
	var selection, overridden []int64
	for _, l := range selected {
		id := l.ID
		line := FleetPlanLine{Ordinal: l.Ordinal, Token: l.TargetKey, ReversesLineId: &id, OverrideChanged: r.overrideAll || r.overrides[l.Ordinal]}
		if l.TargetId.Valid {
			t := uint(l.TargetId.Int64)
			line.TargetId = &t
		}
		switch {
		case l.Posting != FleetPostingPosted:
			line.Code, line.Posting = FleetLineNothingToReverse, string(ledger.PostingSkipped)
			counts.notPosted++
		case l.RedactedAt.Valid || !l.Prior.Valid || !l.PostedValue.Valid:
			line.Code = FleetLinePriorRedacted
			counts.redacted++
		case reversed[l.ID]:
			line.Code = FleetLineAlreadyReversed
			counts.alreadyReversed++
		default:
			counts.reversible++
			counts.admitted = append(counts.admitted, l)
		}
		dl := ledger.PlanLine{Ordinal: int64(line.Ordinal), Admission: "ADMITTED"}
		if line.TargetId != nil {
			t := int64(*line.TargetId)
			dl.TargetID = &t
		}
		if line.Code != "" {
			dl.Admission = line.Code
			plan.Refused++
			plan.RefusalCounts[line.Code]++
			if plan.RefusalCounts[line.Code] <= MaxFleetPlanRefusals {
				plan.Refusals = append(plan.Refusals, FleetPlanRefusal{Ordinal: line.Ordinal, Token: line.Token, Code: line.Code})
			}
		} else {
			plan.Admitted++
		}
		digestLines = append(digestLines, dl)
		plan.Lines = append(plan.Lines, line)
		selection = append(selection, int64(l.Ordinal))
		if line.OverrideChanged {
			overridden = append(overridden, int64(l.Ordinal))
		}
	}
	origID := int64(orig.ID)
	if err := sealFleetPlan(plan, n, digestLines, ledger.Overrides{ReversalOf: &origID, LineSelection: selection, OverrideOrdinals: overridden}); err != nil {
		return nil, nil, err
	}
	return plan, counts, nil
}

// fleetReversedLines answers which of the given original lines already have an applied reversal: a
// retained POSTED contra, or the REVERSED event retention leaves on the original line when it purges one.
func fleetReversedLines(db *gorm.DB, lines []FleetOperationLine) (map[uint]bool, error) {
	ids := make([]uint, 0, len(lines))
	for _, l := range lines {
		ids = append(ids, l.ID)
	}
	out, err := fleetContraRecorded(db, ids)
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(ids); start += rdb.MaxLookupKeys {
		chunk := ids[start:min(start+rdb.MaxLookupKeys, len(ids))]
		var contras []FleetOperationLine
		if err := db.Select("reverses_line_id").Where("posting = ? AND reverses_line_id IN ?", FleetPostingPosted, chunk).Find(&contras).Error; err != nil {
			return nil, err
		}
		for _, c := range contras {
			out[uint(c.ReversesLineId.Int64)] = true
		}
	}
	return out, nil
}

// fleetEventReversed is left on an original line when retention purges the applied reversal line that
// restored it. The contra line and its unique-index entry go with the reversal's lines, while the original
// may be retained much longer (its evidence still open): without this record a later reversal could
// restore the same line a second time. It holds no identifier and leaves with the original's lines.
const fleetEventReversed = "REVERSED"

// fleetContraRecorded answers which of the given original lines carry a REVERSED event.
func fleetContraRecorded(db *gorm.DB, ids []uint) (map[uint]bool, error) {
	out := make(map[uint]bool)
	for start := 0; start < len(ids); start += rdb.MaxLookupKeys {
		chunk := ids[start:min(start+rdb.MaxLookupKeys, len(ids))]
		var events []FleetOperationEvent
		if err := db.Select("line_id").Where("kind = ? AND line_id IN ?", fleetEventReversed, chunk).Find(&events).Error; err != nil {
			return nil, err
		}
		for _, e := range events {
			out[uint(e.LineId.Int64)] = true
		}
	}
	return out, nil
}

// fleetRecordPurgedContrasOnTx runs in retention's transaction, under the reversal's own fence, just before
// a chunk of its lines is deleted: every applied contra in the chunk becomes a REVERSED event on the line it
// restored, in the same transaction, so there is no moment at which neither record exists.
func fleetRecordPurgedContrasOnTx(tx *gorm.DB, op *FleetOperation, lineIDs *gorm.DB) error {
	if !op.ReversalOf.Valid {
		return nil
	}
	var contras []FleetOperationLine
	if err := tx.Select("id", "reverses_line_id").Where("id IN (?) AND posting = ? AND reverses_line_id IS NOT NULL", lineIDs, FleetPostingPosted).
		Find(&contras).Error; err != nil {
		return err
	}
	if len(contras) == 0 {
		return nil
	}
	at, err := fleetMaintenanceNow(tx)
	if err != nil {
		return err
	}
	events := make([]FleetOperationEvent, len(contras))
	for i, c := range contras {
		events[i] = FleetOperationEvent{OperationId: op.ReversalOf.Int64, LineId: c.ReversesLineId, At: at, Kind: fleetEventReversed}
	}
	return tx.CreateInBatches(&events, fleetLinesPerInsert).Error
}

// fleetReversalOriginal reads a reversal request's original and answers the normalized request.
func (api *Api) fleetReversalOriginal(ctx context.Context, req *FleetReversalRequest) (*fleetNormalized, error) {
	if req == nil {
		return nil, fleetErr(FleetCodeInvalidRequest, "a request is required")
	}
	if strings.TrimSpace(req.Original) == "" {
		return nil, fleetErr(FleetCodeInvalidRequest, "the original bulk change's token is required")
	}
	orig, err := api.fleetOperationByToken(ctx, req.Original)
	if err != nil {
		return nil, err
	}
	if orig == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return normalizeFleetReversal(req, orig)
}

// PreviewFleetReversal answers what reversing would do, writing nothing. The changed-since count is a
// read of this moment; the posting transaction decides each line again under the device lock.
func (api *Api) PreviewFleetReversal(ctx context.Context, req *FleetReversalRequest) (*FleetReversalPreview, error) {
	n, err := api.fleetReversalOriginal(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := fleetReversalAdmissible(n.reversal.original); err != nil {
		return nil, err
	}
	plan, counts, err := api.planFleetReversal(ctx, n)
	if err != nil {
		return nil, err
	}
	out := &FleetReversalPreview{ReversibleCount: counts.reversible, RedactedCount: counts.redacted,
		AlreadyReversedCount: counts.alreadyReversed, NotPostedCount: counts.notPosted,
		RequestDigest: plan.RequestDigest, PlanDigest: plan.PlanDigest, ContractDigest: plan.ContractDigest}
	if out.ChangedSinceCount, err = fleetCountChangedSince(api.RDB.DB(ctx), n.reversal.key, counts.admitted); err != nil {
		return nil, err
	}
	return out, nil
}

// ReverseFleetOperation creates a reversal in READY, through the same two-step create as any operation,
// and is idempotent on its token the same way. Any override needs a fresh second factor.
func (api *Api) ReverseFleetOperation(ctx context.Context, req *FleetReversalRequest, actor string) (*FleetOperation, error) {
	if req == nil {
		return nil, fleetErr(FleetCodeInvalidRequest, "a request is required")
	}
	if req.Token == "" || len(req.Token) > 128 || req.Name == "" || len(req.Name) > 128 || actor == "" {
		return nil, fleetErr(FleetCodeInvalidRequest, "token and name (1-128 characters) and an initiator are required")
	}
	if !fleetKindExecutable(FleetKindConfigSet) {
		return nil, fleetErr(FleetCodeKindNotExecutable, "kind %q cannot be executed by this build", FleetKindConfigSet)
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, errors.New("a bulk change needs a tenant in the context")
	}
	n, err := api.fleetReversalOriginal(ctx, req)
	if err != nil {
		return nil, err
	}
	// The replay probe comes before every check of the original's state: replaying a reversal that was
	// created writes nothing, whatever has happened to the original since.
	existing, err := api.fleetOperationByToken(ctx, req.Token)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.RequestDigest != n.requestDigest {
			return nil, fleetErr(FleetCodeConflict, "operation token %q is already used by a different request", req.Token)
		}
		if existing.Dispatch != FleetDispatchPlanning {
			return existing, nil
		}
	}
	// Step-up guards a write, so it comes after the replay probe: replaying a reversal that was created
	// writes nothing and needs no fresh factor. Finishing a header an earlier call left in PLANNING does
	// write, and is checked like a first create.
	if req.wantsOverride() {
		if _, _, err := api.fleetSecondFactor(ctx, "overriding a change in a reversal", "reversed with an override"); err != nil {
			return nil, err
		}
	}
	if err := fleetReversalAdmissible(n.reversal.original); err != nil {
		return nil, err
	}
	plan, _, err := api.planFleetReversal(ctx, n)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		if plan.Admitted == 0 {
			return nil, fleetErr(FleetCodeNothingToReverse, "no selected line of bulk change %q can be reversed", n.reversal.original.Token)
		}
		// Step 1 reads only the token and the name of the request it is given.
		if existing, err = api.fleetCreateStepOne(ctx, tenant, &FleetOperationCreateRequest{Token: req.Token, Name: req.Name}, n, plan, actor); err != nil {
			return nil, err
		}
	}
	return api.fleetCreateStepTwo(ctx, tenant, existing, plan, actor)
}

// fleetReversalFenceOnTx re-checks the original under its own fence, SHARED, inside create step 1. Retention
// takes that fence exclusively and skips an original with a live reversal, and every control transition
// takes it exclusively too, so once the reversal's header commits the original can neither resume nor be
// purged under it. A partial line set means retention already started: the evidence has expired.
func fleetReversalFenceOnTx(tx *gorm.DB, tenant string, r *fleetReversalNormalized) error {
	if err := fleetLock(tx, fleetOpKey(tenant, r.original.ID), true); err != nil {
		return err
	}
	var cur FleetOperation
	if err := tx.First(&cur, r.original.ID).Error; err != nil {
		return err
	}
	if err := fleetReversalAdmissible(&cur); err != nil {
		return err
	}
	var lines int64
	if err := tx.Model(&FleetOperationLine{}).Where("operation_id = ?", cur.ID).Count(&lines).Error; err != nil {
		return err
	}
	if lines != int64(cur.TargetCount) {
		return fleetErr(FleetCodeEvidenceExpired, "the lines of bulk change %q are past retention and cannot be reversed", cur.Token)
	}
	return nil
}

// fleetReversalPost is what the posting transaction does with one reversal line.
type fleetReversalPost struct {
	// target is the value the line writes: the original's before-image.
	target fleetValueImage
	// changed is true when the key changed since the original and the line is overridden.
	changed bool
	// decided is a refusal; nothing is written.
	decided *fleetDecision
	// original is the line being reversed, for the revision walk after the effect.
	original *FleetOperationLine
}

// fleetReversalOnTx judges one reversal line inside the posting transaction. The caller holds the device
// lock: every read here is of state no other writer can change before the effect commits.
func fleetReversalOnTx(tx *gorm.DB, op *FleetOperation, line *FleetOperationLine, devID uint, intent *FleetConfigSetIntent) (*fleetReversalPost, error) {
	if !intent.Reverse || !op.ReversalOf.Valid || !line.ReversesLineId.Valid {
		return nil, fmt.Errorf("bulk change line %d is not a well-formed reversal line", line.ID)
	}
	refuse := func(code, detail string) (*fleetReversalPost, error) {
		return &fleetReversalPost{decided: &fleetDecision{FleetPostingRefused, code, detail}}, nil
	}
	var orig FleetOperationLine
	err := tx.Where("id = ? AND operation_id = ?", line.ReversesLineId.Int64, op.ReversalOf.Int64).First(&orig).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return refuse(FleetLinePriorRedacted, "the original line is no longer retained")
	}
	if err != nil {
		return nil, err
	}
	if orig.RedactedAt.Valid || !orig.Prior.Valid || !orig.PostedValue.Valid || orig.Posting != FleetPostingPosted {
		return refuse(FleetLinePriorRedacted, "the original line's before-image was erased")
	}
	if !orig.TargetId.Valid || orig.TargetId.Int64 != int64(devID) {
		return nil, fmt.Errorf("bulk change line %d reverses a line of another device", line.ID)
	}
	var winners []FleetOperationLine
	if err := tx.Select("id", "operation_id", "ordinal").Where("reverses_line_id = ? AND posting = ? AND id <> ?", orig.ID, FleetPostingPosted, line.ID).
		Limit(1).Find(&winners).Error; err != nil {
		return nil, err
	}
	if len(winners) > 0 {
		var w FleetOperation
		if err := tx.Select("id", "token").First(&w, winners[0].OperationId).Error; err != nil {
			return nil, err
		}
		return refuse(FleetLineAlreadyReversed, fmt.Sprintf("already reversed by line %d of bulk change %q", winners[0].Ordinal, w.Token))
	}
	if reversed, err := fleetContraRecorded(tx, []uint{orig.ID}); err != nil {
		return nil, err
	} else if reversed[orig.ID] {
		return refuse(FleetLineAlreadyReversed, "already reversed by a bulk change whose lines are past retention")
	}
	// Changed-since, first half: the generation. Only an attribute writer advances it, and every one of
	// them holds the device lock this transaction holds, so this read cannot go stale before the effect.
	var posted fleetValueImage
	if err := json.Unmarshal([]byte(orig.PostedValue.String), &posted); err != nil {
		return nil, err
	}
	gen, _, err := attributeGeneration(tx, devID, string(AttributeScopeShared), intent.Key)
	if err != nil {
		return nil, err
	}
	changed := gen != posted.Generation
	if changed && !line.OverrideChanged {
		return refuse(FleetLineChangedSince, fleetChangedSinceDetail)
	}
	var prior fleetValueImage
	if err := json.Unmarshal([]byte(orig.Prior.String), &prior); err != nil {
		return nil, err
	}
	prior.Generation = 0
	return &fleetReversalPost{target: prior, changed: changed, original: &orig}, nil
}

const fleetChangedSinceDetail = "the key changed after the original was applied; reverse it again with an override to apply it anyway"

// fleetReversalRevisionsOnTx is changed-since, second half: the revision walk. It runs AFTER the effect,
// under the device's configuration lock. The device lock does not serialise with revision minting: a
// configuration sync mints under the configuration lock alone, so a walk taken earlier could miss a
// revision a sync had minted and not yet committed. Under the configuration lock every revision minted
// before this one is committed and visible (each statement reads a fresh snapshot), and the walk stops
// below the revision this transaction minted itself, if it minted one.
func fleetReversalRevisionsOnTx(ctx context.Context, tx *gorm.DB, devID uint, key string, orig *FleetOperationLine, minted *DeviceConfigurationRevision) (bool, error) {
	if err := lockDeviceConfiguration(ctx, tx, devID); err != nil {
		return false, err
	}
	before := int64(0)
	if minted != nil {
		before = minted.Revision
	}
	return fleetRevisionsChangedKey(tx, devID, key, orig.PostedRevision, before)
}

// Changed-since asks two questions, and either answer is enough:
//  1. the key's generation differs from the one the original recorded. Every attribute write advances
//     it, so this sees a write that minted no revision (the key undeclared at the time) and a write that
//     put back the same value;
//  2. some configuration revision after the original's changed the key's presence, value or type
//     relative to its predecessor. This sees a change that wrote no attribute, such as a profile that
//     stopped declaring the key and then declared it again: the current value alone would look untouched.

// fleetCountChangedSince counts, for a preview, the original lines whose key changed since they were
// applied. It asks the same two questions as the posting transaction, but in batches: the generation heads and
// the latest revision of every device in one read per chunk, and the revision walk only for a device that
// minted a revision after the original's. A device with no newer revision and the same generation is
// unchanged without a walk, which is the common case for an undo soon after the change.
func fleetCountChangedSince(db *gorm.DB, key string, lines []FleetOperationLine) (int, error) {
	changed := 0
	for start := 0; start < len(lines); start += rdb.MaxLookupKeys {
		chunk := lines[start:min(start+rdb.MaxLookupKeys, len(lines))]
		ids := make([]int64, len(chunk))
		for i, l := range chunk {
			ids[i] = l.TargetId.Int64
		}
		var heads []AttributeGeneration
		if err := db.Select("device_id", "generation").Where("scope = ? AND attr_key = ? AND device_id IN ?", string(AttributeScopeShared), key, ids).
			Find(&heads).Error; err != nil {
			return 0, err
		}
		gens := make(map[int64]int64, len(heads))
		for _, h := range heads {
			gens[int64(h.DeviceId)] = h.Generation
		}
		var latest []struct {
			DeviceId int64
			Revision int64
		}
		if err := db.Model(&DeviceConfigurationRevision{}).Select("device_id, MAX(revision) AS revision").Where("device_id IN ?", ids).
			Group("device_id").Scan(&latest).Error; err != nil {
			return 0, err
		}
		newest := make(map[int64]int64, len(latest))
		for _, r := range latest {
			newest[r.DeviceId] = r.Revision
		}
		for i := range chunk {
			l := &chunk[i]
			var posted fleetValueImage
			if err := json.Unmarshal([]byte(l.PostedValue.String), &posted); err != nil {
				return 0, err
			}
			dev := l.TargetId.Int64
			if gens[dev] != posted.Generation {
				changed++
				continue
			}
			top, minted := newest[dev]
			if (!minted && !l.PostedRevision.Valid) || (minted && l.PostedRevision.Valid && top <= l.PostedRevision.Int64) {
				continue // no revision after the original's: nothing to walk
			}
			moved, err := fleetRevisionsChangedKey(db, uint(dev), key, l.PostedRevision, 0)
			if err != nil {
				return 0, err
			}
			if moved {
				changed++
			}
		}
	}
	return changed, nil
}

// fleetRevisionWalkChunk bounds how many revision documents one read of the walk holds.
const fleetRevisionWalkChunk = 100

// fleetRevisionsChangedKey walks the device's revisions from the original's (its baseline) to the latest,
// or to the last one below before when before is not 0, and reports whether any of them changed the key
// relative to its predecessor. An original that minted no revision because the device had none counts
// every later appearance of the key as a change.
func fleetRevisionsChangedKey(db *gorm.DB, devID uint, key string, from sql.NullInt64, before int64) (bool, error) {
	after := int64(0)
	var prev *string
	started := !from.Valid
	if from.Valid {
		after = from.Int64 - 1
	}
	for {
		var revs []DeviceConfigurationRevision
		q := db.Select("revision", "document").Where("device_id = ? AND revision > ?", devID, after)
		if before > 0 {
			q = q.Where("revision < ?", before)
		}
		if err := q.Order("revision").Limit(fleetRevisionWalkChunk).Find(&revs).Error; err != nil {
			return false, err
		}
		if len(revs) == 0 {
			return false, nil
		}
		for _, r := range revs {
			var doc map[string]json.RawMessage
			if err := json.Unmarshal([]byte(r.Document), &doc); err != nil {
				return false, err
			}
			var cur *string
			if raw, ok := doc[key]; ok {
				s := string(raw)
				cur = &s
			}
			if !started {
				if r.Revision != from.Int64 {
					// The original's own revision is missing: nothing proves the key is unchanged.
					return true, nil
				}
				started = true
			} else if (prev == nil) != (cur == nil) || (prev != nil && *prev != *cur) {
				return true, nil
			}
			prev = cur
			after = r.Revision
		}
	}
}

// FleetOperationByID reads one operation of the caller's tenant, or nil.
func (api *Api) FleetOperationByID(ctx context.Context, id int64) (*FleetOperation, error) {
	var op FleetOperation
	err := api.RDB.DB(ctx).First(&op, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &op, nil
}

// FleetReversalsOf lists the reversals of an operation, oldest first.
func (api *Api) FleetReversalsOf(ctx context.Context, op *FleetOperation) ([]FleetOperation, error) {
	out := make([]FleetOperation, 0)
	err := api.RDB.DB(ctx).Where("reversal_of = ?", op.ID).Order("id").Find(&out).Error
	return out, err
}

// FleetLineReversedBy answers the applied reversal line of a line, or nil when nothing restored it.
func (api *Api) FleetLineReversedBy(ctx context.Context, line *FleetOperationLine) (*FleetOperationLine, error) {
	var contra FleetOperationLine
	err := api.RDB.DB(ctx).Where("reverses_line_id = ? AND posting = ?", line.ID, FleetPostingPosted).First(&contra).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &contra, nil
}

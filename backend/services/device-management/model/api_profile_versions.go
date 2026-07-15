// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// This file implements device-profile versioning (ADR-045 slice c), mirroring the
// dashboard versioning machinery (ADR-039): the live definition tables are the
// mutable DRAFT; PublishDeviceProfile freezes that draft into the next immutable
// DeviceProfileVersion and points the profile's ActiveVersion at it; a device
// resolves the ACTIVE PUBLISHED version, not the draft (see activeProfileSnapshot,
// consumed by the ...ByDeviceType loaders). RollbackDeviceProfile re-points
// ActiveVersion at an earlier version (non-destructive; the draft is untouched).

// buildProfileSnapshot serializes a profile's current draft — its metric, command,
// and detection-rule definitions — into a ProfileSnapshot document. The
// back-reference to the profile is cleared on each definition so the blob stays tight
// and acyclic. Disabled definitions are captured too (the flag travels with the version).
func (api *Api) buildProfileSnapshot(ctx context.Context, profileId uint) (datatypes.JSON, error) {
	metrics, err := api.MetricDefinitionsByDeviceProfile(ctx, profileId)
	if err != nil {
		return nil, err
	}
	commands, err := api.CommandDefinitionsByDeviceProfile(ctx, profileId)
	if err != nil {
		return nil, err
	}
	rules, err := api.DetectionRulesByDeviceProfile(ctx, profileId)
	if err != nil {
		return nil, err
	}
	for _, m := range metrics {
		m.DeviceProfile = nil
	}
	for _, c := range commands {
		c.DeviceProfile = nil
	}
	for _, dr := range rules {
		dr.DeviceProfile = nil
	}
	raw, err := json.Marshal(ProfileSnapshot{Metrics: metrics, Commands: commands, Rules: rules})
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(raw), nil
}

// parseProfileSnapshot decodes a version's snapshot blob back into definition
// lists, normalizing nil lists to empty slices so callers never dereference nil.
func parseProfileSnapshot(raw datatypes.JSON) (*ProfileSnapshot, error) {
	snap := &ProfileSnapshot{
		Metrics:  []*MetricDefinition{},
		Commands: []*CommandDefinition{},
		Rules:    []*DetectionRule{},
	}
	if len(raw) == 0 {
		return snap, nil
	}
	if err := json.Unmarshal(raw, snap); err != nil {
		return nil, err
	}
	if snap.Metrics == nil {
		snap.Metrics = []*MetricDefinition{}
	}
	if snap.Commands == nil {
		snap.Commands = []*CommandDefinition{}
	}
	if snap.Rules == nil {
		snap.Rules = []*DetectionRule{}
	}
	return snap, nil
}

// validateSnapshotDetectionRules compiles the ENABLED detection rules carried in a
// just-built profile snapshot against event-processing (ADR-044 sync gate), returning a
// fail-closed error if any rule is rejected or the check cannot be performed. It parses the
// exact snapshot bytes the publish is about to freeze, so what is validated is precisely
// what is frozen (no TOCTOU with a separate draft read). Only enabled rules are gated: a
// disabled rule is inert (the runtime skips it at the fact-emit boundary) and a parked,
// still-WIP rule must not block publishing the rest — it is re-validated when a later draft
// enables it. With no validator wired (service secret unset) the gate is skipped.
func (api *Api) validateSnapshotDetectionRules(ctx context.Context, snapshot datatypes.JSON) error {
	if api.DetectionRuleValidator == nil {
		return nil
	}
	snap, err := parseProfileSnapshot(snapshot)
	if err != nil {
		return err
	}
	toValidate := enabledRulesToValidate(snap.Rules)
	if len(toValidate) == 0 {
		return nil
	}

	failures, err := api.DetectionRuleValidator.ValidateDetectionRules(ctx, toValidate)
	if err != nil {
		// A transport/availability failure: fail the publish closed, but log the detail
		// and return a sanitized message so the tenant API client learns nothing of the
		// in-cluster topology (mirrors command-delivery's device check).
		log.Error().Err(err).
			Msg("Detection-rule validation failed; refusing profile publish.")
		return fmt.Errorf("cannot publish device profile: detection-rule validation is unavailable")
	}
	if len(failures) > 0 {
		msgs := make([]string, 0, len(failures))
		for _, f := range failures {
			msgs = append(msgs, fmt.Sprintf("%q: %s", f.Token, f.Message))
		}
		return fmt.Errorf("cannot publish device profile: %d detection rule(s) invalid: %s",
			len(failures), strings.Join(msgs, "; "))
	}
	return nil
}

// enabledRulesToValidate projects the ENABLED detection rules to the (token, definition)
// pairs the validation gate compiles. Disabled rules are dropped: they are inert (the
// runtime skips them at the fact-emit boundary), so gating a publish on a parked, still-WIP
// rule would only block shipping the rest — it is re-validated if a later draft enables it.
func enabledRulesToValidate(rules []*DetectionRule) []RuleToValidate {
	out := make([]RuleToValidate, 0, len(rules))
	for _, dr := range rules {
		if !dr.Enabled {
			continue
		}
		out = append(out, RuleToValidate{
			Token:      dr.Token,
			Definition: string(dr.Definition),
			// A rule is group-scoped iff it pins a group token (ADR-062 S4).
			GroupScoped: dr.EntityGroupToken != nil && *dr.EntityGroupToken != "",
		})
	}
	return out
}

// deviceProfileByToken loads the single profile addressed by token, returning
// gorm.ErrRecordNotFound when absent so the versioning entry points fail closed.
func (api *Api) deviceProfileByToken(ctx context.Context, token string) (*DeviceProfile, error) {
	matches, err := api.DeviceProfilesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return matches[0], nil
}

// PublishDeviceProfile freezes the profile's current draft (all its definition
// lists) into a new immutable version — the next monotonic integer for that
// profile — and points the profile's ActiveVersion at it, so devices immediately
// resolve the just-published capability set. label/description are optional
// annotations; publishedBy is the caller's identity. Concurrent publishes are safe:
// the unique (device_profile_id, version) index rejects a duplicate version number.
func (api *Api) PublishDeviceProfile(ctx context.Context, token string,
	label, description *string, publishedBy string) (*DeviceProfileVersion, error) {
	profile, err := api.deviceProfileByToken(ctx, token)
	if err != nil {
		return nil, err
	}

	// Snapshot the draft outside the write transaction: it is a read of the current
	// definition rows, and a concurrent draft edit racing a publish is a benign,
	// rare pre-GA edge (the publish simply captures the draft as of this read).
	snapshot, err := api.buildProfileSnapshot(ctx, profile.ID)
	if err != nil {
		return nil, err
	}

	// ADR-044 sync gate (ADR-051 slice 4b): compile the snapshot's detection rules against
	// event-processing BEFORE freezing this version, so a profile can never publish a rule
	// the DETECT engine cannot run. It validates the EXACT bytes just serialized — not a
	// second, independently-read (and thus race-able) view of the draft — so validated ≡
	// frozen with no TOCTOU window. A rejected rule fails the publish closed with the
	// author-facing reason; an unavailable validator fails closed too, sanitized.
	if err := api.validateSnapshotDetectionRules(ctx, snapshot); err != nil {
		return nil, err
	}

	var maxVersion int32
	if err := api.RDB.DB(ctx).Model(&DeviceProfileVersion{}).
		Where("device_profile_id = ?", profile.ID).
		Select("COALESCE(MAX(version), 0)").Scan(&maxVersion).Error; err != nil {
		return nil, err
	}

	version := &DeviceProfileVersion{
		DeviceProfileId: profile.ID,
		Version:         maxVersion + 1,
		Label:           rdb.NullStrOf(label),
		Description:     rdb.NullStrOf(description),
		Snapshot:        snapshot,
		PublishedBy:     publishedBy,
	}
	// Parse the just-built snapshot to extract its ENABLED scoped rules — the live scope
	// references this new active version carries (ADR-062 S4). A parse failure fails the
	// publish closed: enrollment must match the version being frozen.
	newSnap, err := parseProfileSnapshot(snapshot)
	if err != nil {
		return nil, err
	}

	// Insert the version and advance the active pointer atomically: if the pointer
	// update failed after the insert we would leave an orphan version and devices
	// resolving the stale one, so wrap both.
	var evictions []membershipEviction
	var changed bool
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(version).Error; err != nil {
			return err
		}
		res := tx.Model(&DeviceProfile{}).Where("id = ?", profile.ID).
			Update("active_version", version.Version)
		if res.Error != nil {
			return res.Error
		}
		// The profile was deleted between the load and here (its cascade already
		// removed the version rows): roll the whole publish back rather than commit
		// a version row no device can ever resolve.
		if res.RowsAffected == 0 {
			return fmt.Errorf("%w: device profile %q", gorm.ErrRecordNotFound, token)
		}
		// Re-sync this profile's live scope references to the new active version and
		// reconcile read-model enrollment (ADR-062 S4) — in the SAME transaction as the
		// active-version change, so a scoped rule is enrolled before its fact arms the engine,
		// and a group@v this version stopped referencing is GC'd. Enrollment tracks published
		// state, not the mutable draft.
		evictions, changed, err = api.syncProfileScopeRefsAndEnroll(ctx, tx, profile.ID, scopedRulesInSnapshot(newSnap))
		return err
	})
	if err != nil {
		return nil, err
	}
	api.fireScopingEvictions(ctx, evictions, changed)

	// Propagate the frozen rule set to event-processing (ADR-051 slice 4b-3): emit the
	// ENABLED detection rules keyed on this version's token, POST-COMMIT and best-effort. It
	// runs after the version is durable so the fact never advertises a version that was rolled
	// back. Emission is at-most-once (ADR-044): a delivered fact is durably persisted by
	// event-processing's consumer, but a dropped emit is recovered by a later publish or the
	// planned reconcile, not by replay. Disabled rules ride the frozen snapshot but are omitted
	// here — inert until a later publish enables them, exactly the set the gate compiled above.
	api.emitDetectionRulesPublished(ctx, &DetectionRulesPublishedEvent{
		ProfileVersionToken: fmt.Sprintf("%s@%d", token, version.Version),
		Rules:               api.enabledSnapshotRules(snapshot),
		// The version row's gorm-stamped creation time (app-clock at insert, ~ms before the
		// commit) is the rule-activation instant (ADR-051 slice 4c-2): event-processing uses
		// it as the grace-period base so publishing an absence rule gives an already-existing
		// quiet device one timeout of grace, not an instant burst.
		PublishedAt: version.CreatedAt,
	})
	return version, nil
}

// enabledSnapshotRules projects the ENABLED detection rules carried in a just-frozen
// profile snapshot to the (token, definition) pairs the published-rule fact carries
// (ADR-051 slice 4b-3). It parses the exact snapshot bytes the version froze, so the
// propagated rule set is precisely the one published. A parse failure yields no rules
// (logged, not fatal): emission is best-effort side-band to the already-committed
// publish, so a corrupt snapshot cannot roll back a durable version — but it is loud,
// because it should be impossible (the same bytes were just built and, when the gate
// is wired, validated).
func (api *Api) enabledSnapshotRules(snapshot datatypes.JSON) []PublishedDetectionRule {
	snap, err := parseProfileSnapshot(snapshot)
	if err != nil {
		log.Error().Err(err).Msg("Unable to parse just-frozen profile snapshot for rule propagation; emitting no rules.")
		return nil
	}
	out := make([]PublishedDetectionRule, 0, len(snap.Rules))
	for _, dr := range snap.Rules {
		if !dr.Enabled {
			continue
		}
		pr := PublishedDetectionRule{Token: dr.Token, Definition: string(dr.Definition)}
		// Propagate the rule's optional group scope (ADR-062 S4) to event-processing. The
		// frozen snapshot carried the scope columns (not json:"-"), so a scoped rule ships
		// its pin; an unscoped rule ships empty token / version 0 (the engine's "no scope").
		if dr.EntityGroupToken != nil && dr.EntityGroupVersion != nil {
			pr.EntityGroupToken = *dr.EntityGroupToken
			pr.EntityGroupVersion = *dr.EntityGroupVersion
		}
		out = append(out, pr)
	}
	return out
}

// RollbackDeviceProfile re-points the profile's active published version at an
// existing version, so devices resolve that earlier capability set again. It is a
// non-destructive pointer flip (ADR-045 slice c): history is append-only and the
// mutable draft is untouched, so a bad publish is reverted instantly and can be
// rolled forward again. Returns gorm.ErrRecordNotFound if the profile or the target
// version does not exist.
func (api *Api) RollbackDeviceProfile(ctx context.Context, token string, version int32) (*DeviceProfile, error) {
	profile, err := api.deviceProfileByToken(ctx, token)
	if err != nil {
		return nil, err
	}

	// Load the target version (existence check + its frozen snapshot): the snapshot's scoped
	// rules are the live references the rolled-back-to active version carries, which enrollment
	// must be re-synced to (a rollback can re-activate a scoped rule the current active version
	// dropped — without re-enrolling, that resurrected rule would fire against an empty
	// read-model). ADR-062 S4.
	var target DeviceProfileVersion
	if err := api.RDB.DB(ctx).Where("device_profile_id = ? AND version = ?", profile.ID, version).
		First(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: device profile %q has no version %d", gorm.ErrRecordNotFound, token, version)
		}
		return nil, err
	}
	targetSnap, err := parseProfileSnapshot(target.Snapshot)
	if err != nil {
		return nil, err
	}

	var evictions []membershipEviction
	var changed bool
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&DeviceProfile{}).Where("id = ?", profile.ID).
			Update("active_version", version)
		if res.Error != nil {
			return res.Error
		}
		// The profile was deleted between the existence check and here.
		if res.RowsAffected == 0 {
			return fmt.Errorf("%w: device profile %q", gorm.ErrRecordNotFound, token)
		}
		// Re-sync scope references to the rolled-back-to version and reconcile enrollment in
		// the same transaction as the pointer flip.
		evictions, changed, err = api.syncProfileScopeRefsAndEnroll(ctx, tx, profile.ID, scopedRulesInSnapshot(targetSnap))
		return err
	})
	if err != nil {
		return nil, err
	}
	api.fireScopingEvictions(ctx, evictions, changed)
	// Re-propagate the rolled-back-to version's rules POST-COMMIT (ADR-051 slice 4c-2):
	// a rollback re-points ActiveVersion WITHOUT publishing, so without this emit
	// event-processing has no signal that the active version changed — its rule facts only
	// ever advance, and it would keep the dead-man roster armed under the wrong (later)
	// version's absence rules. Re-emitting the target version's fact restores the "the most
	// recent rule fact for a profile names its active version" invariant the roster relies
	// on. The bodies are identical to the version's original publish, so the DETECT engine's
	// upsert preserves running state (no reset); PublishedAt = reactivation time gives any
	// re-activated absence rule a fresh grace window. Best-effort, like every other fact emit.
	api.emitRolledBackRules(ctx, token, profile.ID, version)

	// Reload so the returned profile carries the freshly-bumped updated_at (the
	// column-scoped Update advanced it in the DB) rather than the pre-update value.
	return api.deviceProfileByToken(ctx, token)
}

// emitRolledBackRules loads the rolled-back-to version's frozen snapshot and re-emits its
// enabled detection rules as a detection-rules-published fact (ADR-051 slice 4c-2), so a
// rollback looks to event-processing like a (re)publish of that version. Best-effort: a
// load failure is logged and swallowed — the rollback itself is durable, and the reconcile
// sweep backstops a missed re-propagation.
func (api *Api) emitRolledBackRules(ctx context.Context, token string, profileId uint, version int32) {
	if api.DetectionRulesPublishedPublisher == nil {
		return
	}
	var v DeviceProfileVersion
	if err := api.RDB.DB(ctx).Where("device_profile_id = ? AND version = ?", profileId, version).
		First(&v).Error; err != nil {
		log.Error().Err(err).Str("profile", token).Int32("version", version).
			Msg("Unable to load rolled-back version for rule re-propagation; skipping emit")
		return
	}
	api.emitDetectionRulesPublished(ctx, &DetectionRulesPublishedEvent{
		ProfileVersionToken: fmt.Sprintf("%s@%d", token, version),
		Rules:               api.enabledSnapshotRules(v.Snapshot),
		PublishedAt:         time.Now().UTC(),
	})
}

// DeviceProfileVersions lists a profile's published versions, newest first. Returns
// gorm.ErrRecordNotFound if the profile does not exist.
func (api *Api) DeviceProfileVersions(ctx context.Context, token string) ([]*DeviceProfileVersion, error) {
	profile, err := api.deviceProfileByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	versions := make([]*DeviceProfileVersion, 0)
	result := api.RDB.DB(ctx).Where("device_profile_id = ?", profile.ID).
		Order("version DESC").Find(&versions)
	if result.Error != nil {
		return nil, result.Error
	}
	return versions, nil
}

// activeProfileSnapshot returns the parsed capability snapshot of a profile's
// currently-active published version (ADR-045 decision 4) — what a device resolves
// through the profile. A profile with no active version (never published) yields an
// empty snapshot: its draft definitions are inert until published, the same
// limiting case as a type with no profile.
func (api *Api) activeProfileSnapshot(ctx context.Context, profileId uint) (*ProfileSnapshot, error) {
	profiles, err := api.DeviceProfilesById(ctx, []uint{profileId})
	if err != nil {
		return nil, err
	}
	if len(profiles) == 0 || !profiles[0].ActiveVersion.Valid {
		return parseProfileSnapshot(nil)
	}
	var version DeviceProfileVersion
	result := api.RDB.DB(ctx).Where("device_profile_id = ? AND version = ?",
		profileId, profiles[0].ActiveVersion.Int32).First(&version)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			// The pointer references a version that no longer exists. This should be
			// impossible (versions are append-only and the delete cascade is
			// transactional), so it signals an invariant breach — DB surgery, a bug,
			// a botched upgrade. Resolve nothing rather than error the hot ingest
			// path, but log it: silent device inertness is exactly the failure a
			// "can't happen" branch must make visible.
			log.Warn().Uint("profile", profileId).Int32("activeVersion", profiles[0].ActiveVersion.Int32).
				Msg("device profile active_version references a missing version row; resolving empty capability")
			return parseProfileSnapshot(nil)
		}
		return nil, result.Error
	}
	return parseProfileSnapshot(version.Snapshot)
}

// ProfileScope is a device's denormalized rule-scoping identity (ADR-051): the
// device-type token plus a "{profileToken}@{version}" token naming the active
// published profile version (ADR-045) whose rules apply. It is stamped onto every
// resolved event so event-processing's DETECT engine can select the applicable
// rules without a graph read back into device-management. ProfileVersionToken is
// empty when the type has no profile or the profile is unpublished — the device
// has no resolvable rules, the same limiting case as an empty active snapshot.
type ProfileScope struct {
	DeviceTypeToken     string
	ProfileVersionToken string
}

// ProfileScopeByDeviceType resolves a device type to its ProfileScope (ADR-051).
// It walks the same device→type→profile→active-version chain the ingest path
// already uses for metric resolution, so it is cheap and cache-friendly (the
// cached decorator keys it by device type, evicted on the same publish/rollback/
// reprofile events as the metric-definition cache). A missing type or an
// unpublished/absent profile yields an empty ProfileVersionToken rather than an
// error: the device simply has no resolvable rules.
func (api *Api) ProfileScopeByDeviceType(ctx context.Context, deviceTypeId uint) (*ProfileScope, error) {
	types, err := api.DeviceTypesById(ctx, []uint{deviceTypeId})
	if err != nil {
		return nil, err
	}
	if len(types) == 0 {
		return &ProfileScope{}, nil
	}
	scope := &ProfileScope{DeviceTypeToken: types[0].Token}
	if types[0].ProfileId == nil {
		return scope, nil
	}
	profiles, err := api.DeviceProfilesById(ctx, []uint{*types[0].ProfileId})
	if err != nil {
		return nil, err
	}
	if len(profiles) == 0 || !profiles[0].ActiveVersion.Valid {
		return scope, nil
	}
	scope.ProfileVersionToken = fmt.Sprintf("%s@%d", profiles[0].Token, profiles[0].ActiveVersion.Int32)
	return scope, nil
}

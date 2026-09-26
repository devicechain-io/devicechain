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
// and detection-rule definitions, plus its singular position declaration (ADR-078) —
// into a ProfileSnapshot document. The
// back-reference to the profile is cleared on each definition so the blob stays tight
// and acyclic. Disabled definitions are captured too (the flag travels with the version).
func (api *Api) buildProfileSnapshot(ctx context.Context, profileId uint) (datatypes.JSON, error) {
	// The position declaration lives on the profile row itself (it is singular, so it
	// has no table of its own), so capturing it means reading the profile back rather
	// than a definition list. An undeclared profile yields a nil Location, which is a
	// value the snapshot must carry faithfully — see ProfileSnapshot.Location.
	location, err := api.locationDeclarationForProfile(ctx, profileId)
	if err != nil {
		return nil, err
	}
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
	raw, err := json.Marshal(ProfileSnapshot{Metrics: metrics, Commands: commands, Rules: rules, Location: location})
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(raw), nil
}

// parseProfileSnapshot decodes a version's snapshot blob back into definition
// lists, normalizing nil lists to empty slices so callers never dereference nil.
//
// 🔴 Location is deliberately EXEMPT from that normalization, and the exemption is
// the point rather than an oversight. An empty list and an absent list mean the same
// thing (no definitions), so collapsing them loses nothing; a nil Location and a
// non-nil empty one mean DIFFERENT things (this device kind does not report its
// position / it does, with no expectations stated), so a "helpful" normalization here
// would destroy the one distinction the singular-nullable shape exists to preserve —
// irreversibly, since a published snapshot is immutable.
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
			Token:       dr.Token,
			Definition:  string(dr.Definition),
			GroupScoped: dr.GroupScoped(), // ADR-062 S4 — single shared predicate
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
	var activeSince time.Time
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// The version row's creation time IS the activation instant, and both are stored: the
		// fact below carries it, and the detection engine's reconcile reads it back. It is
		// minted here, inside the transaction, from the floor the profile's own rows set — see
		// nextActivationInstant for why the wall clock alone is not enough.
		floor, err := activationFloor(tx, profile.ID)
		if err != nil {
			return err
		}
		activeSince = nextActivationInstant(floor)
		version.CreatedAt = activeSince
		if err := tx.Create(version).Error; err != nil {
			return err
		}
		res := tx.Model(profile).Where("id = ?", profile.ID).
			Updates(map[string]any{"active_version": version.Version, "active_since": activeSince})
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
	// event-processing's consumer, and a fact that never reaches the stream is repaired by
	// event-processing's reconcile against this service (at the start of its leadership term
	// and every five minutes), not by replay. Disabled rules ride the frozen snapshot but are
	// omitted — inert until a later publish enables them, exactly the set the gate compiled
	// above. PublishedAt is the stored activation instant (ADR-051 slice 4c-2):
	// event-processing uses it as the grace-period base so publishing an absence rule gives an
	// already-existing quiet device one timeout of grace, not an instant burst.
	api.emitActiveVersionRules(ctx, token, profile.ID, version.Version, activeSince)
	return version, nil
}

// enabledSnapshotRules projects the ENABLED detection rules carried in a frozen profile
// snapshot to the (token, definition) pairs the published-rule fact carries (ADR-051 slice
// 4b-3). A parse failure yields no rules (logged, not fatal): emission is best-effort side-band
// to the already-committed publish or rollback, so a corrupt snapshot cannot roll back a durable
// version — but it is loud, because it should be impossible (the same bytes were built and, when
// the gate is wired, validated at publish). The reconcile door does NOT use this: it calls
// enabledSnapshotRulesStrict, because there "no rules" would read as an instruction to delete
// every rule the engine holds for the version.
func (api *Api) enabledSnapshotRules(snapshot datatypes.JSON) []PublishedDetectionRule {
	out, err := enabledSnapshotRulesStrict(snapshot)
	if err != nil {
		log.Error().Err(err).Msg("Unable to parse a frozen profile snapshot for rule propagation; emitting no rules.")
		return nil
	}
	return out
}

// enabledSnapshotRulesStrict is the projection itself: the enabled rules of a frozen snapshot, or
// the parse error. It is the one definition of "the rules a version publishes", shared by the fact
// emit and the reconcile door so the two cannot disagree.
func enabledSnapshotRulesStrict(snapshot datatypes.JSON) ([]PublishedDetectionRule, error) {
	snap, err := parseProfileSnapshot(snapshot)
	if err != nil {
		return nil, err
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
		if dr.GroupScoped() {
			pr.EntityGroupToken = *dr.EntityGroupToken
			pr.EntityGroupVersion = *dr.EntityGroupVersion
		}
		out = append(out, pr)
	}
	return out, nil
}

// activationFloor is the latest activation instant a profile's rows already record, read inside
// the caller's transaction (resolveActiveSince over the profile row and its newest version). A new
// activation must land strictly after it (nextActivationInstant). The zero time means the profile
// has no version yet.
func activationFloor(tx *gorm.DB, profileId uint) (time.Time, error) {
	var current DeviceProfile
	if err := tx.Select("id", "active_version", "active_since").Where("id = ?", profileId).
		First(&current).Error; err != nil {
		return time.Time{}, err
	}
	latest, found, err := latestProfileVersion(tx, profileId)
	if err != nil || !found {
		return time.Time{}, err
	}
	return resolveActiveSince(current.ActiveSince, current.ActiveVersion, latest), nil
}

// latestProfileVersion reads the number and creation time of a profile's newest version (not its
// snapshot). found is false when the profile has none.
func latestProfileVersion(db *gorm.DB, profileId uint) (DeviceProfileVersion, bool, error) {
	var latest DeviceProfileVersion
	res := db.Model(&DeviceProfileVersion{}).Select("id", "version", "created_at").
		Where("device_profile_id = ?", profileId).Order("version DESC").Limit(1).Find(&latest)
	if res.Error != nil {
		return DeviceProfileVersion{}, false, res.Error
	}
	return latest, res.RowsAffected > 0, nil
}

// resolveActiveSince is the ONE rule for when a profile's active version became active, and every
// reader of the instant goes through it: the reconcile door returns it and the next activation is
// floored on it.
//
// It is the stored active_since, unless the version rows imply something later: the newest
// version's publish time when the active version IS the newest, and a microsecond after it when it
// is not (the active version was rolled back to, so it became active after the newest was
// published). The version rows only ever win when active_since was not written by this code — a
// profile last published or rolled back before the column existed, or whose pointer an older
// replica moved during a rolling upgrade — because publish stores active_since equal to the new
// version's creation time and rollback stores one strictly later than everything recorded.
func resolveActiveSince(stored sql.NullTime, active sql.NullInt32, latest DeviceProfileVersion) time.Time {
	implied := latest.CreatedAt
	if active.Valid && active.Int32 != latest.Version {
		implied = implied.Add(time.Microsecond)
	}
	if stored.Valid && !stored.Time.Before(implied) {
		return stored.Time.UTC()
	}
	return implied.UTC()
}

// nextActivationInstant is when a profile's active version changes now: the wall clock, but never
// at or before floor (the activation already recorded), and truncated to the microsecond the
// database stores, so the value a fact carries is the value that reads back.
//
// The floor is what makes a clock that runs behind harmless. The detection engine refuses an
// active-version change older than the one it holds (its monotonic guard), so a publish or a
// rollback on a replica whose clock trails the one that made the previous change would otherwise
// mint an activation the engine never applies. floor+1µs is later than everything recorded.
//
// It is not a lock: two replicas moving the SAME profile's pointer at the same moment each read
// the floor before the other commits, and the later commit can store the earlier instant. The
// engine's reconcile compares WHICH version is active, not the instant, so it still converges on
// the version this service stores.
func nextActivationInstant(floor time.Time) time.Time {
	now := time.Now().UTC().Truncate(time.Microsecond)
	if floor.IsZero() {
		return now
	}
	if next := floor.UTC().Truncate(time.Microsecond).Add(time.Microsecond); now.Before(next) {
		return next
	}
	return now
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
	var activeSince time.Time
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		floor, err := activationFloor(tx, profile.ID)
		if err != nil {
			return err
		}
		activeSince = nextActivationInstant(floor)
		res := tx.Model(profile).Where("id = ?", profile.ID).
			Updates(map[string]any{"active_version": version, "active_since": activeSince})
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
	// on. The bodies are the ones the version's publish carried — both are read from the same
	// stored snapshot — so the DETECT engine's upsert preserves running state (no reset).
	// PublishedAt is the STORED reactivation instant, which gives any re-activated absence rule
	// a fresh grace window and is exactly what the engine's reconcile against this service reads
	// back. Best-effort, like every other fact emit.
	api.emitActiveVersionRules(ctx, token, profile.ID, version, activeSince)

	// Reload so the returned profile carries the freshly-bumped updated_at (the
	// column-scoped Update advanced it in the DB) rather than the pre-update value.
	return api.deviceProfileByToken(ctx, token)
}

// emitActiveVersionRules announces that a profile version became active — after a publish or a
// rollback — as a detection-rules-published fact carrying the version's enabled rules and the
// stored activation instant (ADR-051 slice 4b-3 / 4c-2).
//
// It reads the version's snapshot back from the database rather than using the bytes a publish
// holds in memory, and that is the point: the stored snapshot is what a rollback re-emits and what
// the reconcile door returns, and on Postgres its jsonb rendering differs byte-for-byte from the
// compact form json.Marshal built. Emitting the stored form gives a version's publish fact, its
// rollback fact and its reconcile answer identical bodies.
//
// Best-effort: a load failure is logged and swallowed — the publish or rollback itself is durable,
// and event-processing's reconcile against this service (at the start of its leadership term and
// every five minutes) repairs a missed announcement.
func (api *Api) emitActiveVersionRules(ctx context.Context, token string, profileId uint, version int32, activeSince time.Time) {
	if api.DetectionRulesPublishedPublisher == nil {
		return
	}
	var v DeviceProfileVersion
	if err := api.RDB.DB(ctx).Where("device_profile_id = ? AND version = ?", profileId, version).
		First(&v).Error; err != nil {
		log.Error().Err(err).Str("profile", token).Int32("version", version).
			Msg("Unable to load the newly active profile version for rule propagation; skipping emit")
		return
	}
	api.emitDetectionRulesPublished(ctx, &DetectionRulesPublishedEvent{
		ProfileVersionToken: fmt.Sprintf("%s@%d", token, version),
		Rules:               api.enabledSnapshotRules(v.Snapshot),
		PublishedAt:         activeSince,
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
	return api.profileVersionSnapshot(ctx, profileId, profiles[0].ActiveVersion.Int32)
}

// profileVersionSnapshot returns the parsed capability snapshot of ONE published version
// of a profile. It takes the version rather than reading the profile's active-version
// pointer itself, so a caller that has already read the pointer — and derived anything
// else from it — gets the snapshot of exactly that version.
func (api *Api) profileVersionSnapshot(ctx context.Context, profileId uint, version int32) (*ProfileSnapshot, error) {
	var row DeviceProfileVersion
	result := api.RDB.DB(ctx).Where("device_profile_id = ? AND version = ?",
		profileId, version).First(&row)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			// The pointer references a version that no longer exists. This should be
			// impossible (versions are append-only and the delete cascade is
			// transactional), so it signals an invariant breach — DB surgery, a bug,
			// a botched upgrade. Resolve nothing rather than error the hot ingest
			// path, but log it: silent device inertness is exactly the failure a
			// "can't happen" branch must make visible.
			log.Warn().Uint("profile", profileId).Int32("activeVersion", version).
				Msg("device profile active_version references a missing version row; resolving empty capability")
			return parseProfileSnapshot(nil)
		}
		return nil, result.Error
	}
	return parseProfileSnapshot(row.Snapshot)
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
	// FenceSetVersion is the tenant's ACTIVE geofence-set version at resolve time
	// (ADR-078), stamped onto resolved LOCATION events only. It rides here rather than in
	// a cache of its own precisely because the resolve path already fetches this struct
	// once per event, inside the per-device-type ProfileResolution: adding a field costs
	// nothing on the hot path, while a second per-tenant lookup would be a second cache to
	// keep coherent. The price is that a fence change must evict the resolution of every
	// device type of the tenant, which is an authoring-time fan-out over a small set
	// (CachedApi.EvictFenceSetVersion).
	//
	// It is NOT part of the "rule-scoping identity" the rest of this struct denormalizes,
	// and deliberately so: it changes on a fence edit, which is not a profile publish.
	// 0 means the tenant has never had a fence.
	FenceSetVersion int32
}

// ProfileResolutionByDeviceType resolves a device type to its ProfileResolution. It walks
// the device→type→profile→active-version chain once: the profile's active-version pointer
// is read ONCE, and both the version token and the metric definitions come from exactly
// that version, so a publish racing this read cannot mix two versions into one result.
//
// A missing type, a type with no profile, or an unpublished profile yields an empty
// version token and no metrics rather than an error: the device simply has no resolvable
// rules and no declared metrics.
func (api *Api) ProfileResolutionByDeviceType(ctx context.Context, deviceTypeId uint) (*ProfileResolution, error) {
	// The fence-set version (ADR-078) is resolved FIRST and on every path, including the
	// "no such device type" early return below. A device whose type has been deleted still
	// reports positions, and stamping it with 0 would claim the tenant has no fences when
	// it may have a hundred — a lie that survives into the immutable event.
	fenceSetVersion, err := api.CurrentFenceSetVersion(ctx)
	if err != nil {
		return nil, err
	}
	scope := ProfileScope{FenceSetVersion: fenceSetVersion}
	types, err := api.DeviceTypesById(ctx, []uint{deviceTypeId})
	if err != nil {
		return nil, err
	}
	if len(types) == 0 {
		return NewProfileResolution(scope, nil), nil
	}
	scope.DeviceTypeToken = types[0].Token
	if types[0].ProfileId == nil {
		return NewProfileResolution(scope, nil), nil
	}
	profileId := *types[0].ProfileId
	profiles, err := api.DeviceProfilesById(ctx, []uint{profileId})
	if err != nil {
		return nil, err
	}
	if len(profiles) == 0 || !profiles[0].ActiveVersion.Valid {
		return NewProfileResolution(scope, nil), nil
	}
	version := profiles[0].ActiveVersion.Int32
	snap, err := api.profileVersionSnapshot(ctx, profileId, version)
	if err != nil {
		return nil, err
	}
	scope.ProfileVersionToken = fmt.Sprintf("%s@%d", profiles[0].Token, version)
	return NewProfileResolution(scope, snap.Metrics), nil
}

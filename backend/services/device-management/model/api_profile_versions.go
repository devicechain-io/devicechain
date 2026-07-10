// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
// alarm, and detection-rule definitions — into a ProfileSnapshot document. The
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
	alarms, err := api.AlarmDefinitionsByDeviceProfile(ctx, profileId)
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
	for _, a := range alarms {
		a.DeviceProfile = nil
	}
	for _, dr := range rules {
		dr.DeviceProfile = nil
	}
	raw, err := json.Marshal(ProfileSnapshot{Metrics: metrics, Commands: commands, Alarms: alarms, Rules: rules})
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
		Alarms:   []*AlarmDefinition{},
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
	if snap.Alarms == nil {
		snap.Alarms = []*AlarmDefinition{}
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
		out = append(out, RuleToValidate{Token: dr.Token, Definition: string(dr.Definition)})
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
	// Insert the version and advance the active pointer atomically: if the pointer
	// update failed after the insert we would leave an orphan version and devices
	// resolving the stale one, so wrap both.
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
		return nil
	})
	if err != nil {
		return nil, err
	}
	return version, nil
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

	var count int64
	if err := api.RDB.DB(ctx).Model(&DeviceProfileVersion{}).
		Where("device_profile_id = ? AND version = ?", profile.ID, version).
		Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, fmt.Errorf("%w: device profile %q has no version %d", gorm.ErrRecordNotFound, token, version)
	}

	res := api.RDB.DB(ctx).Model(&DeviceProfile{}).Where("id = ?", profile.ID).
		Update("active_version", version)
	if res.Error != nil {
		return nil, res.Error
	}
	// The profile was deleted between the existence check and here.
	if res.RowsAffected == 0 {
		return nil, fmt.Errorf("%w: device profile %q", gorm.ErrRecordNotFound, token)
	}
	// Reload so the returned profile carries the freshly-bumped updated_at (the
	// column-scoped Update advanced it in the DB) rather than the pre-update value.
	return api.deviceProfileByToken(ctx, token)
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

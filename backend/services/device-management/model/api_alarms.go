// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "context"

// AlarmDefinition authoring (create/update/delete, by-id/by-token/search) was retired
// with ADR-057 — DetectionRule is the one alarm-authoring path. The AlarmDefinition model
// and these two reads survive only to feed the legacy measurement evaluator until the 6d
// cutover deletes it: AlarmDefinitionsByDeviceProfile freezes the profile's rules into the
// published snapshot, and AlarmDefinitionsByDeviceType is the evaluator's snapshot loader.

// AlarmDefinitionsByDeviceProfile loads all alarm rules declared on a device profile
// without pagination (ADR-041/ADR-045). It is read at PUBLISH time by buildProfileSnapshot
// to freeze the rules into the version snapshot the evaluator later reads.
func (api *Api) AlarmDefinitionsByDeviceProfile(ctx context.Context, profileId uint) ([]*AlarmDefinition, error) {
	found := make([]*AlarmDefinition, 0)
	result := api.RDB.DB(ctx).Where("device_profile_id = ?", profileId).Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// AlarmDefinitionsByDeviceType is the alarm evaluator's loader (ADR-041): it resolves the
// type → profile hop (ADR-045) when a measurement arrives and returns the rules of the
// profile's currently-active PUBLISHED version (decision 4), not the draft — a device is
// evaluated against published rules, so draft edits are inert until published. A device
// whose type has no profile, or whose profile is not yet published, has no rules (empty).
func (api *Api) AlarmDefinitionsByDeviceType(ctx context.Context, deviceTypeId uint) ([]*AlarmDefinition, error) {
	profileId, ok, err := api.profileIdForDeviceType(ctx, deviceTypeId)
	if err != nil || !ok {
		return []*AlarmDefinition{}, err
	}
	snap, err := api.activeProfileSnapshot(ctx, profileId)
	if err != nil {
		return nil, err
	}
	return snap.Alarms, nil
}

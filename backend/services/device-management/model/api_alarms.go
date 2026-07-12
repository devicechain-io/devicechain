// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "context"

// AlarmDefinition authoring was retired with ADR-057 (DetectionRule is the one alarm-authoring
// path), and the 6d cutover deleted the measurement evaluator that read the rules. This one read
// survives because profile publish still freezes any residual AlarmDefinition rows into the version
// snapshot (ProfileSnapshot.Alarms); the frozen copy now has no reader — the whole AlarmDefinition
// model is dead and its teardown (struct, snapshot field, table) is a follow-up cleanup.

// AlarmDefinitionsByDeviceProfile loads all alarm rules declared on a device profile
// without pagination (ADR-041/ADR-045). It is read at PUBLISH time by buildProfileSnapshot.
func (api *Api) AlarmDefinitionsByDeviceProfile(ctx context.Context, profileId uint) ([]*AlarmDefinition, error) {
	found := make([]*AlarmDefinition, 0)
	result := api.RDB.DB(ctx).Where("device_profile_id = ?", profileId).Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// SetDeviceProfileConfigurationDeclaration replaces a profile's DRAFT configuration
// declaration: which shared attribute keys a device of this profile may see. An empty
// list withdraws it (nothing declared).
//
// It writes the draft only. Published versions are immutable, so a declaration change
// reaches devices only through the next publish; the version a device currently
// resolves, and the declaration frozen in it, are untouched. Only the declaration column
// is written, so a racing publish or edit does not revert other profile fields; the
// converse holds because UpdateDeviceProfile omits this column from its save.
func (api *Api) SetDeviceProfileConfigurationDeclaration(ctx context.Context, token string,
	keys []ConfigurationKey) (*DeviceProfile, error) {
	profile, err := api.deviceProfileByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	encoded, err := encodeConfigurationDeclaration(keys)
	if err != nil {
		return nil, err
	}
	// Select the one column so a nil (cleared) value is written as NULL rather than skipped.
	res := api.RDB.DB(ctx).Model(profile).Where("id = ?", profile.ID).
		Select("ConfigurationDeclaration").
		Updates(&DeviceProfile{ConfigurationDeclaration: encoded})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	profile.ConfigurationDeclaration = encoded
	return profile, nil
}

// configurationDeclarationForProfile reads a profile's DRAFT declaration for a publish
// to freeze, re-validating it first: what is frozen is immutable, so a declaration that
// reached the column by any route but the setter must fail the publish rather than ship.
func (api *Api) configurationDeclarationForProfile(ctx context.Context, profileId uint) ([]ConfigurationKey, error) {
	profiles, err := api.DeviceProfilesById(ctx, []uint{profileId})
	if err != nil {
		return nil, err
	}
	if len(profiles) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	keys, err := profiles[0].ConfigurationKeys()
	if err != nil {
		return nil, err
	}
	if err := ValidateConfigurationDeclaration(keys); err != nil {
		return nil, fmt.Errorf("cannot publish device profile: invalid configuration declaration: %w", err)
	}
	return keys, nil
}

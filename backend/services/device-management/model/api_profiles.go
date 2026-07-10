// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Create a new device profile.
func (api *Api) CreateDeviceProfile(ctx context.Context, request *DeviceProfileCreateRequest) (*DeviceProfile, error) {
	created := &DeviceProfile{
		TokenReference: rdb.TokenReference{
			Token: request.Token,
		},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		MetadataEntity: rdb.MetadataEntity{
			Metadata: rdb.MetadataStrOf(request.Metadata),
		},
		Category: rdb.NullStrOf(request.Category),
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing device profile. The profile is located by the token argument
// (so a rename via request.Token works and an update never silently retargets a
// different row). Provenance (ADR-046) is not settable through this path — it is
// owned by the future catalog fork-adopt flow, never the editor.
func (api *Api) UpdateDeviceProfile(ctx context.Context, token string,
	request *DeviceProfileCreateRequest) (*DeviceProfile, error) {
	matches, err := api.DeviceProfilesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	found := matches[0]

	// A profile token is IMMUTABLE once the profile has published versions (ADR-051 slice
	// 4b-3). The published-version token "{profileToken}@{version}" (ADR-045) is the key the
	// DETECT engine files a version's detection rules under and the scope resolved events are
	// stamped with; renaming the profile would leave every already-published rule filed under
	// the OLD token while events carry the new one — detections silently stop and armed
	// absence dead-men false-fire. Pre-GA decisive cutover: reject the rename rather than
	// re-key or orphan rules.
	if request.Token != token {
		var published int64
		if err := api.RDB.DB(ctx).Model(&DeviceProfileVersion{}).
			Where("device_profile_id = ?", found.ID).Count(&published).Error; err != nil {
			return nil, err
		}
		if published > 0 {
			return nil, fmt.Errorf("cannot rename device profile %q to %q: it has %d published version(s); the token is immutable once published",
				token, request.Token, published)
		}
	}

	found.Token = request.Token
	found.Name = rdb.NullStrOf(request.Name)
	found.Description = rdb.NullStrOf(request.Description)
	found.Category = rdb.NullStrOf(request.Category)
	found.Metadata = rdb.MetadataStrOf(request.Metadata)

	result := api.RDB.DB(ctx).Save(found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get device profiles by id.
func (api *Api) DeviceProfilesById(ctx context.Context, ids []uint) ([]*DeviceProfile, error) {
	found := make([]*DeviceProfile, 0)
	result := api.RDB.DB(ctx).Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get device profiles by token.
func (api *Api) DeviceProfilesByToken(ctx context.Context, tokens []string) ([]*DeviceProfile, error) {
	found := make([]*DeviceProfile, 0)
	result := api.RDB.DB(ctx).Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for device profiles that meet criteria.
func (api *Api) DeviceProfiles(ctx context.Context, criteria DeviceProfileSearchCriteria) (*DeviceProfileSearchResults, error) {
	results := make([]DeviceProfile, 0)
	db, pag := api.RDB.ListOf(ctx, &DeviceProfile{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	return &DeviceProfileSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

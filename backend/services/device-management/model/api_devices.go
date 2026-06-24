/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Create a new device type.
func (api *Api) CreateDeviceType(ctx context.Context, request *DeviceTypeCreateRequest) (*DeviceType, error) {
	created := &DeviceType{
		TokenReference: rdb.TokenReference{
			Token: request.Token,
		},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		BrandedEntity: rdb.BrandedEntity{
			ImageUrl:        rdb.NullStrOf(request.ImageUrl),
			Icon:            rdb.NullStrOf(request.Icon),
			BackgroundColor: rdb.NullStrOf(request.BackgroundColor),
			ForegroundColor: rdb.NullStrOf(request.ForegroundColor),
			BorderColor:     rdb.NullStrOf(request.BorderColor),
		},
		MetadataEntity: rdb.MetadataEntity{
			Metadata: rdb.MetadataStrOf(request.Metadata),
		},
	}
	result := api.RDB.Database.Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing device type.
func (api *Api) UpdateDeviceType(ctx context.Context, token string,
	request *DeviceTypeCreateRequest) (*DeviceType, error) {
	matches, err := api.DeviceTypesByToken(ctx, []string{request.Token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	found := matches[0]
	found.Token = request.Token
	found.Name = rdb.NullStrOf(request.Name)
	found.Description = rdb.NullStrOf(request.Description)
	found.ImageUrl = rdb.NullStrOf(request.ImageUrl)
	found.Icon = rdb.NullStrOf(request.Icon)
	found.BackgroundColor = rdb.NullStrOf(request.BackgroundColor)
	found.ForegroundColor = rdb.NullStrOf(request.ForegroundColor)
	found.BorderColor = rdb.NullStrOf(request.BorderColor)
	found.Metadata = rdb.MetadataStrOf(request.Metadata)

	result := api.RDB.Database.Save(found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get device types by id.
func (api *Api) DeviceTypesById(ctx context.Context, ids []uint) ([]*DeviceType, error) {
	found := make([]*DeviceType, 0)
	result := api.RDB.Database.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get device types by token.
func (api *Api) DeviceTypesByToken(ctx context.Context, tokens []string) ([]*DeviceType, error) {
	found := make([]*DeviceType, 0)
	result := api.RDB.Database.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for device types that meet criteria.
func (api *Api) DeviceTypes(ctx context.Context, criteria DeviceTypeSearchCriteria) (*DeviceTypeSearchResults, error) {
	results := make([]DeviceType, 0)
	db, pag := api.RDB.ListOf(&DeviceType{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &DeviceTypeSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new device.
func (api *Api) CreateDevice(ctx context.Context, request *DeviceCreateRequest) (*Device, error) {
	matches, err := api.DeviceTypesByToken(ctx, []string{request.DeviceTypeToken})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	created := &Device{
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
		DeviceType: matches[0],
	}
	result := api.RDB.Database.Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing device.
func (api *Api) UpdateDevice(ctx context.Context, token string, request *DeviceCreateRequest) (*Device, error) {
	matches, err := api.DevicesByToken(ctx, []string{request.Token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	// Update fields that changed.
	updated := matches[0]
	updated.Token = request.Token
	updated.Name = rdb.NullStrOf(request.Name)
	updated.Description = rdb.NullStrOf(request.Description)
	updated.Metadata = rdb.MetadataStrOf(request.Metadata)

	// Update device type if changed.
	if request.DeviceTypeToken != updated.DeviceType.Token {
		matches, err := api.DeviceTypesByToken(ctx, []string{request.DeviceTypeToken})
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, gorm.ErrRecordNotFound
		}
		updated.DeviceType = matches[0]
	}

	result := api.RDB.Database.Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get devices by id.
func (api *Api) DevicesById(ctx context.Context, ids []uint) ([]*Device, error) {
	found := make([]*Device, 0)
	result := api.RDB.Database
	result = result.Preload("DeviceType")
	result = result.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get devices by token.
func (api *Api) DevicesByToken(ctx context.Context, tokens []string) ([]*Device, error) {
	found := make([]*Device, 0)
	result := api.RDB.Database
	result = result.Preload("DeviceType")
	result = result.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for devices that meet criteria.
func (api *Api) Devices(ctx context.Context, criteria DeviceSearchCriteria) (*DeviceSearchResults, error) {
	results := make([]Device, 0)
	db, pag := api.RDB.ListOf(&Device{}, func(result *gorm.DB) *gorm.DB {
		if criteria.DeviceType != nil {
			result = result.Where("device_type_id = (?)",
				api.RDB.Database.Model(&DeviceType{}).Select("id").Where("token = ?", criteria.DeviceType))
		}
		return result.Preload("DeviceType")
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &DeviceSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new device relationship type.
func (api *Api) CreateDeviceRelationshipType(ctx context.Context, request *DeviceRelationshipTypeCreateRequest) (*DeviceRelationshipType, error) {
	created := &DeviceRelationshipType{
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
		Tracked: request.Tracked,
	}
	result := api.RDB.Database.Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing device relationship type.
func (api *Api) UpdateDeviceRelationshipType(ctx context.Context, token string,
	request *DeviceRelationshipTypeCreateRequest) (*DeviceRelationshipType, error) {
	matches, err := api.DeviceRelationshipTypesByToken(ctx, []string{request.Token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	updated := matches[0]
	updated.Token = request.Token
	updated.Name = rdb.NullStrOf(request.Name)
	updated.Description = rdb.NullStrOf(request.Description)
	updated.Metadata = rdb.MetadataStrOf(request.Metadata)
	updated.Tracked = request.Tracked

	result := api.RDB.Database.Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get device relationship types by id.
func (api *Api) DeviceRelationshipTypesById(ctx context.Context, ids []uint) ([]*DeviceRelationshipType, error) {
	found := make([]*DeviceRelationshipType, 0)
	result := api.RDB.Database.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get device relationship types by token.
func (api *Api) DeviceRelationshipTypesByToken(ctx context.Context, tokens []string) ([]*DeviceRelationshipType, error) {
	found := make([]*DeviceRelationshipType, 0)
	result := api.RDB.Database.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for device relationship types that meet criteria.
func (api *Api) DeviceRelationshipTypes(ctx context.Context,
	criteria DeviceRelationshipTypeSearchCriteria) (*DeviceRelationshipTypeSearchResults, error) {
	results := make([]DeviceRelationshipType, 0)
	db, pag := api.RDB.ListOf(&DeviceRelationshipType{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &DeviceRelationshipTypeSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create map by token from list of devices.
func deviceByTokenMap(vals []*Device) map[string]*Device {
	results := make(map[string]*Device)
	for _, val := range vals {
		results[val.Token] = val
	}
	return results
}

// Create a new device relationship.
func (api *Api) CreateDeviceRelationship(ctx context.Context, request *DeviceRelationshipCreateRequest) (*DeviceRelationship, error) {
	// Look up source reference.
	dvmatches, err := api.DevicesByToken(ctx, []string{request.SourceDevice})
	if err != nil {
		return nil, err
	}
	if len(dvmatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	mmap := deviceByTokenMap(dvmatches)

	// Look up relationship reference.
	drmatches, err := api.DeviceRelationshipTypesByToken(ctx, []string{request.RelationshipType})
	if err != nil {
		return nil, err
	}
	if len(drmatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	created := &DeviceRelationship{
		EntityRelationship: EntityRelationship{
			TokenReference: rdb.TokenReference{
				Token: request.Token,
			},
			MetadataEntity: rdb.MetadataEntity{
				Metadata: rdb.MetadataStrOf(request.Metadata),
			},
		},
		SourceDevice:     *mmap[request.SourceDevice],
		RelationshipType: *drmatches[0],
	}
	api.resolveRelationshipTargets(ctx, request.Targets, &created.EntityRelationship)
	result := api.RDB.Database.Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Get device relationships by id.
func (api *Api) DeviceRelationshipsById(ctx context.Context, ids []uint) ([]*DeviceRelationship, error) {
	found := make([]*DeviceRelationship, 0)
	result := api.RDB.Database
	result = result.Preload("SourceDevice").Preload("RelationshipType")
	result = preloadRelationshipTargets(result)
	result = result.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get device relationships by token.
func (api *Api) DeviceRelationshipsByToken(ctx context.Context, tokens []string) ([]*DeviceRelationship, error) {
	found := make([]*DeviceRelationship, 0)
	result := api.RDB.Database
	result = result.Preload("SourceDevice").Preload("RelationshipType")
	result = preloadRelationshipTargets(result)
	result = result.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for device relationships that meet criteria.
func (api *Api) DeviceRelationships(ctx context.Context,
	criteria DeviceRelationshipSearchCriteria) (*DeviceRelationshipSearchResults, error) {
	results := make([]DeviceRelationship, 0)
	db, pag := api.RDB.ListOf(&DeviceRelationship{}, func(result *gorm.DB) *gorm.DB {
		if criteria.SourceDevice != nil {
			result = result.Where("source_device_id = (?)",
				api.RDB.Database.Model(&Device{}).Select("id").Where("token = ?", criteria.SourceDevice))
		}
		if criteria.RelationshipType != nil {
			result = result.Where("relationship_type_id = (?)",
				api.RDB.Database.Model(&DeviceRelationshipType{}).Select("id").Where("token = ?", criteria.RelationshipType))
		}
		if criteria.Tracked != nil {
			result = result.Where("relationship_type_id in (?)",
				api.RDB.Database.Model(&DeviceRelationshipType{}).Select("id").Where("tracked = ?", criteria.Tracked))
		}
		return result
	}, criteria.Pagination)
	db.Preload("SourceDevice").Preload("RelationshipType")
	db = preloadRelationshipTargets(db)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &DeviceRelationshipSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new device group.
func (api *Api) CreateDeviceGroup(ctx context.Context, request *DeviceGroupCreateRequest) (*DeviceGroup, error) {
	created := &DeviceGroup{
		TokenReference: rdb.TokenReference{
			Token: request.Token,
		},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		BrandedEntity: rdb.BrandedEntity{
			ImageUrl:        rdb.NullStrOf(request.ImageUrl),
			Icon:            rdb.NullStrOf(request.Icon),
			BackgroundColor: rdb.NullStrOf(request.BackgroundColor),
			ForegroundColor: rdb.NullStrOf(request.ForegroundColor),
			BorderColor:     rdb.NullStrOf(request.BorderColor),
		},
		MetadataEntity: rdb.MetadataEntity{
			Metadata: rdb.MetadataStrOf(request.Metadata),
		},
	}
	result := api.RDB.Database.Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing device type.
func (api *Api) UpdateDeviceGroup(ctx context.Context, token string,
	request *DeviceGroupCreateRequest) (*DeviceGroup, error) {
	matches, err := api.DeviceGroupsByToken(ctx, []string{request.Token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	updated := matches[0]
	updated.Token = request.Token
	updated.Name = rdb.NullStrOf(request.Name)
	updated.Description = rdb.NullStrOf(request.Description)
	updated.ImageUrl = rdb.NullStrOf(request.ImageUrl)
	updated.Icon = rdb.NullStrOf(request.Icon)
	updated.BackgroundColor = rdb.NullStrOf(request.BackgroundColor)
	updated.ForegroundColor = rdb.NullStrOf(request.ForegroundColor)
	updated.BorderColor = rdb.NullStrOf(request.BorderColor)
	updated.Metadata = rdb.MetadataStrOf(request.Metadata)

	result := api.RDB.Database.Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get device groups by id.
func (api *Api) DeviceGroupsById(ctx context.Context, ids []uint) ([]*DeviceGroup, error) {
	found := make([]*DeviceGroup, 0)
	result := api.RDB.Database.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get device groups by token.
func (api *Api) DeviceGroupsByToken(ctx context.Context, tokens []string) ([]*DeviceGroup, error) {
	found := make([]*DeviceGroup, 0)
	result := api.RDB.Database.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for device groups that meet criteria.
func (api *Api) DeviceGroups(ctx context.Context, criteria DeviceGroupSearchCriteria) (*DeviceGroupSearchResults, error) {
	results := make([]DeviceGroup, 0)
	db, pag := api.RDB.ListOf(&DeviceGroup{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &DeviceGroupSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new device group relationship type.
func (api *Api) CreateDeviceGroupRelationshipType(ctx context.Context,
	request *DeviceGroupRelationshipTypeCreateRequest) (*DeviceGroupRelationshipType, error) {
	created := &DeviceGroupRelationshipType{
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
	}
	result := api.RDB.Database.Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing device group relationship type.
func (api *Api) UpdateDeviceGroupRelationshipType(ctx context.Context, token string,
	request *DeviceGroupRelationshipTypeCreateRequest) (*DeviceGroupRelationshipType, error) {
	matches, err := api.DeviceGroupRelationshipTypesByToken(ctx, []string{request.Token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	updated := matches[0]
	updated.Token = request.Token
	updated.Name = rdb.NullStrOf(request.Name)
	updated.Description = rdb.NullStrOf(request.Description)
	updated.Metadata = rdb.MetadataStrOf(request.Metadata)

	result := api.RDB.Database.Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get device group relationship types by id.
func (api *Api) DeviceGroupRelationshipTypesById(ctx context.Context, ids []uint) ([]*DeviceGroupRelationshipType, error) {
	found := make([]*DeviceGroupRelationshipType, 0)
	result := api.RDB.Database.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get device group relationship types by token.
func (api *Api) DeviceGroupRelationshipTypesByToken(ctx context.Context, tokens []string) ([]*DeviceGroupRelationshipType, error) {
	found := make([]*DeviceGroupRelationshipType, 0)
	result := api.RDB.Database.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for device group relationship types that meet criteria.
func (api *Api) DeviceGroupRelationshipTypes(ctx context.Context,
	criteria DeviceGroupRelationshipTypeSearchCriteria) (*DeviceGroupRelationshipTypeSearchResults, error) {
	results := make([]DeviceGroupRelationshipType, 0)
	db, pag := api.RDB.ListOf(&DeviceGroupRelationshipType{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &DeviceGroupRelationshipTypeSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new device group relationship.
func (api *Api) CreateDeviceGroupRelationship(ctx context.Context,
	request *DeviceGroupRelationshipCreateRequest) (*DeviceGroupRelationship, error) {

	// Look up source reference.
	gmatches, err := api.DeviceGroupsByToken(ctx, []string{request.SourceDeviceGroup})
	if err != nil {
		return nil, err
	}
	if len(gmatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	// Look up relationship reference.
	rtmatches, err := api.DeviceGroupRelationshipTypesByToken(ctx, []string{request.RelationshipType})
	if err != nil {
		return nil, err
	}
	if len(rtmatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	created := &DeviceGroupRelationship{
		EntityRelationship: EntityRelationship{
			TokenReference: rdb.TokenReference{
				Token: request.Token,
			},
			MetadataEntity: rdb.MetadataEntity{
				Metadata: rdb.MetadataStrOf(request.Metadata),
			},
		},
		SourceDeviceGroup: *gmatches[0],
		RelationshipType:  *rtmatches[0],
	}
	api.resolveRelationshipTargets(ctx, request.Targets, &created.EntityRelationship)
	result := api.RDB.Database.Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Get device group relationships by id.
func (api *Api) DeviceGroupRelationshipsById(ctx context.Context, ids []uint) ([]*DeviceGroupRelationship, error) {
	found := make([]*DeviceGroupRelationship, 0)
	result := api.RDB.Database
	result = result.Preload("SourceDeviceGroup").Preload("RelationshipType")
	result = preloadRelationshipTargets(result)
	result = result.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get device group relationships by token.
func (api *Api) DeviceGroupRelationshipsByToken(ctx context.Context, tokens []string) ([]*DeviceGroupRelationship, error) {
	found := make([]*DeviceGroupRelationship, 0)
	result := api.RDB.Database
	result = result.Preload("SourceDeviceGroup").Preload("RelationshipType")
	result = preloadRelationshipTargets(result)
	result = result.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for device group relationships that meet criteria.
func (api *Api) DeviceGroupRelationships(ctx context.Context,
	criteria DeviceGroupRelationshipSearchCriteria) (*DeviceGroupRelationshipSearchResults, error) {
	results := make([]DeviceGroupRelationship, 0)
	db, pag := api.RDB.ListOf(&DeviceGroupRelationship{}, func(result *gorm.DB) *gorm.DB {
		if criteria.SourceDeviceGroup != nil {
			result = result.Where("source_device_group_id = (?)",
				api.RDB.Database.Model(&DeviceGroup{}).Select("id").Where("token = ?", criteria.SourceDeviceGroup))
		}
		if criteria.RelationshipType != nil {
			result = result.Where("relationship_type_id = (?)",
				api.RDB.Database.Model(&DeviceGroupRelationshipType{}).Select("id").Where("token = ?", criteria.RelationshipType))
		}
		return result
	}, criteria.Pagination)
	db.Preload("SourceDeviceGroup").Preload("RelationshipType")
	db = preloadRelationshipTargets(db)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &DeviceGroupRelationshipSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

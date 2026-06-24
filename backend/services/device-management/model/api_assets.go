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

// Create a new asset type.
func (api *Api) CreateAssetType(ctx context.Context, request *AssetTypeCreateRequest) (*AssetType, error) {
	created := &AssetType{
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

// Update an existing asset type.
func (api *Api) UpdateAssetType(ctx context.Context, token string,
	request *AssetTypeCreateRequest) (*AssetType, error) {
	matches, err := api.AssetTypesByToken(ctx, []string{request.Token})
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

// Get asset types by id.
func (api *Api) AssetTypesById(ctx context.Context, ids []uint) ([]*AssetType, error) {
	found := make([]*AssetType, 0)
	result := api.RDB.Database.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get asset types by token.
func (api *Api) AssetTypesByToken(ctx context.Context, tokens []string) ([]*AssetType, error) {
	found := make([]*AssetType, 0)
	result := api.RDB.Database.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for asset types that meet criteria.
func (api *Api) AssetTypes(ctx context.Context, criteria AssetTypeSearchCriteria) (*AssetTypeSearchResults, error) {
	results := make([]AssetType, 0)
	db, pag := api.RDB.ListOf(&AssetType{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &AssetTypeSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new asset.
func (api *Api) CreateAsset(ctx context.Context, request *AssetCreateRequest) (*Asset, error) {
	matches, err := api.AssetTypesByToken(ctx, []string{request.AssetTypeToken})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	created := &Asset{
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
		AssetType: matches[0],
	}
	result := api.RDB.Database.Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing asset.
func (api *Api) UpdateAsset(ctx context.Context, token string, request *AssetCreateRequest) (*Asset, error) {
	matches, err := api.AssetsByToken(ctx, []string{request.Token})
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

	// Update asset type if changed.
	if request.AssetTypeToken != updated.AssetType.Token {
		matches, err := api.AssetTypesByToken(ctx, []string{request.AssetTypeToken})
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, gorm.ErrRecordNotFound
		}
		updated.AssetType = matches[0]
	}

	result := api.RDB.Database.Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get assets by id.
func (api *Api) AssetsById(ctx context.Context, ids []uint) ([]*Asset, error) {
	found := make([]*Asset, 0)
	result := api.RDB.Database
	result = result.Preload("AssetType")
	result = result.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get assets by token.
func (api *Api) AssetsByToken(ctx context.Context, tokens []string) ([]*Asset, error) {
	found := make([]*Asset, 0)
	result := api.RDB.Database
	result = result.Preload("AssetType")
	result = result.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for assets that meet criteria.
func (api *Api) Assets(ctx context.Context, criteria AssetSearchCriteria) (*AssetSearchResults, error) {
	results := make([]Asset, 0)
	db, pag := api.RDB.ListOf(&Asset{}, func(result *gorm.DB) *gorm.DB {
		if criteria.AssetTypeToken != nil {
			result = result.Where("asset_type_id = (?)",
				api.RDB.Database.Model(&AssetType{}).Select("id").Where("token = ?", criteria.AssetTypeToken))
		}
		return result.Preload("AssetType")
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &AssetSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new asset relationship type.
func (api *Api) CreateAssetRelationshipType(ctx context.Context, request *AssetRelationshipTypeCreateRequest) (*AssetRelationshipType, error) {
	created := &AssetRelationshipType{
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

// Update an existing asset relationship type.
func (api *Api) UpdateAssetRelationshipType(ctx context.Context, token string,
	request *AssetRelationshipTypeCreateRequest) (*AssetRelationshipType, error) {
	matches, err := api.AssetRelationshipTypesByToken(ctx, []string{request.Token})
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

// Get asset relationship types by id.
func (api *Api) AssetRelationshipTypesById(ctx context.Context, ids []uint) ([]*AssetRelationshipType, error) {
	found := make([]*AssetRelationshipType, 0)
	result := api.RDB.Database.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get asset relationship types by token.
func (api *Api) AssetRelationshipTypesByToken(ctx context.Context, tokens []string) ([]*AssetRelationshipType, error) {
	found := make([]*AssetRelationshipType, 0)
	result := api.RDB.Database.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for asset relationship types that meet criteria.
func (api *Api) AssetRelationshipTypes(ctx context.Context,
	criteria AssetRelationshipTypeSearchCriteria) (*AssetRelationshipTypeSearchResults, error) {
	results := make([]AssetRelationshipType, 0)
	db, pag := api.RDB.ListOf(&AssetRelationshipType{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &AssetRelationshipTypeSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create map by token from list of assets.
func assetByTokenMap(vals []*Asset) map[string]*Asset {
	results := make(map[string]*Asset)
	for _, val := range vals {
		results[val.Token] = val
	}
	return results
}

// Create a new asset relationship.
func (api *Api) CreateAssetRelationship(ctx context.Context, request *AssetRelationshipCreateRequest) (*AssetRelationship, error) {
	// Look up source reference.
	amatches, err := api.AssetsByToken(ctx, []string{request.SourceAsset})
	if err != nil {
		return nil, err
	}
	if len(amatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	mmap := assetByTokenMap(amatches)

	// Look up relationship reference.
	artmatches, err := api.AssetRelationshipTypesByToken(ctx, []string{request.RelationshipType})
	if err != nil {
		return nil, err
	}
	if len(artmatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	created := &AssetRelationship{
		EntityRelationship: EntityRelationship{
			TokenReference: rdb.TokenReference{
				Token: request.Token,
			},
			MetadataEntity: rdb.MetadataEntity{
				Metadata: rdb.MetadataStrOf(request.Metadata),
			},
		},
		SourceAsset:      *mmap[request.SourceAsset],
		RelationshipType: *artmatches[0],
	}
	api.resolveRelationshipTargets(ctx, request.Targets, &created.EntityRelationship)
	result := api.RDB.Database.Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Get asset relationships by id.
func (api *Api) AssetRelationshipsById(ctx context.Context, ids []uint) ([]*AssetRelationship, error) {
	found := make([]*AssetRelationship, 0)
	result := api.RDB.Database
	result = result.Preload("SourceAsset").Preload("RelationshipType")
	result = preloadRelationshipTargets(result)
	result = result.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get asset relationships by token.
func (api *Api) AssetRelationshipsByToken(ctx context.Context, tokens []string) ([]*AssetRelationship, error) {
	found := make([]*AssetRelationship, 0)
	result := api.RDB.Database
	result = result.Preload("SourceAsset").Preload("RelationshipType")
	result = preloadRelationshipTargets(result)
	result = result.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for asset relationships that meet criteria.
func (api *Api) AssetRelationships(ctx context.Context,
	criteria AssetRelationshipSearchCriteria) (*AssetRelationshipSearchResults, error) {
	results := make([]AssetRelationship, 0)
	db, pag := api.RDB.ListOf(&AssetRelationship{}, func(result *gorm.DB) *gorm.DB {
		if criteria.SourceAsset != nil {
			result = result.Where("source_asset_id = (?)",
				api.RDB.Database.Model(&Asset{}).Select("id").Where("token = ?", criteria.SourceAsset))
		}
		if criteria.RelationshipType != nil {
			result = result.Where("relationship_type_id = (?)",
				api.RDB.Database.Model(&AssetRelationshipType{}).Select("id").Where("token = ?", criteria.RelationshipType))
		}
		return result
	}, criteria.Pagination)
	db.Preload("SourceAsset").Preload("RelationshipType")
	db = preloadRelationshipTargets(db)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &AssetRelationshipSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new asset group.
func (api *Api) CreateAssetGroup(ctx context.Context, request *AssetGroupCreateRequest) (*AssetGroup, error) {
	created := &AssetGroup{
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

// Update an existing asset type.
func (api *Api) UpdateAssetGroup(ctx context.Context, token string,
	request *AssetGroupCreateRequest) (*AssetGroup, error) {
	matches, err := api.AssetGroupsByToken(ctx, []string{request.Token})
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

// Get asset groups by id.
func (api *Api) AssetGroupsById(ctx context.Context, ids []uint) ([]*AssetGroup, error) {
	found := make([]*AssetGroup, 0)
	result := api.RDB.Database.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get asset groups by token.
func (api *Api) AssetGroupsByToken(ctx context.Context, tokens []string) ([]*AssetGroup, error) {
	found := make([]*AssetGroup, 0)
	result := api.RDB.Database.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for asset groups that meet criteria.
func (api *Api) AssetGroups(ctx context.Context, criteria AssetGroupSearchCriteria) (*AssetGroupSearchResults, error) {
	results := make([]AssetGroup, 0)
	db, pag := api.RDB.ListOf(&AssetGroup{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &AssetGroupSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new asset group relationship type.
func (api *Api) CreateAssetGroupRelationshipType(ctx context.Context,
	request *AssetGroupRelationshipTypeCreateRequest) (*AssetGroupRelationshipType, error) {
	created := &AssetGroupRelationshipType{
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

// Update an existing asset group relationship type.
func (api *Api) UpdateAssetGroupRelationshipType(ctx context.Context, token string,
	request *AssetGroupRelationshipTypeCreateRequest) (*AssetGroupRelationshipType, error) {
	matches, err := api.AssetGroupRelationshipTypesByToken(ctx, []string{request.Token})
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

// Get asset group relationship types by id.
func (api *Api) AssetGroupRelationshipTypesById(ctx context.Context, ids []uint) ([]*AssetGroupRelationshipType, error) {
	found := make([]*AssetGroupRelationshipType, 0)
	result := api.RDB.Database.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get asset group relationship types by token.
func (api *Api) AssetGroupRelationshipTypesByToken(ctx context.Context, tokens []string) ([]*AssetGroupRelationshipType, error) {
	found := make([]*AssetGroupRelationshipType, 0)
	result := api.RDB.Database.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for asset group relationship types that meet criteria.
func (api *Api) AssetGroupRelationshipTypes(ctx context.Context,
	criteria AssetGroupRelationshipTypeSearchCriteria) (*AssetGroupRelationshipTypeSearchResults, error) {
	results := make([]AssetGroupRelationshipType, 0)
	db, pag := api.RDB.ListOf(&AssetGroupRelationshipType{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &AssetGroupRelationshipTypeSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new asset group relationship.
func (api *Api) CreateAssetGroupRelationship(ctx context.Context,
	request *AssetGroupRelationshipCreateRequest) (*AssetGroupRelationship, error) {

	// Look up source reference.
	agmatches, err := api.AssetGroupsByToken(ctx, []string{request.SourceAssetGroup})
	if err != nil {
		return nil, err
	}
	if len(agmatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	// Look up relationship reference.
	agrmatches, err := api.AssetGroupRelationshipTypesByToken(ctx, []string{request.RelationshipType})
	if err != nil {
		return nil, err
	}
	if len(agrmatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	created := &AssetGroupRelationship{
		EntityRelationship: EntityRelationship{
			TokenReference: rdb.TokenReference{
				Token: request.Token,
			},
			MetadataEntity: rdb.MetadataEntity{
				Metadata: rdb.MetadataStrOf(request.Metadata),
			},
		},
		SourceAssetGroup: *agmatches[0],
		RelationshipType: *agrmatches[0],
	}
	api.resolveRelationshipTargets(ctx, request.Targets, &created.EntityRelationship)
	result := api.RDB.Database.Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Get asset group relationships by id.
func (api *Api) AssetGroupRelationshipsById(ctx context.Context, ids []uint) ([]*AssetGroupRelationship, error) {
	found := make([]*AssetGroupRelationship, 0)
	result := api.RDB.Database
	result = result.Preload("SourceAssetGroup").Preload("RelationshipType")
	result = preloadRelationshipTargets(result)
	result = result.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get asset group relationships by token.
func (api *Api) AssetGroupRelationshipsByToken(ctx context.Context, tokens []string) ([]*AssetGroupRelationship, error) {
	found := make([]*AssetGroupRelationship, 0)
	result := api.RDB.Database
	result = result.Preload("SourceAssetGroup").Preload("RelationshipType")
	result = preloadRelationshipTargets(result)
	result = result.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for asset group relationships that meet criteria.
func (api *Api) AssetGroupRelationships(ctx context.Context,
	criteria AssetGroupRelationshipSearchCriteria) (*AssetGroupRelationshipSearchResults, error) {
	results := make([]AssetGroupRelationship, 0)
	db, pag := api.RDB.ListOf(&AssetGroupRelationship{}, func(result *gorm.DB) *gorm.DB {
		if criteria.SourceAssetGroup != nil {
			result = result.Where("source_asset_group_id = (?)",
				api.RDB.Database.Model(&AssetGroup{}).Select("id").Where("token = ?", criteria.SourceAssetGroup))
		}
		if criteria.RelationshipType != nil {
			result = result.Where("relationship_type_id = (?)",
				api.RDB.Database.Model(&AssetGroupRelationshipType{}).Select("id").Where("token = ?", criteria.RelationshipType))
		}
		return result
	}, criteria.Pagination)
	db.Preload("SourceAssetGroup").Preload("RelationshipType")
	db = preloadRelationshipTargets(db)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &AssetGroupRelationshipSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

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
	result := api.RDB.DB(ctx).Create(created)
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

	result := api.RDB.DB(ctx).Save(found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get asset types by id.
func (api *Api) AssetTypesById(ctx context.Context, ids []uint) ([]*AssetType, error) {
	found := make([]*AssetType, 0)
	result := api.RDB.DB(ctx).Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get asset types by token.
func (api *Api) AssetTypesByToken(ctx context.Context, tokens []string) ([]*AssetType, error) {
	found := make([]*AssetType, 0)
	result := api.RDB.DB(ctx).Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for asset types that meet criteria.
func (api *Api) AssetTypes(ctx context.Context, criteria AssetTypeSearchCriteria) (*AssetTypeSearchResults, error) {
	results := make([]AssetType, 0)
	db, pag := api.RDB.ListOf(ctx, &AssetType{}, nil, criteria.Pagination)
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
	result := api.RDB.DB(ctx).Create(created)
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

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get assets by id.
func (api *Api) AssetsById(ctx context.Context, ids []uint) ([]*Asset, error) {
	found := make([]*Asset, 0)
	result := api.RDB.DB(ctx)
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
	result := api.RDB.DB(ctx)
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
	db, pag := api.RDB.ListOf(ctx, &Asset{}, func(result *gorm.DB) *gorm.DB {
		if criteria.AssetTypeToken != nil {
			result = result.Where("asset_type_id = (?)",
				api.RDB.DB(ctx).Model(&AssetType{}).Select("id").Where("token = ?", criteria.AssetTypeToken))
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

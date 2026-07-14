// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Create a new area type.
func (api *Api) CreateAreaType(ctx context.Context, request *AreaTypeCreateRequest) (*AreaType, error) {
	created := &AreaType{
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

// Update an existing area type.
func (api *Api) UpdateAreaType(ctx context.Context, token string,
	request *AreaTypeCreateRequest) (*AreaType, error) {
	matches, err := api.AreaTypesByToken(ctx, []string{request.Token})
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

// Get area types by id.
func (api *Api) AreaTypesById(ctx context.Context, ids []uint) ([]*AreaType, error) {
	found := make([]*AreaType, 0)
	result := api.RDB.DB(ctx).Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get area types by token.
func (api *Api) AreaTypesByToken(ctx context.Context, tokens []string) ([]*AreaType, error) {
	found := make([]*AreaType, 0)
	result := api.RDB.DB(ctx).Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for area types that meet criteria.
func (api *Api) AreaTypes(ctx context.Context, criteria AreaTypeSearchCriteria) (*AreaTypeSearchResults, error) {
	results := make([]AreaType, 0)
	db, pag := api.RDB.ListOf(ctx, &AreaType{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &AreaTypeSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new area.
func (api *Api) CreateArea(ctx context.Context, request *AreaCreateRequest) (*Area, error) {
	atmatches, err := api.AreaTypesByToken(ctx, []string{request.AreaTypeToken})
	if err != nil {
		return nil, err
	}
	if len(atmatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	created := &Area{
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
		AreaType: atmatches[0],
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing area.
func (api *Api) UpdateArea(ctx context.Context, token string, request *AreaCreateRequest) (*Area, error) {
	matches, err := api.AreasByToken(ctx, []string{request.Token})
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

	// Update area type if changed.
	if request.AreaTypeToken != updated.AreaType.Token {
		atmatches, err := api.AreaTypesByToken(ctx, []string{request.AreaTypeToken})
		if err != nil {
			return nil, err
		}
		if len(atmatches) == 0 {
			return nil, gorm.ErrRecordNotFound
		}
		updated.AreaType = atmatches[0]
	}

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get areas by id.
func (api *Api) AreasById(ctx context.Context, ids []uint) ([]*Area, error) {
	found := make([]*Area, 0)
	result := api.RDB.DB(ctx)
	result = result.Preload("AreaType")
	result = result.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get areas by token.
func (api *Api) AreasByToken(ctx context.Context, tokens []string) ([]*Area, error) {
	found := make([]*Area, 0)
	result := api.RDB.DB(ctx)
	result = result.Preload("AreaType")
	result = result.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for areas that meet criteria.
func (api *Api) Areas(ctx context.Context, criteria AreaSearchCriteria) (*AreaSearchResults, error) {
	results := make([]Area, 0)
	db, pag := api.RDB.ListOf(ctx, &Area{}, func(result *gorm.DB) *gorm.DB {
		if criteria.AreaTypeToken != nil {
			result = result.Where("area_type_id = (?)",
				api.RDB.DB(ctx).Model(&AreaType{}).Select("id").Where("token = ?", criteria.AreaTypeToken))
		}
		return result.Preload("AreaType")
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &AreaSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

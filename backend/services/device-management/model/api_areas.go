// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Create a new area type.
func (api *Api) CreateAreaType(ctx context.Context, request *AreaTypeCreateRequest) (*AreaType, error) {
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
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
			Metadata: metadataJSON,
		},
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing area type, applying only the fields the caller actually
// sent. The entity is looked up by the `token` ARGUMENT — the request payload no
// longer carries one, which closes two defects at once: an update can no longer
// move an area type's token, and the mandatory `token` argument is no longer dead.
// It used to be ignored entirely in favour of request.Token, so a caller naming one
// type in the argument and another in the payload silently updated the second and
// got a 200 back for it.
//
// Each assignment folds the field's three states onto the stored value: absent
// keeps it, null clears it, a value sets it. Reading `found.X` as the "current"
// argument is what makes an omitted field a no-op, so these must stay assignments
// FROM the loaded record rather than from the request alone.
func (api *Api) UpdateAreaType(ctx context.Context, token string,
	request *AreaTypeUpdateRequest) (*AreaType, error) {
	matches, err := api.AreaTypesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	found := matches[0]
	found.Name = rdb.NullStrOf(request.Name.ApplyTo(dcgraphql.NullStr(found.Name)))
	found.Description = rdb.NullStrOf(request.Description.ApplyTo(dcgraphql.NullStr(found.Description)))
	found.ImageUrl = rdb.NullStrOf(request.ImageUrl.ApplyTo(dcgraphql.NullStr(found.ImageUrl)))
	found.Icon = rdb.NullStrOf(request.Icon.ApplyTo(dcgraphql.NullStr(found.Icon)))
	found.BackgroundColor = rdb.NullStrOf(request.BackgroundColor.ApplyTo(dcgraphql.NullStr(found.BackgroundColor)))
	found.ForegroundColor = rdb.NullStrOf(request.ForegroundColor.ApplyTo(dcgraphql.NullStr(found.ForegroundColor)))
	found.BorderColor = rdb.NullStrOf(request.BorderColor.ApplyTo(dcgraphql.NullStr(found.BorderColor)))
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata.ApplyTo(dcgraphql.MetadataStr(found.Metadata)))
	if err != nil {
		return nil, err
	}
	found.Metadata = metadataJSON

	result := api.RDB.DB(ctx).Save(found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get area types by id.
func (api *Api) AreaTypesById(ctx context.Context, ids []uint) ([]*AreaType, error) {
	return rdb.FindByIds[AreaType](api.RDB.DB(ctx), ids)
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

	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
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
			Metadata: metadataJSON,
		},
		AreaType: atmatches[0],
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing area, applying only the fields the caller actually sent.
// Looked up by the `token` ARGUMENT; the payload no longer carries a token, so the
// argument is no longer dead and a token move is unrepresentable rather than merely
// refused.
func (api *Api) UpdateArea(ctx context.Context, token string, request *AreaUpdateRequest) (*Area, error) {
	matches, err := api.AreasByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	updated := matches[0]

	// The type hop resolves BEFORE anything is written, so an unknown area type
	// refuses the WHOLE update rather than applying the fields it liked first. The
	// nil guard is not decoration: the preload comes back nil for a dangling FK, and
	// the comparison this replaces dereferenced it unconditionally.
	currentTypeToken := ""
	if updated.AreaType != nil {
		currentTypeToken = updated.AreaType.Token
	}
	retypeTo, retype, err := resolveRequiredTypeRef(request.AreaTypeToken, currentTypeToken, "areaTypeToken")
	if err != nil {
		return nil, err
	}
	if retype {
		types, err := api.AreaTypesByToken(ctx, []string{retypeTo})
		if err != nil {
			return nil, err
		}
		if len(types) == 0 {
			return nil, gorm.ErrRecordNotFound
		}
		updated.AreaType = types[0]
		updated.AreaTypeId = types[0].ID // keep the FK in lockstep with the association
	}

	updated.Name = rdb.NullStrOf(request.Name.ApplyTo(dcgraphql.NullStr(updated.Name)))
	updated.Description = rdb.NullStrOf(request.Description.ApplyTo(dcgraphql.NullStr(updated.Description)))
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata.ApplyTo(dcgraphql.MetadataStr(updated.Metadata)))
	if err != nil {
		return nil, err
	}
	updated.Metadata = metadataJSON

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get areas by id.
func (api *Api) AreasById(ctx context.Context, ids []uint) ([]*Area, error) {
	return rdb.FindByIds[Area](api.RDB.DB(ctx).Preload("AreaType"), ids)
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

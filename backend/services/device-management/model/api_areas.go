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
	result := api.RDB.Database.Create(created)
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

	result := api.RDB.Database.Save(found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get area types by id.
func (api *Api) AreaTypesById(ctx context.Context, ids []uint) ([]*AreaType, error) {
	found := make([]*AreaType, 0)
	result := api.RDB.Database.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get area types by token.
func (api *Api) AreaTypesByToken(ctx context.Context, tokens []string) ([]*AreaType, error) {
	found := make([]*AreaType, 0)
	result := api.RDB.Database.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for area types that meet criteria.
func (api *Api) AreaTypes(ctx context.Context, criteria AreaTypeSearchCriteria) (*AreaTypeSearchResults, error) {
	results := make([]AreaType, 0)
	db, pag := api.RDB.ListOf(&AreaType{}, nil, criteria.Pagination)
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
	result := api.RDB.Database.Create(created)
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

	result := api.RDB.Database.Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get areas by id.
func (api *Api) AreasById(ctx context.Context, ids []uint) ([]*Area, error) {
	found := make([]*Area, 0)
	result := api.RDB.Database
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
	result := api.RDB.Database
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
	db, pag := api.RDB.ListOf(&Area{}, func(result *gorm.DB) *gorm.DB {
		if criteria.AreaTypeToken != nil {
			result = result.Where("area_type_id = (?)",
				api.RDB.Database.Model(&AreaType{}).Select("id").Where("token = ?", criteria.AreaTypeToken))
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

// Create a new area relationship type.
func (api *Api) CreateAreaRelationshipType(ctx context.Context, request *AreaRelationshipTypeCreateRequest) (*AreaRelationshipType, error) {
	created := &AreaRelationshipType{
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

// Update an existing area relationship type.
func (api *Api) UpdateAreaRelationshipType(ctx context.Context, token string,
	request *AreaRelationshipTypeCreateRequest) (*AreaRelationshipType, error) {
	artmatches, err := api.AreaRelationshipTypesByToken(ctx, []string{request.Token})
	if err != nil {
		return nil, err
	}
	if len(artmatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	updated := artmatches[0]
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

// Get area relationship types by id.
func (api *Api) AreaRelationshipTypesById(ctx context.Context, ids []uint) ([]*AreaRelationshipType, error) {
	found := make([]*AreaRelationshipType, 0)
	result := api.RDB.Database.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get area relationship types by token.
func (api *Api) AreaRelationshipTypesByToken(ctx context.Context, tokens []string) ([]*AreaRelationshipType, error) {
	found := make([]*AreaRelationshipType, 0)
	result := api.RDB.Database.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for area relationship types that meet criteria.
func (api *Api) AreaRelationshipTypes(ctx context.Context,
	criteria AreaRelationshipTypeSearchCriteria) (*AreaRelationshipTypeSearchResults, error) {
	results := make([]AreaRelationshipType, 0)
	db, pag := api.RDB.ListOf(&AreaRelationshipType{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &AreaRelationshipTypeSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create map by token from list of areas.
func areasByTokenMap(vals []*Area) map[string]*Area {
	results := make(map[string]*Area)
	for _, val := range vals {
		results[val.Token] = val
	}
	return results
}

// Create a new area relationship.
func (api *Api) CreateAreaRelationship(ctx context.Context, request *AreaRelationshipCreateRequest) (*AreaRelationship, error) {
	// Look up source reference.
	amatches, err := api.AreasByToken(ctx, []string{request.SourceArea})
	if err != nil {
		return nil, err
	}
	if len(amatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	mmap := areasByTokenMap(amatches)

	// Look up relationship reference.
	artmatches, err := api.AreaRelationshipTypesByToken(ctx, []string{request.RelationshipType})
	if err != nil {
		return nil, err
	}
	if len(artmatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	created := &AreaRelationship{
		EntityRelationship: EntityRelationship{
			TokenReference: rdb.TokenReference{
				Token: request.Token,
			},
			MetadataEntity: rdb.MetadataEntity{
				Metadata: rdb.MetadataStrOf(request.Metadata),
			},
		},
		SourceArea:       *mmap[request.SourceArea],
		RelationshipType: *artmatches[0],
	}
	api.resolveRelationshipTargets(ctx, request.Targets, &created.EntityRelationship)
	result := api.RDB.Database.Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Get area relationships by id.
func (api *Api) AreaRelationshipsById(ctx context.Context, ids []uint) ([]*AreaRelationship, error) {
	found := make([]*AreaRelationship, 0)
	result := api.RDB.Database
	result = result.Preload("SourceArea").Preload("RelationshipType")
	result = preloadRelationshipTargets(result)
	result = result.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get area relationships by token.
func (api *Api) AreaRelationshipsByToken(ctx context.Context, tokens []string) ([]*AreaRelationship, error) {
	found := make([]*AreaRelationship, 0)
	result := api.RDB.Database
	result = result.Preload("SourceArea").Preload("RelationshipType")
	result = preloadRelationshipTargets(result)
	result = result.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for area relationships that meet criteria.
func (api *Api) AreaRelationships(ctx context.Context,
	criteria AreaRelationshipSearchCriteria) (*AreaRelationshipSearchResults, error) {
	results := make([]AreaRelationship, 0)
	db, pag := api.RDB.ListOf(&AreaRelationship{}, func(result *gorm.DB) *gorm.DB {
		if criteria.SourceArea != nil {
			result = result.Where("source_area_id = (?)",
				api.RDB.Database.Model(&Area{}).Select("id").Where("token = ?", criteria.SourceArea))
		}
		if criteria.RelationshipType != nil {
			result = result.Where("relationship_type_id = (?)",
				api.RDB.Database.Model(&AreaRelationshipType{}).Select("id").Where("token = ?", criteria.RelationshipType))
		}
		return result
	}, criteria.Pagination)
	db.Preload("SourceArea").Preload("RelationshipType")
	db = preloadRelationshipTargets(db)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &AreaRelationshipSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new area group.
func (api *Api) CreateAreaGroup(ctx context.Context, request *AreaGroupCreateRequest) (*AreaGroup, error) {
	created := &AreaGroup{
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

// Update an existing area type.
func (api *Api) UpdateAreaGroup(ctx context.Context, token string,
	request *AreaGroupCreateRequest) (*AreaGroup, error) {
	matches, err := api.AreaGroupsByToken(ctx, []string{request.Token})
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

// Get area groups by id.
func (api *Api) AreaGroupsById(ctx context.Context, ids []uint) ([]*AreaGroup, error) {
	found := make([]*AreaGroup, 0)
	result := api.RDB.Database.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get area groups by token.
func (api *Api) AreaGroupsByToken(ctx context.Context, tokens []string) ([]*AreaGroup, error) {
	found := make([]*AreaGroup, 0)
	result := api.RDB.Database.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for area groups that meet criteria.
func (api *Api) AreaGroups(ctx context.Context, criteria AreaGroupSearchCriteria) (*AreaGroupSearchResults, error) {
	results := make([]AreaGroup, 0)
	db, pag := api.RDB.ListOf(&AreaGroup{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &AreaGroupSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new area group relationship type.
func (api *Api) CreateAreaGroupRelationshipType(ctx context.Context,
	request *AreaGroupRelationshipTypeCreateRequest) (*AreaGroupRelationshipType, error) {
	created := &AreaGroupRelationshipType{
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

// Update an existing area group relationship type.
func (api *Api) UpdateAreaGroupRelationshipType(ctx context.Context, token string,
	request *AreaGroupRelationshipTypeCreateRequest) (*AreaGroupRelationshipType, error) {
	matches, err := api.AreaGroupRelationshipTypesByToken(ctx, []string{request.Token})
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

// Get area group relationship types by id.
func (api *Api) AreaGroupRelationshipTypesById(ctx context.Context, ids []uint) ([]*AreaGroupRelationshipType, error) {
	found := make([]*AreaGroupRelationshipType, 0)
	result := api.RDB.Database.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get area group relationship types by token.
func (api *Api) AreaGroupRelationshipTypesByToken(ctx context.Context, tokens []string) ([]*AreaGroupRelationshipType, error) {
	found := make([]*AreaGroupRelationshipType, 0)
	result := api.RDB.Database.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for area group relationship types that meet criteria.
func (api *Api) AreaGroupRelationshipTypes(ctx context.Context,
	criteria AreaGroupRelationshipTypeSearchCriteria) (*AreaGroupRelationshipTypeSearchResults, error) {
	results := make([]AreaGroupRelationshipType, 0)
	db, pag := api.RDB.ListOf(&AreaGroupRelationshipType{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &AreaGroupRelationshipTypeSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new area group relationship.
func (api *Api) CreateAreaGroupRelationship(ctx context.Context,
	request *AreaGroupRelationshipCreateRequest) (*AreaGroupRelationship, error) {

	// Look up source reference.
	agmatches, err := api.AreaGroupsByToken(ctx, []string{request.SourceAreaGroup})
	if err != nil {
		return nil, err
	}
	if len(agmatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	// Look up relationship reference.
	agrtmatches, err := api.AreaGroupRelationshipTypesByToken(ctx, []string{request.RelationshipType})
	if err != nil {
		return nil, err
	}
	if len(agrtmatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	created := &AreaGroupRelationship{
		EntityRelationship: EntityRelationship{
			TokenReference: rdb.TokenReference{
				Token: request.Token,
			},
			MetadataEntity: rdb.MetadataEntity{
				Metadata: rdb.MetadataStrOf(request.Metadata),
			},
		},
		SourceAreaGroup:  *agmatches[0],
		RelationshipType: *agrtmatches[0],
	}
	api.resolveRelationshipTargets(ctx, request.Targets, &created.EntityRelationship)
	result := api.RDB.Database.Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Get area group relationships by id.
func (api *Api) AreaGroupRelationshipsById(ctx context.Context, ids []uint) ([]*AreaGroupRelationship, error) {
	found := make([]*AreaGroupRelationship, 0)
	result := api.RDB.Database
	result = result.Preload("SourceAreaGroup").Preload("RelationshipType")
	result = preloadRelationshipTargets(result)
	result = result.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get area group relationships by token.
func (api *Api) AreaGroupRelationshipsByToken(ctx context.Context, tokens []string) ([]*AreaGroupRelationship, error) {
	found := make([]*AreaGroupRelationship, 0)
	result := api.RDB.Database
	result = result.Preload("SourceAreaGroup").Preload("RelationshipType")
	result = preloadRelationshipTargets(result)
	result = result.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for area group relationships that meet criteria.
func (api *Api) AreaGroupRelationships(ctx context.Context,
	criteria AreaGroupRelationshipSearchCriteria) (*AreaGroupRelationshipSearchResults, error) {
	results := make([]AreaGroupRelationship, 0)
	db, pag := api.RDB.ListOf(&AreaGroupRelationship{}, func(result *gorm.DB) *gorm.DB {
		if criteria.SourceAreaGroup != nil {
			result = result.Where("source_area_group_id = (?)",
				api.RDB.Database.Model(&AreaGroup{}).Select("id").Where("token = ?", criteria.SourceAreaGroup))
		}
		if criteria.RelationshipType != nil {
			result = result.Where("relationship_type_id = (?)",
				api.RDB.Database.Model(&AreaGroupRelationshipType{}).Select("id").Where("token = ?", criteria.RelationshipType))
		}
		return result
	}, criteria.Pagination)
	db.Preload("SourceAreaGroup").Preload("RelationshipType")
	db = preloadRelationshipTargets(db)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &AreaGroupRelationshipSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

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

// Create a new customer type.
func (api *Api) CreateCustomerType(ctx context.Context, request *CustomerTypeCreateRequest) (*CustomerType, error) {
	created := &CustomerType{
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

// Update an existing customer type.
func (api *Api) UpdateCustomerType(ctx context.Context, token string,
	request *CustomerTypeCreateRequest) (*CustomerType, error) {
	matches, err := api.CustomerTypesByToken(ctx, []string{request.Token})
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

// Get customer types by id.
func (api *Api) CustomerTypesById(ctx context.Context, ids []uint) ([]*CustomerType, error) {
	found := make([]*CustomerType, 0)
	result := api.RDB.Database.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get customer types by token.
func (api *Api) CustomerTypesByToken(ctx context.Context, tokens []string) ([]*CustomerType, error) {
	found := make([]*CustomerType, 0)
	result := api.RDB.Database.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for customer types that meet criteria.
func (api *Api) CustomerTypes(ctx context.Context, criteria CustomerTypeSearchCriteria) (*CustomerTypeSearchResults, error) {
	results := make([]CustomerType, 0)
	db, pag := api.RDB.ListOf(&CustomerType{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &CustomerTypeSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new customer.
func (api *Api) CreateCustomer(ctx context.Context, request *CustomerCreateRequest) (*Customer, error) {
	matches, err := api.CustomerTypesByToken(ctx, []string{request.CustomerTypeToken})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	created := &Customer{
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
		CustomerType: matches[0],
	}
	result := api.RDB.Database.Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing customer.
func (api *Api) UpdateCustomer(ctx context.Context, token string, request *CustomerCreateRequest) (*Customer, error) {
	matches, err := api.CustomersByToken(ctx, []string{request.Token})
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

	// Update customer type if changed.
	if request.CustomerTypeToken != updated.CustomerType.Token {
		ctmatches, err := api.CustomerTypesByToken(ctx, []string{request.CustomerTypeToken})
		if err != nil {
			return nil, err
		}
		if len(ctmatches) == 0 {
			return nil, gorm.ErrRecordNotFound
		}
		updated.CustomerType = ctmatches[0]
	}

	result := api.RDB.Database.Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get customers by id.
func (api *Api) CustomersById(ctx context.Context, ids []uint) ([]*Customer, error) {
	found := make([]*Customer, 0)
	result := api.RDB.Database
	result = result.Preload("CustomerType")
	result = result.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get customers by token.
func (api *Api) CustomersByToken(ctx context.Context, tokens []string) ([]*Customer, error) {
	found := make([]*Customer, 0)
	result := api.RDB.Database
	result = result.Preload("CustomerType")
	result = result.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for customers that meet criteria.
func (api *Api) Customers(ctx context.Context, criteria CustomerSearchCriteria) (*CustomerSearchResults, error) {
	results := make([]Customer, 0)
	db, pag := api.RDB.ListOf(&Customer{}, func(result *gorm.DB) *gorm.DB {
		if criteria.CustomerTypeToken != nil {
			result = result.Where("customer_type_id = (?)",
				api.RDB.Database.Model(&CustomerType{}).Select("id").Where("token = ?", criteria.CustomerTypeToken))
		}
		return result.Preload("CustomerType")
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &CustomerSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new customer relationship type.
func (api *Api) CreateCustomerRelationshipType(ctx context.Context, request *CustomerRelationshipTypeCreateRequest) (*CustomerRelationshipType, error) {
	created := &CustomerRelationshipType{
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

// Update an existing customer relationship type.
func (api *Api) UpdateCustomerRelationshipType(ctx context.Context, token string,
	request *CustomerRelationshipTypeCreateRequest) (*CustomerRelationshipType, error) {
	matches, err := api.CustomerRelationshipTypesByToken(ctx, []string{request.Token})
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

// Get customer relationship types by id.
func (api *Api) CustomerRelationshipTypesById(ctx context.Context, ids []uint) ([]*CustomerRelationshipType, error) {
	found := make([]*CustomerRelationshipType, 0)
	result := api.RDB.Database.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get customer relationship types by token.
func (api *Api) CustomerRelationshipTypesByToken(ctx context.Context, tokens []string) ([]*CustomerRelationshipType, error) {
	found := make([]*CustomerRelationshipType, 0)
	result := api.RDB.Database.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for customer relationship types that meet criteria.
func (api *Api) CustomerRelationshipTypes(ctx context.Context,
	criteria CustomerRelationshipTypeSearchCriteria) (*CustomerRelationshipTypeSearchResults, error) {
	results := make([]CustomerRelationshipType, 0)
	db, pag := api.RDB.ListOf(&CustomerRelationshipType{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &CustomerRelationshipTypeSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create map by token from list of customers.
func customerByTokenMap(vals []*Customer) map[string]*Customer {
	results := make(map[string]*Customer)
	for _, val := range vals {
		results[val.Token] = val
	}
	return results
}

// Create a new customer relationship.
func (api *Api) CreateCustomerRelationship(ctx context.Context, request *CustomerRelationshipCreateRequest) (*CustomerRelationship, error) {
	// Look up source reference.
	cmatches, err := api.CustomersByToken(ctx, []string{request.SourceCustomer})
	if err != nil {
		return nil, err
	}
	if len(cmatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	mmap := customerByTokenMap(cmatches)

	// Look up relationship reference.
	crtmatches, err := api.CustomerRelationshipTypesByToken(ctx, []string{request.RelationshipType})
	if err != nil {
		return nil, err
	}
	if len(crtmatches) == 2 {
		return nil, gorm.ErrRecordNotFound
	}

	created := &CustomerRelationship{
		EntityRelationship: EntityRelationship{
			TokenReference: rdb.TokenReference{
				Token: request.Token,
			},
			MetadataEntity: rdb.MetadataEntity{
				Metadata: rdb.MetadataStrOf(request.Metadata),
			},
		},
		SourceCustomer:   *mmap[request.SourceCustomer],
		RelationshipType: *crtmatches[0],
	}
	api.resolveRelationshipTargets(ctx, request.Targets, &created.EntityRelationship)
	result := api.RDB.Database.Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Get customer relationships by id.
func (api *Api) CustomerRelationshipsById(ctx context.Context, ids []uint) ([]*CustomerRelationship, error) {
	found := make([]*CustomerRelationship, 0)
	result := api.RDB.Database
	result = result.Preload("SourceCustomer").Preload("RelationshipType")
	result = preloadRelationshipTargets(result)
	result = result.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get customer relationships by token.
func (api *Api) CustomerRelationshipsByToken(ctx context.Context, tokens []string) ([]*CustomerRelationship, error) {
	found := make([]*CustomerRelationship, 0)
	result := api.RDB.Database
	result = result.Preload("SourceCustomer").Preload("RelationshipType")
	result = preloadRelationshipTargets(result)
	result = result.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for customer relationships that meet criteria.
func (api *Api) CustomerRelationships(ctx context.Context,
	criteria CustomerRelationshipSearchCriteria) (*CustomerRelationshipSearchResults, error) {
	results := make([]CustomerRelationship, 0)
	db, pag := api.RDB.ListOf(&CustomerRelationship{}, func(result *gorm.DB) *gorm.DB {
		if criteria.SourceCustomer != nil {
			result = result.Where("source_customer_id = (?)",
				api.RDB.Database.Model(&Customer{}).Select("id").Where("token = ?", criteria.SourceCustomer))
		}
		if criteria.RelationshipType != nil {
			result = result.Where("relationship_type_id = (?)",
				api.RDB.Database.Model(&CustomerRelationshipType{}).Select("id").Where("token = ?", criteria.RelationshipType))
		}
		return result
	}, criteria.Pagination)
	db.Preload("SourceCustomer").Preload("RelationshipType")
	db = preloadRelationshipTargets(db)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &CustomerRelationshipSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new customer group.
func (api *Api) CreateCustomerGroup(ctx context.Context, request *CustomerGroupCreateRequest) (*CustomerGroup, error) {
	created := &CustomerGroup{
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

// Update an existing customer type.
func (api *Api) UpdateCustomerGroup(ctx context.Context, token string,
	request *CustomerGroupCreateRequest) (*CustomerGroup, error) {
	matches, err := api.CustomerGroupsByToken(ctx, []string{request.Token})
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

// Get customer groups by id.
func (api *Api) CustomerGroupsById(ctx context.Context, ids []uint) ([]*CustomerGroup, error) {
	found := make([]*CustomerGroup, 0)
	result := api.RDB.Database.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get customer groups by token.
func (api *Api) CustomerGroupsByToken(ctx context.Context, tokens []string) ([]*CustomerGroup, error) {
	found := make([]*CustomerGroup, 0)
	result := api.RDB.Database.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for customer groups that meet criteria.
func (api *Api) CustomerGroups(ctx context.Context, criteria CustomerGroupSearchCriteria) (*CustomerGroupSearchResults, error) {
	results := make([]CustomerGroup, 0)
	db, pag := api.RDB.ListOf(&CustomerGroup{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &CustomerGroupSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new customer group relationship type.
func (api *Api) CreateCustomerGroupRelationshipType(ctx context.Context,
	request *CustomerGroupRelationshipTypeCreateRequest) (*CustomerGroupRelationshipType, error) {
	created := &CustomerGroupRelationshipType{
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

// Update an existing customer group relationship type.
func (api *Api) UpdateCustomerGroupRelationshipType(ctx context.Context, token string,
	request *CustomerGroupRelationshipTypeCreateRequest) (*CustomerGroupRelationshipType, error) {
	matches, err := api.CustomerGroupRelationshipTypesByToken(ctx, []string{request.Token})
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

// Get customer group relationship types by id.
func (api *Api) CustomerGroupRelationshipTypesById(ctx context.Context, ids []uint) ([]*CustomerGroupRelationshipType, error) {
	found := make([]*CustomerGroupRelationshipType, 0)
	result := api.RDB.Database.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get customer group relationship types by token.
func (api *Api) CustomerGroupRelationshipTypesByToken(ctx context.Context, tokens []string) ([]*CustomerGroupRelationshipType, error) {
	found := make([]*CustomerGroupRelationshipType, 0)
	result := api.RDB.Database.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for customer group relationship types that meet criteria.
func (api *Api) CustomerGroupRelationshipTypes(ctx context.Context,
	criteria CustomerGroupRelationshipTypeSearchCriteria) (*CustomerGroupRelationshipTypeSearchResults, error) {
	results := make([]CustomerGroupRelationshipType, 0)
	db, pag := api.RDB.ListOf(&CustomerGroupRelationshipType{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &CustomerGroupRelationshipTypeSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new customer group relationship.
func (api *Api) CreateCustomerGroupRelationship(ctx context.Context,
	request *CustomerGroupRelationshipCreateRequest) (*CustomerGroupRelationship, error) {

	// Look up source reference.
	cgmatches, err := api.CustomerGroupsByToken(ctx, []string{request.SourceCustomerGroup})
	if err != nil {
		return nil, err
	}
	if len(cgmatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	// Look up relationship reference.
	cgrtmatches, err := api.CustomerGroupRelationshipTypesByToken(ctx, []string{request.RelationshipType})
	if err != nil {
		return nil, err
	}
	if len(cgrtmatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	created := &CustomerGroupRelationship{
		EntityRelationship: EntityRelationship{
			TokenReference: rdb.TokenReference{
				Token: request.Token,
			},
			MetadataEntity: rdb.MetadataEntity{
				Metadata: rdb.MetadataStrOf(request.Metadata),
			},
		},
		SourceCustomerGroup: *cgmatches[0],
		RelationshipType:    *cgrtmatches[0],
	}
	result := api.RDB.Database.Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Get customer group relationships by id.
func (api *Api) CustomerGroupRelationshipsById(ctx context.Context, ids []uint) ([]*CustomerGroupRelationship, error) {
	found := make([]*CustomerGroupRelationship, 0)
	result := api.RDB.Database
	result = result.Preload("SourceCustomerGroup").Preload("RelationshipType")
	result = preloadRelationshipTargets(result)
	result = result.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get customer group relationships by token.
func (api *Api) CustomerGroupRelationshipsByToken(ctx context.Context, tokens []string) ([]*CustomerGroupRelationship, error) {
	found := make([]*CustomerGroupRelationship, 0)
	result := api.RDB.Database
	result = result.Preload("SourceCustomerGroup").Preload("RelationshipType")
	result = preloadRelationshipTargets(result)
	result = result.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for customer group relationships that meet criteria.
func (api *Api) CustomerGroupRelationships(ctx context.Context,
	criteria CustomerGroupRelationshipSearchCriteria) (*CustomerGroupRelationshipSearchResults, error) {
	results := make([]CustomerGroupRelationship, 0)
	db, pag := api.RDB.ListOf(&CustomerGroupRelationship{}, func(result *gorm.DB) *gorm.DB {
		if criteria.SourceCustomerGroup != nil {
			result = result.Where("source_customer_group_id = (?)",
				api.RDB.Database.Model(&CustomerGroup{}).Select("id").Where("token = ?", criteria.SourceCustomerGroup))
		}
		if criteria.RelationshipType != nil {
			result = result.Where("relationship_type_id = (?)",
				api.RDB.Database.Model(&CustomerGroupRelationshipType{}).Select("id").Where("token = ?", criteria.RelationshipType))
		}
		return result
	}, criteria.Pagination)
	db.Preload("SourceCustomerGroup").Preload("RelationshipType")
	db = preloadRelationshipTargets(db)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &CustomerGroupRelationshipSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

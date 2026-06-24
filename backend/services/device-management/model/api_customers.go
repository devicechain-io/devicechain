// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

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
	result := api.RDB.DB(ctx).Create(created)
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

	result := api.RDB.DB(ctx).Save(found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get customer types by id.
func (api *Api) CustomerTypesById(ctx context.Context, ids []uint) ([]*CustomerType, error) {
	found := make([]*CustomerType, 0)
	result := api.RDB.DB(ctx).Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get customer types by token.
func (api *Api) CustomerTypesByToken(ctx context.Context, tokens []string) ([]*CustomerType, error) {
	found := make([]*CustomerType, 0)
	result := api.RDB.DB(ctx).Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for customer types that meet criteria.
func (api *Api) CustomerTypes(ctx context.Context, criteria CustomerTypeSearchCriteria) (*CustomerTypeSearchResults, error) {
	results := make([]CustomerType, 0)
	db, pag := api.RDB.ListOf(ctx, &CustomerType{}, nil, criteria.Pagination)
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
	result := api.RDB.DB(ctx).Create(created)
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

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get customers by id.
func (api *Api) CustomersById(ctx context.Context, ids []uint) ([]*Customer, error) {
	found := make([]*Customer, 0)
	result := api.RDB.DB(ctx)
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
	result := api.RDB.DB(ctx)
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
	db, pag := api.RDB.ListOf(ctx, &Customer{}, func(result *gorm.DB) *gorm.DB {
		if criteria.CustomerTypeToken != nil {
			result = result.Where("customer_type_id = (?)",
				api.RDB.DB(ctx).Model(&CustomerType{}).Select("id").Where("token = ?", criteria.CustomerTypeToken))
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
	result := api.RDB.DB(ctx).Create(created)
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

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get customer groups by id.
func (api *Api) CustomerGroupsById(ctx context.Context, ids []uint) ([]*CustomerGroup, error) {
	found := make([]*CustomerGroup, 0)
	result := api.RDB.DB(ctx).Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get customer groups by token.
func (api *Api) CustomerGroupsByToken(ctx context.Context, tokens []string) ([]*CustomerGroup, error) {
	found := make([]*CustomerGroup, 0)
	result := api.RDB.DB(ctx).Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for customer groups that meet criteria.
func (api *Api) CustomerGroups(ctx context.Context, criteria CustomerGroupSearchCriteria) (*CustomerGroupSearchResults, error) {
	results := make([]CustomerGroup, 0)
	db, pag := api.RDB.ListOf(ctx, &CustomerGroup{}, nil, criteria.Pagination)
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

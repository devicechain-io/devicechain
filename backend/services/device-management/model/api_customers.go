// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Create a new customer type.
func (api *Api) CreateCustomerType(ctx context.Context, request *CustomerTypeCreateRequest) (*CustomerType, error) {
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
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
			Metadata: metadataJSON,
		},
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing customer type, applying only the fields the caller actually
// sent. The entity is looked up by the `token` ARGUMENT — the request payload no
// longer carries one, which closes two defects at once: an update can no longer
// move a customer type's token, and the mandatory `token` argument is no longer dead.
// It used to be ignored entirely in favour of request.Token, so a caller naming one
// type in the argument and another in the payload silently updated the second and
// got a 200 back for it.
//
// Each assignment folds the field's three states onto the stored value: absent
// keeps it, null clears it, a value sets it. Reading `found.X` as the "current"
// argument is what makes an omitted field a no-op, so these must stay assignments
// FROM the loaded record rather than from the request alone.
func (api *Api) UpdateCustomerType(ctx context.Context, token string,
	request *CustomerTypeUpdateRequest) (*CustomerType, error) {
	matches, err := api.CustomerTypesByToken(ctx, []string{token})
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

// Get customer types by id.
func (api *Api) CustomerTypesById(ctx context.Context, ids []uint) ([]*CustomerType, error) {
	return rdb.FindByIds[CustomerType](api.RDB.DB(ctx), ids)
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

	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
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
			Metadata: metadataJSON,
		},
		CustomerType: matches[0],
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing customer, applying only the fields the caller actually sent.
// Looked up by the `token` ARGUMENT; the payload no longer carries a token, so the
// argument is no longer dead and a token move is unrepresentable rather than merely
// refused.
func (api *Api) UpdateCustomer(ctx context.Context, token string, request *CustomerUpdateRequest) (*Customer, error) {
	matches, err := api.CustomersByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	updated := matches[0]

	// The type hop resolves BEFORE anything is written, so an unknown customer type
	// refuses the WHOLE update rather than applying the fields it liked first. The
	// nil guard is not decoration: the preload comes back nil for a dangling FK, and
	// the comparison this replaces dereferenced it unconditionally.
	currentTypeToken := ""
	if updated.CustomerType != nil {
		currentTypeToken = updated.CustomerType.Token
	}
	retypeTo, retype, err := resolveRequiredTypeRef(request.CustomerTypeToken, currentTypeToken, "customerTypeToken")
	if err != nil {
		return nil, err
	}
	if retype {
		types, err := api.CustomerTypesByToken(ctx, []string{retypeTo})
		if err != nil {
			return nil, err
		}
		if len(types) == 0 {
			return nil, gorm.ErrRecordNotFound
		}
		updated.CustomerType = types[0]
		updated.CustomerTypeId = types[0].ID // keep the FK in lockstep with the association
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

// Get customers by id.
func (api *Api) CustomersById(ctx context.Context, ids []uint) ([]*Customer, error) {
	return rdb.FindByIds[Customer](api.RDB.DB(ctx).Preload("CustomerType"), ids)
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

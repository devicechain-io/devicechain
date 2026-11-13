// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/conflict"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Create a new customer type.
func (api *Api) CreateCustomerType(ctx context.Context, request *CustomerTypeCreateRequest) (*CustomerType, error) {
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	semanticTypes, err := api.semanticTypesOnCreate(ctx, request.SemanticTypes)
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
		SemanticTypes: semanticTypes,
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
// Only named columns are written; omitted fields remain untouched even when
// another writer changes them after the read. Null clears a nullable field.
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
	assignments := map[string]any{}
	if request.Name.Set {
		assignments["name"] = request.Name.ApplyToNullString(found.Name)
	}
	if request.Description.Set {
		assignments["description"] = request.Description.ApplyToNullString(found.Description)
	}
	if request.ImageUrl.Set {
		assignments["image_url"] = request.ImageUrl.ApplyToNullString(found.ImageUrl)
	}
	if request.Icon.Set {
		assignments["icon"] = request.Icon.ApplyToNullString(found.Icon)
	}
	if request.BackgroundColor.Set {
		assignments["background_color"] = request.BackgroundColor.ApplyToNullString(found.BackgroundColor)
	}
	if request.ForegroundColor.Set {
		assignments["foreground_color"] = request.ForegroundColor.ApplyToNullString(found.ForegroundColor)
	}
	if request.BorderColor.Set {
		assignments["border_color"] = request.BorderColor.ApplyToNullString(found.BorderColor)
	}
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata.ApplyTo(dcgraphql.MetadataStr(found.Metadata)))
	if err != nil {
		return nil, err
	}
	if request.Metadata.Set {
		assignments["metadata"] = metadataJSON
	}
	semanticTypes, err := api.semanticTypesOnUpdate(ctx, request.SemanticTypes, found.SemanticTypes)
	if err != nil {
		return nil, err
	}

	if request.SemanticTypes.Set {
		assignments["semantic_types"] = semanticTypes
	}

	if len(assignments) == 0 {
		return found, nil
	}
	if err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		res := rdb.AdvancingFrom(tx, found.UpdatedAt).Model(found).Omit(clause.Associations).
			Where("id = ?", found.ID).Updates(assignments)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	}); err != nil {
		return nil, err
	}
	matches, err = api.CustomerTypesById(ctx, []uint{found.ID})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return matches[0], nil
}

// Get customer types by id.
func (api *Api) CustomerTypesById(ctx context.Context, ids []uint) ([]*CustomerType, error) {
	return rdb.FindByIds[CustomerType](api.RDB.DB(ctx), ids)
}

// Get customer types by token.
func (api *Api) CustomerTypesByToken(ctx context.Context, tokens []string) ([]*CustomerType, error) {
	found := make([]*CustomerType, 0)
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &found, "token", tokens); err != nil {
		return nil, err
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
	// The token is issued and the customer written in ONE transaction, so a token is in
	// the ledger exactly when a customer was created at it. The ledger's primary key is
	// what refuses a second issue, race-free, whether the first customer is live or gone.
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&IssuedCustomerToken{Token: request.Token}).Error; err != nil {
			return err
		}
		return tx.Create(created).Error
	})
	if err != nil {
		// A taken token is one of two things, and only one of them is an already-exists.
		// A LIVE customer at it is, and keeps the CONFLICT the boundary gives a unique
		// violation, which is what makes a re-create idempotent. A token whose customer
		// is gone is retired, and must never read as an already-exists.
		if conflict.Is(err) {
			if rerr := api.refuseRetiredCustomerToken(ctx, request.Token); rerr != nil {
				return nil, rerr
			}
		}
		return nil, err
	}
	return created, nil
}

// ErrCustomerTokenRetired refuses a create at the token of a customer that has been
// deleted in the same tenant. The refusal is permanent: a deleted customer's token is
// never handed to another customer.
//
// 🔑 WHY A DELETED CUSTOMER STILL HOLDS ITS TOKEN. Customer-scoped records outlive the
// customer and name it only by token, as the anchor "customer:<token>": command-delivery's
// commands.scope_anchors, update-management's update_assignments.scope_anchors, this area's
// alarms.scope_anchors, dashboard-management's dashboard_shares, and event-management's
// event anchors. None of them is erased when the customer is deleted, so a new customer at
// the same token would inherit all of it — every member scoped to the new customer would
// see the old one's history as its own. Retiring the token is what keeps those stamps
// meaning the customer that wrote them.
//
// The database enforces it (IssuedCustomerToken's primary key); this sentinel is how the
// caller is told why.
//
// Only a tenant purge frees a retired token, because only the purge removes the ledger
// row — along with every stamp that named the customer.
//
// 🔴 IT MUST NEVER BE A conflict.Error, for the reason user-management's
// ErrTenantTokenReserved gives: CONFLICT is the code a client treats as "it already exists,
// carry on", and the simulator's provisioning (and any client written like it) would then
// carry on against a customer that is gone. It is INVALID_VALUE: the caller's next move is
// about the request itself — pick another token. Its wording stays clear of "already
// exists", "duplicate" and "unique", which an older dcctl matched on.
//
// A deleted group's token is retired by the same rule, in its own ledger
// (ErrGroupTokenRetired): "group:<token>" anchors outlive their group just as customer ones do.
var ErrCustomerTokenRetired = integrity.NewRefusal(integrity.ClassInvalid,
	"that customer token is retired: a deleted customer held it, and records stamped with it "+
		"(commands, alarms, update assignments, dashboard shares and event history) still name "+
		"that customer, so it cannot be given to another one. Pick another token")

// refuseRetiredCustomerToken reads a unique violation on a customer create. Only two
// constraints can raise one — the ledger's primary key and the live customers' token
// index — so when no live customer holds token, the ledger refused it, and the token is
// retired: it answers ErrCustomerTokenRetired. When a live customer holds it, that is an
// already-exists, and it answers nil so the insert's own CONFLICT stands.
func (api *Api) refuseRetiredCustomerToken(ctx context.Context, token string) error {
	var live int64
	if err := api.RDB.DB(ctx).Model(&Customer{}).
		Where("token = ?", token).Count(&live).Error; err != nil {
		return err
	}
	if live == 0 {
		return fmt.Errorf("create customer %q: %w", token, ErrCustomerTokenRetired)
	}
	return nil
}

// retireCustomerToken records token as issued, inside a customer delete's transaction. A
// customer created through CreateCustomer is already recorded, so this is a no-op for it;
// it keeps the token retired for a customer that reached the table any other way (one
// created before the ledger existed is covered by the migration's backfill, and this
// covers it again).
func retireCustomerToken(tx *gorm.DB, token string) error {
	return tx.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&IssuedCustomerToken{Token: token}).Error
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
	assignments := make(map[string]any)

	// Resolve every explicitly named type before writing anything, including a
	// request naming the type read above. Omitted references are left alone.
	retypeTo, retype, err := resolveRequiredTypeRef(request.CustomerTypeToken, "", "customerTypeToken")
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
		assignments["customer_type_id"] = types[0].ID
	}

	if request.Name.Set {
		assignments["name"] = request.Name.ApplyToNullString(updated.Name)
	}
	if request.Description.Set {
		assignments["description"] = request.Description.ApplyToNullString(updated.Description)
	}
	if request.Metadata.Set {
		metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata.Value)
		if err != nil {
			return nil, err
		}
		assignments["metadata"] = metadataJSON
	}
	if len(assignments) == 0 {
		return updated, nil
	}

	// Only named columns are written: values read above for omitted fields may
	// already be stale. The preloaded type must not be saved as an association.
	result := api.RDB.DB(ctx).Model(updated).Omit(clause.Associations).Updates(assignments)
	if result.Error != nil {
		return nil, result.Error
	}
	reloaded, err := api.CustomersById(ctx, []uint{updated.ID})
	if err != nil {
		return nil, err
	}
	if len(reloaded) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return reloaded[0], nil
}

// Get customers by id.
func (api *Api) CustomersById(ctx context.Context, ids []uint) ([]*Customer, error) {
	return rdb.FindByIds[Customer](api.RDB.DB(ctx).Preload("CustomerType"), ids)
}

// Get customers by token.
func (api *Api) CustomersByToken(ctx context.Context, tokens []string) ([]*Customer, error) {
	found := make([]*Customer, 0)
	if err := rdb.FindByKeys(api.RDB.DB(ctx).Preload("CustomerType"), &found, "token", tokens); err != nil {
		return nil, err
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

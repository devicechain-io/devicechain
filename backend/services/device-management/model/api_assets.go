// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Create a new asset type.
func (api *Api) CreateAssetType(ctx context.Context, request *AssetTypeCreateRequest) (*AssetType, error) {
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	// The draft property contract is checked before the row exists, so a malformed
	// schema never reaches storage. ActiveVersion is left null: a draft binds nothing
	// until PublishAssetType freezes it.
	propertySchema, err := validateAssetPropertySchemaInput(request.PropertySchema)
	if err != nil {
		return nil, err
	}
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
			Metadata: metadataJSON,
		},
		PropertySchema: propertySchema,
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing asset type, applying only the fields the caller actually
// sent. The entity is looked up by the `token` ARGUMENT — the request payload no
// longer carries one, which closes two defects at once: an update can no longer
// move an asset type's token, and the mandatory `token` argument is no longer dead.
// It used to be ignored entirely in favour of request.Token, so a caller naming one
// type in the argument and another in the payload silently updated the second and
// got a 200 back for it.
//
// Each assignment folds the field's three states onto the stored value: absent
// keeps it, null clears it, a value sets it. Reading `found.X` as the "current"
// argument is what makes an omitted field a no-op, so these must stay assignments
// FROM the loaded record rather than from the request alone.
func (api *Api) UpdateAssetType(ctx context.Context, token string,
	request *AssetTypeUpdateRequest) (*AssetType, error) {
	matches, err := api.AssetTypesByToken(ctx, []string{token})
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

	// MetadataStr is the generic *datatypes.JSON -> *string projection; it is named for
	// its first caller, not for metadata specifically.
	schemaInput := request.PropertySchema.ApplyTo(dcgraphql.MetadataStr(found.PropertySchema))
	propertySchema, err := validateAssetPropertySchemaInput(schemaInput)
	if err != nil {
		return nil, err
	}
	found.PropertySchema = propertySchema

	// Omit active_version: a draft edit must never write the version pointer back.
	// `found` was loaded before this Save, so writing it whole would let an edit racing
	// a concurrent PublishAssetType/RollbackAssetType silently revert the active
	// pointer — the version every asset of this type is validated against — to its
	// stale value. This is the third instance of the identical latent bug, after
	// DeviceProfile and EntityGroup; the pointer is moved by publish/rollback only.
	result := api.RDB.DB(ctx).Omit("ActiveVersion").Save(found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get asset types by id.
func (api *Api) AssetTypesById(ctx context.Context, ids []uint) ([]*AssetType, error) {
	return rdb.FindByIds[AssetType](api.RDB.DB(ctx), ids)
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

	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	propertiesJSON, err := rdb.JSONInputOf("properties", request.Properties)
	if err != nil {
		return nil, err
	}
	// The property document is checked against the type's ACTIVE PUBLISHED contract
	// before anything is written, so a create either stores a conformant asset or
	// stores nothing. A type declaring a required property therefore cannot have an
	// asset created under it without that property.
	if err := api.validateAssetProperties(ctx, matches[0], propertiesJSON); err != nil {
		return nil, err
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
			Metadata: metadataJSON,
		},
		AssetType:  matches[0],
		Properties: propertiesJSON,
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing asset, applying only the fields the caller actually sent.
// Looked up by the `token` ARGUMENT; the payload no longer carries a token, so the
// argument is no longer dead and a token move is unrepresentable rather than merely
// refused.
func (api *Api) UpdateAsset(ctx context.Context, token string, request *AssetUpdateRequest) (*Asset, error) {
	matches, err := api.AssetsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	updated := matches[0]

	// The type hop resolves BEFORE anything is written, so an unknown asset type
	// refuses the WHOLE update rather than applying the fields it liked first. The
	// nil guard is not decoration: the preload comes back nil for a dangling FK, and
	// the comparison this replaces dereferenced it unconditionally.
	currentTypeToken := ""
	if updated.AssetType != nil {
		currentTypeToken = updated.AssetType.Token
	}
	retypeTo, retype, err := resolveRequiredTypeRef(request.AssetTypeToken, currentTypeToken, "assetTypeToken")
	if err != nil {
		return nil, err
	}
	if retype {
		types, err := api.AssetTypesByToken(ctx, []string{retypeTo})
		if err != nil {
			return nil, err
		}
		if len(types) == 0 {
			return nil, gorm.ErrRecordNotFound
		}
		updated.AssetType = types[0]
		// Belt-and-braces: gorm's Save syncs a belongs-to FK from the association it is
		// given, so this is not load-bearing the way the device version is (that one is
		// READ back by the post-commit roster resolve). It is set anyway so the in-memory
		// value the caller is handed back agrees with the row.
		updated.AssetTypeId = types[0].ID
	}

	updated.Name = rdb.NullStrOf(request.Name.ApplyTo(dcgraphql.NullStr(updated.Name)))
	updated.Description = rdb.NullStrOf(request.Description.ApplyTo(dcgraphql.NullStr(updated.Description)))
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata.ApplyTo(dcgraphql.MetadataStr(updated.Metadata)))
	if err != nil {
		return nil, err
	}
	updated.Metadata = metadataJSON

	propertiesJSON, err := rdb.JSONInputOf("properties",
		request.Properties.ApplyTo(dcgraphql.MetadataStr(updated.Properties)))
	if err != nil {
		return nil, err
	}
	// Validate the RESULTING pair, not the incoming field. Either half can move: a
	// retype re-points the contract while leaving the document alone, and a document
	// edit fills a contract that did not move. Checking only what the caller mentioned
	// would let a retype strand an asset carrying properties its new type never
	// declared — conformant when it was written, silently not afterwards.
	if err := api.validateAssetProperties(ctx, updated.AssetType, propertiesJSON); err != nil {
		return nil, err
	}
	updated.Properties = propertiesJSON

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get assets by id.
func (api *Api) AssetsById(ctx context.Context, ids []uint) ([]*Asset, error) {
	return rdb.FindByIds[Asset](api.RDB.DB(ctx).Preload("AssetType"), ids)
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

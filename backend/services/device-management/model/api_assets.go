// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	semanticTypes, err := api.semanticTypesOnCreate(ctx, request.SemanticTypes)
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
		SemanticTypes:  semanticTypes,
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
// Only named columns are written; omitted fields remain untouched even when
// another writer changes them after the read. Null clears a nullable field.
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

	// MetadataStr is the generic *datatypes.JSON -> *string projection; it is named for
	// its first caller, not for metadata specifically.
	schemaInput := request.PropertySchema.ApplyTo(dcgraphql.MetadataStr(found.PropertySchema))
	propertySchema, err := validateAssetPropertySchemaInput(schemaInput)
	if err != nil {
		return nil, err
	}
	if request.PropertySchema.Set {
		assignments["property_schema"] = propertySchema
	}

	// Publish/rollback own active_version; draft edits write only named columns.
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
	matches, err = api.AssetTypesById(ctx, []uint{found.ID})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return matches[0], nil
}

// Get asset types by id.
func (api *Api) AssetTypesById(ctx context.Context, ids []uint) ([]*AssetType, error) {
	return rdb.FindByIds[AssetType](api.RDB.DB(ctx), ids)
}

// Get asset types by token.
func (api *Api) AssetTypesByToken(ctx context.Context, tokens []string) ([]*AssetType, error) {
	found := make([]*AssetType, 0)
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &found, "token", tokens); err != nil {
		return nil, err
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
	// The literal `null` means the same thing as no document; store it that way so
	// the column has one representation of "carries none".
	propertiesJSON = jsonNullToNil(propertiesJSON)
	// The property document is checked against the type's ACTIVE PUBLISHED contract
	// before anything is written, so a create either stores a conformant asset or
	// stores nothing. A type declaring a required property therefore cannot have an
	// asset created under it without that property.
	if err := api.validateAssetProperties(api.RDB.DB(ctx), matches[0], propertiesJSON); err != nil {
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
	assignments := make(map[string]any)
	retypeTo, retype, err := resolveRequiredTypeRef(request.AssetTypeToken, "", "assetTypeToken")
	if err != nil {
		return nil, err
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
	propertiesJSON := updated.Properties
	if request.Properties.Set {
		propertiesJSON, err = rdb.JSONInputOf("properties", request.Properties.Value)
		if err != nil {
			return nil, err
		}
		propertiesJSON = jsonNullToNil(propertiesJSON)
		assignments["properties"] = propertiesJSON
	}
	if len(assignments) == 0 && !retype {
		return updated, nil
	}

	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if request.Properties.Set || retype {
			// Lock and reread the effective pair: either omitted half may have
			// changed since the initial lookup. Never assign that retained half.
			var current Asset
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&current, updated.ID).Error; err != nil {
				return err
			}
			if !request.Properties.Set {
				propertiesJSON = current.Properties
			}
			typeQuery := tx.Clauses(clause.Locking{Strength: "SHARE"})
			if retype {
				typeQuery = typeQuery.Where("token = ?", retypeTo)
			} else {
				typeQuery = typeQuery.Where("id = ?", current.AssetTypeId)
			}
			// An omitted, dangling type retains the existing absent-document
			// exemption. An explicitly named unknown type always refuses.
			types := make([]*AssetType, 0)
			if err := typeQuery.Find(&types).Error; err != nil {
				return err
			}
			var effectiveType *AssetType
			if len(types) != 0 {
				effectiveType = types[0]
			} else if retype {
				return gorm.ErrRecordNotFound
			}
			if retype {
				assignments["asset_type_id"] = effectiveType.ID
			}
			if err := api.validateAssetProperties(tx, effectiveType, propertiesJSON); err != nil {
				return err
			}
		}
		// Unrelated edits deliberately skip property validation: rollback can
		// leave historical documents nonconforming, without making them uneditable.
		result := tx.Model(updated).Omit(clause.Associations).Updates(assignments)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	reloaded, err := api.AssetsById(ctx, []uint{updated.ID})
	if err != nil {
		return nil, err
	}
	if len(reloaded) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return reloaded[0], nil
}

// Get assets by id.
func (api *Api) AssetsById(ctx context.Context, ids []uint) ([]*Asset, error) {
	return rdb.FindByIds[Asset](api.RDB.DB(ctx).Preload("AssetType"), ids)
}

// Get assets by token.
func (api *Api) AssetsByToken(ctx context.Context, tokens []string) ([]*Asset, error) {
	found := make([]*Asset, 0)
	if err := rdb.FindByKeys(api.RDB.DB(ctx).Preload("AssetType"), &found, "token", tokens); err != nil {
		return nil, err
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

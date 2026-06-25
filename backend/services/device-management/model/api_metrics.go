// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Create a new metric definition.
func (api *Api) CreateMetricDefinition(ctx context.Context,
	request *MetricDefinitionCreateRequest) (*MetricDefinition, error) {
	matches, err := api.DeviceTypesByToken(ctx, []string{request.DeviceTypeToken})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	// Validate the metric data-type vocabulary.
	if !MetricDataType(request.DataType).Valid() {
		return nil, fmt.Errorf("invalid metric data type: %s", request.DataType)
	}

	created := &MetricDefinition{
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
		DeviceType: matches[0],
		MetricKey:  request.MetricKey,
		DataType:   request.DataType,
		Unit:       rdb.NullStrOf(request.Unit),
		MinValue:   rdb.NullFloat64Of(request.MinValue),
		MaxValue:   rdb.NullFloat64Of(request.MaxValue),
		Enum:       rdb.MetadataStrOf(request.Enum),
		Descriptor: rdb.NullStrOf(request.Descriptor),
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing metric definition.
func (api *Api) UpdateMetricDefinition(ctx context.Context, token string,
	request *MetricDefinitionCreateRequest) (*MetricDefinition, error) {
	matches, err := api.MetricDefinitionsByToken(ctx, []string{request.Token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	// Validate the metric data-type vocabulary.
	if !MetricDataType(request.DataType).Valid() {
		return nil, fmt.Errorf("invalid metric data type: %s", request.DataType)
	}

	// Update fields that changed.
	updated := matches[0]
	updated.Token = request.Token
	updated.Name = rdb.NullStrOf(request.Name)
	updated.Description = rdb.NullStrOf(request.Description)
	updated.Metadata = rdb.MetadataStrOf(request.Metadata)
	updated.MetricKey = request.MetricKey
	updated.DataType = request.DataType
	updated.Unit = rdb.NullStrOf(request.Unit)
	updated.MinValue = rdb.NullFloat64Of(request.MinValue)
	updated.MaxValue = rdb.NullFloat64Of(request.MaxValue)
	updated.Enum = rdb.MetadataStrOf(request.Enum)
	updated.Descriptor = rdb.NullStrOf(request.Descriptor)

	// Update device type if changed.
	if updated.DeviceType == nil || request.DeviceTypeToken != updated.DeviceType.Token {
		matches, err := api.DeviceTypesByToken(ctx, []string{request.DeviceTypeToken})
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, gorm.ErrRecordNotFound
		}
		updated.DeviceType = matches[0]
	}

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get metric definitions by id.
func (api *Api) MetricDefinitionsById(ctx context.Context, ids []uint) ([]*MetricDefinition, error) {
	found := make([]*MetricDefinition, 0)
	result := api.RDB.DB(ctx)
	result = result.Preload("DeviceType")
	result = result.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get metric definitions by token.
func (api *Api) MetricDefinitionsByToken(ctx context.Context, tokens []string) ([]*MetricDefinition, error) {
	found := make([]*MetricDefinition, 0)
	result := api.RDB.DB(ctx)
	result = result.Preload("DeviceType")
	result = result.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for metric definitions that meet criteria.
func (api *Api) MetricDefinitions(ctx context.Context,
	criteria MetricDefinitionSearchCriteria) (*MetricDefinitionSearchResults, error) {
	results := make([]MetricDefinition, 0)
	db, pag := api.RDB.ListOf(ctx, &MetricDefinition{}, func(result *gorm.DB) *gorm.DB {
		if criteria.DeviceType != nil {
			result = result.Where("device_type_id = (?)",
				api.RDB.DB(ctx).Model(&DeviceType{}).Select("id").Where("token = ?", criteria.DeviceType))
		}
		if criteria.MetricKey != nil {
			result = result.Where("metric_key = ?", *criteria.MetricKey)
		}
		return result.Preload("DeviceType")
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &MetricDefinitionSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// MetricDefinitionsByDeviceType loads all metric definitions declared on a device
// profile without pagination. This is the direct loader the ingest validation path
// uses to type/normalize measurements against the profile (ADR-016).
func (api *Api) MetricDefinitionsByDeviceType(ctx context.Context, deviceTypeId uint) ([]*MetricDefinition, error) {
	found := make([]*MetricDefinition, 0)
	result := api.RDB.DB(ctx).Where("device_type_id = ?", deviceTypeId).Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

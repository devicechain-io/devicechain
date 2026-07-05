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
	matches, err := api.DeviceProfilesByToken(ctx, []string{request.DeviceProfileToken})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	// Validate the metric data-type vocabulary and storability (ADR-016 amd): a
	// metric must be a numeric, aggregatable time-series type; STRING telemetry is
	// device state, not a metric.
	if !MetricDataType(request.DataType).Valid() {
		return nil, fmt.Errorf("invalid metric data type: %s", request.DataType)
	}
	if !MetricDataType(request.DataType).StorableAsMetric() {
		return nil, fmt.Errorf("metric data type %s is not storable as a time-series measurement; "+
			"model string-valued telemetry as device state (ADR-016 amd)", request.DataType)
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
		DeviceProfile: matches[0],
		MetricKey:     request.MetricKey,
		DataType:      request.DataType,
		Unit:          rdb.NullStrOf(request.Unit),
		MinValue:      rdb.NullFloat64Of(request.MinValue),
		MaxValue:      rdb.NullFloat64Of(request.MaxValue),
		Enum:          rdb.MetadataStrOf(request.Enum),
		Descriptor:    rdb.NullStrOf(request.Descriptor),
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

	// Validate the metric data-type vocabulary and storability (ADR-016 amd): a
	// metric must be a numeric, aggregatable time-series type; STRING telemetry is
	// device state, not a metric.
	if !MetricDataType(request.DataType).Valid() {
		return nil, fmt.Errorf("invalid metric data type: %s", request.DataType)
	}
	if !MetricDataType(request.DataType).StorableAsMetric() {
		return nil, fmt.Errorf("metric data type %s is not storable as a time-series measurement; "+
			"model string-valued telemetry as device state (ADR-016 amd)", request.DataType)
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

	// Update device profile if changed.
	if updated.DeviceProfile == nil || request.DeviceProfileToken != updated.DeviceProfile.Token {
		matches, err := api.DeviceProfilesByToken(ctx, []string{request.DeviceProfileToken})
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, gorm.ErrRecordNotFound
		}
		updated.DeviceProfile = matches[0]
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
	result = result.Preload("DeviceProfile")
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
	result = result.Preload("DeviceProfile")
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
		if criteria.DeviceProfile != nil {
			result = result.Where("device_profile_id = (?)",
				api.RDB.DB(ctx).Model(&DeviceProfile{}).Select("id").Where("token = ?", criteria.DeviceProfile))
		}
		if criteria.MetricKey != nil {
			result = result.Where("metric_key = ?", *criteria.MetricKey)
		}
		return result.Preload("DeviceProfile")
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

// MetricDefinitionsByDeviceProfile loads all metric definitions declared on a
// device profile without pagination (ADR-016/ADR-045).
func (api *Api) MetricDefinitionsByDeviceProfile(ctx context.Context, profileId uint) ([]*MetricDefinition, error) {
	found := make([]*MetricDefinition, 0)
	result := api.RDB.DB(ctx).Where("device_profile_id = ?", profileId).Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// MetricDefinitionsByDeviceType is the ingest validation path's loader: it resolves
// the type → profile hop (ADR-045) and returns the metric definitions of the
// profile's currently-active PUBLISHED version (decision 4), not the mutable draft —
// a device sees the capability set that was published, so draft edits are inert
// until published. A type with no profile, or a profile not yet published, has no
// definitions, so it returns empty — the common untyped/unpublished case the cache
// is careful to remember.
func (api *Api) MetricDefinitionsByDeviceType(ctx context.Context, deviceTypeId uint) ([]*MetricDefinition, error) {
	profileId, ok, err := api.profileIdForDeviceType(ctx, deviceTypeId)
	if err != nil || !ok {
		return []*MetricDefinition{}, err
	}
	snap, err := api.activeProfileSnapshot(ctx, profileId)
	if err != nil {
		return nil, err
	}
	return snap.Metrics, nil
}

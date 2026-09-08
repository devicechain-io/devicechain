// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
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

	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	enumJSON, err := rdb.JSONInputOf("enum", request.Enum)
	if err != nil {
		return nil, err
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
			Metadata: metadataJSON,
		},
		DeviceProfile: matches[0],
		MetricKey:     request.MetricKey,
		DataType:      request.DataType,
		Unit:          rdb.NullStrOf(request.Unit),
		MinValue:      rdb.NullFloat64Of(request.MinValue),
		MaxValue:      rdb.NullFloat64Of(request.MaxValue),
		Enum:          enumJSON,
		Descriptor:    rdb.NullStrOf(request.Descriptor),
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// UpdateMetricDefinition applies a PARTIAL update: a field the caller did not name
// keeps its stored value, an explicit null clears a nullable one, and the required
// fields (deviceProfileToken, metricKey, dataType) refuse a null rather than folding
// it to a blank the create path would have rejected.
func (api *Api) UpdateMetricDefinition(ctx context.Context, token string,
	request *MetricDefinitionUpdateRequest) (*MetricDefinition, error) {
	matches, err := api.MetricDefinitionsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	updated := matches[0]

	// EVERYTHING THAT CAN REFUSE RESOLVES BEFORE ANYTHING IS WRITTEN — the profile
	// hop, the required folds, the vocabulary check — so a refused update leaves the
	// row exactly as it was rather than applying the fields it liked first.
	reparent, err := api.resolveProfileRef(ctx, request.DeviceProfileToken, updated.DeviceProfile)
	if err != nil {
		return nil, err
	}
	metricKey, err := request.MetricKey.ApplyToRequired("metricKey", updated.MetricKey)
	if err != nil {
		return nil, err
	}
	dataType, err := request.DataType.ApplyToRequired("dataType", updated.DataType)
	if err != nil {
		return nil, err
	}
	// The vocabulary + storability check (ADR-016 amd) runs ONLY when the caller named
	// dataType. An absent field has nothing to validate, and validating the STORED value
	// instead would refuse an unrelated edit over a type the caller never sent — see
	// MetricDefinitionUpdateRequest for why the invariant is inductive rather than
	// re-proved on every write.
	if request.DataType.Set {
		if !MetricDataType(dataType).Valid() {
			return nil, fmt.Errorf("invalid metric data type: %s", dataType)
		}
		if !MetricDataType(dataType).StorableAsMetric() {
			return nil, fmt.Errorf("metric data type %s is not storable as a time-series measurement; "+
				"model string-valued telemetry as device state (ADR-016 amd)", dataType)
		}
	}
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata.ApplyTo(dcgraphql.MetadataStr(updated.Metadata)))
	if err != nil {
		return nil, err
	}
	enumJSON, err := rdb.JSONInputOf("enum", request.Enum.ApplyTo(dcgraphql.MetadataStr(updated.Enum)))
	if err != nil {
		return nil, err
	}

	updated.MetricKey = metricKey
	updated.DataType = dataType
	updated.Metadata = metadataJSON
	updated.Enum = enumJSON
	updated.Name = rdb.NullStrOf(request.Name.ApplyTo(dcgraphql.NullStr(updated.Name)))
	updated.Description = rdb.NullStrOf(request.Description.ApplyTo(dcgraphql.NullStr(updated.Description)))
	updated.Unit = rdb.NullStrOf(request.Unit.ApplyTo(dcgraphql.NullStr(updated.Unit)))
	updated.MinValue = rdb.NullFloat64Of(request.MinValue.ApplyTo(dcgraphql.NullFloat64(updated.MinValue)))
	updated.MaxValue = rdb.NullFloat64Of(request.MaxValue.ApplyTo(dcgraphql.NullFloat64(updated.MaxValue)))
	updated.Descriptor = rdb.NullStrOf(request.Descriptor.ApplyTo(dcgraphql.NullStr(updated.Descriptor)))
	if reparent != nil {
		updated.DeviceProfile = reparent
		updated.DeviceProfileId = reparent.ID
	}

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get metric definitions by id.
func (api *Api) MetricDefinitionsById(ctx context.Context, ids []uint) ([]*MetricDefinition, error) {
	return rdb.FindByIds[MetricDefinition](api.RDB.DB(ctx).Preload("DeviceProfile"), ids)
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

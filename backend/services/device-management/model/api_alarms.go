// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// nullInt64Of adapts an optional GraphQL Int (int32) to a nullable bigint column.
func nullInt64Of(v *int32) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*v), Valid: true}
}

// Create a new alarm definition (ADR-041).
func (api *Api) CreateAlarmDefinition(ctx context.Context,
	request *AlarmDefinitionCreateRequest) (*AlarmDefinition, error) {
	matches, err := api.DeviceProfilesByToken(ctx, []string{request.DeviceProfileToken})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	if err := validateAlarmKey(request.AlarmKey); err != nil {
		return nil, err
	}
	if err := ValidateAlarmDefinition(request); err != nil {
		return nil, err
	}
	if err := api.checkAlarmKeyMetric(ctx, matches[0].ID, request.AlarmKey, request.MetricKey, ""); err != nil {
		return nil, err
	}

	created := &AlarmDefinition{
		TokenReference: rdb.TokenReference{Token: request.Token},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		MetadataEntity:      rdb.MetadataEntity{Metadata: rdb.MetadataStrOf(request.Metadata)},
		DeviceProfile:       matches[0],
		AlarmKey:            request.AlarmKey,
		MetricKey:           request.MetricKey,
		ConditionType:       request.ConditionType,
		Operator:            request.Operator,
		Severity:            request.Severity,
		Threshold:           rdb.NullFloat64Of(request.Threshold),
		ThresholdAttr:       rdb.NullStrOf(request.ThresholdAttr),
		DurationSeconds:     nullInt64Of(request.DurationSeconds),
		RepeatCount:         nullInt64Of(request.RepeatCount),
		RepeatWindowSeconds: nullInt64Of(request.RepeatWindowSeconds),
		Enabled:             request.Enabled,
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing alarm definition.
func (api *Api) UpdateAlarmDefinition(ctx context.Context, token string,
	request *AlarmDefinitionCreateRequest) (*AlarmDefinition, error) {
	matches, err := api.AlarmDefinitionsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	if err := validateAlarmKey(request.AlarmKey); err != nil {
		return nil, err
	}
	if err := ValidateAlarmDefinition(request); err != nil {
		return nil, err
	}

	updated := matches[0]
	updated.Token = request.Token
	updated.Name = rdb.NullStrOf(request.Name)
	updated.Description = rdb.NullStrOf(request.Description)
	updated.Metadata = rdb.MetadataStrOf(request.Metadata)
	updated.AlarmKey = request.AlarmKey
	updated.MetricKey = request.MetricKey
	updated.ConditionType = request.ConditionType
	updated.Operator = request.Operator
	updated.Severity = request.Severity
	updated.Threshold = rdb.NullFloat64Of(request.Threshold)
	updated.ThresholdAttr = rdb.NullStrOf(request.ThresholdAttr)
	updated.DurationSeconds = nullInt64Of(request.DurationSeconds)
	updated.RepeatCount = nullInt64Of(request.RepeatCount)
	updated.RepeatWindowSeconds = nullInt64Of(request.RepeatWindowSeconds)
	updated.Enabled = request.Enabled

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

	if updated.DeviceProfile != nil {
		if err := api.checkAlarmKeyMetric(ctx, updated.DeviceProfile.ID, request.AlarmKey, request.MetricKey, token); err != nil {
			return nil, err
		}
	}

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// checkAlarmKeyMetric enforces that every severity tier of one alarm key on a
// profile watches the same metric (ADR-041): tiers of a single alarm describe one
// escalating condition, so a same-key rule bound to a different metric is a
// misconfiguration. excludeToken skips the row being updated. Best-effort at the
// app layer (the unique index only guards (profile, key, severity)); concurrent
// creates of conflicting tiers is a benign, rare edge left to the evaluator.
func (api *Api) checkAlarmKeyMetric(ctx context.Context,
	profileId uint, alarmKey, metricKey, excludeToken string) error {
	q := api.RDB.DB(ctx).Model(&AlarmDefinition{}).
		Where("device_profile_id = ? AND alarm_key = ? AND metric_key <> ?", profileId, alarmKey, metricKey)
	if excludeToken != "" {
		q = q.Where("token <> ?", excludeToken)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("alarm key %q on this profile already watches a different metric; "+
			"the severity tiers of one alarm must share a metric", alarmKey)
	}
	return nil
}

// Get alarm definitions by id.
func (api *Api) AlarmDefinitionsById(ctx context.Context, ids []uint) ([]*AlarmDefinition, error) {
	found := make([]*AlarmDefinition, 0)
	result := api.RDB.DB(ctx).Preload("DeviceProfile").Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get alarm definitions by token.
func (api *Api) AlarmDefinitionsByToken(ctx context.Context, tokens []string) ([]*AlarmDefinition, error) {
	found := make([]*AlarmDefinition, 0)
	result := api.RDB.DB(ctx).Preload("DeviceProfile").Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for alarm definitions that meet criteria.
func (api *Api) AlarmDefinitions(ctx context.Context,
	criteria AlarmDefinitionSearchCriteria) (*AlarmDefinitionSearchResults, error) {
	results := make([]AlarmDefinition, 0)
	db, pag := api.RDB.ListOf(ctx, &AlarmDefinition{}, func(result *gorm.DB) *gorm.DB {
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
	return &AlarmDefinitionSearchResults{Results: results, Pagination: pag}, nil
}

// AlarmDefinitionsByDeviceProfile loads all alarm rules declared on a device
// profile without pagination (ADR-041/ADR-045).
func (api *Api) AlarmDefinitionsByDeviceProfile(ctx context.Context, profileId uint) ([]*AlarmDefinition, error) {
	found := make([]*AlarmDefinition, 0)
	result := api.RDB.DB(ctx).Where("device_profile_id = ?", profileId).Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// AlarmDefinitionsByDeviceType is the alarm evaluator's loader (ADR-041): it
// resolves the type → profile hop (ADR-045) when a measurement arrives and returns
// the profile's rules; a device whose type has no profile has no rules (empty).
func (api *Api) AlarmDefinitionsByDeviceType(ctx context.Context, deviceTypeId uint) ([]*AlarmDefinition, error) {
	profileId, ok, err := api.profileIdForDeviceType(ctx, deviceTypeId)
	if err != nil || !ok {
		return []*AlarmDefinition{}, err
	}
	return api.AlarmDefinitionsByDeviceProfile(ctx, profileId)
}

// validateAlarmKey enforces that an alarm key is present and token-grammar-safe
// (ADR-042): it keys the alarm on its originator and may appear in a subject, so a
// free-form key must not ossify before later enforcement.
func validateAlarmKey(key string) error {
	if err := core.ValidateToken(key); err != nil {
		return fmt.Errorf("invalid alarm key %q: %w", key, err)
	}
	return nil
}

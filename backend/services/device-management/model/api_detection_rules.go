// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// validateDetectionRuleToken enforces that a detection-rule token is present and
// token-grammar-safe (ADR-042): it becomes part of the runtime rule id
// ("{tenant}/{profileVersionToken}/{token}", ADR-051 slice 4b) which is carried on a
// per-tenant subject, so a free-form token must not ossify before that enforcement.
func validateDetectionRuleToken(token string) error {
	if err := core.ValidateToken(token); err != nil {
		return fmt.Errorf("invalid detection rule token %q: %w", token, err)
	}
	return nil
}

// validateDetectionRuleDefinition checks only that the authored rule is well-formed JSON —
// non-empty and a JSON object. It deliberately does NOT parse the detection taxonomy: the
// authoritative type/cost/injection validation is the synchronous compile event-processing
// performs at publish (ADR-044). Checking JSON shape here gives the author an immediate,
// event-processing-independent error for the common mistake (malformed blob) while keeping
// cel-go single-homed. A blob that is valid JSON but not a real rule is caught at publish.
func validateDetectionRuleDefinition(definition string) error {
	if definition == "" {
		return fmt.Errorf("a detection rule definition is required")
	}
	if !json.Valid([]byte(definition)) {
		return fmt.Errorf("detection rule definition is not valid JSON")
	}
	// Require a JSON object (the rules.Rule shape) rather than an array/scalar/null. Note
	// the literal `null` unmarshals into a map as a no-op with no error (and jsonb `null`
	// satisfies the not-null column), so it must be rejected explicitly — else an
	// obviously-wrong blob slips past this guard and only fails at publish-compile.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(definition), &probe); err != nil {
		return fmt.Errorf("detection rule definition must be a JSON object: %w", err)
	}
	if probe == nil {
		return fmt.Errorf("detection rule definition must be a JSON object, not null")
	}
	return nil
}

// Create a new detection rule (ADR-051 slice 4b).
func (api *Api) CreateDetectionRule(ctx context.Context,
	request *DetectionRuleCreateRequest) (*DetectionRule, error) {
	matches, err := api.DeviceProfilesByToken(ctx, []string{request.DeviceProfileToken})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	if err := validateDetectionRuleToken(request.Token); err != nil {
		return nil, err
	}
	if err := validateDetectionRuleDefinition(request.Definition); err != nil {
		return nil, err
	}

	created := &DetectionRule{
		TokenReference: rdb.TokenReference{Token: request.Token},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		MetadataEntity: rdb.MetadataEntity{Metadata: rdb.MetadataStrOf(request.Metadata)},
		DeviceProfile:  matches[0],
		Definition:     datatypes.JSON(request.Definition),
		Enabled:        request.Enabled,
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing detection rule.
func (api *Api) UpdateDetectionRule(ctx context.Context, token string,
	request *DetectionRuleCreateRequest) (*DetectionRule, error) {
	matches, err := api.DetectionRulesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	if err := validateDetectionRuleToken(request.Token); err != nil {
		return nil, err
	}
	if err := validateDetectionRuleDefinition(request.Definition); err != nil {
		return nil, err
	}

	updated := matches[0]
	updated.Token = request.Token
	updated.Name = rdb.NullStrOf(request.Name)
	updated.Description = rdb.NullStrOf(request.Description)
	updated.Metadata = rdb.MetadataStrOf(request.Metadata)
	updated.Definition = datatypes.JSON(request.Definition)
	updated.Enabled = request.Enabled

	// Re-parent if the profile token changed.
	if updated.DeviceProfile == nil || request.DeviceProfileToken != updated.DeviceProfile.Token {
		matches, err := api.DeviceProfilesByToken(ctx, []string{request.DeviceProfileToken})
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, gorm.ErrRecordNotFound
		}
		updated.DeviceProfile = matches[0]
		updated.DeviceProfileId = matches[0].ID
	}

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get detection rules by id.
func (api *Api) DetectionRulesById(ctx context.Context, ids []uint) ([]*DetectionRule, error) {
	found := make([]*DetectionRule, 0)
	result := api.RDB.DB(ctx).Preload("DeviceProfile").Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get detection rules by token.
func (api *Api) DetectionRulesByToken(ctx context.Context, tokens []string) ([]*DetectionRule, error) {
	found := make([]*DetectionRule, 0)
	result := api.RDB.DB(ctx).Preload("DeviceProfile").Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for detection rules that meet criteria.
func (api *Api) DetectionRules(ctx context.Context,
	criteria DetectionRuleSearchCriteria) (*DetectionRuleSearchResults, error) {
	results := make([]DetectionRule, 0)
	db, pag := api.RDB.ListOf(ctx, &DetectionRule{}, func(result *gorm.DB) *gorm.DB {
		if criteria.DeviceProfile != nil {
			result = result.Where("device_profile_id = (?)",
				api.RDB.DB(ctx).Model(&DeviceProfile{}).Select("id").Where("token = ?", criteria.DeviceProfile))
		}
		return result.Preload("DeviceProfile")
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &DetectionRuleSearchResults{Results: results, Pagination: pag}, nil
}

// DetectionRulesByDeviceProfile loads all detection rules declared on a device profile
// without pagination — the draft loader used to build a publish snapshot (ADR-045 slice c).
func (api *Api) DetectionRulesByDeviceProfile(ctx context.Context, profileId uint) ([]*DetectionRule, error) {
	found := make([]*DetectionRule, 0)
	result := api.RDB.DB(ctx).Where("device_profile_id = ?", profileId).Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

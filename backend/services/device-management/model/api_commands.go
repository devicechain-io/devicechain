// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Create a new command definition (ADR-043).
// assertCommandKeyUnused rejects a second definition of the same command key on one
// profile. Without this the vocabulary is ambiguous: the enqueue gate (ADR-043
// decision 3) resolves a command by key and can only honour one definition, so two
// definitions of "drive" with different parameter schemas would mean a payload's
// validity depends on which row the publish-time SELECT happened to return first.
// The ambiguity has to be refused where it is introduced — a gate that picks a
// winner is just a deterministic coin flip.
//
// excludeId (0 for a create) is the definition being updated, so re-saving a
// definition without changing its key is not a self-collision.
func (api *Api) assertCommandKeyUnused(ctx context.Context, profileId uint, commandKey string, excludeId uint) error {
	if profileId == 0 {
		return nil
	}
	existing := make([]*CommandDefinition, 0)
	query := api.RDB.DB(ctx).Where("device_profile_id = ? AND command_key = ?", profileId, commandKey)
	if excludeId != 0 {
		query = query.Where("id <> ?", excludeId)
	}
	if err := query.Find(&existing).Error; err != nil {
		return err
	}
	if len(existing) > 0 {
		return fmt.Errorf("device profile already declares a command %q; command keys must be unique per profile", commandKey)
	}
	return nil
}

func (api *Api) CreateCommandDefinition(ctx context.Context,
	request *CommandDefinitionCreateRequest) (*CommandDefinition, error) {
	matches, err := api.DeviceProfilesByToken(ctx, []string{request.DeviceProfileToken})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	if err := validateCommandKey(request.CommandKey); err != nil {
		return nil, err
	}
	if err := validateRequestSchema(request.ParameterSchema); err != nil {
		return nil, err
	}

	if err := api.assertCommandKeyUnused(ctx, matches[0].ID, request.CommandKey, 0); err != nil {
		return nil, err
	}

	created := &CommandDefinition{
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
		DeviceProfile:   matches[0],
		CommandKey:      request.CommandKey,
		ParameterSchema: rdb.MetadataStrOf(request.ParameterSchema),
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing command definition.
func (api *Api) UpdateCommandDefinition(ctx context.Context, token string,
	request *CommandDefinitionCreateRequest) (*CommandDefinition, error) {
	matches, err := api.CommandDefinitionsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	if err := validateCommandKey(request.CommandKey); err != nil {
		return nil, err
	}
	if err := validateRequestSchema(request.ParameterSchema); err != nil {
		return nil, err
	}

	// The profile the definition will belong to AFTER this update — the key must be
	// unique there, not in the profile it is moving away from.
	targetProfileId := uint(0)
	if matches[0].DeviceProfile != nil {
		targetProfileId = matches[0].DeviceProfile.ID
	}
	if request.DeviceProfileToken != "" &&
		(matches[0].DeviceProfile == nil || request.DeviceProfileToken != matches[0].DeviceProfile.Token) {
		profiles, err := api.DeviceProfilesByToken(ctx, []string{request.DeviceProfileToken})
		if err != nil {
			return nil, err
		}
		if len(profiles) == 0 {
			return nil, gorm.ErrRecordNotFound
		}
		targetProfileId = profiles[0].ID
	}
	if err := api.assertCommandKeyUnused(ctx, targetProfileId, request.CommandKey, matches[0].ID); err != nil {
		return nil, err
	}

	// Update fields that changed.
	updated := matches[0]
	updated.Token = request.Token
	updated.Name = rdb.NullStrOf(request.Name)
	updated.Description = rdb.NullStrOf(request.Description)
	updated.Metadata = rdb.MetadataStrOf(request.Metadata)
	updated.CommandKey = request.CommandKey
	updated.ParameterSchema = rdb.MetadataStrOf(request.ParameterSchema)

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

// Get command definitions by id.
func (api *Api) CommandDefinitionsById(ctx context.Context, ids []uint) ([]*CommandDefinition, error) {
	found := make([]*CommandDefinition, 0)
	result := api.RDB.DB(ctx)
	result = result.Preload("DeviceProfile")
	result = result.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get command definitions by token.
func (api *Api) CommandDefinitionsByToken(ctx context.Context, tokens []string) ([]*CommandDefinition, error) {
	found := make([]*CommandDefinition, 0)
	result := api.RDB.DB(ctx)
	result = result.Preload("DeviceProfile")
	result = result.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for command definitions that meet criteria.
func (api *Api) CommandDefinitions(ctx context.Context,
	criteria CommandDefinitionSearchCriteria) (*CommandDefinitionSearchResults, error) {
	results := make([]CommandDefinition, 0)
	db, pag := api.RDB.ListOf(ctx, &CommandDefinition{}, func(result *gorm.DB) *gorm.DB {
		if criteria.DeviceProfile != nil {
			result = result.Where("device_profile_id = (?)",
				api.RDB.DB(ctx).Model(&DeviceProfile{}).Select("id").Where("token = ?", criteria.DeviceProfile))
		}
		if criteria.CommandKey != nil {
			result = result.Where("command_key = ?", *criteria.CommandKey)
		}
		return result.Preload("DeviceProfile")
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &CommandDefinitionSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// CommandDefinitionsByDeviceProfile loads all command definitions declared on a
// device profile without pagination — the loader for reading a profile's full
// command vocabulary (e.g. the console command form) (ADR-043/ADR-045).
func (api *Api) CommandDefinitionsByDeviceProfile(ctx context.Context, profileId uint) ([]*CommandDefinition, error) {
	found := make([]*CommandDefinition, 0)
	result := api.RDB.DB(ctx).Where("device_profile_id = ?", profileId).Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// CommandDefinitionsByDeviceType resolves the type → profile hop (ADR-045) and
// returns the command vocabulary of the profile's currently-active PUBLISHED version
// (decision 4) — the commands a device actually accepts are the published ones, not
// the draft. Empty for a type with no profile or a profile not yet published.
func (api *Api) CommandDefinitionsByDeviceType(ctx context.Context, deviceTypeId uint) ([]*CommandDefinition, error) {
	profileId, ok, err := api.profileIdForDeviceType(ctx, deviceTypeId)
	if err != nil || !ok {
		return []*CommandDefinition{}, err
	}
	snap, err := api.activeProfileSnapshot(ctx, profileId)
	if err != nil {
		return nil, err
	}
	return snap.Commands, nil
}

// validateCommandKey enforces that a command key is present and token-grammar-safe
// (ADR-043 decision 1 / ADR-042): the vocabulary a profile declares must stay
// safe to carry in a subject/topic, so free-form keys can't ossify before later
// enforcement lands.
func validateCommandKey(key string) error {
	if err := core.ValidateToken(key); err != nil {
		return fmt.Errorf("invalid command key %q: %w", key, err)
	}
	return nil
}

// validateRequestSchema decodes and validates a create/update request's parameter
// schema for well-formedness (ADR-043). Decoding is strict — an unrecognized
// descriptor field is rejected rather than silently dropped. A nil/empty schema
// is valid.
func validateRequestSchema(raw *string) error {
	if raw == nil || *raw == "" {
		return nil
	}
	schema, err := decodeParameterSchemaStrict([]byte(*raw))
	if err != nil {
		return fmt.Errorf("invalid parameter schema: %w", err)
	}
	if err := ValidateParameterSchema(schema); err != nil {
		return fmt.Errorf("invalid parameter schema: %w", err)
	}
	return nil
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Create a new device credential.
func (api *Api) CreateDeviceCredential(ctx context.Context, request *DeviceCredentialCreateRequest) (*DeviceCredential, error) {
	matches, err := api.DevicesByToken(ctx, []string{request.DeviceToken})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	// Validate credential type against the known vocabulary.
	if !CredentialType(request.CredentialType).Valid() {
		return nil, fmt.Errorf("invalid credential type: %s", request.CredentialType)
	}

	// Parse optional expiration timestamp (RFC3339).
	expiresAt := sql.NullTime{}
	if request.ExpiresAt != nil {
		parsed, err := time.Parse(time.RFC3339, *request.ExpiresAt)
		if err != nil {
			return nil, err
		}
		expiresAt = sql.NullTime{Time: parsed, Valid: true}
	}

	created := &DeviceCredential{
		TokenReference: rdb.TokenReference{
			Token: request.Token,
		},
		MetadataEntity: rdb.MetadataEntity{
			Metadata: rdb.MetadataStrOf(request.Metadata),
		},
		Device:          matches[0],
		CredentialType:  request.CredentialType,
		CredentialId:    request.CredentialId,
		CredentialValue: rdb.NullStrOf(request.CredentialValue),
		Enabled:         request.Enabled,
		ExpiresAt:       expiresAt,
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing device credential.
func (api *Api) UpdateDeviceCredential(ctx context.Context, token string,
	request *DeviceCredentialCreateRequest) (*DeviceCredential, error) {
	matches, err := api.DeviceCredentialsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	// Validate credential type against the known vocabulary.
	if !CredentialType(request.CredentialType).Valid() {
		return nil, fmt.Errorf("invalid credential type: %s", request.CredentialType)
	}

	// Parse optional expiration timestamp (RFC3339).
	expiresAt := sql.NullTime{}
	if request.ExpiresAt != nil {
		parsed, err := time.Parse(time.RFC3339, *request.ExpiresAt)
		if err != nil {
			return nil, err
		}
		expiresAt = sql.NullTime{Time: parsed, Valid: true}
	}

	// Update fields that changed.
	updated := matches[0]
	updated.Token = request.Token
	updated.Metadata = rdb.MetadataStrOf(request.Metadata)
	updated.CredentialType = request.CredentialType
	updated.CredentialId = request.CredentialId
	updated.CredentialValue = rdb.NullStrOf(request.CredentialValue)
	updated.Enabled = request.Enabled
	updated.ExpiresAt = expiresAt

	// Update owning device if changed. Guard for a nil Device by reloading.
	if updated.Device == nil || request.DeviceToken != updated.Device.Token {
		devices, err := api.DevicesByToken(ctx, []string{request.DeviceToken})
		if err != nil {
			return nil, err
		}
		if len(devices) == 0 {
			return nil, gorm.ErrRecordNotFound
		}
		updated.Device = devices[0]
	}

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get device credentials by id.
func (api *Api) DeviceCredentialsById(ctx context.Context, ids []uint) ([]*DeviceCredential, error) {
	found := make([]*DeviceCredential, 0)
	result := api.RDB.DB(ctx)
	result = result.Preload("Device")
	result = result.Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get device credentials by token.
func (api *Api) DeviceCredentialsByToken(ctx context.Context, tokens []string) ([]*DeviceCredential, error) {
	found := make([]*DeviceCredential, 0)
	result := api.RDB.DB(ctx)
	result = result.Preload("Device")
	result = result.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for device credentials that meet criteria.
func (api *Api) DeviceCredentials(ctx context.Context, criteria DeviceCredentialSearchCriteria) (*DeviceCredentialSearchResults, error) {
	results := make([]DeviceCredential, 0)
	db, pag := api.RDB.ListOf(ctx, &DeviceCredential{}, func(result *gorm.DB) *gorm.DB {
		if criteria.Device != nil {
			result = result.Where("device_id = (?)",
				api.RDB.DB(ctx).Model(&Device{}).Select("id").Where("token = ?", criteria.Device))
		}
		if criteria.CredentialType != nil {
			result = result.Where("credential_type = ?", criteria.CredentialType)
		}
		if criteria.CredentialId != nil {
			result = result.Where("credential_id = ?", criteria.CredentialId)
		}
		if criteria.Enabled != nil {
			result = result.Where("enabled = ?", criteria.Enabled)
		}
		return result.Preload("Device")
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &DeviceCredentialSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Resolve a presented credential (type + id) to its owning device credential.
// This is the foundational hook the future transport-auth path uses to resolve a
// presented credential to a device identity (ADR-014). Only enabled credentials
// match; returns gorm.ErrRecordNotFound if none.
func (api *Api) DeviceCredentialByCredentialId(ctx context.Context, credentialType string, credentialId string) (*DeviceCredential, error) {
	found := make([]*DeviceCredential, 0)
	result := api.RDB.DB(ctx)
	result = result.Preload("Device")
	result = result.Where("credential_type = ? and credential_id = ? and enabled = ?",
		credentialType, credentialId, true)
	result = result.Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	if len(found) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return found[0], nil
}

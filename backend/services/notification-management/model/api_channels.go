// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// CreateNotificationChannel creates a delivery channel. The channel type must be
// in the catalog; config (if given) must be a well-formed JSON object.
func (api *Api) CreateNotificationChannel(ctx context.Context,
	request *NotificationChannelCreateRequest) (*NotificationChannel, error) {
	if err := validateChannelType(request.ChannelType); err != nil {
		return nil, err
	}
	if err := validateJSONObject(request.Config, "config"); err != nil {
		return nil, err
	}

	created := &NotificationChannel{
		TokenReference: rdb.TokenReference{Token: request.Token},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		MetadataEntity: rdb.MetadataEntity{Metadata: rdb.MetadataStrOf(request.Metadata)},
		ChannelType:    request.ChannelType,
		Config:         rdb.MetadataStrOf(request.Config),
		Secret:         rdb.NullStrOf(request.Secret),
		Enabled:        request.Enabled,
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// UpdateNotificationChannel updates a channel by token. The secret is write-only,
// so a nil request.Secret preserves the stored secret (the caller cannot read it
// back to resend it); a non-nil request.Secret replaces it, and an explicit empty
// string clears it. Every other field is fully replaced from the request.
func (api *Api) UpdateNotificationChannel(ctx context.Context, token string,
	request *NotificationChannelCreateRequest) (*NotificationChannel, error) {
	matches, err := api.NotificationChannelsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if err := validateChannelType(request.ChannelType); err != nil {
		return nil, err
	}
	if err := validateJSONObject(request.Config, "config"); err != nil {
		return nil, err
	}

	updated := matches[0]
	updated.Token = request.Token
	updated.Name = rdb.NullStrOf(request.Name)
	updated.Description = rdb.NullStrOf(request.Description)
	updated.Metadata = rdb.MetadataStrOf(request.Metadata)
	updated.ChannelType = request.ChannelType
	updated.Config = rdb.MetadataStrOf(request.Config)
	updated.Enabled = request.Enabled
	// Preserve-on-omit: only touch the secret when the caller sent one.
	if request.Secret != nil {
		updated.Secret = rdb.NullStrOf(request.Secret)
	}

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// NotificationChannelsById loads channels by numeric id.
func (api *Api) NotificationChannelsById(ctx context.Context, ids []uint) ([]*NotificationChannel, error) {
	found := make([]*NotificationChannel, 0)
	result := api.RDB.DB(ctx).Find(&found, ids)
	return found, result.Error
}

// NotificationChannelsByToken loads channels by token.
func (api *Api) NotificationChannelsByToken(ctx context.Context, tokens []string) ([]*NotificationChannel, error) {
	found := make([]*NotificationChannel, 0)
	result := api.RDB.DB(ctx).Find(&found, "token in ?", tokens)
	return found, result.Error
}

// NotificationChannels searches channels by criteria.
func (api *Api) NotificationChannels(ctx context.Context,
	criteria NotificationChannelSearchCriteria) (*NotificationChannelSearchResults, error) {
	results := make([]NotificationChannel, 0)
	db, pag := api.RDB.ListOf(ctx, &NotificationChannel{}, func(result *gorm.DB) *gorm.DB {
		if criteria.ChannelType != nil {
			result = result.Where("channel_type = ?", *criteria.ChannelType)
		}
		if criteria.Enabled != nil {
			result = result.Where("enabled = ?", *criteria.Enabled)
		}
		return result
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &NotificationChannelSearchResults{Results: results, Pagination: pag}, nil
}

// DeleteNotificationChannel hard-deletes a channel by token. It fails closed if
// the channel is still referenced by a routing rule (ErrChannelInUse) rather than
// leaving a rule pointing at a missing channel.
func (api *Api) DeleteNotificationChannel(ctx context.Context, token string) (bool, error) {
	matches, err := api.NotificationChannelsByToken(ctx, []string{token})
	if err != nil {
		return false, err
	}
	if len(matches) == 0 {
		return false, nil
	}
	refs, err := api.countRulesForChannel(ctx, matches[0].ID)
	if err != nil {
		return false, err
	}
	if refs > 0 {
		return false, ErrChannelInUse
	}
	result := api.RDB.DB(ctx).Unscoped().Where("token = ?", token).Delete(&NotificationChannel{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

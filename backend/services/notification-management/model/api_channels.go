// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// CreateNotificationChannel creates a delivery channel. The channel type must be
// in the catalog; config (if given) must be a well-formed JSON object. A non-empty
// request.Secret is sealed into the secret store under the channel's handle (never a
// column); a nil or empty Secret stores no secret.
func (api *Api) CreateNotificationChannel(ctx context.Context,
	request *NotificationChannelCreateRequest) (*NotificationChannel, error) {
	if err := validateChannelType(request.ChannelType); err != nil {
		return nil, err
	}
	if err := validateJSONObject(request.Config, "config"); err != nil {
		return nil, err
	}
	if err := validateJSONObject(request.Metadata, "metadata"); err != nil {
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
		Enabled:        request.Enabled,
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	// Seal the delivery secret under the channel's handle. The row is written first so
	// its immutable ID (the secret's stable key) exists; the secret is a separate write
	// to the store (same DB, not one transaction).
	if err := api.applyChannelSecret(ctx, created.ID, request.Secret); err != nil {
		// The row committed but sealing the secret failed. Roll the row back (best
		// effort) so the create is atomic from the caller's view — otherwise a retry
		// would collide on the now-existing token. A cleanup failure is logged, not
		// masked; the original secret error is what the caller needs.
		if delErr := api.RDB.DB(ctx).Unscoped().Delete(created).Error; delErr != nil {
			log.Warn().Err(delErr).Str("token", request.Token).
				Msg("Failed to roll back channel row after secret write failure; channel may exist without a secret")
		}
		return nil, err
	}
	return created, nil
}

// UpdateNotificationChannel updates a channel by token. The secret is write-only,
// so a nil request.Secret preserves the stored secret (the caller cannot read it
// back to resend it); a non-nil request.Secret replaces it in the store, and an
// explicit empty string clears it (store.Delete). Every other field is fully
// replaced from the request.
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
	if err := validateJSONObject(request.Metadata, "metadata"); err != nil {
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

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	// Preserve-on-omit: only touch the secret when the caller sent one. The secret is
	// keyed by the channel's immutable ID, so a token rename in this same update keeps
	// the existing secret bound to the channel (no orphaning).
	if request.Secret != nil {
		if err := api.applyChannelSecret(ctx, updated.ID, request.Secret); err != nil {
			return nil, err
		}
	}
	return updated, nil
}

// applyChannelSecret writes the channel's delivery secret to the store to match the
// request: a non-empty value is sealed (Put), an explicit empty string clears it
// (Delete, idempotent). A nil secret is a caller decision made above (preserve) and
// never reaches here. It centralizes the write-only secret handling shared by create
// and update, keyed by the channel's immutable id.
func (api *Api) applyChannelSecret(ctx context.Context, id uint, secret *string) error {
	if secret == nil {
		return nil
	}
	ref, err := ChannelSecretRef(ctx, id)
	if err != nil {
		return err
	}
	if *secret == "" {
		return api.Secrets.Delete(ctx, ref)
	}
	return api.Secrets.Put(ctx, ref, []byte(*secret))
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
//
// The reference count and the delete are separate statements (there is no DB
// foreign key on notification_rules.channel_id by design), so a rule created
// against this channel in the narrow window between them would be left dangling.
// That is acceptable pre-GA: the schema makes a rule's channel nullable and reads
// preload it, so a dangling rule renders channel:null rather than erroring, and the
// N.C dispatcher must already tolerate a rule whose channel resolves to nothing.
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
	// Remove the channel's delivery secret so a deleted channel leaves no orphaned
	// secret (Delete is idempotent, so a channel that never had one is a no-op). The
	// row is already hard-deleted at this point, so a failure to remove the (now
	// unreachable) secret must not report the channel as undeleted: log and continue.
	// The orphaned ciphertext is benign — ids are never recycled, so it can never be
	// resolved by a future channel.
	ref, err := ChannelSecretRef(ctx, matches[0].ID)
	if err != nil {
		return false, err
	}
	if err := api.Secrets.Delete(ctx, ref); err != nil {
		log.Warn().Err(err).Str("token", token).
			Msg("Deleted channel but failed to remove its stored secret (orphaned ciphertext)")
	}
	return result.RowsAffected > 0, nil
}

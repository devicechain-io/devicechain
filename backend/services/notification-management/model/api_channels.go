// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/conflict"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// CreateNotificationChannel creates a delivery channel. The channel type must be
// in the catalog; config (if given) must be a well-formed JSON object. A webhook's config
// must also parse as one and declare `auth` (none | bearer | header), and the secret must
// agree with it: bearer and header need one, none must not have one. A non-empty
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
	if err := validateChannelCredential(request.Token, request.ChannelType, request.Config,
		request.Secret != nil && *request.Secret != ""); err != nil {
		return nil, err
	}

	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	configJSON, err := rdb.JSONInputOf("config", request.Config)
	if err != nil {
		return nil, err
	}
	created := &NotificationChannel{
		TokenReference: rdb.TokenReference{Token: request.Token},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		MetadataEntity: rdb.MetadataEntity{Metadata: metadataJSON},
		ChannelType:    request.ChannelType,
		Config:         configJSON,
		Enabled:        request.Enabled,
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	// Seal the delivery secret under the channel's handle. The row is written first so
	// its immutable ID (the secret's stable key) exists; the secret is a separate write
	// to the store (same DB, not one transaction). A create that named no secret does not
	// reach the store at all — there is nothing to seal and nothing to remove, and going
	// there anyway would put a fresh channel's success at the mercy of a store it never
	// used.
	if request.Secret == nil {
		return created, nil
	}
	if err := api.applyChannelSecret(ctx, created.ID, request.Secret); err != nil {
		// The row committed but sealing the secret failed. Roll the row back (best
		// effort) so the create is atomic from the caller's view — otherwise a retry
		// would collide on the now-existing token. A cleanup failure is logged, not
		// masked; the original secret error is what the caller needs.
		if delErr := api.RDB.DB(ctx).Unscoped().Delete(created).Error; delErr != nil {
			log.Warn().Err(delErr).Str("token", request.Token).
				Msg("Failed to roll back channel row after secret write failure; channel may exist without a " +
					"secret, and if its config declares bearer or header auth it will refuse to deliver until a " +
					"secret is set")
		}
		return nil, err
	}
	return created, nil
}

// UpdateNotificationChannel applies a PARTIAL update to the channel named by the token
// argument: a field the request omits is left alone, an explicit null clears it, and a
// value sets it.
//
// The token argument is the only thing that names the record — the input carries no
// token, so an update cannot move a channel's identity at all. RenameNotificationChannel
// is where a rename lives now.
//
// 🔴 EVERYTHING THAT CAN REFUSE RESOLVES BEFORE ANYTHING IS WRITTEN. A channel type
// outside the catalog, malformed config, malformed metadata, a cleared `enabled` and a
// webhook whose declared auth and secret disagree all fail the WHOLE update rather than
// landing after `name` has already been saved: this is the alarm→human last mile, and a
// half-applied edit is a channel in a state no caller asked for.
func (api *Api) UpdateNotificationChannel(ctx context.Context, token string,
	request *NotificationChannelUpdateRequest) (*NotificationChannel, error) {
	matches, err := api.NotificationChannelsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	updated := matches[0]

	// channelType is a NOT NULL vocabulary column: absent keeps it, a value sets it, and
	// an explicit null is REFUSED rather than folded to "" — which would be a channel
	// that routes nowhere, written successfully.
	channelType, err := request.ChannelType.ApplyToRequired("channelType", updated.ChannelType)
	if err != nil {
		return nil, err
	}
	// The catalog check runs only when the caller NAMED a type. Checking the stored value
	// instead would refuse a metadata edit over a type the caller never sent — and could
	// only ever fail on a row the create path already refused to write.
	if request.ChannelType.Set {
		if err := validateChannelType(channelType); err != nil {
			return nil, err
		}
	}
	enabled, err := request.Enabled.ApplyToRequired("enabled", updated.Enabled)
	if err != nil {
		return nil, err
	}
	config := request.Config.ApplyTo(dcgraphql.MetadataStr(updated.Config))
	if err := validateJSONObject(config, "config"); err != nil {
		return nil, err
	}
	configJSON, err := rdb.JSONInputOf("config", config)
	if err != nil {
		return nil, err
	}
	metadata := request.Metadata.ApplyTo(dcgraphql.MetadataStr(updated.Metadata))
	if err := validateJSONObject(metadata, "metadata"); err != nil {
		return nil, err
	}
	metadataJSON, err := rdb.JSONInputOf("metadata", metadata)
	if err != nil {
		return nil, err
	}
	// A webhook's declared auth and its secret must agree. The pairing is judged only when
	// the request touches something it depends on — the type, the config, the secret — or
	// switches the channel ON. That mirrors the catalog check above: a rename or a disable of
	// a channel saved before `auth` was required must still go through, because refusing
	// `enabled: false` on a broken channel would refuse the one edit that stops it failing
	// every alarm. Enabling one is judged, because that is the moment it would start to.
	//
	// 🔴 THIS IS THE SAVE-TIME HALF, NOT THE GUARANTEE. The secret write below runs after the
	// row is saved and outside its transaction, and two concurrent updates can each read a
	// store the other is about to change, so a channel can still end up declaring bearer
	// with no secret. Delivery re-checks the same rule (httpsink.Send) and refuses such a
	// channel terminally; this check exists so the caller hears about the mistake now, from
	// the request that made it, rather than from a page that never arrived.
	if channelType == ChannelTypeWebhook && (request.ChannelType.Set || request.Config.Set ||
		request.Secret.Set || (request.Enabled.Set && enabled)) {
		hasSecret, err := api.channelHasSecretAfter(ctx, updated.ID, request.Secret)
		if err != nil {
			return nil, err
		}
		if err := validateChannelCredential(token, channelType, config, hasSecret); err != nil {
			return nil, err
		}
	}

	updated.Name = rdb.NullStrOf(request.Name.ApplyTo(dcgraphql.NullStr(updated.Name)))
	updated.Description = rdb.NullStrOf(request.Description.ApplyTo(dcgraphql.NullStr(updated.Description)))
	updated.Metadata = metadataJSON
	updated.ChannelType = channelType
	updated.Config = configJSON
	updated.Enabled = enabled

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	// The secret is the one field whose three states do not map onto a column, because it
	// is not one: it lives in the envelope-encrypted store, keyed by the channel's
	// immutable id. applyChannelSecret owns the fold; it is reached only when the caller
	// mentioned the field, which is what "omitted preserves" means for a value that
	// cannot be read back to re-send.
	//
	// 🔴 THIS WRITE IS OUTSIDE THE ROW'S TRANSACTION, AND THE UPDATE PATH HAS NO
	// COMPENSATION WHERE THE CREATE PATH DOES. A store failure here leaves the row already
	// saved and the secret not rotated, and returns the error — so the caller is told the
	// update failed while part of it stands. Create handles the same split by rolling its
	// row back (see the Unscoped().Delete above); update cannot, because there is no
	// earlier state to roll back TO that is any more correct than what is now stored.
	//
	// It is left as it is deliberately rather than overlooked. The outcome is convergent:
	// the fields are already what the caller asked for, so a retry sends the same request
	// and only the secret write is repeated, and Put/Delete are both idempotent. Making it
	// atomic means putting the secret store's write inside the row's transaction, which is
	// a change to the store's contract and belongs with the store, not here.
	if request.Secret.Set {
		if err := api.applyChannelSecret(ctx, updated.ID, request.Secret.Value); err != nil {
			return nil, err
		}
	}
	return updated, nil
}

// RenameNotificationChannel moves a channel from token to newToken, and is the ONLY way
// a channel's token changes.
//
// It exists because a channel rename is a real operation rather than an accident of a
// shared create input: the delivery secret is keyed by the channel's immutable id, and a
// policy's rules store ChannelId rather than the token, so a rename orphans nothing.
// TestRenameChannelPreservesSecret is the evidence for that claim and is the reason the
// capability survived the conversion to a token-free update input instead of being
// deleted with it.
//
// Three rules, and the third is new:
//
//   - a BLANK newToken — empty or whitespace-only — is refused, because it would leave a
//     live channel addressable by nothing and return success;
//   - newToken == token is an idempotent no-op SUCCESS returning the record, so a retry
//     after a partial failure is safe rather than a not-found;
//   - a newToken already held by another of this tenant's channels is refused BY NAME,
//     under contention as well as without it. See ErrChannelTokenTaken for why the lookup
//     alone is not enough and what closes the gap.
func (api *Api) RenameNotificationChannel(ctx context.Context, token string,
	newToken string) (*NotificationChannel, error) {
	if err := dcgraphql.ErrRenameTokenUnusable("notification channel", token, newToken); err != nil {
		return nil, err
	}
	matches, err := api.NotificationChannelsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	renamed := matches[0]
	// The no-op comes AFTER the load so that renaming a channel that does not exist is
	// still a not-found rather than a success that wrote nothing.
	if newToken == token {
		return renamed, nil
	}

	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		taken := make([]*NotificationChannel, 0, 1)
		if err := tx.Find(&taken, "token = ?", newToken).Error; err != nil {
			return err
		}
		if len(taken) > 0 {
			return ErrChannelTokenTaken(token, newToken)
		}
		renamed.Token = newToken
		if err := tx.Save(renamed).Error; err != nil {
			// The losing side of a race gets here rather than through the lookup above.
			// It must read the same as the uncontended refusal, because a caller cannot
			// write two error handlers for one condition that only differ by timing.
			// Any uniqueness conflict on this Save is the token's: the row was loaded in
			// this transaction, so every other column rewrites a value it already held.
			if conflict.Is(err) {
				return ErrChannelTokenTaken(token, newToken)
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return renamed, nil
}

// ErrChannelTokenTaken is the refusal a rename onto an occupied token produces, from
// EITHER of the two paths that can discover the occupation.
//
// 🔴 THE LOOKUP ABOVE CANNOT BE THE WHOLE ANSWER, AND SAYING SO IS THE POINT OF THIS
// FUNCTION EXISTING SEPARATELY. It runs inside the write's transaction, which is the right
// place for it — but at READ COMMITTED a SELECT cannot lock a row that does not exist yet.
// Two renames onto the same token, or a rename racing a create, both see zero rows; the
// second UPDATE then blocks on the partial unique index until the first commits and fails
// with a driver-level unique violation. Nothing is corrupted — the index predicate
// (deleted_at IS NULL) covers exactly the set the lookup queries, and channels are
// hard-deleted anyway — but without the translation below the loser would get the
// GraphQL boundary's NEUTRAL conflict sentence (with code CONFLICT) rather than this
// rename's own sentence, which is not what this API promises.
//
// So the lookup is the fast, common path and the index is the authority, and both are made
// to say the same sentence. It is a conflict.Error, so it carries extensions.code CONFLICT
// on either path.
func ErrChannelTokenTaken(token, newToken string) error {
	return conflict.Errorf("cannot rename notification channel %q to %q: that token is already "+
		"in use by another channel in this tenant", token, newToken)
}

// applyChannelSecret writes the channel's delivery secret to the store to match the
// request: a non-empty value is sealed (Put), and an explicit null OR an empty string
// clears it (Delete, idempotent). It centralizes the write-only secret handling shared by
// create and update, keyed by the channel's immutable id.
//
// 🔴 A nil secret here means CLEAR, not preserve, and NEITHER CALLER EVER PASSES ONE THAT
// MEANS ANYTHING ELSE. Create returns before reaching this function when request.Secret is
// nil, so "no secret was given" never arrives here at all; update reaches it only when
// request.Secret.Set, so a nil at that point is an explicit null the caller sent. Preserve
// is expressed by NOT CALLING THIS — which is what keeps the function from being given a
// meaning it does not implement.
func (api *Api) applyChannelSecret(ctx context.Context, id uint, secret *string) error {
	ref, err := ChannelSecretRef(ctx, id)
	if err != nil {
		return err
	}
	if secret == nil || *secret == "" {
		return api.Secrets.Delete(ctx, ref)
	}
	return api.Secrets.Put(ctx, ref, []byte(*secret))
}

// channelHasSecretAfter answers "will this channel hold a secret once this update lands":
// the request's value when it names the field (null or "" clears, as applyChannelSecret
// does), otherwise the store.
func (api *Api) channelHasSecretAfter(ctx context.Context, id uint, requested dcgraphql.OptionalString) (bool, error) {
	if requested.Set {
		return requested.Value != nil && *requested.Value != "", nil
	}
	ref, err := ChannelSecretRef(ctx, id)
	if err != nil {
		return false, err
	}
	return api.Secrets.Exists(ctx, ref)
}

// NotificationChannelsById loads channels by numeric id.
func (api *Api) NotificationChannelsById(ctx context.Context, ids []uint) ([]*NotificationChannel, error) {
	return rdb.FindByIds[NotificationChannel](api.RDB.DB(ctx), ids)
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

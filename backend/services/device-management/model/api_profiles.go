// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"strings"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Create a new device profile.
func (api *Api) CreateDeviceProfile(ctx context.Context, request *DeviceProfileCreateRequest) (*DeviceProfile, error) {
	location, err := encodeLocationDeclaration(request.Location)
	if err != nil {
		return nil, err
	}
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	created := &DeviceProfile{
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
		Category:            rdb.NullStrOf(request.Category),
		LocationDeclaration: location,
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing device profile. The profile is located by the token ARGUMENT;
// the request carries no token at all, so naming a second profile is unrepresentable
// rather than refused. Renaming is renameDeviceProfile's job.
//
// Provenance (ADR-046) is not settable through this path — it is owned by the future
// catalog fork-adopt flow, never the editor.
func (api *Api) UpdateDeviceProfile(ctx context.Context, token string,
	request *DeviceProfileUpdateRequest) (*DeviceProfile, error) {
	matches, err := api.DeviceProfilesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	found := matches[0]

	found.Name = rdb.NullStrOf(request.Name.ApplyTo(dcgraphql.NullStr(found.Name)))
	found.Description = rdb.NullStrOf(request.Description.ApplyTo(dcgraphql.NullStr(found.Description)))
	found.Category = rdb.NullStrOf(request.Category.ApplyTo(dcgraphql.NullStr(found.Category)))
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata.ApplyTo(dcgraphql.MetadataStr(found.Metadata)))
	if err != nil {
		return nil, err
	}
	found.Metadata = metadataJSON

	// The position declaration (ADR-078) now takes the same three states as every other
	// field here: omitted PRESERVES it, an explicit null clears it, an object replaces
	// it. Omission used to be the CLEAR operation, which meant a caller restating the
	// profile's other fields silently un-declared position for every device built on it
	// — the reason the console had to carry the declaration forward by hand. The Save
	// below writes a nil pointer as SQL NULL (Metadata already depends on this), which
	// restores exactly the never-declared state in the DRAFT — as it should, since a
	// device that no longer reports position should display exactly like one that never
	// did. What distinguishes a cleared profile from one that never declared is the
	// append-only version history: the version published while it was declared still
	// carries the declaration, so the clear is visible as a change between versions
	// rather than as a tombstone in the draft.
	//
	// 🔴 THE STORED DECLARATION IS DECODED, NOT COPIED AS A COLUMN. The absent state has
	// to fold onto the current value, and reading it through Location() is what keeps
	// "declared with no expectations stated" (`{}`) distinct from "does not report
	// position" (SQL NULL) across the fold — the two values the whole design rests on.
	currentLocation, err := found.Location()
	if err != nil {
		return nil, err
	}
	location, err := encodeLocationDeclaration(request.Location.ApplyTo(currentLocation))
	if err != nil {
		return nil, err
	}
	found.LocationDeclaration = location

	// Omit active_version: a draft/metadata edit must never write the version pointer
	// back. `found` was loaded before this Save, so writing it whole would let an edit
	// racing a concurrent PublishDeviceProfile/RollbackDeviceProfile silently revert
	// the active pointer — the version devices resolve — to its stale value. The
	// pointer is moved only by publish/rollback (same class of race as ADR-062's
	// EntityGroup update; fixed here too since it is the identical latent bug).
	result := api.RDB.DB(ctx).Omit("ActiveVersion").Save(found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// RenameDeviceProfile changes a profile's token, and changes nothing else.
//
// 🔴 IT EXISTS BECAUSE THE RENAME WAS A REAL CAPABILITY THE UPDATE PAYLOAD CARRIED,
// NOT AN ACCIDENT OF SHARING THE CREATE INPUT. updateDeviceProfile used to take a
// `token` INSIDE its request that meant the profile's NEW name, which is exactly why
// this family could not be converted mechanically: a dedicated update input dropping
// the field would have deleted the capability rather than converted it. So the rename
// moved here, where `newToken` can mean only one thing, and the update input lost its
// token.
//
// The rules, in the order they are applied:
//
//  1. A BLANK new token — empty or WHITESPACE-ONLY — is refused. It is not a rename
//     anyone can have meant: it leaves the profile addressable by nothing, which is
//     what used to happen, successfully. Whitespace is included because "   " is a
//     blank spelled differently and the string is not one the grammar sees as absent.
//  2. newToken == token is an idempotent NO-OP SUCCESS returning the profile, so a
//     retry after a partial failure is safe rather than an error a client has to
//     special-case.
//  3. A profile that is IN USE cannot be renamed. That guard is pre-existing and MOVED
//     here rather than dropped, with the same two counts in the same message.
//  4. A token another profile in the tenant already holds is refused BY NAME, from
//     inside the transaction that does the write.
//
// The new token is stored VERBATIM, never trimmed. Trimming would silently accept
// " prof " as naming "prof", while the token grammar — which is what refuses a token
// carrying whitespace — says so plainly instead.
func (api *Api) RenameDeviceProfile(ctx context.Context, token string, newToken string) (*DeviceProfile, error) {
	if strings.TrimSpace(newToken) == "" {
		return nil, fmt.Errorf("cannot rename device profile %q: the new token is blank, and a "+
			"profile named by nothing can never be found again", token)
	}

	matches, err := api.DeviceProfilesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	found := matches[0]

	// Renaming a profile to the name it already has is what the retry of a
	// half-completed rename looks like, so it succeeds and writes nothing.
	if newToken == found.Token {
		return found, nil
	}

	// A profile token is IMMUTABLE once the profile is IN USE — published, or adopted by any
	// device type (ADR-051 slices 4b-3 / 4c-2). The published-version token
	// "{profileToken}@{version}" (ADR-045) is the key the DETECT engine files a version's
	// detection rules under and the scope resolved events are stamped with; renaming would
	// leave every already-published rule filed under the OLD token while events carry the new
	// one — detections silently stop. And the dead-man roster keys a device's entry on the
	// STABLE profile token captured at emit time (even for an unpublished-but-adopted profile),
	// so a rename orphans those entries and armed absence dead-men false-fire. Pre-GA decisive
	// cutover: reject the rename rather than re-key or orphan rules.
	var published int64
	if err := api.RDB.DB(ctx).Model(&DeviceProfileVersion{}).
		Where("device_profile_id = ?", found.ID).Count(&published).Error; err != nil {
		return nil, err
	}
	var adopters int64
	if err := api.RDB.DB(ctx).Model(&DeviceType{}).
		Where("profile_id = ?", found.ID).Count(&adopters).Error; err != nil {
		return nil, err
	}
	if published > 0 || adopters > 0 {
		return nil, fmt.Errorf("cannot rename device profile %q to %q: it is in use (%d published version(s), %d adopting type(s)); the token is immutable once published or adopted",
			token, newToken, published, adopters)
	}

	// 🔴 THE LOOKUP IS THE FAST PATH; THE UNIQUE INDEX IS THE AUTHORITY. Both are made to
	// say the same sentence, and the reason is that the transaction does NOT close the
	// race. At READ COMMITTED a Count that matches nothing takes no lock — there is no
	// row to lock — so two concurrent renames onto one free token both see it free and
	// the loser is stopped by the index instead. Without the translation below it is
	// handed `SQLSTATE 23505` and an index name, which is not what this API promises.
	//
	// The tenant predicate on the Count is the scoping callback's, so it counts within
	// the caller's tenant; the index carries the same predicate plus `deleted_at IS NULL`,
	// which is exactly the set the lookup queries.
	if err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		var taken int64
		if err := tx.Model(&DeviceProfile{}).Where("token = ?", newToken).Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return ErrDeviceProfileTokenTaken(token, newToken)
		}
		// 🔴 ONE COLUMN, NOT THE WHOLE ROW, and here that subsumes a guard rather than
		// merely being tidier. `found` was loaded before this write, so saving it whole
		// would let a rename racing a concurrent publish or rollback revert the active
		// version pointer devices resolve — which is why UpdateDeviceProfile has to
		// Omit("ActiveVersion") explicitly. A write naming one column cannot revert any
		// other, so there is nothing to omit. It still passes through the token-grammar
		// callback (a map destination is checked the same way a struct is) and the
		// tenant-scope callback.
		//
		// The model is `found` rather than a zero struct so the audit journal records the
		// row's PRIMARY KEY: an empty one means "a bulk/condition update" to a reader of
		// the journal, and on a rename the PK is the only thing linking the entry naming
		// the old token to the one naming the new.
		if err := tx.Model(found).Update("token", newToken).Error; err != nil {
			// THE LOSING RACER ARRIVES HERE rather than through the Count above, and it
			// must read exactly as the uncontended refusal does.
			if rdb.IsUniqueViolation(err, deviceProfileTokenIndexName, "device_profiles.token") {
				return ErrDeviceProfileTokenTaken(token, newToken)
			}
			return err
		}
		found.Token = newToken
		return nil
	}); err != nil {
		return nil, err
	}
	return found, nil
}

// ErrDeviceProfileTokenTaken is the ONE sentence a caller gets when the token they asked
// for belongs to another profile — whether the pre-write lookup found it or the unique
// index did. Both paths are made to say this, because a client cannot be asked to write two
// handlers for one condition that differ only by timing.
func ErrDeviceProfileTokenTaken(token, newToken string) error {
	return fmt.Errorf("cannot rename device profile %q to %q: that token is already in "+
		"use by another device profile in this tenant", token, newToken)
}

// deviceProfileTokenIndexName is the per-tenant partial unique index the baseline creates
// on device_profiles (tenant_id, token) among live rows. Postgres names it in the text of a
// unique violation, and that name is what distinguishes "this token is taken" from any
// other write failure.
//
// It mirrors schema/baseline.go's createTenantTokenIndex naming rule, "uix_" + the bare
// table name + "_tenant_token". The rule is spelled in two places because that helper is a
// deliberate copy inside the migration and is unexported;
// TestDeviceProfileTokenIndexNameMatchesTheMigration is what keeps the two from drifting.
const deviceProfileTokenIndexName = "uix_device_profiles_tenant_token"

// Get device profiles by id.
func (api *Api) DeviceProfilesById(ctx context.Context, ids []uint) ([]*DeviceProfile, error) {
	return rdb.FindByIds[DeviceProfile](api.RDB.DB(ctx), ids)
}

// Get device profiles by token.
func (api *Api) DeviceProfilesByToken(ctx context.Context, tokens []string) ([]*DeviceProfile, error) {
	found := make([]*DeviceProfile, 0)
	result := api.RDB.DB(ctx).Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for device profiles that meet criteria.
func (api *Api) DeviceProfiles(ctx context.Context, criteria DeviceProfileSearchCriteria) (*DeviceProfileSearchResults, error) {
	results := make([]DeviceProfile, 0)
	db, pag := api.RDB.ListOf(ctx, &DeviceProfile{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	return &DeviceProfileSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

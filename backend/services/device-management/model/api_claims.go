// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Errors returned by the claim operations. They are sentinels so a caller can
// distinguish the failure modes without string matching.
var (
	// ErrClaimNotFound means the device has no claim row at all.
	ErrClaimNotFound = rdb.NewNotFound("device has no claim")
	// ErrClaimNotOpen means the device's claim is not open (already redeemed or
	// canceled) and cannot be redeemed.
	ErrClaimNotOpen = errors.New("device claim is not open")
	// ErrClaimExpired means the claim resolved but its ExpiresAt has passed.
	ErrClaimExpired = errors.New("device claim has expired")
	// ErrClaimSecretMismatch means the presented claim secret did not match.
	ErrClaimSecretMismatch = errors.New("device claim secret did not match")
	// ErrClaimSecretEmpty means an empty claim secret was supplied on initiate. An
	// empty secret is no proof of possession, so it is rejected at write time.
	ErrClaimSecretEmpty = errors.New("device claim secret must not be empty")
)

// InitiateDeviceClaim opens (or reopens) a possession claim on a device: the
// platform sets the secret the eventual owner must present. A device has a single
// claim row, so re-initiating a previously claimed/canceled device reopens it
// (e.g. after resale), clearing the prior redemption record.
func (api *Api) InitiateDeviceClaim(ctx context.Context, request *DeviceClaimInitiateRequest) (*DeviceClaim, error) {
	// An empty secret is no proof of possession (review #5): reject it rather than
	// persist a claim anyone can redeem.
	if strings.TrimSpace(request.ClaimSecret) == "" {
		return nil, ErrClaimSecretEmpty
	}

	devices, err := api.DevicesByToken(ctx, []string{request.DeviceToken})
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	secretDigest, err := api.digestClaimSecret(devices[0].TenantId, request.ClaimSecret)
	if err != nil {
		return nil, err
	}

	expiresAt, err := parseOptionalTime(request.ExpiresAt)
	if err != nil {
		return nil, err
	}

	existing, err := api.deviceClaimByDeviceId(ctx, devices[0].ID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		// Reopening a CLAIMED claim is a resale. Revoke the prior owner's edge and
		// reopen in one transaction (review #2): without the revoke the previous
		// customer keeps ownership of a device they sold.
		var queued ownershipQueued
		err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			if ClaimStatus(existing.Status) == ClaimStatusClaimed && existing.ClaimedByCustomerId.Valid {
				if err := tx.Where(
					"source_type = ? AND source_id = ? AND target_type = ? AND target_id = ?",
					string(entity.TypeDevice), existing.DeviceId,
					string(entity.TypeCustomer), uint(existing.ClaimedByCustomerId.Int64),
				).Delete(&EntityRelationship{}).Error; err != nil {
					return err
				}
			}
			existing.ClaimSecretDigest = secretDigest
			existing.Status = string(ClaimStatusOpen)
			existing.ExpiresAt = expiresAt
			existing.ClaimedTime = sql.NullTime{}
			existing.ClaimedByCustomerId = sql.NullInt64{}
			if err := tx.Save(existing).Error; err != nil {
				return err
			}
			// The prior owner's edge is gone: device-state is owed the device's new owners.
			var err error
			queued, err = queueOwnershipOnTx(tx, []uint{existing.DeviceId})
			return err
		})
		if err != nil {
			return nil, err
		}
		// The prior owner's edge is gone: the device's cached tracked set still lists it.
		api.evictRelationshipSources(ctx, queued)
		return existing, nil
	}

	created := &DeviceClaim{
		Device:            devices[0],
		ClaimSecretDigest: secretDigest,
		Status:            string(ClaimStatusOpen),
		ExpiresAt:         expiresAt,
	}
	if err := api.RDB.DB(ctx).Create(created).Error; err != nil {
		return nil, err
	}
	return created, nil
}

// errNoClaimSecretKey refuses a claim carrying a secret when no EnrollmentKeys is wired:
// nothing falls back to storing the secret.
var errNoClaimSecretKey = errors.New("device claim secrets cannot be stored: no enrollment secret key is configured")

// digestClaimSecret is the stored form of a claim secret: its keyed digest, bound to the
// claim's tenant. The secret is digested EXACTLY AS SENT, whitespace included, because the
// owner presents it byte for byte. One over credential.MaxEnrollmentSecretBytes is refused
// with LIMIT_EXCEEDED, as the plaintext column's width did before it.
func (api *Api) digestClaimSecret(tenant, secret string) (sql.NullString, error) {
	if len(secret) > credential.MaxEnrollmentSecretBytes {
		return sql.NullString{}, limit.Exceeded("claimSecret bytes", len(secret), credential.MaxEnrollmentSecretBytes)
	}
	return api.digestStoredClaimSecret(tenant, secret)
}

// digestStoredClaimSecret digests a secret already at rest, which the length limit on a SET
// does not apply to: the old column bounded it at 256 characters, up to four bytes each.
func (api *Api) digestStoredClaimSecret(tenant, secret string) (sql.NullString, error) {
	if api.EnrollmentKeys == nil {
		return sql.NullString{}, errNoClaimSecretKey
	}
	digest, err := api.EnrollmentKeys.Claim.Digest(tenant, secret)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: digest, Valid: true}, nil
}

// claimSecretState is what a claim's secret is, as ClaimDevice reads it: the digest to verify
// against, and the two stored columns it was decided from, for the redemption to be written
// against exactly those.
type claimSecretState struct {
	// digest is the digest the presented secret is verified against.
	digest sql.NullString
	// storedDigest and legacy are the claim_secret_digest and claim_secret columns as read.
	storedDigest sql.NullString
	legacy       sql.NullString
}

// claimSecretOf decides which secret a claim holds.
//
// 🔴 A NON-BLANK PLAINTEXT IS AUTHORITATIVE. During a rolling upgrade an old-version pod opens
// and re-opens claims by writing claim_secret and leaving claim_secret_digest alone. A claim it
// RE-OPENED therefore holds the new secret in the plaintext column and the SUPERSEDED secret's
// digest beside it; verifying against the digest would let the secret the owner was told to
// stop using redeem, and refuse the new one. A claim it OPENED has no digest at all. Either
// way the plaintext is the later write, so it is digested here and verified against. Outside
// an upgrade's overlap the column is always NULL and the stored digest is the answer.
func (api *Api) claimSecretOf(ctx context.Context, claim *DeviceClaim) (claimSecretState, error) {
	state := claimSecretState{digest: claim.ClaimSecretDigest, storedDigest: claim.ClaimSecretDigest}
	var rows []struct {
		ClaimSecret sql.NullString
	}
	if err := api.RDB.DB(ctx).Model(&DeviceClaim{}).Select("claim_secret").
		Where("id = ?", claim.ID).Scan(&rows).Error; err != nil {
		return state, err
	}
	if len(rows) != 1 {
		return state, nil
	}
	state.legacy = rows[0].ClaimSecret
	if state.legacy.Valid && strings.TrimSpace(state.legacy.String) != "" {
		d, err := api.digestStoredClaimSecret(claim.TenantId, state.legacy.String)
		if err != nil {
			return state, err
		}
		state.digest = d
	}
	return state, nil
}

// evaluateDeviceClaim verifies an open claim can be redeemed with the presented
// secret: status OPEN, not expired, and the secret matches. Pure (no I/O) so the
// policy is unit-testable in isolation; now is supplied by the caller. key is the
// enrollment secret key the claim's digest was made under; nil fails closed.
func evaluateDeviceClaim(key *credential.DeviceSecretKey, claim *DeviceClaim, presentedSecret string, now time.Time) error {
	if ClaimStatus(claim.Status) != ClaimStatusOpen {
		return ErrClaimNotOpen
	}
	if claim.ExpiresAt.Valid && !now.Before(claim.ExpiresAt.Time) {
		return ErrClaimExpired
	}
	// No stored digest is never a valid proof (review #5, defense in depth): a claim with
	// no secret is redeemed by nothing, an empty presented secret included.
	// InitiateDeviceClaim rejects empty secrets, so this only guards rows that predate
	// that check.
	if !claim.ClaimSecretDigest.Valid || presentedSecret == "" {
		return ErrClaimSecretMismatch
	}
	// Constant-time compare of the presented secret's digest to the stored one, so the
	// secret does not leak via timing; a digest made under another key, another tenant's
	// or a malformed one is a mismatch. Gated by an exemption in
	// hack/check-credential-compare.sh.
	if credential.VerifyDeviceSecret(key, claim.TenantId, claim.ClaimSecretDigest.String, presentedSecret) != nil {
		return ErrClaimSecretMismatch
	}
	return nil
}

// ClaimDevice assigns a device to a customer on proof of possession: it verifies
// the device's open, unexpired claim secret, creates a device→customer
// relationship of the requested type, and consumes the claim (the secret is
// one-time). now is supplied by the caller so expiry is deterministic in tests.
//
// The consume and the edge create commit in ONE transaction, and the consume is a
// guarded compare-and-swap (UPDATE … WHERE status = OPEN) that is the
// authoritative one-time check (review #1): a concurrent redemption, or a replay
// after a partial failure, affects zero rows and is rejected — so a single secret
// can never spend into two ownership edges, and the device is never left owned but
// still redeemable. evaluateDeviceClaim is kept as a fast pre-check for clear
// errors; the CAS is what enforces the invariant.
func (api *Api) ClaimDevice(ctx context.Context, request *DeviceClaimRequest, now time.Time) (*EntityRelationship, error) {
	claim, err := api.DeviceClaimByDeviceToken(ctx, request.DeviceToken)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrClaimNotFound
		}
		return nil, err
	}
	state, err := api.claimSecretOf(ctx, claim)
	if err != nil {
		return nil, err
	}
	// Verify against the secret the claim actually holds (see claimSecretOf).
	verified := *claim
	verified.ClaimSecretDigest = state.digest
	var claimKey *credential.DeviceSecretKey
	if api.EnrollmentKeys != nil {
		claimKey = api.EnrollmentKeys.Claim
	}
	if err := evaluateDeviceClaim(claimKey, &verified, request.ClaimSecret, now); err != nil {
		return nil, err
	}

	// Resolve (and validate) the target customer + relationship type before the
	// transaction; the customer id also records who claimed.
	customerId, err := api.ResolveEntityToken(ctx, string(entity.TypeCustomer), request.CustomerToken)
	if err != nil {
		return nil, fmt.Errorf("customer: %w", err)
	}
	relType, err := api.claimRelationshipType(ctx, request.RelationshipType)
	if err != nil {
		return nil, err
	}
	rtMatches := []*EntityRelationshipType{relType}

	rel := &EntityRelationship{
		TokenReference: rdb.TokenReference{Token: uuid.New().String()},
		SourceType:     string(entity.TypeDevice),
		SourceId:       claim.DeviceId,
		TargetType:     string(entity.TypeCustomer),
		TargetId:       customerId,
		// Denormalize the target token (ADR-044) so a claimed device's events emit
		// customer-addressed anchors; without it the anchor token is empty and the
		// device's telemetry is unqueryable by its claiming customer.
		TargetToken:        request.CustomerToken,
		RelationshipTypeId: rtMatches[0].ID,
	}
	var queued ownershipQueued
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// 🔴 THE REDEMPTION IS WRITTEN AGAINST THE SECRET IT WAS VERIFIED AGAINST. The compare-and-set
		// holds both secret columns to what was read, so a re-open that lands between the
		// verification and this write (a new pod's, or an old pod's) leaves zero rows affected and
		// the superseded secret spends nothing. It also stores the digest that was verified and
		// clears the plaintext, which is how a claim an old pod opened is converted by being used.
		cas := tx.Model(claim).Where("id = ? AND status = ?", claim.ID, string(ClaimStatusOpen))
		if state.storedDigest.Valid {
			cas = cas.Where("claim_secret_digest = ?", state.storedDigest.String)
		} else {
			cas = cas.Where("claim_secret_digest IS NULL")
		}
		if state.legacy.Valid {
			cas = cas.Where("claim_secret = ?", state.legacy.String)
		} else {
			cas = cas.Where("claim_secret IS NULL")
		}
		res := cas.
			Updates(map[string]interface{}{
				"claim_secret_digest":    state.digest,
				"claim_secret":           nil,
				"status":                 string(ClaimStatusClaimed),
				"claimed_time":           now,
				"claimed_by_customer_id": int64(customerId),
			})
		if res.Error != nil {
			return res.Error
		}
		// Lost the race (or replay): the claim was already redeemed/canceled, so no
		// edge is created and the transaction rolls back.
		if res.RowsAffected == 0 {
			return ErrClaimNotOpen
		}
		// This is the THIRD writer of relationship edges, and it takes a
		// CALLER-CHOSEN relationship type — so redeeming a claim with
		// relationshipType "contains" would create a containment edge that never met
		// the asset hierarchy's structural contract (ADR-072). It is refused here for
		// the same reason CreateEntityRelationship refuses it: a gate with a third
		// door around it is not a gate. A no-op for every other type, which is every
		// type a claim is legitimately redeemed with.
		if err := api.admitContainmentEdge(tx, relType.Token,
			rel.SourceType, rel.SourceId, rel.TargetType, rel.TargetId); err != nil {
			return err
		}
		if err := api.checkTrackedCeiling(ctx, tx, rtMatches[0], rel.SourceType, rel.SourceId, 1); err != nil {
			return err
		}
		if err := tx.Create(rel).Error; err != nil {
			return err
		}
		var err error
		queued, err = queueOwnershipOnTx(tx, []uint{rel.SourceId})
		return err
	})
	if err != nil {
		return nil, err
	}
	// Populate the association from the type we already resolved so a resolver
	// selecting relationshipType on the claim result gets real values (the create
	// path doesn't Preload it).
	rel.RelationshipType = *rtMatches[0]
	// The new owner's edge is on the device: its cached tracked set does not list it yet.
	api.evictRelationshipSources(ctx, queued)
	return rel, nil
}

// claimRelationshipType resolves the type of the edge a claim writes. No type named is
// the built-in tracked "assigned" type, provisioned for the tenant on first use (ADR-081
// decision 5): ownership is a TRACKED edge, so the default must be one. A type the caller
// names is used as given — a caller-defined tracked type is ownership too — and an
// unknown one answers NOT_FOUND as before.
func (api *Api) claimRelationshipType(ctx context.Context, requested *string) (*EntityRelationshipType, error) {
	if requested == nil || *requested == "" {
		return api.EnsureAssignmentType(ctx)
	}
	matches, err := api.EntityRelationshipTypesByToken(ctx, []string{*requested})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return matches[0], nil
}

// CancelDeviceClaim closes an open claim without assigning the device. It returns
// false (no error) when the device has no claim or the claim is not open, so the
// call is idempotent.
func (api *Api) CancelDeviceClaim(ctx context.Context, deviceToken string) (bool, error) {
	claim, err := api.DeviceClaimByDeviceToken(ctx, deviceToken)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if ClaimStatus(claim.Status) != ClaimStatusOpen {
		return false, nil
	}
	claim.Status = string(ClaimStatusCanceled)
	if err := api.RDB.DB(ctx).Save(claim).Error; err != nil {
		return false, err
	}
	return true, nil
}

// DeviceClaimByDeviceToken returns the device's current claim (operator
// visibility), or gorm.ErrRecordNotFound if the device has no claim.
func (api *Api) DeviceClaimByDeviceToken(ctx context.Context, deviceToken string) (*DeviceClaim, error) {
	devices, err := api.DevicesByToken(ctx, []string{deviceToken})
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	claim, err := api.deviceClaimByDeviceId(ctx, devices[0].ID)
	if err != nil {
		return nil, err
	}
	if claim == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return claim, nil
}

// deviceClaimByDeviceId loads the single claim row for a device id, or (nil, nil)
// when none exists. It preloads the device for resolver convenience.
func (api *Api) deviceClaimByDeviceId(ctx context.Context, deviceId uint) (*DeviceClaim, error) {
	found := make([]*DeviceClaim, 0)
	if err := api.RDB.DB(ctx).Preload("Device").Where("device_id = ?", deviceId).Find(&found).Error; err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	return found[0], nil
}

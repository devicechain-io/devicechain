// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Errors returned by the claim operations. They are sentinels so a caller can
// distinguish the failure modes without string matching.
var (
	// ErrClaimNotFound means the device has no claim row at all.
	ErrClaimNotFound = errors.New("device has no claim")
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
			existing.ClaimSecret = request.ClaimSecret
			existing.Status = string(ClaimStatusOpen)
			existing.ExpiresAt = expiresAt
			existing.ClaimedTime = sql.NullTime{}
			existing.ClaimedByCustomerId = sql.NullInt64{}
			return tx.Save(existing).Error
		})
		if err != nil {
			return nil, err
		}
		return existing, nil
	}

	created := &DeviceClaim{
		Device:      devices[0],
		ClaimSecret: request.ClaimSecret,
		Status:      string(ClaimStatusOpen),
		ExpiresAt:   expiresAt,
	}
	if err := api.RDB.DB(ctx).Create(created).Error; err != nil {
		return nil, err
	}
	return created, nil
}

// evaluateDeviceClaim verifies an open claim can be redeemed with the presented
// secret: status OPEN, not expired, and the secret matches. Pure (no I/O) so the
// policy is unit-testable in isolation; now is supplied by the caller.
func evaluateDeviceClaim(claim *DeviceClaim, presentedSecret string, now time.Time) error {
	if ClaimStatus(claim.Status) != ClaimStatusOpen {
		return ErrClaimNotOpen
	}
	if claim.ExpiresAt.Valid && !now.Before(claim.ExpiresAt.Time) {
		return ErrClaimExpired
	}
	// An empty stored secret is never a valid proof (review #5, defense in depth):
	// a constant-time compare of "" == "" would otherwise match an empty presented
	// secret. InitiateDeviceClaim rejects empty secrets, so this only guards rows
	// that predate that check.
	if claim.ClaimSecret == "" {
		return ErrClaimSecretMismatch
	}
	// Constant-time compare to avoid leaking the secret via timing.
	if subtle.ConstantTimeCompare([]byte(presentedSecret), []byte(claim.ClaimSecret)) != 1 {
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
	if err := evaluateDeviceClaim(claim, request.ClaimSecret, now); err != nil {
		return nil, err
	}

	// Resolve (and validate) the target customer + relationship type before the
	// transaction; the customer id also records who claimed.
	customerId, err := api.ResolveEntityToken(ctx, string(entity.TypeCustomer), request.CustomerToken)
	if err != nil {
		return nil, fmt.Errorf("customer: %w", err)
	}
	rtMatches, err := api.EntityRelationshipTypesByToken(ctx, []string{request.RelationshipType})
	if err != nil {
		return nil, err
	}
	if len(rtMatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	rel := &EntityRelationship{
		TokenReference:     rdb.TokenReference{Token: uuid.New().String()},
		SourceType:         string(entity.TypeDevice),
		SourceId:           claim.DeviceId,
		TargetType:         string(entity.TypeCustomer),
		TargetId:           customerId,
		RelationshipTypeId: rtMatches[0].ID,
	}
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&DeviceClaim{}).
			Where("id = ? AND status = ?", claim.ID, string(ClaimStatusOpen)).
			Updates(map[string]interface{}{
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
		return tx.Create(rel).Error
	})
	if err != nil {
		return nil, err
	}
	// Populate the association from the type we already resolved so a resolver
	// selecting relationshipType on the claim result gets real values (the create
	// path doesn't Preload it).
	rel.RelationshipType = *rtMatches[0]
	return rel, nil
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

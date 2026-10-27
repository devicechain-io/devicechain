// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"slices"

	"github.com/devicechain-io/dc-microservice/messaging"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The two operator controls over what self-registration issued. Both take the profile's row
// lock FIRST and the device's second, the order a registration keeps, so neither can deadlock
// with one.

// errResetRetry is internal: a credential from a profile that is not among the profiles locked
// appeared between the reset's unlocked read and its locked one. Deleting it would act on a
// profile this transaction does not hold, so the pass starts again with the right set. It keeps
// the "profile before device" order true to the letter; it is not known to prevent a deadlock
// (none could be constructed without it), and TestProvisionResetWaitsForAnAdoptInFlight...
// is what drives it.
var errResetRetry = errors.New("the device's provisioning changed while it was being reset")

// ResetDeviceProvisioning hard-deletes every credential self-registration issued to the device
// and ends its live broker sessions, returning how many credentials it removed. The EARMARK
// stays, so the unit can register again, which is the way back from a revoke: revocation sticks
// until this is called.
//
// A credential an operator created is not touched. A device that holds none from any profile
// is a no-op returning zero. An unknown device is gorm.ErrRecordNotFound.
//
// A kick that could not be confirmed is recorded (PendingSessionKick) with the delete, so
// repeating the reset asks the broker again even though no credential is left to remove: the
// repeat returns zero and the kick's outcome.
//
// LOCK ORDER: the profiles that issued the credentials, in id order, then the device. Which
// profiles those are is learned from an unlocked read, so after locking the answer is read again
// and, if a credential from a profile that is not locked has appeared, the whole thing is tried
// again (a handful of times) rather than acting on a profile it does not hold.
func (api *Api) ResetDeviceProvisioning(ctx context.Context, deviceToken string) (int, error) {
	const attempts = 3
	for attempt := 0; attempt < attempts; attempt++ {
		removed, tenant, deviceId, err := api.resetDeviceProvisioningOnce(ctx, deviceToken)
		if errors.Is(err, errResetRetry) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if removed > 0 {
			api.evictDeviceCredentials(ctx, tenant, deviceId)
		}
		// Pending kicks are drained whether or not this call removed anything: a retry after a
		// failed kick finds no credentials left, and still owes the broker the kick.
		if kerr := api.drainSessionKicks(ctx, tenant, func(db *gorm.DB) *gorm.DB {
			return db.Where("device_token = ?", deviceToken)
		}); kerr != nil {
			return removed, kerr
		}
		return removed, nil
	}
	return 0, errResetRetry
}

func (api *Api) resetDeviceProvisioningOnce(ctx context.Context, deviceToken string) (removed int, tenant string, deviceId uint, err error) {
	devices, err := api.DevicesByToken(ctx, []string{deviceToken})
	if err != nil {
		return 0, "", 0, err
	}
	if len(devices) == 0 {
		return 0, "", 0, gorm.ErrRecordNotFound
	}
	device := devices[0]

	var profileIds []uint
	if err := api.RDB.DB(ctx).Model(&DeviceCredential{}).
		Where("device_id = ? AND provisioning_profile_id IS NOT NULL", device.ID).
		Distinct().Order("provisioning_profile_id ASC").Pluck("provisioning_profile_id", &profileIds).Error; err != nil {
		return 0, "", 0, err
	}

	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		removed = 0
		if len(profileIds) > 0 {
			var locked []ProvisioningProfile
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id IN ?", profileIds).Order("id ASC").Find(&locked).Error; err != nil {
				return err
			}
		}
		var lockedDevice Device
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", device.ID).First(&lockedDevice).Error; err != nil {
			return err
		}
		var issued []DeviceCredential
		if err := tx.Where("device_id = ? AND provisioning_profile_id IS NOT NULL", device.ID).
			Order("id ASC").Find(&issued).Error; err != nil {
			return err
		}
		if len(issued) == 0 {
			return nil
		}
		ids := make([]uint, 0, len(issued))
		for _, c := range issued {
			if !slices.Contains(profileIds, *c.ProvisioningProfileId) {
				return errResetRetry
			}
			ids = append(ids, c.ID)
		}
		var gone []DeviceCredential
		if err := deleteCredentialsReturningOwners(tx.Unscoped().Where("id IN ?", ids), &gone); err != nil {
			return err
		}
		removed = len(gone)
		// The kick is recorded in the same transaction as the delete, because the credentials
		// are about to be gone and a retry could not rediscover the device from them.
		return api.queueSessionKicksOnTx(tx, device.TenantId, messaging.SessionKickCredentialDeleted, nil, []string{device.Token})
	})
	return removed, device.TenantId, device.ID, err
}

// RevokeProvisionedDevices disables every credential the profile issued that is still enabled,
// ends the live broker sessions of the devices that held them, and returns how many credentials
// it disabled. The compromise answer: together with RotateProvisioningProfileSecret it shuts
// out whoever holds the fleet secret AND whatever it already registered. New registrations
// against the profile are untouched; a revoked device stays refused until its provisioning is
// reset.
//
// 🔴 THE PROFILE'S ROW LOCK IS TAKEN FIRST and held to the commit. A registration against the
// same profile holds it for the whole of its transaction, so one either committed before this
// read its credentials (and is revoked with the rest) or starts after this commits (and finds
// the revoke already in force). Without it a registration that was mid-flight would issue a
// credential this call never saw, and the operator would be told the fleet was revoked.
//
// A failure to confirm the sessions ended is ErrSessionsNotEnded: the revoke is saved, the
// count is the number disabled, and repeating the call asks again.
func (api *Api) RevokeProvisionedDevices(ctx context.Context, profileToken string) (int, error) {
	var disabled []DeviceCredential
	var profileId uint
	var tenant string
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		disabled = disabled[:0]
		profile, err := lockProfileByToken(tx, profileToken)
		if err != nil {
			return err
		}
		profileId, tenant = profile.ID, profile.TenantId
		if err := tx.Clauses(clause.Returning{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "device_id"}}}).
			Model(&disabled).
			Where("provisioning_profile_id = ? AND enabled = ?", profile.ID, true).
			Update("enabled", false).Error; err != nil || len(disabled) == 0 {
			return err
		}
		// Record the kick with the disable, in the same transaction: a repeat finds nothing
		// enabled, and must still know which devices' sessions were never confirmed ended.
		ids := make([]uint, 0, len(disabled))
		for _, c := range disabled {
			ids = append(ids, c.DeviceId)
		}
		var tokens []string
		if err := tx.Unscoped().Model(&Device{}).Where("id IN ?", ids).Pluck("token", &tokens).Error; err != nil {
			return err
		}
		return api.queueSessionKicksOnTx(tx, tenant, messaging.SessionKickCredentialChanged, &profileId, tokens)
	})
	if err != nil {
		return 0, err
	}
	for _, c := range disabled {
		api.evictDeviceCredentials(ctx, c.TenantId, c.DeviceId)
	}
	// Drained whether or not this call disabled anything: kicks left pending by an earlier
	// revoke of the profile are retried here, and success means they were all confirmed.
	if kerr := api.drainSessionKicks(ctx, tenant, func(db *gorm.DB) *gorm.DB {
		return db.Where("provisioning_profile_id = ?", profileId)
	}); kerr != nil {
		return len(disabled), kerr
	}
	return len(disabled), nil
}

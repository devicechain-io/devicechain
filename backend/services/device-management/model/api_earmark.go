// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/integrity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrInvalidEarmark refuses a provisioning profile named on a device that cannot be earmarked
// for it: one that does not exist in the tenant, or one of another device type. INVALID_VALUE.
// The sentence it is wrapped with names the profile and, for a type mismatch, both types.
var ErrInvalidEarmark = integrity.NewRefusal(integrity.ClassInvalid,
	"the device cannot be earmarked for this provisioning profile")

// earmarkToken is the provisioning profile token a create names, or "" for none. A blank
// token means none, as a blank gatewayToken does.
func earmarkToken(token *string) string {
	if token == nil {
		return ""
	}
	return strings.TrimSpace(*token)
}

// lockEarmarkProfiles reads the provisioning profiles named by tokens on tx, in id order, each
// FOR SHARE, and returns them by token. A token that names nothing in the tenant is absent from
// the result; the caller refuses it.
//
// 🔴 THE LOCK IS THE POINT, and it is taken BEFORE the device is inserted. A registration locks
// the profile first and then looks for its device by external id, inserting it when none exists;
// an earmarked create inserts a device carrying a foreign key to the profile, which needs a
// share lock on the profile row. Inserting first and locking after (which is what the foreign
// key does on its own) takes the two in the opposite order: the create holds an uncommitted
// device with the external id the registration is about to insert, the registration holds the
// profile the create is waiting for, and neither can proceed. Taking the profile share lock up
// front makes both paths profile-first. Several profiles are locked in ONE statement ordered by
// id, the order ResetDeviceProvisioning takes them in.
//
// The lock is a no-op on the SQLite test harness (see ReplaceDevice).
func lockEarmarkProfiles(tx *gorm.DB, tokens []string) (map[string]*ProvisioningProfile, error) {
	if len(tokens) == 0 {
		return nil, nil
	}
	var found []*ProvisioningProfile
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).
		Where("token IN ?", tokens).Order("id ASC").Find(&found).Error; err != nil {
		return nil, err
	}
	out := make(map[string]*ProvisioningProfile, len(found))
	for _, p := range found {
		out[p.Token] = p
	}
	return out, nil
}

// checkEarmark is the rule for an earmark: the profile exists and is of the device's type.
func checkEarmark(token string, profile *ProvisioningProfile, deviceTypeId uint, deviceTypeToken string) error {
	if profile == nil {
		return fmt.Errorf("%w: provisioning profile %q does not exist", ErrInvalidEarmark, token)
	}
	if profile.DeviceTypeId != deviceTypeId {
		return fmt.Errorf("%w: provisioning profile %q is for a different device type than %q",
			ErrInvalidEarmark, token, deviceTypeToken)
	}
	return nil
}

// lockEarmarkProfile locks and checks the one profile a device is earmarked for, returning it.
func lockEarmarkProfile(tx *gorm.DB, token string, deviceType *DeviceType) (*ProvisioningProfile, error) {
	profiles, err := lockEarmarkProfiles(tx, []string{token})
	if err != nil {
		return nil, err
	}
	profile := profiles[token]
	if err := checkEarmark(token, profile, deviceType.ID, deviceType.Token); err != nil {
		return nil, err
	}
	return profile, nil
}

// x509DeviceTokenPrefix namespaces a token derived from a certificate's common name, as "sp-" and
// "lw-" do for their protocols.
const x509DeviceTokenPrefix = "pv-"

// DeriveX509DeviceToken is the token of a device that registers itself by certificate: a pure
// function of the certificate's common name (the device's external id), so the MQTT client id
// the broker requires, which carries the token, can be computed from the certificate alone and
// checked BEFORE anything is written.
func DeriveX509DeviceToken(commonName string) string {
	return core.DeriveDeviceToken(commonName, x509DeviceTokenPrefix)
}

// x509EarmarkToken applies the rule for a device earmarked for a certificate profile: its token
// must be the derived one, because a profile that adopts the device when its certificate first
// connects pins the MQTT client id to the device's token, and the connecting side can only
// compute the derived token. An omitted token takes the derived value; one that differs is
// refused. The device's external id is the certificate's common name, so it is required. A
// profile that is not a certificate profile leaves the token as given.
func x509EarmarkToken(profile *ProvisioningProfile, token string, externalId *string) (string, error) {
	if profile == nil || profile.TrustAnchorId == nil {
		return token, nil
	}
	cn := ""
	if externalId != nil {
		cn = strings.TrimSpace(*externalId)
	}
	if cn == "" {
		return "", fmt.Errorf("%w: externalId is required for a device earmarked for certificate profile %q: it is the certificate's common name",
			ErrInvalidEarmark, profile.Token)
	}
	derived := DeriveX509DeviceToken(cn)
	switch {
	case strings.TrimSpace(token) == "":
		return derived, nil
	case token != derived:
		return "", fmt.Errorf("%w: a device earmarked for certificate profile %q must have the token %q, derived from its externalId; omit the token to take it",
			ErrInvalidEarmark, profile.Token, derived)
	}
	return token, nil
}

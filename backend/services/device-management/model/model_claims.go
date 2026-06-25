// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// ClaimStatus is the lifecycle of a device's possession claim (ADR-012). A device
// has at most one claim row; re-initiating reopens it (e.g. a resold device).
type ClaimStatus string

const (
	// ClaimStatusOpen means the claim can be redeemed by presenting its secret.
	ClaimStatusOpen ClaimStatus = "OPEN"
	// ClaimStatusClaimed means the claim was redeemed and the device assigned; the
	// one-time secret is spent.
	ClaimStatusClaimed ClaimStatus = "CLAIMED"
	// ClaimStatusCanceled means the operator closed the claim without assigning the
	// device.
	ClaimStatusCanceled ClaimStatus = "CANCELED"
)

// Valid reports whether the status names one of the known states.
func (s ClaimStatus) Valid() bool {
	switch s {
	case ClaimStatusOpen, ClaimStatusClaimed, ClaimStatusCanceled:
		return true
	default:
		return false
	}
}

// String returns the underlying string value.
func (s ClaimStatus) String() string {
	return string(s)
}

// Data required to open (or reopen) a device claim.
type DeviceClaimInitiateRequest struct {
	DeviceToken string
	ClaimSecret string
	ExpiresAt   *string
}

// Data required to redeem a device claim. The caller presents the device, the
// claim secret (proof of possession), and the customer the device should be
// assigned to via a relationship of the given type.
type DeviceClaimRequest struct {
	DeviceToken      string
	ClaimSecret      string
	CustomerToken    string
	RelationshipType string
}

// DeviceClaim is a possession claim on a device (ADR-012): a one-time, optionally
// expiring secret an eventual owner presents to assign the device to a customer.
// It is the device-side half of device→customer claiming — the platform sets the
// secret (often printed on the device or its packaging) and whoever proves
// possession by presenting it redeems the claim into an ownership relationship.
//
// ClaimSecret is stored as-is and verified with a constant-time compare, mirroring
// the DeviceCredential / ProvisioningProfile secret posture (ADR-014); it is never
// exposed on the GraphQL type.
type DeviceClaim struct {
	gorm.Model
	rdb.TenantScoped

	DeviceId            uint
	Device              *Device
	ClaimSecret         string
	Status              string
	ExpiresAt           sql.NullTime
	ClaimedTime         sql.NullTime
	ClaimedByCustomerId sql.NullInt64
}

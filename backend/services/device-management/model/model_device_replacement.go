// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// DeviceReplacement is one append-only record of a physical unit swap (ADR-074):
// the failed hardware was retired and a new unit bound to the SAME logical device
// identity, so the device's events, alarms, relationships, group membership and
// command vocabulary carry forward unbroken.
//
// # Why the record exists at all
//
// The swap is otherwise invisible. Identity is stable by design (ADR-014 separates
// the Device from its rotatable DeviceCredentials), so replacing a unit is, at the
// storage layer, nothing but a credential rotation — and a credential rotation is
// also what a routine key roll looks like. Without this row the two are
// indistinguishable after the fact, which is precisely the "silent credential
// change hides a physical-unit change" that ADR-074 rejects. The row is what makes
// the swap a queryable fact rather than an inference from timestamps.
//
// # It is APPEND-ONLY, and that is enforced by what is absent, not by a comment
//
// There is no update or delete entry point for this type anywhere in the API or the
// GraphQL schema, and it carries no Token — so there is nothing to address a
// re-point at and no partial-update request shape that could reach it. The one
// writer is ReplaceDevice. Deliberately NOT relying on a "don't mutate this"
// convention: an entity with an update mutation is mutable no matter what its
// doc-comment claims.
//
// # What it does NOT carry
//
// No credential SECRET. RetiredCredentialTokens and NewCredentialToken name the
// credential ROWS by their entity tokens, never by CredentialId (which for an
// ACCESS_TOKEN *is* the bearer secret) and never by CredentialValue. An auditor
// reading this journal learns which credentials changed hands; it does not become a
// second place a bearer token can be read out of.
type DeviceReplacement struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	rdb.TenantScoped

	// DeviceId is the logical identity that SURVIVED the swap — the whole point of
	// the operation. It is the same value before and after, which is why the
	// device's event history, alarms, relationship edges and group memberships need
	// no migration: they all key on it.
	DeviceId uint `gorm:"not null;index"`
	Device   *Device

	// OccurredTime is when the replacement was APPLIED, stamped from the server's
	// clock by ReplaceDevice. It is not caller-supplied: a lifecycle record whose
	// instant the caller chooses is forgeable provenance, the same reason a
	// published version's publisher comes from the authenticated subject rather
	// than the request.
	OccurredTime time.Time `gorm:"not null;index"`

	// Actor is the authenticated subject that performed the replacement, taken from
	// the JWT claims by the resolver (never from the request body). Empty only for
	// an unauthenticated caller, which the auth gate already excludes.
	Actor string `gorm:"size:256"`

	// Reason is the operator's free-text note ("water ingress", "RMA 41822"). Purely
	// descriptive; nothing reads it back but a human.
	Reason sql.NullString `gorm:"size:1024"`

	// UnitIdentifier is the incoming physical unit's own identifier — the
	// manufacturer serial or IMEI a field tech reads off the box. It is recorded
	// HERE and nowhere else, and specifically NOT written onto Device.ExternalId:
	// the external id is the stable business key of the logical thing being measured
	// (ADR-049), so a swap must not move it. This column answers "which box is in the
	// field right now" without making that mistake.
	UnitIdentifier sql.NullString `gorm:"size:256"`

	// RetiredCredentialTokens lists the entity tokens of every credential this
	// replacement DISABLED, as a JSON array of strings. The credential rows survive
	// (disabled, not deleted) so the historical binding stays readable; this column
	// is what attributes their retirement to THIS swap rather than to some other
	// rotation.
	RetiredCredentialTokens datatypes.JSON `gorm:"not null"`

	// NewCredentialToken names the credential row minted for the incoming unit.
	NewCredentialToken string `gorm:"not null;size:128"`
	// NewCredentialType is that credential's type, denormalized so a reader does not
	// need a join to see whether the fleet moved (say) from ACCESS_TOKEN to X509.
	NewCredentialType string `gorm:"not null;size:32"`
}

// DefaultOrder implements rdb.Sortable. Newest replacement first — this is a
// journal, and the question asked of it is almost always "what happened most
// recently to this device".
//
// occurred_time is emphatically NOT total: replacing a rack of units in one
// maintenance window writes rows a microsecond apart at best, and ReplaceDevice
// stamps every row in a single call from ONE captured instant. id DESC is the
// unique tiebreak, and it agrees with occurred_time's direction so the two never
// disagree about which of two rows is "later".
func (DeviceReplacement) DefaultOrder() string {
	return "device_replacements.occurred_time DESC, device_replacements.id DESC"
}

// AuditLabel contributes the replaced device's id to the audit journal (ADR-019)
// entry for this row's insert, so "device_replacements #7" reads as the device it
// concerns. It is deliberately the numeric device id and not the device TOKEN: the
// token is customer-chosen and routinely a name or a serial, and the journal's
// label column is one of the two emptied at tenant erasure precisely because it
// carries such values. The id is internal and identifies just as well here.
func (r DeviceReplacement) AuditLabel() string {
	return fmt.Sprintf("device #%d", r.DeviceId)
}

// DeviceReplaceRequest is what a caller sends to swap the physical unit behind a
// device identity (ADR-074).
//
// 🔴 IT CARRIES NO DEVICE IDENTITY FIELDS, AND THAT IS THE DECISION, NOT AN
// OMISSION. There is no Token, no ExternalId, no DeviceTypeToken and no Name here,
// so a replacement CANNOT move the identity, the business key or the profile
// binding — it is unrepresentable rather than merely refused. That is the same line
// DeviceTypeUpdateRequest drew when it dropped Token, and it matters more here:
// "rotate credentials, not identity" is the entire contract of the operation, and a
// contract a request shape can express a violation of is a contract enforced by
// review.
type DeviceReplaceRequest struct {
	// DeviceToken addresses the surviving logical identity. It is the mutation's
	// subject, not a field of it.
	DeviceToken string

	// CredentialType is the type of credential minted for the incoming unit.
	// Optional; defaults to ACCESS_TOKEN, matching provisioning's default.
	CredentialType *string
	// CredentialId is the identifier the new unit will present at connect time (an
	// X.509 thumbprint, an MQTT username). Optional ONLY for ACCESS_TOKEN, where a
	// fresh random id is minted because the id is itself the bearer token; for every
	// other type it is required, because there is no material the server can invent.
	CredentialId *string
	// CredentialValue is the secret material for types that carry one separately
	// (an MQTT password, a certificate PEM). Never returned on read.
	CredentialValue *string
	// CredentialToken is the entity token for the new credential row. Optional; a
	// UUID is minted when absent.
	CredentialToken *string
	// ExpiresAt optionally bounds the new credential's life (RFC3339).
	ExpiresAt *string

	// Reason and UnitIdentifier are the operator's annotations; both optional. See
	// the DeviceReplacement fields of the same names.
	Reason         *string
	UnitIdentifier *string
}

// DeviceReplaceResult is what ReplaceDevice hands back.
//
// NewCredential is returned in full — including CredentialValue when the caller
// supplied one and the CredentialId when it was minted — because this is the ONE
// moment the operator can read the material the incoming unit must be programmed
// with. Nothing about that is special-cased: it is the same object CreateDeviceCredential
// returns, and the GraphQL layer's write-only rule on credentialValue applies to it
// identically.
type DeviceReplaceResult struct {
	// Device is the surviving identity, unchanged.
	Device *Device
	// Replacement is the append-only record this call wrote.
	Replacement *DeviceReplacement
	// NewCredential is the credential minted for the incoming unit.
	NewCredential *DeviceCredential
	// RetiredCredentials are the credential rows this call disabled, in the order
	// they were retired. Empty is a legitimate outcome, not an error: a device may
	// have held no live credential (never provisioned, or already retired by an
	// earlier replacement), and refusing the swap for that would block the exact
	// case of a unit that died before it ever connected.
	RetiredCredentials []*DeviceCredential
}

// DeviceReplacementSearchCriteria locates replacement records.
type DeviceReplacementSearchCriteria struct {
	rdb.Pagination
	// Device filters to one device by its token.
	Device *string
}

// DeviceReplacementSearchResults is a page of replacement records.
type DeviceReplacementSearchResults struct {
	Results    []DeviceReplacement
	Pagination rdb.SearchResultsPagination
}

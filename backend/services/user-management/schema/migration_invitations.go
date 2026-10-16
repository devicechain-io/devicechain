// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// invitationMembershipRef and invitationIdentityRef name tables this migration does not
// own: the primary key only, so a foreign key can reference them. AutoMigrate on an
// existing table with a struct this thin adds nothing and drops nothing. Declared here
// rather than shared with migration_sso.go's refs: snapshots are free to diverge, and a
// shared one would make this migration's meaning depend on another's.
type invitationMembershipRef struct {
	ID uint `gorm:"primarykey"`
}

func (invitationMembershipRef) TableName() string { return "iam_memberships" }

type invitationIdentityRef struct {
	ID uint `gorm:"primarykey"`
}

func (invitationIdentityRef) TableName() string { return "iam_identities" }

// invitation is this migration's snapshot of iam_invitations (ADR-081): a one-time link a
// tenant administrator issues to bring a person into the tenant.
//
//   - tenant_id is the tenant TOKEN, a plain reference like every other iam tenant
//     column; it is what the tenant purge sweeps this table by, with no list to maintain.
//   - membership_id CASCADES: an invitation means nothing without the membership it
//     enables, so removing the membership removes its invitations.
//   - token_digest is the SHA-256 of the link's secret. The secret itself is never
//     stored, so a database read cannot be turned back into a working link.
//   - invited_by is SET NULL on delete: the inviter leaving must neither be refused nor
//     take the invitation with them.
//
// No gorm.Model and no deleted_at: an invitation is revoked by stamping revoked_at, and
// removed only with its membership.
type invitation struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time

	TenantId     string                   `gorm:"not null;size:128;index:idx_iam_invitations_tenant"`
	MembershipID uint                     `gorm:"not null;index:idx_iam_invitations_membership"`
	Membership   *invitationMembershipRef `gorm:"foreignKey:MembershipID;constraint:OnDelete:CASCADE"`
	TokenDigest  []byte                   `gorm:"not null;uniqueIndex:idx_iam_invitations_digest"`
	InvitedBy    *uint                    `gorm:"index:idx_iam_invitations_invited_by"`
	Inviter      *invitationIdentityRef   `gorm:"foreignKey:InvitedBy;constraint:OnDelete:SET NULL"`
	ExpiresAt    time.Time                `gorm:"not null"`
	AcceptedAt   *time.Time
	RevokedAt    *time.Time
}

func (invitation) TableName() string { return "iam_invitations" }

// NewInvitationsMigration creates iam_invitations (ADR-081). Individually re-runnable:
// AutoMigrate creates the table when it is absent and converges nothing when it is
// present.
func NewInvitationsMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261016120200",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&invitation{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("iam_invitations")
		},
	}
}

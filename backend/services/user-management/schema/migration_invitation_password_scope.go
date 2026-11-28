// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// invitationPasswordIdentitySnapshot is this migration's own snapshot of iam_identities: the
// primary key, the two columns the backfill reads, and the one column it adds. A SNAPSHOT,
// never iam.Identity, with the TableName pinned so AutoMigrate alters iam_identities.
type invitationPasswordIdentitySnapshot struct {
	ID           uint `gorm:"primarykey"`
	CreatedAt    time.Time
	PasswordHash string `gorm:"not null;size:256"`

	// PasswordFromInvitation: the password was set through an invitation code, so it proves
	// only that whoever set it held a code some tenant issued. false for every row an older
	// pod inserts (the column default), which is every password but the invitation path's.
	PasswordFromInvitation bool `gorm:"not null;default:false"`
}

func (invitationPasswordIdentitySnapshot) TableName() string { return "iam_identities" }

// invitationPasswordMembershipSnapshot is this migration's own snapshot of iam_memberships.
type invitationPasswordMembershipSnapshot struct {
	ID         uint `gorm:"primarykey"`
	IdentityID uint `gorm:"not null"`

	// AdmitsInvitedPassword: this tenant vouched for whoever set the identity's invitation
	// password (it issued that code, or a code that was accepted by signing in with it).
	AdmitsInvitedPassword bool `gorm:"not null;default:false"`
}

func (invitationPasswordMembershipSnapshot) TableName() string { return "iam_memberships" }

// invitationPasswordInvitationSnapshot is the read-only view of iam_invitations the backfill uses.
type invitationPasswordInvitationSnapshot struct {
	ID           uint `gorm:"primarykey"`
	CreatedAt    time.Time
	MembershipID uint
	AcceptedAt   *time.Time
}

func (invitationPasswordInvitationSnapshot) TableName() string { return "iam_invitations" }

// invitationCreatedIdentityWindow is how close an identity's creation and one of its
// invitations' must be for the backfill to read the identity as created BY that invitation:
// the tenant plane inserts both in one transaction, milliseconds apart.
const invitationCreatedIdentityWindow = time.Minute

// NewInvitationPasswordScopeMigration adds iam_identities.password_from_invitation and
// iam_memberships.admits_invited_password, and marks the passwords that were most likely set
// through an invitation before this release.
//
// The backfill reads an identity as invitation-made when it has a password, holds no system
// role, and was created within a minute of one of its own invitations. Such an identity's
// password is marked, and the memberships it may enter with it are the ones whose
// invitation was accepted, plus the ones with no invitation at all (added by the operator
// before this release). A membership that was invited but never accepted — enabled by hand
// after its invitation was revoked — no longer admits it. Individually re-runnable: the
// columns are added only when absent, and the marks are set, never toggled.
func NewInvitationPasswordScopeMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261127230000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&invitationPasswordIdentitySnapshot{}, &invitationPasswordMembershipSnapshot{}); err != nil {
				return err
			}
			return backfillInvitationPasswords(tx)
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropColumn(&invitationPasswordMembershipSnapshot{}, "admits_invited_password"); err != nil {
				return err
			}
			return tx.Migrator().DropColumn(&invitationPasswordIdentitySnapshot{}, "password_from_invitation")
		},
	}
}

func backfillInvitationPasswords(tx *gorm.DB) error {
	var ids []invitationPasswordIdentitySnapshot
	if err := tx.Where("password_hash <> ''").
		Where("id NOT IN (SELECT identity_id FROM iam_identity_system_roles)").
		Find(&ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		var mems []invitationPasswordMembershipSnapshot
		if err := tx.Where("identity_id = ?", id.ID).Find(&mems).Error; err != nil {
			return err
		}
		if len(mems) == 0 {
			continue
		}
		memIDs := make([]uint, 0, len(mems))
		for _, m := range mems {
			memIDs = append(memIDs, m.ID)
		}
		var invs []invitationPasswordInvitationSnapshot
		if err := tx.Where("membership_id IN ?", memIDs).Find(&invs).Error; err != nil {
			return err
		}
		madeByInvitation := false
		invited := map[uint]bool{}
		accepted := map[uint]bool{}
		for _, inv := range invs {
			d := inv.CreatedAt.Sub(id.CreatedAt)
			if d < 0 {
				d = -d
			}
			if d <= invitationCreatedIdentityWindow {
				madeByInvitation = true
			}
			invited[inv.MembershipID] = true
			if inv.AcceptedAt != nil {
				accepted[inv.MembershipID] = true
			}
		}
		if !madeByInvitation {
			continue
		}
		admit := make([]uint, 0, len(mems))
		for _, m := range mems {
			if accepted[m.ID] || !invited[m.ID] {
				admit = append(admit, m.ID)
			}
		}
		if len(admit) > 0 {
			if err := tx.Model(&invitationPasswordMembershipSnapshot{}).Where("id IN ?", admit).
				Update("admits_invited_password", true).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&invitationPasswordIdentitySnapshot{}).Where("id = ?", id.ID).
			Update("password_from_invitation", true).Error; err != nil {
			return err
		}
	}
	return nil
}

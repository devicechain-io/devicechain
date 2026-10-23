// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ServiceAccount is a tenant's non-human principal: an integration that authenticates at
// the OAuth token endpoint with a client id and secret (the client_credentials grant) and
// receives an ordinary short-lived tenant access token.
//
// It is NOT an Identity. An identity is a person — global, keyed by email, able to sign in,
// to be invited, linked to an identity provider and counted as a tenant's last administrator
// — and an account is none of those, so it is a principal of its own rather than an
// identity every person-only path would have to remember to exclude. Nor is it an
// OAuthClient, which is the operator's platform-level registration; an account belongs to
// exactly one tenant, whose administrators define it.
//
// TenantId is the owning tenant's TOKEN, stamped from the creating administrator's own
// access token, never from a request. It is what the tenant purge sweeps the table by. The
// account's roles are tenant-scope roles assignable in that tenant (the catalogue's and its
// own), and Scope bounds what it sees exactly as a membership's does: NIL is the whole
// tenant, never stored empty.
//
// No gorm.Model: an account is deleted, not soft-deleted, so its (tenant, token) is free
// for reuse at once — and reuse is safe because ClientId is random and never reused, so a
// secret issued to the old account authenticates nothing for the new one.
type ServiceAccount struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	rdb.NamedEntity

	TenantId string `gorm:"not null;size:128;uniqueIndex:idx_iam_service_accounts_tenant_token,priority:1"`
	Token    string `gorm:"not null;size:128;uniqueIndex:idx_iam_service_accounts_tenant_token,priority:2"`
	ClientId string `gorm:"not null;size:64;uniqueIndex:idx_iam_service_accounts_client_id"`
	// Enabled carries no gorm default, for the reason Identity.Enabled does not: gorm would
	// omit an explicit false from a Create and let a column default win.
	Enabled bool     `gorm:"not null"`
	Scope   []string `gorm:"type:jsonb;serializer:json"`
	// CreatedBy is the administrator who created the account (their token's subject), kept
	// for the record.
	CreatedBy string `gorm:"size:256"`

	Roles   []Role                 `gorm:"many2many:iam_service_account_roles;"`
	Secrets []ServiceAccountSecret `gorm:"constraint:OnDelete:CASCADE"`
}

func (ServiceAccount) TableName() string { return "iam_service_accounts" }

// AuditLabel names the account in the audit journal by its token.
func (a ServiceAccount) AuditLabel() string { return a.Token }

// IsScoped reports whether the account is limited to part of its tenant.
func (a *ServiceAccount) IsScoped() bool { return len(a.Scope) > 0 }

// Authorities is the deduped union of the account's roles' authorities: exactly what its
// tokens carry. There is no viewer baseline for an account.
func (a *ServiceAccount) Authorities() []string { return unionAuthorities(a.Roles) }

// ServiceAccountSecret is one client secret of a service account. The row holds a KEYED
// DIGEST of the secret (credential.DeriveServiceAccountSecretKey), never the secret, and a
// four-character hint so an administrator can tell two secrets apart; the secret itself is
// shown once, when it is minted, and is unrecoverable afterwards.
//
// Every secret expires. Rotation mints a new one and pulls the older ones' expiry in to the
// end of the overlap, so both work until then and only the new one after.
type ServiceAccountSecret struct {
	ID               uint `gorm:"primarykey"`
	CreatedAt        time.Time
	ServiceAccountID uint      `gorm:"not null;index:idx_iam_service_account_secrets_account"`
	Digest           string    `gorm:"not null;size:128" json:"-"`
	Hint             string    `gorm:"not null;size:8"`
	ExpiresAt        time.Time `gorm:"not null"`
	LastUsedAt       *time.Time
}

func (ServiceAccountSecret) TableName() string { return "iam_service_account_secrets" }

// Live reports whether the secret can still authenticate at now.
func (s *ServiceAccountSecret) Live(now time.Time) bool { return now.Before(s.ExpiresAt) }

// ---- store ----------------------------------------------------------------------------------

// preloadAccount loads what every reader of an account needs.
func preloadAccount(db *gorm.DB) *gorm.DB {
	return db.Preload("Roles").Preload("Secrets", func(db *gorm.DB) *gorm.DB {
		return db.Order("created_at DESC").Order("id DESC")
	})
}

// TenantServiceAccounts returns one page of a tenant's service accounts, ordered by token.
func (s *Store) TenantServiceAccounts(ctx context.Context, tenant string, pag rdb.Pagination) ([]ServiceAccount, rdb.SearchResultsPagination, error) {
	q := s.sys(ctx).Model(&ServiceAccount{}).Where("tenant_id = ?", tenant)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, rdb.SearchResultsPagination{}, err
	}
	var rows []ServiceAccount
	if err := preloadAccount(q).Order("token").Scopes(rdb.Paginate(pag)).Find(&rows).Error; err != nil {
		return nil, rdb.SearchResultsPagination{}, err
	}
	size := pag.EffectivePageSize()
	page := pag.PageNumber
	if page < 1 {
		page = 1
	}
	start := (page-1)*size + 1
	end := start + int32(len(rows)) - 1
	if len(rows) == 0 {
		end = start - 1
	}
	return rows, rdb.SearchResultsPagination{PageStart: start, PageEnd: end, TotalRecords: int32(total)}, nil
}

// CountTenantServiceAccounts counts a tenant's service accounts.
func (s *Store) CountTenantServiceAccounts(ctx context.Context, tenant string) (int64, error) {
	var n int64
	err := s.sys(ctx).Model(&ServiceAccount{}).Where("tenant_id = ?", tenant).Count(&n).Error
	return n, err
}

// TenantServiceAccount resolves one of a tenant's accounts by token, or
// gorm.ErrRecordNotFound. Another tenant's account of the same token is never returned.
func (s *Store) TenantServiceAccount(ctx context.Context, tenant, token string) (*ServiceAccount, error) {
	var a ServiceAccount
	if err := preloadAccount(s.sys(ctx)).Where("tenant_id = ? AND token = ?", tenant, token).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// LockTenantServiceAccount is TenantServiceAccount reading the row FOR UPDATE, so two
// rotations of one account serialize on it.
func (s *Store) LockTenantServiceAccount(ctx context.Context, tenant, token string) (*ServiceAccount, error) {
	var a ServiceAccount
	if err := s.sys(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND token = ?", tenant, token).First(&a).Error; err != nil {
		return nil, err
	}
	return s.TenantServiceAccount(ctx, tenant, token)
}

// ServiceAccountByClientId resolves an account by its client id, across tenants: the token
// endpoint has nothing else to go on. gorm.ErrRecordNotFound when none has it.
func (s *Store) ServiceAccountByClientId(ctx context.Context, clientID string) (*ServiceAccount, error) {
	var a ServiceAccount
	if err := preloadAccount(s.sys(ctx)).Where("client_id = ?", clientID).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// CreateServiceAccount inserts an account with its roles and first secret.
func (s *Store) CreateServiceAccount(ctx context.Context, a *ServiceAccount) error {
	if len(a.Scope) == 0 {
		a.Scope = nil
	}
	return s.sys(ctx).Create(a).Error
}

// SetServiceAccountAccess replaces an account's roles and scope. An empty scope is stored as
// NULL, the whole tenant.
func (s *Store) SetServiceAccountAccess(ctx context.Context, a *ServiceAccount, roles []Role, scope []string) error {
	if len(scope) == 0 {
		scope = nil
	}
	db := s.sys(ctx)
	if err := db.Model(a).Association("Roles").Replace(roles); err != nil {
		return err
	}
	// Select names the column so a nil scope is written, which a struct update would skip.
	if err := db.Model(a).Select("scope").Updates(&ServiceAccount{Scope: scope}).Error; err != nil {
		return err
	}
	a.Roles, a.Scope = roles, scope
	return nil
}

// SetServiceAccountEnabled enables or disables an account.
func (s *Store) SetServiceAccountEnabled(ctx context.Context, a *ServiceAccount, enabled bool) error {
	if err := s.sys(ctx).Model(a).Select("enabled").Updates(map[string]any{"enabled": enabled}).Error; err != nil {
		return err
	}
	a.Enabled = enabled
	return nil
}

// AddServiceAccountSecret inserts a secret for an account.
func (s *Store) AddServiceAccountSecret(ctx context.Context, sec *ServiceAccountSecret) error {
	return s.sys(ctx).Create(sec).Error
}

// ExpireServiceAccountSecret pulls a secret's expiry in to at, never pushing it out.
func (s *Store) ExpireServiceAccountSecret(ctx context.Context, secretID uint, at time.Time) error {
	return s.sys(ctx).Model(&ServiceAccountSecret{ID: secretID}).
		Where("id = ? AND expires_at > ?", secretID, at).Update("expires_at", at).Error
}

// TouchServiceAccountSecret records that a secret was used at now, at most once per
// minute per secret, so a busy integration does not write on every mint.
func (s *Store) TouchServiceAccountSecret(ctx context.Context, secretID uint, now time.Time) error {
	return s.sys(ctx).Model(&ServiceAccountSecret{ID: secretID}).
		Where("id = ? AND (last_used_at IS NULL OR last_used_at < ?)", secretID, now.Add(-time.Minute)).
		Update("last_used_at", now).Error
}

// DeleteServiceAccount removes an account, its secrets and its role assignments.
func (s *Store) DeleteServiceAccount(ctx context.Context, a *ServiceAccount) error {
	return s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM iam_service_account_roles WHERE service_account_id = ?", a.ID).Error; err != nil {
			return err
		}
		if err := tx.Where("service_account_id = ?", a.ID).Delete(&ServiceAccountSecret{}).Error; err != nil {
			return err
		}
		return tx.Delete(a).Error
	})
}

// ServiceAccountsHoldingRole counts the accounts that hold a role.
func (s *Store) ServiceAccountsHoldingRole(ctx context.Context, roleID uint) (int64, error) {
	var n int64
	err := s.sys(ctx).Model(&ServiceAccount{}).
		Where("id IN (SELECT service_account_id FROM iam_service_account_roles WHERE role_id = ?)", roleID).
		Count(&n).Error
	return n, err
}

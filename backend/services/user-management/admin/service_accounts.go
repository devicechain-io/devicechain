// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"gorm.io/gorm"
)

// A tenant's SERVICE ACCOUNTS, administered on its own plane under the rules its members
// are (tenant_plane.go): the caller and its tenant come from the verified access token and
// nowhere else, a scoped caller is refused, and nothing may be granted that the caller does
// not hold.
//
//   - ESCALATION — every role assigned to an account may carry only authorities the caller
//     holds ("*" only by a holder of "*"), and acting on an existing account — changing,
//     disabling, rotating or deleting it — requires holding everything it holds, so a
//     delegated administrator cannot take over an integration above them by rotating its
//     secret.
//   - An account cannot administer this plane itself. resolveActor reads the caller as a
//     PERSON, and an account's subject names none, so every method here refuses it.
//
// The secret is minted here and returned once; only its keyed digest is stored.

// Service-account limits.
const (
	// MaxServiceAccountsPerTenant bounds how many accounts one tenant may hold.
	MaxServiceAccountsPerTenant = 200
	// DefaultSecretLifetimeDays and MaxSecretLifetimeDays bound a secret's lifetime. Every
	// secret expires: an integration that is forgotten stops working rather than holding a
	// credential nobody reviews.
	DefaultSecretLifetimeDays = 365
	MaxSecretLifetimeDays     = 730
	// DefaultRotationOverlapHours and MaxRotationOverlapHours bound how long the previous
	// secret keeps working after a rotation. Zero ends it at once.
	DefaultRotationOverlapHours = 24
	MaxRotationOverlapHours     = 168
	// maxNameLen and maxDescriptionLen are the column sizes.
	maxNameLen        = 128
	maxDescriptionLen = 1024
)

// ServiceAccountSecretPrefix and ServiceAccountClientIdPrefix begin every minted secret and
// client id, so a secret pasted somewhere it should not be is recognizable to a scanner.
const (
	ServiceAccountSecretPrefix   = "dcsas_"
	ServiceAccountClientIdPrefix = "dcsa_"
)

// ErrServiceAccountNotFound is the refusal for an account this tenant does not have —
// including another tenant's account of the same token.
var ErrServiceAccountNotFound = rdb.NewNotFound("service account not found")

// ErrServiceAccountTokenTaken refuses an account at a token the tenant already uses.
var ErrServiceAccountTokenTaken error = conflict.New("a service account with this token already exists")

// errNoServiceAccountKey fails a mint on a service built without the digest key — loudly,
// because the alternative is a secret that could never verify.
var errNoServiceAccountKey = errors.New("admin: no service account secret key is configured")

// SetServiceAccountSecretKey installs the key secrets are digested under
// (credential.DeriveServiceAccountSecretKey). Without it no secret can be minted.
func (s *Service) SetServiceAccountSecretKey(k *credential.DeviceSecretKey) { s.saKey = k }

// ServiceAccountInput is an account to create.
type ServiceAccountInput struct {
	Token         string
	Name          string
	Description   string
	RoleTokens    []string
	Scope         []ScopeAnchorInput
	ExpiresInDays *int32
}

// MintedSecret is a secret as it is minted: the cleartext, shown once, and its row.
type MintedSecret struct {
	Secret string
	Row    iam.ServiceAccountSecret
}

// ServiceAccounts returns one page of the caller's tenant's accounts.
func (s *Service) ServiceAccounts(ctx context.Context, pag rdb.Pagination) ([]iam.ServiceAccount, rdb.SearchResultsPagination, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return nil, rdb.SearchResultsPagination{}, err
	}
	return s.iam.TenantServiceAccounts(ctx, caller.Tenant, pag)
}

// ServiceAccount returns one of the caller's tenant's accounts by token.
func (s *Service) ServiceAccount(ctx context.Context, token string) (*iam.ServiceAccount, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return nil, err
	}
	return loadTenantAccount(ctx, s.iam, caller.Tenant, token)
}

func loadTenantAccount(ctx context.Context, tx *iam.Store, tenant, token string) (*iam.ServiceAccount, error) {
	a, err := tx.TenantServiceAccount(ctx, tenant, token)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrServiceAccountNotFound
	}
	return a, err
}

// requireHoldsAccount refuses acting on an account that holds anything the caller does not.
func requireHoldsAccount(a actor, acct *iam.ServiceAccount) error {
	if missing := a.held.missingFrom(acct.Authorities()); missing != "" {
		return refuse(CodeEscalation, "you cannot change a service account that holds %q, which you do not hold", missing)
	}
	return nil
}

// accountChange runs fn for one of the caller's tenant's accounts under the tenant lock,
// after the checks every change shares: the tenant is live, the caller is resolved, the
// account exists here, and it holds nothing the caller does not.
func (s *Service) accountChange(ctx context.Context, token string,
	fn func(tx *iam.Store, a actor, acct *iam.ServiceAccount) error) (*iam.ServiceAccount, error) {
	return s.accountChangeThen(ctx, token, true, fn)
}

// accountChangeThen is accountChange, re-reading the account afterwards only when reload is
// set (a delete has nothing to re-read).
func (s *Service) accountChangeThen(ctx context.Context, token string, reload bool,
	fn func(tx *iam.Store, a actor, acct *iam.ServiceAccount) error) (*iam.ServiceAccount, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out *iam.ServiceAccount
	err = s.iam.InTransaction(ctx, func(tx *iam.Store) error {
		if err := lockLiveTenant(ctx, tx, caller.Tenant); err != nil {
			return err
		}
		a, err := s.resolveActor(ctx, tx, caller)
		if err != nil {
			return err
		}
		acct, err := tx.LockTenantServiceAccount(ctx, caller.Tenant, token)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrServiceAccountNotFound
		}
		if err != nil {
			return err
		}
		if err := requireHoldsAccount(a, acct); err != nil {
			return err
		}
		if err := fn(tx, a, acct); err != nil {
			return err
		}
		if !reload {
			return nil
		}
		out, err = loadTenantAccount(ctx, tx, caller.Tenant, token)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CreateServiceAccount defines an account in the caller's tenant with its first secret,
// returning the account and that secret's cleartext — the only time it is ever readable.
func (s *Service) CreateServiceAccount(ctx context.Context, in ServiceAccountInput) (*iam.ServiceAccount, string, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return nil, "", err
	}
	if err := core.ValidateToken(in.Token); err != nil {
		return nil, "", integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf("service account token %q: %v", in.Token, err))
	}
	if len(in.Name) > maxNameLen {
		return nil, "", limit.Exceeded("name length", len(in.Name), maxNameLen)
	}
	if len(in.Description) > maxDescriptionLen {
		return nil, "", limit.Exceeded("description length", len(in.Description), maxDescriptionLen)
	}
	scope, err := NormalizeScope(in.Scope)
	if err != nil {
		return nil, "", err
	}
	lifetime, err := secretLifetime(in.ExpiresInDays)
	if err != nil {
		return nil, "", err
	}
	clientID, err := newClientId()
	if err != nil {
		return nil, "", err
	}
	minted, err := s.mintSecret(caller.Tenant, time.Now().UTC(), lifetime)
	if err != nil {
		return nil, "", err
	}
	var created *iam.ServiceAccount
	err = s.iam.InTransaction(ctx, func(tx *iam.Store) error {
		if err := lockLiveTenant(ctx, tx, caller.Tenant); err != nil {
			return err
		}
		a, err := s.resolveActor(ctx, tx, caller)
		if err != nil {
			return err
		}
		roles, err := tx.AssignableRolesByTokens(ctx, caller.Tenant, in.RoleTokens)
		if err != nil {
			return err
		}
		if err := requireAssignable(a, roles); err != nil {
			return err
		}
		n, err := tx.CountTenantServiceAccounts(ctx, caller.Tenant)
		if err != nil {
			return err
		}
		if n >= MaxServiceAccountsPerTenant {
			return limit.Exceeded("service accounts", int(n)+1, MaxServiceAccountsPerTenant)
		}
		if _, err := tx.TenantServiceAccount(ctx, caller.Tenant, in.Token); err == nil {
			return ErrServiceAccountTokenTaken
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		acct := &iam.ServiceAccount{
			TenantId: caller.Tenant, Token: in.Token, ClientId: clientID, Enabled: true,
			Scope: scope, CreatedBy: caller.Email, Roles: roles,
			NamedEntity: rdb.NamedEntity{Name: rdb.NullStrOf(&in.Name), Description: rdb.NullStrOf(&in.Description)},
			Secrets:     []iam.ServiceAccountSecret{minted.Row},
		}
		if err := tx.CreateServiceAccount(ctx, acct); err != nil {
			return err
		}
		created, err = loadTenantAccount(ctx, tx, caller.Tenant, in.Token)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	return created, minted.Secret, nil
}

// SetServiceAccountAccess replaces an account's roles and scope.
func (s *Service) SetServiceAccountAccess(ctx context.Context, token string, roleTokens []string, anchors []ScopeAnchorInput) (*iam.ServiceAccount, error) {
	scope, err := NormalizeScope(anchors)
	if err != nil {
		return nil, err
	}
	return s.accountChange(ctx, token, func(tx *iam.Store, a actor, acct *iam.ServiceAccount) error {
		roles, err := tx.AssignableRolesByTokens(ctx, acct.TenantId, roleTokens)
		if err != nil {
			return err
		}
		if err := requireAssignable(a, roles); err != nil {
			return err
		}
		return tx.SetServiceAccountAccess(ctx, acct, roles, scope)
	})
}

// SetServiceAccountEnabled enables or disables an account. A disabled account cannot obtain
// a token; one already issued lives out its (short) lifetime.
func (s *Service) SetServiceAccountEnabled(ctx context.Context, token string, enabled bool) (*iam.ServiceAccount, error) {
	return s.accountChange(ctx, token, func(tx *iam.Store, _ actor, acct *iam.ServiceAccount) error {
		return tx.SetServiceAccountEnabled(ctx, acct, enabled)
	})
}

// RotateServiceAccountSecret mints a new secret for an account and returns its cleartext.
// Every older secret that is still live has its expiry pulled in to the end of the overlap
// — except that at most two secrets are ever live: one already overlapping ends now, so the
// account is left with the new secret and the one it replaced.
func (s *Service) RotateServiceAccountSecret(ctx context.Context, token string, overlapHours, expiresInDays *int32) (*iam.ServiceAccount, string, error) {
	overlap := int32(DefaultRotationOverlapHours)
	if overlapHours != nil {
		overlap = *overlapHours
	}
	if overlap < 0 || overlap > MaxRotationOverlapHours {
		return nil, "", integrity.NewRefusal(integrity.ClassInvalid,
			fmt.Sprintf("overlapHours must be between 0 and %d", MaxRotationOverlapHours))
	}
	lifetime, err := secretLifetime(expiresInDays)
	if err != nil {
		return nil, "", err
	}
	var secret string
	acct, err := s.accountChange(ctx, token, func(tx *iam.Store, _ actor, acct *iam.ServiceAccount) error {
		now := time.Now().UTC()
		minted, err := s.mintSecret(acct.TenantId, now, lifetime)
		if err != nil {
			return err
		}
		overlapEnd := now.Add(time.Duration(overlap) * time.Hour)
		// Secrets arrive newest first. The newest live one is the secret being replaced and
		// keeps the overlap; every other live one ends now.
		kept := false
		for i := range acct.Secrets {
			sec := &acct.Secrets[i]
			if !sec.Live(now) {
				continue
			}
			end := now
			if !kept {
				end, kept = overlapEnd, true
			}
			if err := tx.ExpireServiceAccountSecret(ctx, sec.ID, end); err != nil {
				return err
			}
		}
		minted.Row.ServiceAccountID = acct.ID
		if err := tx.AddServiceAccountSecret(ctx, &minted.Row); err != nil {
			return err
		}
		secret = minted.Secret
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return acct, secret, nil
}

// DeleteServiceAccount removes an account and every secret it has. Returns false when there
// was no such account.
func (s *Service) DeleteServiceAccount(ctx context.Context, token string) (bool, error) {
	_, err := s.accountChangeThen(ctx, token, false, func(tx *iam.Store, _ actor, acct *iam.ServiceAccount) error {
		return tx.DeleteServiceAccount(ctx, acct)
	})
	if errors.Is(err, ErrServiceAccountNotFound) {
		return false, nil
	}
	return err == nil, err
}

// secretLifetime resolves a requested secret lifetime in days.
func secretLifetime(days *int32) (time.Duration, error) {
	d := int32(DefaultSecretLifetimeDays)
	if days != nil {
		d = *days
	}
	if d < 1 || d > MaxSecretLifetimeDays {
		return 0, integrity.NewRefusal(integrity.ClassInvalid,
			fmt.Sprintf("expiresInDays must be between 1 and %d", MaxSecretLifetimeDays))
	}
	return time.Duration(d) * 24 * time.Hour, nil
}

// mintSecret draws a new secret and digests it for tenant.
func (s *Service) mintSecret(tenant string, now time.Time, lifetime time.Duration) (MintedSecret, error) {
	if s.saKey == nil {
		return MintedSecret{}, errNoServiceAccountKey
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return MintedSecret{}, err
	}
	secret := ServiceAccountSecretPrefix + base64.RawURLEncoding.EncodeToString(raw)
	digest, err := s.saKey.Digest(tenant, secret)
	if err != nil {
		return MintedSecret{}, err
	}
	return MintedSecret{Secret: secret, Row: iam.ServiceAccountSecret{
		Digest: digest, Hint: secret[len(secret)-4:], ExpiresAt: now.Add(lifetime),
	}}, nil
}

// newClientId draws a fresh client id: 160 random bits, lower-case base32.
func newClientId() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return ServiceAccountClientIdPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)), nil
}

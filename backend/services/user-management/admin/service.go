// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package admin is the instance-scoped control plane for user-management
// (ADR-033): the application service behind the admin GraphQL API. It manages the
// global identity directory, the per-tenant memberships, the global role catalog,
// and the tenant registry — all of which are instance-global iam entities reached
// through the system context (see iam.Store).
//
// It is the admin-plane counterpart to identity.Manager: where the Manager owns
// authentication (tokens, the signing key, login), this Service owns the
// administration of the principals those tokens describe. Authorization is
// enforced at the GraphQL resolver layer (auth.Authorize on a system authority);
// the Service itself is unauthenticated business logic.
package admin

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/deadletters"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/purge"
)

// Service is the admin control-plane application service. It wraps the iam store
// and (in later phases) adds the cross-cutting logic the raw store should not own:
// password hashing on identity creation and authority validation on role writes.
type Service struct {
	iam *iam.Store

	// settle and tokenHold are the purge coordinator's two windows (ADR-077), carried here
	// so a caller asking what a deletion is waiting on gets an answer derived from the
	// SAME values the coordinator decides on.
	//
	// 🔴 THEY ARE PASSED IN RATHER THAN READ FROM CONFIG HERE, and rather than restated as
	// constants, because a surface that restated them would keep answering confidently
	// after an operator changed one — telling them a deletion finishes at a time it will
	// not. The zero value is honest about not knowing: see TenantDeletionProgress.
	settle    time.Duration
	tokenHold time.Duration

	// dead is the ADR-024 store. Nil is tolerated so a test can build a service without
	// one; every reader answers with an empty page rather than an error, because "this
	// deployment records nothing" and "nothing has failed" look the same to a caller and
	// the honest one is the emptier answer.
	dead *deadletters.Store
}

// NewService builds the admin Service over the iam store, carrying the purge coordinator's
// two windows so deletion progress can be reported from the same values it decides on.
func NewService(store *iam.Store, settle, tokenHold time.Duration,
	dead *deadletters.Store) *Service {
	return &Service{iam: store, settle: settle, tokenHold: tokenHold, dead: dead}
}

// DeadLetters returns a page of work the platform gave up on (ADR-024).
func (s *Service) DeadLetters(ctx context.Context,
	criteria deadletters.SearchCriteria) (*deadletters.SearchResults, error) {
	if s.dead == nil {
		return &deadletters.SearchResults{Results: []deadletters.DeadLetter{}}, nil
	}
	return s.dead.List(ctx, criteria)
}

// DeadLetter returns one record, or (nil, nil) when there is none.
func (s *Service) DeadLetter(ctx context.Context, id uint) (*deadletters.DeadLetter, error) {
	if s.dead == nil {
		return nil, nil
	}
	return s.dead.ByID(ctx, id)
}

// ListIdentities returns the full identity directory (system roles + memberships
// preloaded) for the admin console.
func (s *Service) ListIdentities(ctx context.Context) ([]iam.Identity, error) {
	return s.iam.ListIdentities(ctx)
}

// ListTenants returns every control-plane tenant row (ADR-033).
func (s *Service) ListTenants(ctx context.Context) ([]iam.Tenant, error) {
	return s.iam.ListTenants(ctx)
}

// AuditEvents lists this instance's user-management audit journal for the admin
// console — auth events plus identity/role/tenant/membership administration,
// newest first. Instance-wide (cross-tenant) via the store's system-context read.
func (s *Service) AuditEvents(ctx context.Context, criteria rdb.AuditEventSearchCriteria) (*rdb.AuditEventSearchResults, error) {
	return s.iam.AuditEvents(ctx, criteria)
}

// TenantDeletion reads one deletion record with its per-store ledger lines.
//
// epoch is optional. Nil asks for the token's IN-FLIGHT deletion, which is unambiguous
// because at most one can be in flight at a time; a historical one must name its epoch,
// because a token is reusable and only the pair identifies a deletion.
func (s *Service) TenantDeletion(ctx context.Context, token string, epoch *time.Time) (
	*iam.TenantPurge, []iam.TenantPurgeStore, error) {
	var (
		rec *iam.TenantPurge
		err error
	)
	if epoch != nil {
		rec, err = s.iam.PurgeRecordFor(ctx, token, *epoch)
	} else {
		rec, err = s.iam.PurgeRecordForToken(ctx, token)
	}
	if err != nil {
		return nil, nil, err
	}
	lines, err := s.iam.PurgeStores(ctx, rec.ID)
	if err != nil {
		return nil, nil, err
	}
	return rec, lines, nil
}

// TenantDeletions lists deletion records newest cut first, optionally filtered by completion.
func (s *Service) TenantDeletions(ctx context.Context, completed *bool, limit, offset int) (
	[]iam.TenantPurge, error) {
	return s.iam.PurgeRecords(ctx, completed, limit, offset)
}

// TenantDeletionStores reads one deletion's per-store ledger lines.
func (s *Service) TenantDeletionStores(ctx context.Context, purgeID uint) ([]iam.TenantPurgeStore, error) {
	return s.iam.PurgeStores(ctx, purgeID)
}

// TenantDeletionProgress reports what a deletion is waiting on, through the coordinator's own
// decision function rather than a restatement of it.
//
// A COMPLETED record waits on nothing, and that is checked first rather than left to fall out
// of the arithmetic: a finished purge's lines are all clean and both its windows are long
// elapsed, so the arithmetic would reach the same answer today — but only by coincidence, and
// a completed deletion reporting a countdown would be a plain lie if either window grew.
//
// A Service built with zero windows reports WaitNone with nothing blocking rather than
// pretending the windows have elapsed. That case is not reachable from main, which always
// passes the configured values; it is what a test or a future caller that forgot them gets,
// and reporting "nothing outstanding" is the reading least likely to be mistaken for fact.
func (s *Service) TenantDeletionProgress(rec *iam.TenantPurge, lines []iam.TenantPurgeStore) purge.Progress {
	if rec.CompletedAt != nil {
		return purge.Progress{Awaiting: purge.WaitNone}
	}
	return purge.Waiting(lines, rec.Epoch, time.Now().UTC(), s.settle, s.tokenHold)
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	nats "github.com/nats-io/nats.go"
)

// CodeStepUpSso is the refusal of in-session step-up for a single sign-on.
const CodeStepUpSso = "STEP_UP_SSO"

// ErrStepUpSso distinguishes a session whose factor is managed by an identity provider.
var ErrStepUpSso error = &MfaError{CodeStepUpSso, "step-up is unavailable for single sign-on; sign in through your identity provider"}

// StepUp verifies an authenticator code within the caller's own tenant session (Preview).
// Recovery codes are deliberately never evaluated or redeemed here: a single-use recovery
// credential must remain available to recover a sign-in.
func (m *Manager) StepUp(ctx context.Context, access *auth.Claims, refreshToken, code, source string) (*TokenPair, error) {
	if m.credentials == nil {
		return nil, errNoCredentialChecker
	}
	code = compactCode(code)
	if len(code) == 0 || len(code) > maxMfaCodeLen {
		m.countMfa("totp", "invalid")
		return nil, ErrMfaInvalidCode
	}
	if !m.mfaLimiter.allow(source) {
		m.countMfa("totp", "rate_limited")
		return nil, ErrMfaRateLimited
	}
	var pair *TokenPair
	var failure error
	err := m.credentials.CheckOneTime(ctx, credential.KindTotp, func(ctx context.Context, ot credential.OneTime) error {
		pair, failure = m.stepUp(ctx, ot, access, refreshToken, code, source)
		if failure != nil {
			return credential.ErrMismatch
		}
		return nil
	})
	if errors.Is(err, credential.ErrRequestBudgetExhausted) {
		m.countMfa("totp", "budget")
		return nil, err
	}
	if failure != nil {
		return nil, failure
	}
	if err != nil {
		return nil, ErrMfaUnavailable
	}
	return pair, nil
}

func (m *Manager) stepUp(ctx context.Context, ot credential.OneTime, access *auth.Claims, refreshToken, code, source string) (*TokenPair, error) {
	if access == nil || access.TokenType != auth.TokenTypeAccess || auth.IsAgentOrigin(access) ||
		strings.HasPrefix(access.Username, auth.ServiceAccountSubjectPrefix) {
		return nil, auth.ErrForbidden
	}
	claims, err := m.validator.ValidateRefresh(refreshToken)
	if err != nil || claims.Scope != "" {
		return nil, ErrInvalidToken
	}
	if access.Username != claims.Username || access.Tenant != claims.Tenant || claims.Tenant == "" {
		return nil, auth.ErrForbidden
	}
	if m.refreshKV == nil {
		return nil, ErrRefreshUnavailable
	}
	entry, err := m.refreshKV.Get(claims.ID)
	if errors.Is(err, nats.ErrKeyNotFound) {
		return nil, ErrInvalidToken
	}
	if err != nil {
		return nil, ErrRefreshUnavailable
	}
	id, err := m.sessionIdentity(ctx, claims.Username, claims.SessionEpoch, claims.SessionID)
	if err != nil {
		if isRefreshDenial(err) {
			return nil, sessionRefusal(err)
		}
		return nil, ErrRefreshUnavailable
	}
	rec, err := m.loadSession(claims.SessionID, claims.Username)
	if err != nil {
		return nil, ErrRefreshUnavailable
	}
	if rec.Method != SessionMethodPassword {
		return nil, ErrStepUpSso
	}
	factor, err := m.iam.ActiveMfaFactorFor(ctx, id.ID)
	if errors.Is(err, iam.ErrMfaFactorNotFound) {
		return nil, ErrMfaNotEnabled
	}
	if err != nil {
		return nil, ErrMfaUnavailable
	}
	if codeMethod(code) == "recovery" {
		m.countMfa("totp", "invalid")
		return nil, ErrMfaInvalidCode
	}
	method, result, err := m.checkSecondFactor(ctx, ot, id, factor, code, source)
	m.countMfa(method, result)
	if err != nil {
		return nil, ErrMfaUnavailable
	}
	if result != "ok" {
		return nil, ErrMfaInvalidCode
	}

	// Read AFTER verification, then compare-and-set. A logout or concurrent step-up
	// cannot resurrect a session or overwrite a newer factor time. Never retry a stale write.
	session, err := m.refreshKV.Get(sessionKey(claims.SessionID))
	if err != nil {
		return nil, ErrRefreshUnavailable
	}
	var current sessionRecord
	if err := json.Unmarshal(session.Value(), &current); err != nil {
		return nil, ErrRefreshUnavailable
	}
	rec = &current
	if rec.Email != id.Email || rec.Epoch != id.SessionEpoch || rec.Method != SessionMethodPassword {
		return nil, ErrInvalidToken
	}
	now := m.clock().UTC()
	if rec.MfaAt == nil || now.After(*rec.MfaAt) {
		rec.MfaAt = &now
	}
	rec.Amr = auth.AmrPasswordOTP()
	data, err := json.Marshal(rec)
	if err != nil {
		return nil, ErrRefreshUnavailable
	}
	if _, err := m.refreshKV.Update(sessionKey(claims.SessionID), data, session.Revision()); err != nil {
		return nil, ErrRefreshUnavailable
	}
	// Re-read the live identity and grant, including the just-updated session facts.
	id, err = m.sessionIdentity(ctx, claims.Username, claims.SessionEpoch, claims.SessionID)
	if err != nil {
		return nil, m.refuseRefresh(claims.ID, entry.Revision(), err)
	}
	su := isSuperuser(id)
	mem := findMembership(id.Memberships, claims.Tenant)
	if mem == nil && !su {
		return nil, m.refuseRefresh(claims.ID, entry.Revision(), errNotAMember)
	}
	if err := m.admitRefreshTenant(ctx, claims); err != nil {
		return nil, m.refuseRefresh(claims.ID, entry.Revision(), err)
	}
	roles, authorities, scope, signIn, err := m.resolveTenantGrant(ctx, claims.SessionID, id, claims.Tenant, mem, su)
	if err != nil {
		return nil, m.refuseRefresh(claims.ID, entry.Revision(), err)
	}
	if err := m.refreshKV.Delete(claims.ID, nats.LastRevision(entry.Revision())); err != nil {
		if errors.Is(err, nats.ErrKeyRevisionMismatch) {
			return nil, ErrInvalidToken
		}
		return nil, ErrRefreshUnavailable
	}
	pair, err := m.issueTenantTokens(claims.Tenant, id.Email, auth.SessionEpoch(id.SessionEpoch), claims.SessionID, roles, authorities, scope, su, signIn)
	if err != nil {
		return nil, err
	}
	m.recordAuth(ctx, rdb.AuditOpStepUp, id.Email, claims.Tenant)
	return pair, nil
}

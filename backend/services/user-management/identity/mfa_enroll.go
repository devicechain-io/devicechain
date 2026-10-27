// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// Self-service enrolment, and the operator's reset.
//
// Every self-service operation takes the IDENTITY TOKEN and re-checks the PASSWORD, because
// identity tokens travel: one sits in the hidden field of the MCP consent form, and one held by
// someone who does not know the password must not be enough to change how the account signs in.
// Regenerating the recovery codes and switching the factor off (and confirming an enrolment)
// need a code as well; each is ONE re-authentication presenting two secrets, and spends one unit
// of the request's credential budget (credential.Checker.CheckPasswordAndOneTime).

// DefaultMfaIssuer is the label an authenticator app shows when none is configured.
const DefaultMfaIssuer = "DeviceChain"

// TotpEnrollment is what beginTotpEnrollment hands back: everything an authenticator needs, once.
type TotpEnrollment struct {
	// OtpauthURI is the otpauth:// URI the console renders as a QR code.
	OtpauthURI string
	// Secret is the same seed, base32, for typing into an authenticator by hand.
	Secret string
	// ExpiresAt is when the pending enrolment stops being confirmable.
	ExpiresAt time.Time
}

// MfaStatus is what an identity may read about its own second factor.
type MfaStatus struct {
	TotpEnabled            bool
	EnrolledAt             *time.Time
	RecoveryCodesRemaining int
}

// mfaIdentity resolves the identity an identity token names, refusing a token whose sign-in has
// ended exactly as every other exchange of one does.
func (m *Manager) mfaIdentity(ctx context.Context, identityToken string) (*iam.Identity, error) {
	if m.validator == nil {
		return nil, ErrInvalidToken
	}
	claims, err := m.validator.ValidateIdentity(identityToken)
	if err != nil {
		return nil, ErrInvalidToken
	}
	id, err := m.sessionIdentity(ctx, claims.Email, claims.SessionEpoch, claims.SessionID)
	if err != nil {
		if errors.Is(err, errSessionEnded) {
			return nil, sessionRefusal(err)
		}
		return nil, err
	}
	return id, nil
}

// passwordLookup is the stored hash a re-check of id's password compares against.
func passwordLookup(id *iam.Identity) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return id.PasswordHash, nil }
}

func passwordPrincipal(id *iam.Identity) credential.Principal {
	return credential.Principal{Kind: credential.KindIdentity, ID: id.Email}
}

// checkPassword re-checks id's password. A wrong one is ErrInvalidCredentials, like a sign-in's.
func (m *Manager) checkPassword(ctx context.Context, id *iam.Identity, password string) error {
	if m.credentials == nil {
		return errNoCredentialChecker
	}
	err := m.credentials.Check(ctx, passwordPrincipal(id), password, passwordLookup(id))
	if errors.Is(err, credential.ErrMismatch) {
		return ErrInvalidCredentials
	}
	return err
}

// errCodeNotAccepted is reauthenticate's report that the PASSWORD matched and the one-time
// attempt ran but did not accept the code. The caller holds the detail (how it fared).
var errCodeNotAccepted = errors.New("the code was not accepted")

// reauthenticate is the password-and-code re-check: ONE credential-budget unit for the pair.
// attempt runs only after the password matched, with the one-time capability, and returns nil
// only for an accepted code. A wrong password is ErrInvalidCredentials and attempt never runs;
// a code attempt refused is errCodeNotAccepted; any other failure (a throttle, the budget, an
// unavailable store) passes through.
func (m *Manager) reauthenticate(ctx context.Context, id *iam.Identity, password string,
	attempt func(ctx context.Context, ot credential.OneTime) error) error {
	if m.credentials == nil {
		return errNoCredentialChecker
	}
	ran := false
	err := m.credentials.CheckPasswordAndOneTime(ctx, passwordPrincipal(id), password, passwordLookup(id),
		credential.KindTotp, func(ctx context.Context, ot credential.OneTime) error {
			ran = true
			return attempt(ctx, ot)
		})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, credential.ErrMismatch):
		if !ran {
			return ErrInvalidCredentials
		}
		return errCodeNotAccepted
	}
	return err
}

// BeginTotpEnrollment starts enrolling a TOTP factor: it seals a new seed as a PENDING factor
// (replacing any earlier pending one) and returns it for the authenticator. The factor does not
// gate any sign-in until ConfirmTotpEnrollment proves the authenticator holds it. An identity
// that already has an active factor is refused (ErrMfaAlreadyEnrolled).
func (m *Manager) BeginTotpEnrollment(ctx context.Context, identityToken, password string) (*TotpEnrollment, error) {
	id, err := m.mfaIdentity(ctx, identityToken)
	if err != nil {
		return nil, err
	}
	if err := m.checkPassword(ctx, id, password); err != nil {
		return nil, err
	}
	seed, err := credential.NewTOTPSeed()
	if err != nil {
		return nil, err
	}
	factor, err := m.iam.BeginMfaEnrollment(ctx, m.secrets, id.ID, seed)
	if err != nil {
		if errors.Is(err, iam.ErrMfaAlreadyEnrolled) {
			return nil, ErrMfaAlreadyEnrolled
		}
		return nil, err
	}
	issuer := DefaultMfaIssuer
	if m.mfaIssuer != nil {
		if named := strings.TrimSpace(m.mfaIssuer(ctx)); named != "" {
			issuer = named
		}
	}
	uri, err := credential.OTPAuthURI(issuer, id.Email, seed)
	if err != nil {
		return nil, err
	}
	m.countMfaEnrollment("begun")
	return &TotpEnrollment{
		OtpauthURI: uri,
		Secret:     credential.EncodeTOTPSeed(seed),
		ExpiresAt:  factor.CreatedAt.Add(iam.MfaEnrollmentTTL).UTC(),
	}, nil
}

// ConfirmTotpEnrollment activates the pending factor once the person proves their authenticator
// holds the seed, and returns the recovery codes ONCE: they are stored as digests and cannot be
// shown again. The session that enrolled is not upgraded -- its amr stays what it was.
func (m *Manager) ConfirmTotpEnrollment(ctx context.Context, identityToken, password, code string) (*TotpConfirmation, error) {
	id, err := m.mfaIdentity(ctx, identityToken)
	if err != nil {
		return nil, err
	}
	code = compactCode(code)
	if !credential.IsTOTPCodeShape(code) {
		// Nothing to evaluate. The password is still not checked: a malformed code is not a
		// reason to spend a compare.
		return nil, ErrMfaInvalidCode
	}
	codes, digests, err := newRecoveryCodes(id)
	if err != nil {
		return nil, err
	}
	var confirmErr error
	err = m.reauthenticate(ctx, id, password, func(ctx context.Context, ot credential.OneTime) error {
		confirmErr = m.iam.ConfirmMfaEnrollmentRotating(ctx, m.secrets, id, func(seed []byte) (int64, bool, error) {
			step, ok := ot.VerifyTOTP(seed, code, m.clock(), MfaTOTPSkew)
			return step, ok, nil
		}, digests, m.clock())
		if confirmErr != nil {
			return credential.ErrMismatch
		}
		return nil
	})
	if err != nil && !errors.Is(err, errCodeNotAccepted) {
		return nil, err
	}
	if confirmErr != nil {
		switch {
		case errors.Is(confirmErr, iam.ErrMfaCodeInvalid):
			return nil, ErrMfaInvalidCode
		case errors.Is(confirmErr, iam.ErrMfaEnrollmentVoid), errors.Is(confirmErr, iam.ErrMfaEnrollmentNotPending):
			return nil, confirmErr
		}
		log.Error().Err(confirmErr).Msg("Could not confirm a second-factor enrolment.")
		return nil, ErrMfaUnavailable
	}
	m.recordAuthLabeled(ctx, rdb.AuditOpMfaEnrolled, id.Email, "", "")
	m.countMfaEnrollment("confirmed")
	// The epoch rotated with the activation, so every session started before it is over -- the
	// person who enrols because their password leaked does not leave the thief's session standing.
	// The caller just proved a code, so they get a fresh sign-in that says so, not a forced re-login.
	m.endSessionsBestEffort(id.Email)
	m.recordAuthLabeled(ctx, rdb.AuditOpLogin, id.Email, "", mfaLoginLabelOTP)
	ia, err := m.completeSignIn(ctx, id, auth.AmrPasswordOTP())
	if err != nil {
		return nil, err
	}
	return &TotpConfirmation{RecoveryCodes: codes, Auth: ia}, nil
}

// TotpConfirmation is what confirming an enrolment returns: the recovery codes, once, and the
// caller's REPLACEMENT sign-in (the one they enrolled in has ended).
type TotpConfirmation struct {
	RecoveryCodes []string
	Auth          *IdentityAuth
}

// RegenerateRecoveryCodes replaces the identity's recovery codes with a fresh set and returns
// them once. It needs the password AND a code (a TOTP code or one of the codes being replaced).
func (m *Manager) RegenerateRecoveryCodes(ctx context.Context, identityToken, password, code, source string) ([]string, error) {
	id, err := m.mfaIdentity(ctx, identityToken)
	if err != nil {
		return nil, err
	}
	if err := m.reauthenticateWithCode(ctx, id, password, code, source); err != nil {
		return nil, err
	}
	codes, digests, err := newRecoveryCodes(id)
	if err != nil {
		return nil, err
	}
	if err := m.iam.ReplaceMfaRecoveryCodes(ctx, id.ID, digests); err != nil {
		if errors.Is(err, iam.ErrMfaFactorNotFound) {
			return nil, ErrMfaNotEnabled
		}
		return nil, err
	}
	m.recordAuthLabeled(ctx, rdb.AuditOpMfaRecoveryCodesRegenerated, id.Email, "", "")
	return codes, nil
}

// DisableTotp removes the identity's second factor, its recovery codes and its sealed seed, and
// rotates its session epoch, which ends every session it has (this one included): the person
// signs in again with the password alone. It needs the password AND a code.
func (m *Manager) DisableTotp(ctx context.Context, identityToken, password, code, source string) error {
	id, err := m.mfaIdentity(ctx, identityToken)
	if err != nil {
		return err
	}
	if err := m.reauthenticateWithCode(ctx, id, password, code, source); err != nil {
		return err
	}
	if err := m.iam.DisableMfa(ctx, m.secrets, id); err != nil {
		if errors.Is(err, iam.ErrMfaFactorNotFound) {
			return ErrMfaNotEnabled
		}
		return err
	}
	m.recordAuthLabeled(ctx, rdb.AuditOpMfaDisabled, id.Email, "", "")
	m.countMfaEnrollment("disabled")
	m.endSessionsBestEffort(id.Email)
	return nil
}

// reauthenticateWithCode checks the password and then a TOTP or recovery code against the
// identity's ACTIVE factor, through the same locks a sign-in is checked through.
func (m *Manager) reauthenticateWithCode(ctx context.Context, id *iam.Identity, password, code, source string) error {
	code = compactCode(code)
	if len(code) == 0 || len(code) > maxMfaCodeLen {
		return ErrMfaInvalidCode
	}
	factor, err := m.iam.ActiveMfaFactorFor(ctx, id.ID)
	if err != nil {
		if errors.Is(err, iam.ErrMfaFactorNotFound) {
			// A password check still happens first, so this does not say whether the password
			// was right to someone who does not hold it.
			if perr := m.checkPassword(ctx, id, password); perr != nil {
				return perr
			}
			return ErrMfaNotEnabled
		}
		return err
	}
	var result string
	var attemptErr error
	err = m.reauthenticate(ctx, id, password, func(ctx context.Context, ot credential.OneTime) error {
		var method string
		method, result, attemptErr = m.checkSecondFactor(ctx, ot, id, factor, code, source)
		m.countMfa(method, result)
		if attemptErr != nil || result != "ok" {
			return credential.ErrMismatch
		}
		return nil
	})
	if err != nil && !errors.Is(err, errCodeNotAccepted) {
		return err
	}
	if attemptErr != nil {
		return ErrMfaUnavailable
	}
	if result != "ok" {
		return ErrMfaInvalidCode
	}
	return nil
}

// MfaStatusFor reads the identity's own second-factor status.
func (m *Manager) MfaStatusFor(ctx context.Context, identityToken string) (*MfaStatus, error) {
	id, err := m.mfaIdentity(ctx, identityToken)
	if err != nil {
		return nil, err
	}
	factor, err := m.iam.ActiveMfaFactorFor(ctx, id.ID)
	if err != nil {
		if errors.Is(err, iam.ErrMfaFactorNotFound) {
			return &MfaStatus{}, nil
		}
		return nil, err
	}
	remaining, err := m.iam.MfaRecoveryCodesRemaining(ctx, id.ID)
	if err != nil {
		return nil, err
	}
	return &MfaStatus{TotpEnabled: true, EnrolledAt: factor.ConfirmedAt, RecoveryCodesRemaining: int(remaining)}, nil
}

// newRecoveryCodes mints a fresh set of recovery codes and their digests for id.
func newRecoveryCodes(id *iam.Identity) (codes, digests []string, err error) {
	codes, err = credential.NewRecoveryCodes(MfaRecoveryCodeCount)
	if err != nil {
		return nil, nil, err
	}
	digests = make([]string, 0, len(codes))
	for _, c := range codes {
		d, err := credential.RecoveryCodeDigest(identityKey(id), c)
		if err != nil {
			return nil, nil, err
		}
		digests = append(digests, d)
	}
	return codes, digests, nil
}

// endSessionsBestEffort ends the sign-in records the index lists for email. The epoch has
// already rotated, which is what actually ends them; the records are cleared so they end by name.
func (m *Manager) endSessionsBestEffort(email string) {
	if m.refreshKV == nil {
		return
	}
	if _, err := m.endSessionRecords(email); err != nil {
		log.Warn().Err(err).Msg("Could not clear a sign-in's session records; the epoch has ended them.")
	}
}

// ErrMfaResetNeedsPassword is the operator reset's refusal while failed attempts are
// outstanding and no new password was given (MFA_RESET_NEEDS_PASSWORD).
var ErrMfaResetNeedsPassword error = &MfaError{CodeMfaResetNeedsPassword,
	"the second factor has failed attempts outstanding, which means its password may be in use by someone else; reset it together with a new password"}

// ErrMfaNotEnrolled is the operator reset's refusal for an identity with no second factor.
var ErrMfaNotEnrolled = errors.New("the identity has no second factor to reset")

// ResetIdentityMfa is the operator's removal of an identity's second factor (a lost phone).
// It removes the factor, the recovery codes and the sealed seed, and ends every session the
// identity has.
//
// While the factor has failed attempts outstanding, newPassword is REQUIRED and is set in the
// same transaction: a failed code is evidence that someone is using the password, and removing
// the factor alone would hand them the account. Targeting an identity that holds a system role
// requires a superuser caller.
func (m *Manager) ResetIdentityMfa(ctx context.Context, email, newPassword string) (*iam.Identity, error) {
	target, err := m.iam.IdentityForAdmin(ctx, email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrIdentityNotFound
		}
		return nil, err
	}
	if len(target.SystemRoles) > 0 {
		su, err := m.callerIsSuperuser(ctx)
		if err != nil {
			return nil, err
		}
		if !su {
			return nil, auth.ErrForbidden
		}
	}
	hash := ""
	if newPassword != "" {
		b, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		hash = string(b)
	}
	if err := m.iam.ResetMfa(ctx, m.secrets, target, hash); err != nil {
		switch {
		case errors.Is(err, iam.ErrMfaResetNeedsPassword):
			return nil, ErrMfaResetNeedsPassword
		case errors.Is(err, iam.ErrMfaFactorNotFound):
			return nil, ErrMfaNotEnrolled
		}
		return nil, err
	}
	m.recordAuthLabeled(ctx, rdb.AuditOpMfaReset, actor(ctx), "", target.Email)
	m.countMfaEnrollment("reset")
	m.endSessionsBestEffort(target.Email)
	return target, nil
}

// callerIsSuperuser reports whether the caller named by the request's token holds the superuser
// role NOW (read from the store, not taken from the token).
func (m *Manager) callerIsSuperuser(ctx context.Context) (bool, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok || claims.Username == "" {
		return false, auth.ErrForbidden
	}
	caller, err := m.iam.IdentityByEmail(ctx, claims.Username)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, auth.ErrForbidden
		}
		return false, err
	}
	return isSuperuser(caller), nil
}

// MfaEnabled reports whether the identity has an active second factor, for the admin API.
func (m *Manager) MfaEnabled(ctx context.Context, id *iam.Identity) (bool, error) {
	_, err := m.iam.ActiveMfaFactorFor(ctx, id.ID)
	if err != nil {
		if errors.Is(err, iam.ErrMfaFactorNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// MfaSourceFailureSweeper prunes idle per-source failure rows. A row is otherwise pruned only
// when a sign-in succeeds, so a factor that is attacked and then left alone would keep its rows.
type MfaSourceFailureSweeper struct {
	*core.PeriodicTask
	store *iam.Store
}

// NewMfaSourceFailureSweeper builds the sweeper.
func NewMfaSourceFailureSweeper(ms *core.Microservice, store *iam.Store, interval time.Duration,
	callbacks core.LifecycleCallbacks) *MfaSourceFailureSweeper {
	s := &MfaSourceFailureSweeper{store: store}
	s.PeriodicTask = core.NewPeriodicTask(ms.FunctionalArea, "mfa-source-failure-sweep", interval,
		s.RunOnce, callbacks, core.WithDetachedContext(),
		core.WithPassMetrics(ms.NewPeriodicTaskMetrics("mfa_source_failure_sweep")))
	return s
}

// RunOnce deletes the per-source rows idle for longer than iam.MfaSourceFailureRetention.
func (s *MfaSourceFailureSweeper) RunOnce(ctx context.Context) error {
	n, err := s.store.PruneStaleMfaSourceFailures(ctx, time.Now().Add(-iam.MfaSourceFailureRetention))
	if err != nil {
		return err
	}
	if n > 0 {
		log.Info().Int64("removed", n).Msg("Pruned idle second-factor source-failure rows.")
	}
	return nil
}

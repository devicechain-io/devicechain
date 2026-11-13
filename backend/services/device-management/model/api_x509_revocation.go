// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"gorm.io/gorm"
)

// MaxManualRevocationsPerTenant is the default bound on the manual revocation entries one tenant
// holds (RevocationLimits.MaxManualEntriesPerTenant, fixed configuration
// callout.revocation.maxManualEntriesPerTenant).
const MaxManualRevocationsPerTenant = 100_000

// maxRevocationLookupKeys bounds the identities one connect asks about. A verified path holds at
// most x509trust.MaxPresentedCerts certificates, and a device can steer only how many verified
// paths exist, not make them more than a handful; a query bigger than this is a defect, and is
// refused (fail closed) rather than answered.
const maxRevocationLookupKeys = rdb.MaxLookupKeys

// ResolveX509Revocation reads what the anchor's revocation store knows about the identities on a
// connect's verified paths, for x509trust.Verified.ApplyRevocation: the auth callout's one call
// into this check.
//
// 🔴 IT IS A METHOD OF ITS OWN SO THAT NO CACHE CAN SIT IN FRONT OF IT, for the reason
// ResolveX509Connect is: a cached "not revoked" outlives the revocation on every replica, and a
// connect that is granted holds a session for hours. CachedApi must never override this; a test
// asserts the method set.
//
// 🔴 THE ANCHOR IS THE ONE THE CALLER JUST READ, UNCACHED (X509Connect.Anchor, or the chosen
// profile's X509ProfileAnchor.Anchor), and the mode and the grace come from IT, not from a second
// read and not from "no rows found". So the fast path cannot default the mode: under
// REQUIRE_CURRENT an anchor with no list is revocation_unknown, decided from the anchor row. An
// ENTRIES anchor whose row says it holds no lists and no manual entries runs no query at all.
//
// 🔴 TENANT ISOLATION IS EXPLICIT HERE. The rdb tenant callback covers neither raw SQL nor a
// projection destination, so every clause carries `tenant_id = ? AND trust_anchor_id = ?` on top
// of it, the destinations are the entity types, and a row that comes back for any other tenant or
// anchor is an error rather than a skipped row. No tenant in the context, an anchor that is not
// the context tenant's, or an anchor with no id is an error.
//
// Any error refuses the connect (callout result "error"): a lookup that fails never admits.
func (api *Api) ResolveX509Revocation(ctx context.Context, anchor *DeviceTrustAnchor, keys x509trust.RevocationQuery,
	now time.Time) (x509trust.RevocationState, error) {
	tenant, err := ctxTenant(ctx)
	if err != nil {
		return x509trust.RevocationState{}, err
	}
	return readRevocationState(api.RDB.DB(ctx), tenant, anchor, keys, now, api.revocationLimits().DefaultStaleGrace)
}

// readRevocationState is ResolveX509Revocation over a given handle, so the in-transaction recheck
// of a registration reads through the transaction that holds the anchor lock.
func readRevocationState(db *gorm.DB, tenant string, anchor *DeviceTrustAnchor, keys x509trust.RevocationQuery,
	now time.Time, defaultGrace time.Duration) (x509trust.RevocationState, error) {
	if anchor == nil || anchor.ID == 0 {
		return x509trust.RevocationState{}, errors.New("a revocation lookup needs the connect's trust anchor")
	}
	if anchor.TenantId != tenant {
		return x509trust.RevocationState{}, errors.New("a revocation lookup was given another tenant's trust anchor")
	}
	mode, err := anchor.RevocationModeOf()
	if err != nil {
		return x509trust.RevocationState{}, err
	}
	grace, err := anchor.RevocationStaleGraceOf(defaultGrace)
	if err != nil {
		return x509trust.RevocationState{}, err
	}
	state := x509trust.RevocationState{Mode: mode, Now: now, StaleGrace: grace}
	if mode == x509trust.ModeEntries && !anchor.HasRevocationRows() {
		return state, nil
	}
	if len(keys.Fingerprints) > maxRevocationLookupKeys || len(keys.Serials) > maxRevocationLookupKeys ||
		len(keys.Issuers) > maxRevocationLookupKeys {
		return x509trust.RevocationState{}, fmt.Errorf("a revocation lookup names too many identities (%d fingerprints, %d serials, %d issuers)",
			len(keys.Fingerprints), len(keys.Serials), len(keys.Issuers))
	}

	scoped := func() *gorm.DB {
		return db.Session(&gorm.Session{NewDB: true}).Where("tenant_id = ? AND trust_anchor_id = ?", tenant, anchor.ID)
	}
	// One statement for both sources: the manual entries by fingerprint and the CRL entries by
	// issuer and serial, each branch on its own partial index.
	var (
		conds []string
		args  []any
	)
	if len(keys.Fingerprints) > 0 {
		conds = append(conds, "(kind = ? AND fingerprint IN ?)")
		args = append(args, RevokedKindManual, keys.Fingerprints)
	}
	if len(keys.Serials) > 0 {
		tuples := make([][]any, len(keys.Serials))
		for i, s := range keys.Serials {
			tuples[i] = []any{s.Issuer.Key, s.Issuer.Name, s.Serial}
		}
		conds = append(conds, "(kind = ? AND (issuer_key, issuer_name, serial) IN ?)")
		args = append(args, RevokedKindCRL, tuples)
	}
	if len(conds) > 0 {
		var rows []DeviceRevokedCertificate
		if err := scoped().Where("("+strings.Join(conds, " OR ")+")", args...).Find(&rows).Error; err != nil {
			return x509trust.RevocationState{}, err
		}
		for i := range rows {
			r := &rows[i]
			if r.TenantId != tenant || r.TrustAnchorId != anchor.ID {
				return x509trust.RevocationState{}, errors.New("a revocation lookup returned a row of another tenant or anchor")
			}
			reason := x509trust.RevocationReason(r.Reason)
			switch r.Kind {
			case RevokedKindManual:
				if r.Fingerprint == nil {
					return x509trust.RevocationState{}, errors.New("a stored manual revocation holds no fingerprint")
				}
				if state.RevokedFingerprints == nil {
					state.RevokedFingerprints = map[string]x509trust.RevocationReason{}
				}
				state.RevokedFingerprints[*r.Fingerprint] = reason
			case RevokedKindCRL:
				if r.IssuerKey == nil || r.IssuerName == nil || r.Serial == nil {
					return x509trust.RevocationState{}, errors.New("a stored CRL entry holds no issuer or serial")
				}
				if state.RevokedSerials == nil {
					state.RevokedSerials = map[x509trust.IssuerSerial]x509trust.RevocationReason{}
				}
				state.RevokedSerials[x509trust.IssuerSerial{
					Issuer: x509trust.IssuerID{Key: *r.IssuerKey, Name: *r.IssuerName}, Serial: *r.Serial}] = reason
			default:
				return x509trust.RevocationState{}, fmt.Errorf("a stored revocation has the unknown kind %q", r.Kind)
			}
		}
	}

	// Completeness only matters in REQUIRE_CURRENT: ENTRIES admits an issuer with no list or a
	// stale one, so its lists are not read on a connect.
	if mode == x509trust.ModeRequireCurrent && len(keys.Issuers) > 0 {
		pairs := make([][]any, len(keys.Issuers))
		for i, is := range keys.Issuers {
			pairs[i] = []any{is.Key, is.Name}
		}
		var lists []DeviceRevocationList
		if err := scoped().Select("id", "tenant_id", "trust_anchor_id", "issuer_key", "issuer_name", "next_update").
			Where("(issuer_key, issuer_name) IN ?", pairs).Find(&lists).Error; err != nil {
			return x509trust.RevocationState{}, err
		}
		for i := range lists {
			l := &lists[i]
			if l.TenantId != tenant || l.TrustAnchorId != anchor.ID {
				return x509trust.RevocationState{}, errors.New("a revocation lookup returned a list of another tenant or anchor")
			}
			if state.Lists == nil {
				state.Lists = map[x509trust.IssuerID]x509trust.ListStatus{}
			}
			state.Lists[l.IssuerID()] = x509trust.ListStatus{NextUpdate: l.NextUpdate}
		}
	}
	return state, nil
}

// recheckRevocationLocked is the connect check re-run INSIDE a registration's transaction, under
// the anchor's FOR SHARE lock: the gate in the callout ran before the transaction opened, and a
// revocation that committed in between must refuse the registration with nothing written. The
// anchor row passed is the one read under the lock, so a mode changed meanwhile is the mode used.
// It returns the *x509trust.ChainError of a refusal (ReasonOf revoked or revocation_unknown), or
// any other error, and every non-nil result refuses.
func recheckRevocationLocked(tx *gorm.DB, tenant string, anchor *DeviceTrustAnchor, verified *x509trust.Verified,
	defaultGrace time.Duration) error {
	if verified == nil {
		return errors.New("a certificate registration needs the verified chain to re-check its revocation")
	}
	state, err := readRevocationState(tx, tenant, anchor, verified.RevocationKeys(), time.Now(), defaultGrace)
	if err != nil {
		return err
	}
	_, err = verified.ApplyRevocation(state)
	return err
}

// ─── manual revocation ──────────────────────────────────────────────────────

// DeviceCertificateRevokeRequest revokes one certificate by DeviceChain's own word.
type DeviceCertificateRevokeRequest struct {
	AnchorToken string
	// CertificatePem or FingerprintSha256, exactly one. With a certificate the fingerprint is
	// computed. The certificate may be a leaf or an intermediate, registered or not.
	CertificatePem    *string
	FingerprintSha256 *string
	// Reason is the GraphQL enum name; nil is UNSPECIFIED.
	Reason *string
}

// DeviceRevokedCertificateSearchCriteria is the search of a revocation store's entries.
type DeviceRevokedCertificateSearchCriteria struct {
	rdb.Pagination
	// Kind restricts to MANUAL or CRL entries.
	Kind *string
}

// DeviceRevokedCertificateSearchResults is a page of revoked certificates.
type DeviceRevokedCertificateSearchResults struct {
	Results    []DeviceRevokedCertificate
	Pagination rdb.SearchResultsPagination
}

// DefaultOrder implements rdb.Sortable: newest first, id as the unique tiebreak.
func (DeviceRevokedCertificate) DefaultOrder() string {
	return "device_revoked_certificates.revoked_at DESC, device_revoked_certificates.id DESC"
}

var revocationReasonNames = map[string]x509trust.RevocationReason{
	"UNSPECIFIED":            x509trust.RevocationUnspecified,
	"KEY_COMPROMISE":         x509trust.RevocationKeyCompromise,
	"CA_COMPROMISE":          x509trust.RevocationCACompromise,
	"AFFILIATION_CHANGED":    x509trust.RevocationAffiliationChanged,
	"SUPERSEDED":             x509trust.RevocationSuperseded,
	"CESSATION_OF_OPERATION": x509trust.RevocationCessationOfOperation,
	"CERTIFICATE_HOLD":       x509trust.RevocationCertificateHold,
	"PRIVILEGE_WITHDRAWN":    x509trust.RevocationPrivilegeWithdrawn,
	"AA_COMPROMISE":          x509trust.RevocationAACompromise,
}

// RevocationReasonFromEnum maps the GraphQL enum name to the stored reason; ok is false for a
// name outside the vocabulary.
func RevocationReasonFromEnum(name string) (x509trust.RevocationReason, bool) {
	r, ok := revocationReasonNames[name]
	return r, ok
}

// RevocationReasonEnum is the GraphQL enum name of a stored reason. A stored value outside the
// vocabulary reads as UNSPECIFIED rather than failing a listing.
func RevocationReasonEnum(reason string) string {
	for name, r := range revocationReasonNames {
		if string(r) == reason {
			return name
		}
	}
	return "UNSPECIFIED"
}

// validFingerprint is whether s is the lowercase hex of a SHA-256.
func validFingerprint(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// revokeFingerprint is the fingerprint a revoke names: exactly one of a certificate and a
// fingerprint, the certificate parsed and hashed.
func revokeFingerprint(req *DeviceCertificateRevokeRequest) (string, error) {
	hasCert := req.CertificatePem != nil && strings.TrimSpace(*req.CertificatePem) != ""
	hasFp := req.FingerprintSha256 != nil && strings.TrimSpace(*req.FingerprintSha256) != ""
	switch {
	case hasCert == hasFp:
		return "", invalidAnchor("name the certificate to revoke with exactly one of certificatePem and fingerprintSha256")
	case hasFp:
		fp := strings.ToLower(strings.TrimSpace(*req.FingerprintSha256))
		if !validFingerprint(fp) {
			return "", invalidAnchor("fingerprintSha256 must be 64 hexadecimal characters")
		}
		return fp, nil
	}
	_, certs, err := x509trust.ParseCertificates(*req.CertificatePem)
	if err != nil {
		return "", invalidAnchor("certificatePem: %v", err)
	}
	if len(certs) != 1 {
		return "", invalidAnchor("certificatePem must hold exactly one certificate")
	}
	return x509trust.Fingerprint(certs[0]), nil
}

// revokesACA is whether the revoke names a certificate (PEM) that is a CA. A bare fingerprint says
// nothing about what it fingerprints, so it is not.
func revokesACA(req *DeviceCertificateRevokeRequest) bool {
	if req.CertificatePem == nil || strings.TrimSpace(*req.CertificatePem) == "" {
		return false
	}
	_, certs, err := x509trust.ParseCertificates(*req.CertificatePem)
	return err == nil && len(certs) == 1 && certs[0].BasicConstraintsValid && certs[0].IsCA
}

// bumpRevocationRows adds delta to the anchor's revocation_rows counter. The caller holds the
// anchor FOR UPDATE.
func bumpRevocationRows(tx *gorm.DB, anchorId uint, delta int64) error {
	// The model carries the anchor's id, so the audit journal names the row it changed.
	return tx.Model(&DeviceTrustAnchor{Model: gorm.Model{ID: anchorId}}).
		Update("revocation_rows", gorm.Expr("COALESCE(revocation_rows, 0) + ?", delta)).Error
}

// RevokeDeviceCertificate records that DeviceChain will not admit a certificate, whether or not
// it is registered and whatever becomes of its credential or its device: the entry hangs off the
// trust anchor, not off any credential. It applies to any certificate on a verified path, a leaf
// or an intermediate, from the very next connect on every replica (the connect read is uncached).
// Revoking a certificate that is already revoked returns the existing entry unchanged.
//
// The anchor's own certificate is refused: that is "disable the anchor".
//
// LOCK ORDER: the anchor row FOR UPDATE first, then the tenant's manual-revocation advisory lock,
// then the entry, then the kick queue rows. It never takes the CRL-writers advisory lock, which must
// not be taken while the anchor row is held (see revocationLockOrder in
// api_x509_revocation_kicks.go). A certificate registration holds the anchor FOR SHARE to its commit.
//
// LIVE SESSIONS: the devices whose sessions the entry affects are queued for a kick in the same
// transaction, and the call asks the broker before it returns (see revocationKickTargets). A kick
// that cannot be confirmed returns the entry AND ErrSessionsNotEnded: the revocation stands and
// new connects are refused; repeating the call asks again.
func (api *Api) RevokeDeviceCertificate(ctx context.Context, req *DeviceCertificateRevokeRequest) (*DeviceRevokedCertificate, error) {
	tenant, err := ctxTenant(ctx)
	if err != nil {
		return nil, err
	}
	fp, err := revokeFingerprint(req)
	if err != nil {
		return nil, err
	}
	isCA := revokesACA(req)
	bareFingerprint := req.CertificatePem == nil || strings.TrimSpace(*req.CertificatePem) == ""
	reason := x509trust.RevocationUnspecified
	if req.Reason != nil {
		r, ok := RevocationReasonFromEnum(*req.Reason)
		if !ok {
			return nil, invalidAnchor("reason %q is not a certificate revocation reason", *req.Reason)
		}
		reason = r
	}
	token := strings.TrimSpace(req.AnchorToken)
	if token == "" {
		return nil, invalidAnchor("anchorToken is required")
	}
	// The credentials recorded before chain links existed get their depth-0 link now, so the
	// session targeting below finds them (see prepareRevocationKicks).
	linksWhole := api.prepareRevocationKicks(ctx, tenant, token)

	var out *DeviceRevokedCertificate
	var anchorId uint
	var unidentified bool
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		out = nil
		unidentified = false
		anchor, err := lockTrustAnchorByToken(tx, token)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("trust anchor %q: %w", token, gorm.ErrRecordNotFound)
			}
			return err
		}
		anchorId = anchor.ID
		if fp == anchor.FingerprintSha256 {
			return invalidAnchor("that is the trust anchor's own certificate; disable the anchor instead")
		}
		if err := refuseIfEnforcedUnderOtherAnchors(tx, tenant, anchor, fp); err != nil {
			return err
		}
		var existing []DeviceRevokedCertificate
		if err := tx.Where("tenant_id = ? AND trust_anchor_id = ? AND kind = ? AND fingerprint = ?",
			tenant, anchor.ID, RevokedKindManual, fp).Limit(1).Find(&existing).Error; err != nil {
			return err
		}
		if len(existing) == 1 {
			out = &existing[0]
			// A repeat finds the entry; whether the kick debt for it was ever recorded is the entry's
			// own marker. An entry made before the debt existed (or with no session kicker wired) has
			// none: queue it now, once, and say so on the entry.
			if api.SessionKicker != nil && out.KickQueuedAt == nil {
				if err := api.queueRevocationKicksOnTx(tx, tenant, anchor.ID,
					RevocationDelta{Fingerprints: []string{fp}, CA: isCA}, linksWhole); err != nil {
					return err
				}
				queuedAt := time.Now().UTC()
				if err := tx.Model(out).Update("kick_queued_at", queuedAt).Error; err != nil {
					return err
				}
				out.KickQueuedAt = &queuedAt
			} else if api.SessionKicker != nil && isCA {
				// 🔴 A PEM REPEAT WIDENS. The entry's debt was queued once, by its first call, which may
				// have been a bare fingerprint that identified no session (see unidentifiedSessions). The
				// certificate itself, known now to be a CA, is anchor-wide whatever the links say, so the
				// repeat owes that kick again even though KickQueuedAt is set. It costs one anchor-wide
				// marker per explicit repeat; the entry records no "queued as a CA" bit to dedupe on.
				if err := api.queueRevocationKicksOnTx(tx, tenant, anchor.ID,
					RevocationDelta{Fingerprints: []string{fp}, CA: true}, linksWhole); err != nil {
					return err
				}
			}
			unidentified, err = api.unidentifiedSessions(tx, tenant, anchor.ID, fp, bareFingerprint, linksWhole)
			return err
		}
		if err := lockTenantCreates(tx, tenant, "manual_revocations"); err != nil {
			return err
		}
		var n int64
		if err := tx.Model(&DeviceRevokedCertificate{}).Where("tenant_id = ? AND kind = ?", tenant, RevokedKindManual).
			Count(&n).Error; err != nil {
			return err
		}
		if maxManual := api.revocationLimits().MaxManualEntriesPerTenant; n >= int64(maxManual) {
			return limit.Exceeded("manual certificate revocations", int(n)+1, maxManual)
		}
		now := time.Now().UTC()
		entry := &DeviceRevokedCertificate{
			TrustAnchorId: anchor.ID, Kind: RevokedKindManual, Fingerprint: &fp,
			Reason: string(reason), RevokedAt: now, CreatedAt: now,
		}
		if claims, ok := auth.ClaimsFromContext(ctx); ok && claims.Username != "" {
			by := claims.Username
			entry.CreatedBy = &by
		}
		if api.SessionKicker != nil {
			entry.KickQueuedAt = &now
		}
		if err := tx.Create(entry).Error; err != nil {
			return err
		}
		if err := bumpRevocationRows(tx, anchor.ID, 1); err != nil {
			return err
		}
		// The sessions this entry ends are queued in the same transaction (lock order: anchor,
		// then the entry, then the kick rows).
		if err := api.queueRevocationKicksOnTx(tx, tenant, anchor.ID,
			RevocationDelta{Fingerprints: []string{fp}, CA: isCA}, linksWhole); err != nil {
			return err
		}
		out = entry
		unidentified, err = api.unidentifiedSessions(tx, tenant, anchor.ID, fp, bareFingerprint, linksWhole)
		return err
	})
	if err != nil {
		return nil, err
	}
	// Asked whether or not this call wrote the entry: a repeat after an unconfirmed kick finds the
	// entry already there and still owes the broker its question. The entry is returned with the
	// error, which is ErrSessionsNotEnded when the sessions could not be confirmed ended.
	if err := api.endRevokedSessions(ctx, tenant, anchorId); err != nil {
		return out, err
	}
	if unidentified {
		return out, fmt.Errorf("%w: the fingerprint matched no recorded certificate path and credentials recorded "+
			"before chain links existed are present, so which sessions it affects cannot be told; if it is a CA, "+
			"repeat the revocation with its certificatePem to end every session under the anchor", ErrSessionsNotEnded)
	}
	return out, nil
}

// unidentifiedSessions is whether a revocation by bare fingerprint could not identify the sessions
// it affects, so that reporting "sessions ended" would be a plausible value for an unknown. True
// when the call names a bare fingerprint (a certificate given as PEM is known: a leaf, or a CA that is anchor-wide), the links are whole, no link of the anchor names
// the fingerprint, and some credential of the anchor holds only a legacy depth-0 link (no issuer
// key, no deeper link): its path through an intermediate was never recorded. Read in tx, the anchor
// locked. With no kicker wired there are no sessions of ours and the answer is false.
func (api *Api) unidentifiedSessions(tx *gorm.DB, tenant string, anchorId uint, fp string, bareFingerprint, linksWhole bool) (bool, error) {
	if api.SessionKicker == nil || !bareFingerprint || !linksWhole {
		return false, nil
	}
	var named int64
	if err := tx.Session(&gorm.Session{NewDB: true}).Model(&DeviceCredentialChainLink{}).
		Where("tenant_id = ? AND trust_anchor_id = ? AND fingerprint = ?", tenant, anchorId, fp).
		Count(&named).Error; err != nil {
		return false, err
	}
	if named > 0 {
		return false, nil
	}
	var legacy int64
	if err := tx.Session(&gorm.Session{NewDB: true}).Model(&DeviceCredentialChainLink{}).
		Where("tenant_id = ? AND trust_anchor_id = ? AND depth = 0 AND issuer_key IS NULL", tenant, anchorId).
		Where("NOT EXISTS (SELECT 1 FROM device_credential_chain_links d WHERE d.credential_id = device_credential_chain_links.credential_id AND d.depth >= 1)").
		Count(&legacy).Error; err != nil {
		return false, err
	}
	return legacy > 0, nil
}

// UnrevokeDeviceCertificate removes a manual revocation, reporting whether one existed. The
// certificate is admitted again from the next connect if nothing else refuses it (a CRL entry for
// it, a disabled credential, a disabled anchor); nothing is kicked and a device reconnects on its
// own. Same lock order as RevokeDeviceCertificate.
func (api *Api) UnrevokeDeviceCertificate(ctx context.Context, anchorToken, fingerprint string) (bool, error) {
	tenant, err := ctxTenant(ctx)
	if err != nil {
		return false, err
	}
	fp := strings.ToLower(strings.TrimSpace(fingerprint))
	if !validFingerprint(fp) {
		return false, invalidAnchor("fingerprintSha256 must be 64 hexadecimal characters")
	}
	token := strings.TrimSpace(anchorToken)
	if token == "" {
		return false, invalidAnchor("anchorToken is required")
	}
	removed := false
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		removed = false
		anchor, err := lockTrustAnchorByToken(tx, token)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("trust anchor %q: %w", token, gorm.ErrRecordNotFound)
			}
			return err
		}
		// The rows are read under the anchor lock and deleted by identity, so the audit journal
		// names each one (row id and fingerprint label) instead of "something was deleted".
		var rows []DeviceRevokedCertificate
		if err := tx.Where("tenant_id = ? AND trust_anchor_id = ? AND kind = ? AND fingerprint = ?",
			tenant, anchor.ID, RevokedKindManual, fp).Find(&rows).Error; err != nil {
			return err
		}
		for i := range rows {
			if err := tx.Delete(&rows[i]).Error; err != nil {
				return err
			}
		}
		if len(rows) == 0 {
			return nil
		}
		removed = true
		return bumpRevocationRows(tx, anchor.ID, -int64(len(rows)))
	})
	if err != nil {
		return false, err
	}
	return removed, nil
}

// DeviceRevokedCertificates searches the entries of one anchor's revocation store.
func (api *Api) DeviceRevokedCertificates(ctx context.Context, anchorToken string,
	criteria DeviceRevokedCertificateSearchCriteria) (*DeviceRevokedCertificateSearchResults, error) {
	tenant, err := ctxTenant(ctx)
	if err != nil {
		return nil, err
	}
	anchor, err := api.trustAnchorByToken(ctx, strings.TrimSpace(anchorToken))
	if err != nil {
		return nil, err
	}
	if criteria.Kind != nil && *criteria.Kind != RevokedKindManual && *criteria.Kind != RevokedKindCRL {
		return nil, invalidAnchor("kind must be %s or %s", RevokedKindManual, RevokedKindCRL)
	}
	results := make([]DeviceRevokedCertificate, 0)
	db, pag := api.RDB.ListOf(ctx, &DeviceRevokedCertificate{}, func(result *gorm.DB) *gorm.DB {
		result = result.Where("tenant_id = ? AND trust_anchor_id = ?", tenant, anchor.ID)
		if criteria.Kind != nil {
			result = result.Where("kind = ?", *criteria.Kind)
		}
		return result
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &DeviceRevokedCertificateSearchResults{Results: results, Pagination: pag}, nil
}

// manualRevocationRows counts the manual revocation entries of the anchor.
func manualRevocationRows(tx *gorm.DB, anchorId uint) (int64, error) {
	var n int64
	err := tx.Model(&DeviceRevokedCertificate{}).Where("trust_anchor_id = ? AND kind = ?", anchorId, RevokedKindManual).Count(&n).Error
	return n, err
}

// discardCrlRevocations deletes everything the anchor holds from CRLs, for good: the entries, the
// lists (soft-deleted ones included) and the issuer rows. Children first, because the foreign keys
// are RESTRICT. The caller holds the anchor FOR UPDATE and is deleting the anchor.
func discardCrlRevocations(tx *gorm.DB, tenant string, anchorId uint) error {
	if err := tx.Where("tenant_id = ? AND trust_anchor_id = ? AND kind = ?", tenant, anchorId, RevokedKindCRL).
		Delete(&crlEntryRow{}).Error; err != nil {
		return err
	}
	if err := tx.Unscoped().Where("tenant_id = ? AND trust_anchor_id = ?", tenant, anchorId).
		Delete(&DeviceRevocationList{}).Error; err != nil {
		return err
	}
	return tx.Where("tenant_id = ? AND trust_anchor_id = ?", tenant, anchorId).Delete(&DeviceRevocationIssuer{}).Error
}

// refuseIfEnforcedUnderOtherAnchors is the revoke-time guard for the per-anchor store: a revocation
// is enforced only for chains verified against the anchor it is recorded under. If the fingerprint
// pins a certificate credential, or sits on a recorded chain, under ANOTHER anchor of the tenant
// and not under this one, a "success" here would leave those devices connecting. That is refused,
// naming the anchor(s) whose store would enforce it. Called with the named anchor locked.
func refuseIfEnforcedUnderOtherAnchors(tx *gorm.DB, tenant string, anchor *DeviceTrustAnchor, fp string) error {
	var pinned, linked []uint
	if err := tx.Model(&DeviceCredential{}).Where("tenant_id = ? AND credential_type = ? AND credential_id = ? AND trust_anchor_id IS NOT NULL",
		tenant, string(CredentialX509Certificate), fp).Distinct().Pluck("trust_anchor_id", &pinned).Error; err != nil {
		return err
	}
	if err := tx.Model(&DeviceCredentialChainLink{}).Where("tenant_id = ? AND fingerprint = ?", tenant, fp).
		Distinct().Pluck("trust_anchor_id", &linked).Error; err != nil {
		return err
	}
	others := map[uint]bool{}
	for _, id := range append(pinned, linked...) {
		if id == anchor.ID {
			return nil // this anchor enforces it: nothing is stranded
		}
		others[id] = true
	}
	if len(others) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(others))
	for id := range others {
		ids = append(ids, id)
	}
	var anchors []DeviceTrustAnchor
	if err := tx.Where("id IN ?", ids).Order("token").Find(&anchors).Error; err != nil {
		return err
	}
	tokens := make([]string, 0, len(anchors))
	for _, a := range anchors {
		tokens = append(tokens, strconv.Quote(a.Token))
	}
	return invalidAnchor("this certificate is used under trust anchor(s) %s, not under %q, and a revocation is enforced only "+
		"under the anchor it is recorded for; revoke it under %s", strings.Join(tokens, ", "), anchor.Token, strings.Join(tokens, " or "))
}

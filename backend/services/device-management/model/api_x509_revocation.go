// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-device-management/model/identity"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"gorm.io/gorm"
)

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
	return identity.ReadRevocationState(api.RDB.DB(ctx), tenant, anchor, keys, now, api.revocationLimits().DefaultStaleGrace)
}

// ─── manual revocation ──────────────────────────────────────────────────────

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
	fp, err := identity.RevokeFingerprint(req)
	if err != nil {
		return nil, err
	}
	isCA := identity.RevokesACA(req)
	bareFingerprint := req.CertificatePem == nil || strings.TrimSpace(*req.CertificatePem) == ""
	reason := x509trust.RevocationUnspecified
	if req.Reason != nil {
		r, ok := RevocationReasonFromEnum(*req.Reason)
		if !ok {
			return nil, identity.InvalidAnchor("reason %q is not a certificate revocation reason", *req.Reason)
		}
		reason = r
	}
	token := strings.TrimSpace(req.AnchorToken)
	if token == "" {
		return nil, identity.InvalidAnchor("anchorToken is required")
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
		anchor, err := identity.LockTrustAnchorByToken(tx, token)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("trust anchor %q: %w", token, gorm.ErrRecordNotFound)
			}
			return err
		}
		anchorId = anchor.ID
		if fp == anchor.FingerprintSha256 {
			return identity.InvalidAnchor("that is the trust anchor's own certificate; disable the anchor instead")
		}
		if err := identity.RefuseIfEnforcedUnderOtherAnchors(tx, tenant, anchor, fp); err != nil {
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
		if err := identity.BumpRevocationRows(tx, anchor.ID, 1); err != nil {
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
	if !identity.ValidFingerprint(fp) {
		return false, identity.InvalidAnchor("fingerprintSha256 must be 64 hexadecimal characters")
	}
	token := strings.TrimSpace(anchorToken)
	if token == "" {
		return false, identity.InvalidAnchor("anchorToken is required")
	}
	removed := false
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		removed = false
		anchor, err := identity.LockTrustAnchorByToken(tx, token)
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
		return identity.BumpRevocationRows(tx, anchor.ID, -int64(len(rows)))
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
		return nil, identity.InvalidAnchor("kind must be %s or %s", RevokedKindManual, RevokedKindCRL)
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

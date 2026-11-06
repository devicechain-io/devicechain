// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"

	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CHAIN LINKS. A credential's links record the verified path its certificate was created or
// registered through, one row per certificate except the anchor: the fingerprint, and the
// certificate's issuer (key and name) and serial against its parent on that path. They exist only
// so a revocation can find the credentials, and so the devices, whose live sessions it ends.
//
// 🔴 LINKS ARE A HINT, NOT A RECORD OF SESSIONS, and they never admit anything. Only the leaf is
// stored with a credential and a device presents its intermediates at every connect, so a
// credential created through one cross-signed path may connect through another. The connect path
// never reads them.

// chainLinksOf builds the links of a credential from the verified path it was created through.
// The tenant is stamped by the create callback from the context, as for every tenant-scoped row.
func chainLinksOf(credentialId, anchorId uint, chain []*x509.Certificate) ([]DeviceCredentialChainLink, error) {
	path, ok := x509trust.PathLinks(chain)
	if !ok {
		return nil, errors.New("a credential's chain links need a verified path")
	}
	out := make([]DeviceCredentialChainLink, 0, len(path))
	for depth, l := range path {
		if !l.SerialOK {
			// A serial no CRL can name (zero or negative) has no link to match on; the
			// fingerprint-keyed manual entries still apply to the certificate itself.
			continue
		}
		key := l.Issuer.Key
		out = append(out, DeviceCredentialChainLink{
			CredentialId: credentialId, Depth: int16(depth), TrustAnchorId: anchorId,
			Fingerprint: l.Fingerprint, IssuerKey: &key, IssuerName: l.Issuer.Name, Serial: l.Serial,
		})
	}
	return out, nil
}

// writeChainLinks records the links of a credential just inserted, in the credential's own
// transaction. It is idempotent on (credential, depth).
func writeChainLinks(tx *gorm.DB, credentialId, anchorId uint, chain []*x509.Certificate) error {
	links, err := chainLinksOf(credentialId, anchorId, chain)
	if err != nil {
		return err
	}
	if len(links) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&links).Error
}

// legacyLinkBatch is how many credentials one reconciler statement handles.
const legacyLinkBatch = 500

// legacyReconcileBudget bounds the credentials one revocation write reconciles. A revocation
// must not pay O(credentials) of a large tenant before it can commit; what the budget leaves
// unlinked is covered by widening that write's kick to the whole anchor (a conservative
// over-kick, never a missed one), and the NEXT revocation carries the reconciliation on from
// where this one stopped, since each call links the credentials it reaches for good. A variable
// so a test can shrink it.
var legacyReconcileBudget = 20_000

// unlinkableSerial and unlinkableIssuerName are the MARK a legacy credential gets when its stored
// leaf cannot be linked (not PEM, does not parse, or a serial no CRL can name). The mark is a
// depth-0 link that no CRL entry can match (no CRL serial is empty) and that still carries the
// pin as its fingerprint, so a manual revocation finds the credential. Its job is to stop the
// reconciler reading the same unreadable leaf at every revocation for ever.
const (
	unlinkableSerial     = ""
	unlinkableIssuerName = "0000000000000000000000000000000000000000000000000000000000000000"
)

// ReconcileLegacyChainLinks writes the depth-0 link of the tenant's certificate credentials that
// have none, from the leaf each stores: its fingerprint, its serial, the hash of its issuer's
// name, and NO issuer key (a leaf does not carry it). That is enough to match a CRL entry on
// (issuer name, serial) when ending sessions; a name-only match can over-kick, never admit. A
// credential whose leaf cannot be linked gets an unlinkable MARK instead (see above), so it is
// not read again.
//
// It is the app-level reconciler the spec asks for in place of a backfill in the DDL migration. It
// runs in the CALLING TENANT'S context (an instance-wide pass would need a system context, and
// none is added): a revocation writer calls it for its own tenant before it needs the links, so a
// tenant is reconciled the first time it revokes anything. It is idempotent and re-runnable, and
// returns how many credentials it handled; max bounds the work of one call (0 for all).
func (api *Api) ReconcileLegacyChainLinks(ctx context.Context, max int) (int, error) {
	tenant, err := ctxTenant(ctx)
	if err != nil {
		return 0, err
	}
	done, _, err := api.reconcileLegacyLinks(ctx, tenant, 0, max)
	return done, err
}

// reconcileLegacyLinks is ReconcileLegacyChainLinks for one anchor (anchorId 0: every anchor of
// the tenant). complete reports whether the pass ran out of unlinked credentials; a pass stopped
// by max, or by an error, is not complete, and the caller must not assume the links are whole.
func (api *Api) reconcileLegacyLinks(ctx context.Context, tenant string, anchorId uint, max int) (done int, complete bool, err error) {
	after := uint(0)
	for max == 0 || done < max {
		n, last, err := api.reconcileLegacyBatch(ctx, tenant, anchorId, after)
		done += n
		after = last
		if err != nil {
			return done, false, err
		}
		if n < legacyLinkBatch {
			return done, true, nil
		}
	}
	return done, false, nil
}

// reconcileLegacyBatch handles the next page of unlinked credentials after the id `after`, and
// returns how many it looked at and the last id. Paging by id means a credential is looked at
// once per call, and one that cannot be linked is marked, not retried.
func (api *Api) reconcileLegacyBatch(ctx context.Context, tenant string, anchorId, after uint) (int, uint, error) {
	db := api.RDB.DB(ctx)
	q := db.Select("id", "tenant_id", "trust_anchor_id", "credential_id", "certificate_pem").
		Where("tenant_id = ? AND credential_type = ? AND trust_anchor_id IS NOT NULL", tenant, string(CredentialX509Certificate))
	if anchorId != 0 {
		q = q.Where("trust_anchor_id = ?", anchorId)
	}
	var creds []DeviceCredential
	if err := q.Where("NOT EXISTS (SELECT 1 FROM device_credential_chain_links l WHERE l.credential_id = device_credentials.id AND l.depth = 0 AND l.tenant_id = device_credentials.tenant_id AND l.trust_anchor_id = device_credentials.trust_anchor_id)").
		Where("id > ?", after).Order("id ASC").Limit(legacyLinkBatch).Find(&creds).Error; err != nil {
		return 0, after, err
	}
	var links []DeviceCredentialChainLink
	for _, c := range creds {
		link, ok := legacyLinkOf(c)
		if !ok {
			log.Warn().Uint("credential", c.ID).Msg("A certificate credential's stored leaf cannot be linked to a CRL entry; it is marked, and a revocation by fingerprint still finds it.")
			link = unlinkableLink(c)
		}
		links = append(links, link)
	}
	if len(links) > 0 {
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&links).Error; err != nil {
			return 0, after, err
		}
	}
	last := after
	if len(creds) > 0 {
		last = creds[len(creds)-1].ID
	}
	return len(creds), last, nil
}

// legacyLinkOf is the depth-0 link of a legacy credential, from the leaf it stores.
func legacyLinkOf(c DeviceCredential) (DeviceCredentialChainLink, bool) {
	if !c.CertificatePem.Valid {
		return DeviceCredentialChainLink{}, false
	}
	block, _ := pem.Decode([]byte(c.CertificatePem.String))
	if block == nil {
		return DeviceCredentialChainLink{}, false
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return DeviceCredentialChainLink{}, false
	}
	serial, ok := x509trust.SerialHex(leaf.SerialNumber)
	if !ok {
		return DeviceCredentialChainLink{}, false
	}
	return DeviceCredentialChainLink{
		CredentialId: c.ID, Depth: 0, TrustAnchorId: *c.TrustAnchorId,
		Fingerprint: x509trust.Fingerprint(leaf), IssuerName: x509trust.IssuerNameHash(leaf), Serial: serial,
	}, true
}

// unlinkableLink is the mark of a credential that cannot be linked: the pin as its fingerprint (a
// certificate credential's id IS the leaf's fingerprint), and an issuer and serial nothing matches.
func unlinkableLink(c DeviceCredential) DeviceCredentialChainLink {
	fp := c.CredentialId
	if !validFingerprint(fp) {
		fp = unlinkableIssuerName
	}
	return DeviceCredentialChainLink{
		CredentialId: c.ID, Depth: 0, TrustAnchorId: *c.TrustAnchorId,
		Fingerprint: fp, IssuerName: unlinkableIssuerName, Serial: unlinkableSerial,
	}
}

// reconcileLegacyChainLinksBounded runs the reconciler for one anchor of the context's tenant, to
// legacyReconcileBudget credentials, and reports whether the anchor's links are now whole. A
// failure is logged and reported as not whole: a revocation is enforced at connect whatever the
// links say, so the links never gate it, but a kick built on incomplete links must widen.
func (api *Api) reconcileLegacyChainLinksBounded(ctx context.Context, tenant string, anchorId uint) bool {
	_, complete, err := api.reconcileLegacyLinks(ctx, tenant, anchorId, legacyReconcileBudget)
	if err != nil {
		log.Warn().Err(err).Msg("Could not reconcile the chain links of legacy certificate credentials; the live-session kick will cover the whole anchor.")
		return false
	}
	if !complete {
		log.Info().Int("budget", legacyReconcileBudget).Msg("Reconciled a bounded share of the legacy certificate credentials' chain links; the live-session kick covers the whole anchor, and the next revocation continues.")
	}
	return complete
}

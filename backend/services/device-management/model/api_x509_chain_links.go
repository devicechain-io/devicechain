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

// ReconcileLegacyChainLinks writes the depth-0 link of the tenant's certificate credentials that
// have none, from the leaf each stores: its fingerprint, its serial, the hash of its issuer's
// name, and NO issuer key (a leaf does not carry it). That is enough to match a CRL entry on
// (issuer name, serial) when ending sessions; a name-only match can over-kick, never admit.
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
	done, after := 0, uint(0)
	for max == 0 || done < max {
		n, last, err := api.reconcileLegacyBatch(ctx, tenant, after)
		done += n
		after = last
		if err != nil {
			return done, err
		}
		if n < legacyLinkBatch {
			break
		}
	}
	return done, nil
}

// reconcileLegacyBatch handles the next page of unlinked credentials after the id `after`, and
// returns how many it looked at and the last id. Paging by id means a credential that cannot be
// linked (an unreadable leaf) is passed over, not retried for ever within one call.
func (api *Api) reconcileLegacyBatch(ctx context.Context, tenant string, after uint) (int, uint, error) {
	db := api.RDB.DB(ctx)
	var creds []DeviceCredential
	if err := db.Select("id", "tenant_id", "trust_anchor_id", "certificate_pem").
		Where("tenant_id = ? AND credential_type = ? AND trust_anchor_id IS NOT NULL AND certificate_pem IS NOT NULL", tenant, string(CredentialX509Certificate)).
		Where("NOT EXISTS (SELECT 1 FROM device_credential_chain_links l WHERE l.credential_id = device_credentials.id AND l.depth = 0)").
		Where("id > ?", after).Order("id ASC").Limit(legacyLinkBatch).Find(&creds).Error; err != nil {
		return 0, after, err
	}
	var links []DeviceCredentialChainLink
	for _, c := range creds {
		block, _ := pem.Decode([]byte(c.CertificatePem.String))
		if block == nil {
			log.Warn().Uint("credential", c.ID).Msg("A certificate credential's stored leaf is not PEM; it gets no chain link.")
			continue
		}
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			log.Warn().Err(err).Uint("credential", c.ID).Msg("A certificate credential's stored leaf does not parse; it gets no chain link.")
			continue
		}
		serial, ok := x509trust.SerialHex(leaf.SerialNumber)
		if !ok {
			continue
		}
		links = append(links, DeviceCredentialChainLink{
			CredentialId: c.ID, Depth: 0, TrustAnchorId: *c.TrustAnchorId,
			Fingerprint: x509trust.Fingerprint(leaf), IssuerName: x509trust.IssuerNameHash(leaf), Serial: serial,
		})
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

// reconcileLegacyChainLinksBestEffort runs the reconciler for the context's tenant and logs a
// failure: a revocation is enforced at connect whatever the links say, so the links never gate it.
func (api *Api) reconcileLegacyChainLinksBestEffort(ctx context.Context) {
	if _, err := api.ReconcileLegacyChainLinks(ctx, 0); err != nil {
		log.Warn().Err(err).Msg("Could not reconcile the chain links of legacy certificate credentials; a revocation is enforced at connect regardless.")
	}
}

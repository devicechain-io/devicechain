// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-device-management/model/identity"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"gorm.io/gorm"
)

// The CRL half of the revocation store: upload (put), replace and delete of one issuer's list
// under a trust anchor. Parsing and verifying a CRL (signature, issuer chain, every acceptance
// rule of x509trust.ParseRevocationList) happens HERE, on the operator's authenticated write
// path, never on the connect path.

type revocationConfig struct {
	limits RevocationLimits
	slots  chan struct{}
}

// SetRevocationLimits installs the store's bounds. It is called once while the service wires
// itself, before it serves; a zero or negative bound is an error, not "unbounded".
func (api *Api) SetRevocationLimits(l RevocationLimits) error {
	if l.MaxCrlBytes <= 0 || l.MaxCrlEntries <= 0 || l.MaxListsPerAnchor <= 0 || l.MaxManualEntriesPerTenant <= 0 ||
		l.MaxGraphqlCrlBytes <= 0 || l.MaxConcurrentWrites <= 0 || l.DefaultStaleGrace < 0 ||
		l.DefaultStaleGrace > MaxRevocationStaleGrace || l.MaxGraphqlCrlBytes > l.MaxCrlBytes {
		return errors.New("every revocation limit must be positive (the grace non-negative and within bounds, " +
			"the GraphQL bound no larger than the CRL bound)")
	}
	api.revocation.Store(&revocationConfig{limits: l, slots: make(chan struct{}, l.MaxConcurrentWrites)})
	return nil
}

func (api *Api) revocationCfg() *revocationConfig {
	if c := api.revocation.Load(); c != nil {
		return c
	}
	d := DefaultRevocationLimits()
	api.revocation.CompareAndSwap(nil, &revocationConfig{limits: d, slots: make(chan struct{}, d.MaxConcurrentWrites)})
	return api.revocation.Load()
}

// RevocationLimits reports the limits in force.
func (api *Api) RevocationLimits() RevocationLimits { return api.revocationCfg().limits }

func (api *Api) revocationLimits() RevocationLimits { return api.revocationCfg().limits }

// GraphqlCrlTextBound is the largest CRL text (PEM) the GraphQL mutation accepts: the PEM
// expansion (4/3, plus a newline per 64 characters, plus the armour) of MaxGraphqlCrlBytes of DER.
func (api *Api) GraphqlCrlTextBound() int {
	der := api.revocationLimits().MaxGraphqlCrlBytes
	return der/3*4 + der/48 + 256
}

// maxIssuerChainBytes bounds the issuer chain text an upload carries: a handful of CA
// certificates.
const maxIssuerChainBytes = 64 << 10

// acquireRevocationWrite takes one of the replica's CRL-write slots, waiting for it until ctx is done.
func (api *Api) acquireRevocationWrite(ctx context.Context) (func(), error) {
	slots := api.revocationCfg().slots
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// RevocationListPutRequest is one CRL upload.
type RevocationListPutRequest struct {
	AnchorToken string
	// Crl is the CRL, PEM or DER. The caller has bounded the read; it is bounded again here.
	Crl []byte
	// IssuerChainPem is the CRL signer first, then the CAs that link it to the anchor. Empty
	// means the anchor signed the CRL.
	IssuerChainPem string
	// MaxBytes narrows the size bound for the door the CRL came through (the GraphQL mutation's
	// is tighter than the REST upload's). Zero means the configured MaxCrlBytes; it can only
	// narrow it.
	MaxBytes int
}

// PutDeviceRevocationList stores a CRL as the current list of its issuer under a trust anchor,
// replacing the issuer's previous list, and materialises its entries (diffed against the previous
// list, batched).
//
// The CRL is parsed and its signature verified BEFORE the transaction opens (the cost is the
// caller's, bounded by the write slots), against the anchor as read then; the transaction then
// re-reads the anchor under its lock and refuses if it is no longer the one the list was verified
// against.
//
// LOCK ORDER: the CRL-writers advisory lock first (identity.LockCrlWriters), then the anchor's issuer rows
// FOR UPDATE (the rows that carry the CRL-number high-water mark), the list and its entries, and only
// then the anchor row FOR UPDATE with its counter and the kick queue rows. The global order and its
// one invariant are stated at revocationLockOrder (api_x509_revocation_kicks.go).
//
// 🔴 ANTI-ROLLBACK: a CRL whose number is not above the issuer's high-water mark is refused, and
// the mark is read and written under the locks above, so two concurrent uploads of numbers N+1
// and N+2 end with N+2 stored whichever commits first. The mark lives on the issuer row, which is
// never deleted, so it survives deleting the active list. The one exception is the very CRL
// already current (same SHA-256), which is a no-op.
func (api *Api) PutDeviceRevocationList(ctx context.Context, req *RevocationListPutRequest) (*RevocationListPutResult, error) {
	tenant, err := ctxTenant(ctx)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(req.AnchorToken)
	if token == "" {
		return nil, identity.InvalidAnchor("anchorToken is required")
	}
	lim := api.revocationLimits()
	maxBytes := lim.MaxCrlBytes
	if req.MaxBytes > 0 && req.MaxBytes < maxBytes {
		maxBytes = req.MaxBytes
	}
	// The size bound comes before any other work, the slot included.
	if len(req.Crl) > maxBytes {
		return nil, identity.ListRefused(string(x509trust.RuleTooLarge), "at most %d bytes", maxBytes)
	}
	if len(req.Crl) == 0 {
		return nil, identity.ListRefused(string(x509trust.RuleMalformed), "empty")
	}
	if len(req.IssuerChainPem) > maxIssuerChainBytes {
		return nil, identity.InvalidAnchor("issuerChainPem is larger than %d bytes", maxIssuerChainBytes)
	}
	release, err := api.acquireRevocationWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	anchor, err := api.trustAnchorByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	anchorCert, err := anchor.Certificate()
	if err != nil {
		return nil, err
	}
	var chain []*x509.Certificate
	if strings.TrimSpace(req.IssuerChainPem) != "" {
		_, certs, cerr := x509trust.ParseCertificates(req.IssuerChainPem)
		if cerr != nil {
			return nil, identity.InvalidAnchor("issuerChainPem: %v", cerr)
		}
		chain = certs
	}

	rl, err := x509trust.ParseRevocationListLimits(req.Crl, chain, anchorCert, time.Now(),
		x509trust.CRLLimits{MaxBytes: maxBytes, MaxEntries: lim.MaxCrlEntries})
	if err != nil {
		if rule := x509trust.ListRuleOf(err); rule != "" {
			return nil, identity.ListRefused(string(rule), "")
		}
		return nil, identity.InvalidAnchor("revocation list refused")
	}

	// Before any lock: the legacy credentials' links, so the kick can find them (see
	// prepareRevocationKicks). Bounded; what it leaves unlinked widens the kick.
	linksWhole := api.prepareRevocationKicks(ctx, tenant, token)
	keepNew := api.SessionKicker != nil

	var (
		res        *RevocationListPutResult
		entryCount int
	)
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		res = nil
		// 🔴 THE HEAVY WORK HAPPENS BEFORE THE ANCHOR ROW IS LOCKED. Writing the entries is the slow
		// part of an upload (a row costs index and foreign-key work), and a certificate registration
		// takes the anchor FOR SHARE inside the broker's 5 s authorization timeout, so it must never
		// wait for it. CRL writers of one anchor are serialised by an advisory lock taken FIRST
		// (identity.LockCrlWriters), which is what makes the unlocked-anchor part safe: nothing else writes a
		// list, an issuer row or a list's entries. The anchor row is locked FOR UPDATE only at the
		// end, for the counter and to re-verify the anchor, so that lock is held for one UPDATE and the
		// commit. Nothing is visible to anyone before the commit.
		//
		// What the writes DO hold meanwhile is the implicit FOR KEY SHARE on the anchor row that
		// inserting an issuer, a list or an entry takes through its foreign key, for the whole upload.
		// That blocks everything that takes the row FOR UPDATE (a manual revoke, an unrevoke, an anchor
		// update or delete) until the upload commits. It does NOT block a certificate registration,
		// which takes the row FOR SHARE, a mode that coexists with KEY SHARE. For the same reason
		// nothing here may take the CRL-writers lock while holding the anchor row in any mode.
		if err := identity.LockCrlWriters(tx, tenant, anchor.ID); err != nil {
			return err
		}
		// A plain read, taking no lock: an anchor deleted while this upload waited for the writers'
		// lock is refused here, in words, and not by a foreign-key error from the first insert.
		var present int64
		if err := tx.Model(&DeviceTrustAnchor{}).Where("id = ?", anchor.ID).Count(&present).Error; err != nil {
			return err
		}
		if present == 0 {
			return identity.InvalidAnchor("trust anchor %q changed while the list was being checked; upload it again", token)
		}
		// The parsed entries are released as soon as they are staged: nothing below is
		// proportional to them.
		if err := identity.StageCrlEntries(tx, rl.Entries); err != nil {
			return err
		}
		entryCount = len(rl.Entries)
		rl.Entries = nil
		var newList bool
		res, newList, err = identity.StoreRevocationList(tx, tenant, anchor, rl, entryCount, lim, keepNew)
		if err != nil {
			return err
		}
		if err := identity.DropCrlStage(tx); err != nil {
			return err
		}
		// Before the anchor lock too: the newly revoked serials are copied to the durable table, so the
		// locked part below writes one marker row (see expandRevocationMarkers).
		var kickGen int64
		if keepNew && res.NewlyRevoked > 0 {
			if kickGen, err = api.stashCrlKickSerials(tx, tenant, anchor.ID); err != nil {
				return err
			}
			if err := identity.DropCrlNew(tx); err != nil {
				return err
			}
		}
		if res.Unchanged {
			return nil
		}
		locked, err := identity.LockTrustAnchorByToken(tx, token)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("trust anchor %q: %w", token, gorm.ErrRecordNotFound)
			}
			return err
		}
		if locked.ID != anchor.ID || locked.FingerprintSha256 != anchor.FingerprintSha256 {
			return identity.InvalidAnchor("trust anchor %q changed while the list was being checked; upload it again", token)
		}
		if newList {
			if err := identity.BumpRevocationRows(tx, locked.ID, 1); err != nil {
				return err
			}
		}
		// Lock order: crl-writers advisory, anchor row, THEN the kick rows. The anchor is locked, so
		// a credential registered meanwhile has committed its links (and is matched) or waits for this
		// commit and finds the entries.
		if keepNew && res.NewlyRevoked > 0 {
			return api.queueCrlRevocationKicksOnTx(tx, tenant, locked.ID, x509trust.IssuerID{Key: rl.Issuer.Key, Name: rl.Issuer.Name}, kickGen, linksWhole)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// After the commit, and whether or not this upload wrote anything: a repeat of an upload whose
	// kick was unconfirmed finds the debt and asks again. ErrSessionsNotEnded rides with the result.
	kerr := api.endRevokedSessions(ctx, tenant, anchor.ID)
	res.SessionsEnded = kerr == nil
	return res, kerr
}

// DeleteDeviceRevocationList deletes the issuer's ACTIVE list and every entry it carried. The
// issuer's CRL-number high-water mark is kept, so uploading an older list afterwards is still a
// rollback. It reports whether there was a list to delete. Under REQUIRE_CURRENT the anchor then
// has no current list for that issuer and refuses its certificates (revocation_unknown); nothing
// is kicked.
//
// 🔴 THE ENTRIES ARE DELETED, not merely orphaned: the list row is soft-deleted, so the foreign
// key's cascade never fires, and the connect path reads entries by issuer and serial without
// asking whether their list still exists. An entry left behind would keep refusing a certificate
// for a list the operator removed.
//
// Lock order: the CRL-writers advisory lock, then the anchor row (see revocationLockOrder).
func (api *Api) DeleteDeviceRevocationList(ctx context.Context, anchorToken, issuerKey, issuerName string) (bool, error) {
	tenant, err := ctxTenant(ctx)
	if err != nil {
		return false, err
	}
	token := strings.TrimSpace(anchorToken)
	if token == "" {
		return false, identity.InvalidAnchor("anchorToken is required")
	}
	key, name := strings.ToLower(strings.TrimSpace(issuerKey)), strings.ToLower(strings.TrimSpace(issuerName))
	if !identity.ValidFingerprint(key) || !identity.ValidFingerprint(name) {
		return false, identity.InvalidAnchor("issuerKey and issuerName must each be 64 hexadecimal characters")
	}
	deleted := false
	named, err := api.trustAnchorByToken(ctx, token)
	if err != nil {
		return false, err
	}
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		deleted = false
		if err := identity.LockCrlWriters(tx, tenant, named.ID); err != nil {
			return err
		}
		anchor, err := identity.LockTrustAnchorByToken(tx, token)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("trust anchor %q: %w", token, gorm.ErrRecordNotFound)
			}
			return err
		}
		if anchor.ID != named.ID {
			return identity.InvalidAnchor("trust anchor %q was replaced while the delete was waiting; repeat it", token)
		}
		var lists []DeviceRevocationList
		if err := tx.Where("tenant_id = ? AND trust_anchor_id = ? AND issuer_key = ? AND issuer_name = ?",
			tenant, anchor.ID, key, name).Find(&lists).Error; err != nil {
			return err
		}
		for i := range lists {
			if err := tx.Where("tenant_id = ? AND trust_anchor_id = ? AND revocation_list_id = ?",
				tenant, anchor.ID, lists[i].ID).Delete(&identity.CrlEntryRow{}).Error; err != nil {
				return err
			}
			if err := tx.Delete(&lists[i]).Error; err != nil {
				return err
			}
		}
		if len(lists) == 0 {
			return nil
		}
		deleted = true
		return identity.BumpRevocationRows(tx, anchor.ID, -int64(len(lists)))
	})
	if err != nil {
		return false, err
	}
	return deleted, nil
}

// DeviceRevocationListsOf reads the anchor's current lists.
func (api *Api) DeviceRevocationListsOf(ctx context.Context, anchorId uint) ([]DeviceRevocationList, error) {
	tenant, err := ctxTenant(ctx)
	if err != nil {
		return nil, err
	}
	lists := make([]DeviceRevocationList, 0)
	if err := api.RDB.DB(ctx).Where("tenant_id = ? AND trust_anchor_id = ?", tenant, anchorId).
		Order("issuer_subject, id").Find(&lists).Error; err != nil {
		return nil, err
	}
	return lists, nil
}

// ─── mode and grace ─────────────────────────────────────────────────────────
//
// The freshness rules and the enum and grace parsers are in identity (x509_revocation_rules.go).

// applyRevocationSettings folds the revocation mode and grace of an anchor update onto the locked
// anchor row (the caller saves it). Switching to REQUIRE_CURRENT, or shortening the grace while in
// it, runs identity.RequireCurrentCoverage first, so the one click that would refuse the fleet is refused.
func (api *Api) applyRevocationSettings(tx *gorm.DB, locked *DeviceTrustAnchor, request *DeviceTrustAnchorUpdateRequest) error {
	if !request.RevocationMode.Set && !request.RevocationStaleGrace.Set {
		return nil
	}
	defaultGrace := api.revocationLimits().DefaultStaleGrace
	oldMode, err := locked.RevocationModeOf()
	if err != nil {
		return err
	}
	oldGrace, err := locked.RevocationStaleGraceOf(defaultGrace)
	if err != nil {
		return err
	}
	mode, grace := oldMode, oldGrace
	if request.RevocationMode.Set {
		if request.RevocationMode.Value == nil {
			return identity.InvalidAnchor("revocationMode cannot be cleared: send ENTRIES or REQUIRE_CURRENT")
		}
		m, ok := identity.RevocationModeFromEnum(*request.RevocationMode.Value)
		if !ok {
			return identity.InvalidAnchor("revocationMode must be ENTRIES or REQUIRE_CURRENT")
		}
		mode = m
		locked.RevocationMode = sql.NullString{String: string(m), Valid: true}
	}
	if request.RevocationStaleGrace.Set {
		if request.RevocationStaleGrace.Value == nil {
			locked.RevocationStaleGrace = sql.NullInt64{}
			grace = defaultGrace
		} else {
			g, err := identity.ParseRevocationGrace(*request.RevocationStaleGrace.Value)
			if err != nil {
				return err
			}
			grace = g
			locked.RevocationStaleGrace = sql.NullInt64{Int64: int64(g / time.Second), Valid: true}
		}
	}
	if mode == x509trust.ModeRequireCurrent && (oldMode != mode || oldGrace != grace) {
		return identity.RequireCurrentCoverage(tx, locked.TenantId, locked, grace, time.Now())
	}
	return nil
}

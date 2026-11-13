// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The CRL half of the revocation store: upload (put), replace and delete of one issuer's list
// under a trust anchor. Parsing and verifying a CRL (signature, issuer chain, every acceptance
// rule of x509trust.ParseRevocationList) happens HERE, on the operator's authenticated write
// path, never on the connect path.

// RevocationLimits are the fixed-configuration bounds of the store (deviceManagement.callout.
// revocation). Zero fields are not "unbounded": SetRevocationLimits refuses them.
type RevocationLimits struct {
	// MaxCrlBytes bounds a CRL as submitted (PEM or DER), checked before any parsing.
	MaxCrlBytes int
	// MaxCrlEntries bounds the revoked certificates one CRL lists.
	MaxCrlEntries int
	// MaxListsPerAnchor bounds the current lists one trust anchor holds.
	MaxListsPerAnchor int
	// MaxManualEntriesPerTenant bounds a tenant's manual revocations.
	MaxManualEntriesPerTenant int
	// DefaultStaleGrace is the grace an anchor that sets none gets.
	DefaultStaleGrace time.Duration
	// MaxGraphqlCrlBytes bounds the CRL the GraphQL mutation accepts, as DER; the mutation's PEM
	// text is held to GraphqlCrlTextBound of it.
	MaxGraphqlCrlBytes int
	// MaxConcurrentWrites bounds the CRL uploads one replica works on at once.
	MaxConcurrentWrites int
}

// DefaultRevocationLimits are the limits an Api runs with until SetRevocationLimits says otherwise;
// they equal the configuration's defaults.
func DefaultRevocationLimits() RevocationLimits {
	return RevocationLimits{
		MaxCrlBytes:               2 << 20,
		MaxCrlEntries:             100_000,
		MaxListsPerAnchor:         16,
		MaxManualEntriesPerTenant: MaxManualRevocationsPerTenant,
		DefaultStaleGrace:         DefaultRevocationStaleGrace,
		MaxGraphqlCrlBytes:        2 << 20,
		MaxConcurrentWrites:       1,
	}
}

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

// issuerRowsPerListFactor bounds the issuer rows (which are never deleted) an anchor accumulates
// to a multiple of the current lists it may hold, so deleting and uploading lists of ever-new
// issuers cannot grow the table without end.
const issuerRowsPerListFactor = 4

// Closed vocabulary of the rules the store (not the parser) enforces, named in the refusal.
const (
	RuleCrlNumberNotNewer     = "crl_number_not_newer"
	RuleConflictingIssuer     = "conflicting_issuer_identity"
	RuleTooManyIssuers        = "too_many_issuers"
	ruleRevocationListMissing = "no_revocation_list"
)

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

// RevocationListPutResult is the outcome of an upload.
type RevocationListPutResult struct {
	List DeviceRevocationList
	// NewlyRevoked counts the certificates the list names that the issuer's previous list did not
	// (or that no list named before): what a live-session kick will be owed for.
	NewlyRevoked int
	// Unchanged is true when the very same CRL (by its SHA-256) was already the current list: nothing
	// was written.
	Unchanged bool
	// Replaced is true when a previous list of the issuer was replaced.
	Replaced bool
	// Removed counts entries the previous list held that this one does not (un-revoked, as a
	// certificate hold released).
	Removed int
	// SessionsEnded is false when the live sessions the upload affects could not be confirmed ended
	// (the upload itself stands, and PutDeviceRevocationList returns ErrSessionsNotEnded with it).
	SessionsEnded bool
}

// listRefused is the refusal of a CRL the store (as opposed to the parser) will not take.
func listRefused(rule string, format string, args ...any) error {
	msg := "revocation list refused: " + rule
	if format != "" {
		msg += " (" + fmt.Sprintf(format, args...) + ")"
	}
	return invalidAnchor("%s", msg)
}

// crlEntryRow is DeviceRevokedCertificate for the bulk materialisation of a CRL's entries: the
// same table, but it opts OUT of the audit journal, so hundreds of thousands of derived rows are
// not journaled one by one. The list row carries the CRL's hash and entry count and IS journaled.
type crlEntryRow DeviceRevokedCertificate

func (crlEntryRow) TableName() string { return "device_revoked_certificates" }

// AuditExempt: a CRL entry is derived from the list the journal records.
func (crlEntryRow) AuditExempt() bool { return true }

// PutDeviceRevocationList stores a CRL as the current list of its issuer under a trust anchor,
// replacing the issuer's previous list, and materialises its entries (diffed against the previous
// list, batched).
//
// The CRL is parsed and its signature verified BEFORE the transaction opens (the cost is the
// caller's, bounded by the write slots), against the anchor as read then; the transaction then
// re-reads the anchor under its lock and refuses if it is no longer the one the list was verified
// against.
//
// LOCK ORDER: the CRL-writers advisory lock first (lockCrlWriters), then the anchor's issuer rows
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
		return nil, invalidAnchor("anchorToken is required")
	}
	lim := api.revocationLimits()
	maxBytes := lim.MaxCrlBytes
	if req.MaxBytes > 0 && req.MaxBytes < maxBytes {
		maxBytes = req.MaxBytes
	}
	// The size bound comes before any other work, the slot included.
	if len(req.Crl) > maxBytes {
		return nil, listRefused(string(x509trust.RuleTooLarge), "at most %d bytes", maxBytes)
	}
	if len(req.Crl) == 0 {
		return nil, listRefused(string(x509trust.RuleMalformed), "empty")
	}
	if len(req.IssuerChainPem) > maxIssuerChainBytes {
		return nil, invalidAnchor("issuerChainPem is larger than %d bytes", maxIssuerChainBytes)
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
			return nil, invalidAnchor("issuerChainPem: %v", cerr)
		}
		chain = certs
	}

	rl, err := x509trust.ParseRevocationListLimits(req.Crl, chain, anchorCert, time.Now(),
		x509trust.CRLLimits{MaxBytes: maxBytes, MaxEntries: lim.MaxCrlEntries})
	if err != nil {
		if rule := x509trust.ListRuleOf(err); rule != "" {
			return nil, listRefused(string(rule), "")
		}
		return nil, invalidAnchor("revocation list refused")
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
		// (lockCrlWriters), which is what makes the unlocked-anchor part safe: nothing else writes a
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
		if err := lockCrlWriters(tx, tenant, anchor.ID); err != nil {
			return err
		}
		// A plain read, taking no lock: an anchor deleted while this upload waited for the writers'
		// lock is refused here, in words, and not by a foreign-key error from the first insert.
		var present int64
		if err := tx.Model(&DeviceTrustAnchor{}).Where("id = ?", anchor.ID).Count(&present).Error; err != nil {
			return err
		}
		if present == 0 {
			return invalidAnchor("trust anchor %q changed while the list was being checked; upload it again", token)
		}
		// The parsed entries are released as soon as they are staged: nothing below is
		// proportional to them.
		if err := stageCrlEntries(tx, rl.Entries); err != nil {
			return err
		}
		entryCount = len(rl.Entries)
		rl.Entries = nil
		var newList bool
		res, newList, err = storeRevocationList(tx, tenant, anchor, rl, entryCount, lim, keepNew)
		if err != nil {
			return err
		}
		if err := dropCrlStage(tx); err != nil {
			return err
		}
		// Before the anchor lock too: the newly revoked serials are copied to the durable table, so the
		// locked part below writes one marker row (see expandRevocationMarkers).
		var kickGen int64
		if keepNew && res.NewlyRevoked > 0 {
			if kickGen, err = api.stashCrlKickSerials(tx, tenant, anchor.ID); err != nil {
				return err
			}
			if err := dropCrlNew(tx); err != nil {
				return err
			}
		}
		if res.Unchanged {
			return nil
		}
		locked, err := lockTrustAnchorByToken(tx, token)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("trust anchor %q: %w", token, gorm.ErrRecordNotFound)
			}
			return err
		}
		if locked.ID != anchor.ID || locked.FingerprintSha256 != anchor.FingerprintSha256 {
			return invalidAnchor("trust anchor %q changed while the list was being checked; upload it again", token)
		}
		if newList {
			if err := bumpRevocationRows(tx, locked.ID, 1); err != nil {
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

// lockCrlWriters serialises the CRL writes (upload, delete, and the delete of the anchor that
// discards them) of one anchor for the rest of the transaction. It is taken BEFORE the anchor row,
// so that an upload can stage and write its entries without holding that row: every writer of the
// lists, the issuer rows and the list entries passes through it, so none of them races another.
// A transaction-scoped advisory lock on PostgreSQL; the SQLite unit-test database has one writer
// at a time and nothing to take.
func lockCrlWriters(tx *gorm.DB, tenant string, anchorId uint) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", fmt.Sprintf("crl-writers:%s:%d", tenant, anchorId)).Error
}

// storeRevocationList is the write half of an upload, run in the transaction that holds the
// anchor's CRL-writer lock (and, deliberately, not yet the anchor row). newList reports that a list
// was created rather than replaced, which is the caller's counter to bump under the anchor lock.
func storeRevocationList(tx *gorm.DB, tenant string, anchor *DeviceTrustAnchor, rl *x509trust.RevocationList,
	entryCount int, lim RevocationLimits, keepNew bool) (res *RevocationListPutResult, newList bool, err error) {
	// Every issuer row of the anchor, locked: the exact one carries the high-water mark, and the
	// others decide whether the identity conflicts.
	var issuers []DeviceRevocationIssuer
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND trust_anchor_id = ?", tenant, anchor.ID).Order("id").Find(&issuers).Error; err != nil {
		return nil, false, err
	}
	var issuer *DeviceRevocationIssuer
	for i := range issuers {
		is := &issuers[i]
		if is.TenantId != tenant || is.TrustAnchorId != anchor.ID {
			return nil, false, errors.New("a revocation issuer read for another tenant or anchor")
		}
		sameKey, sameName := is.IssuerKey == rl.Issuer.Key, is.IssuerName == rl.Issuer.Name
		switch {
		case sameKey && sameName:
			issuer = is
		case sameKey || sameName:
			return nil, false, listRefused(RuleConflictingIssuer, "this anchor already holds a list for a CA that shares only its key or only its name with this signer")
		}
	}

	var active []DeviceRevocationList
	if err := tx.Where("tenant_id = ? AND trust_anchor_id = ?", tenant, anchor.ID).Order("id").Find(&active).Error; err != nil {
		return nil, false, err
	}
	var current *DeviceRevocationList
	for i := range active {
		if active[i].TenantId != tenant || active[i].TrustAnchorId != anchor.ID {
			return nil, false, errors.New("a revocation list read for another tenant or anchor")
		}
		if active[i].IssuerKey == rl.Issuer.Key && active[i].IssuerName == rl.Issuer.Name {
			current = &active[i]
		}
	}

	// The very CRL already in force: nothing to do, whatever the mark says.
	if current != nil && current.CrlSha256 == rl.SHA256 {
		return &RevocationListPutResult{List: *current, Unchanged: true}, false, nil
	}
	if issuer != nil {
		hw, ok := new(big.Int).SetString(issuer.HighWaterCrlNumber, 10)
		if !ok {
			return nil, false, fmt.Errorf("a stored CRL-number high-water mark is not a number")
		}
		if rl.Number.Cmp(hw) <= 0 {
			return nil, false, listRefused(RuleCrlNumberNotNewer, "this issuer's highest accepted CRL number is %s, this list is %s",
				hw.String(), rl.Number.String())
		}
	}
	if current == nil && len(active) >= lim.MaxListsPerAnchor {
		return nil, false, limit.Exceeded("revocation lists per trust anchor", len(active)+1, lim.MaxListsPerAnchor)
	}
	if issuer == nil && len(issuers) >= lim.MaxListsPerAnchor*issuerRowsPerListFactor {
		return nil, false, listRefused(RuleTooManyIssuers, "this anchor has recorded %d issuers; delete lists you no longer need only if they are not coming back", len(issuers))
	}

	number := rl.Number.String()
	if issuer == nil {
		issuer = &DeviceRevocationIssuer{TrustAnchorId: anchor.ID, IssuerKey: rl.Issuer.Key,
			IssuerName: rl.Issuer.Name, HighWaterCrlNumber: number}
		if err := tx.Create(issuer).Error; err != nil {
			return nil, false, err
		}
	} else if err := tx.Model(issuer).Update("high_water_crl_number", number).Error; err != nil {
		return nil, false, err
	}

	list := current
	replaced := current != nil
	if list == nil {
		list = &DeviceRevocationList{TrustAnchorId: anchor.ID, RevocationIssuerId: issuer.ID,
			IssuerKey: rl.Issuer.Key, IssuerName: rl.Issuer.Name}
	}
	list.IssuerCertificatePem = rl.SignerPEM
	list.IssuerSubject = rl.IssuerSubject
	list.CrlNumber = number
	list.ThisUpdate = rl.ThisUpdate
	list.NextUpdate = rl.NextUpdate
	list.EntryCount = int32(entryCount)
	list.CrlSha256 = rl.SHA256
	list.Source = RevocationSourceUpload
	if err := tx.Save(list).Error; err != nil {
		return nil, false, err
	}

	newly, removed, err := applyStagedEntries(tx, tenant, anchor.ID, list, keepNew)
	if err != nil {
		return nil, false, err
	}
	return &RevocationListPutResult{List: *list, NewlyRevoked: newly, Replaced: replaced, Removed: removed}, !replaced, nil
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
		return false, invalidAnchor("anchorToken is required")
	}
	key, name := strings.ToLower(strings.TrimSpace(issuerKey)), strings.ToLower(strings.TrimSpace(issuerName))
	if !validFingerprint(key) || !validFingerprint(name) {
		return false, invalidAnchor("issuerKey and issuerName must each be 64 hexadecimal characters")
	}
	deleted := false
	named, err := api.trustAnchorByToken(ctx, token)
	if err != nil {
		return false, err
	}
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		deleted = false
		if err := lockCrlWriters(tx, tenant, named.ID); err != nil {
			return err
		}
		anchor, err := lockTrustAnchorByToken(tx, token)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("trust anchor %q: %w", token, gorm.ErrRecordNotFound)
			}
			return err
		}
		if anchor.ID != named.ID {
			return invalidAnchor("trust anchor %q was replaced while the delete was waiting; repeat it", token)
		}
		var lists []DeviceRevocationList
		if err := tx.Where("tenant_id = ? AND trust_anchor_id = ? AND issuer_key = ? AND issuer_name = ?",
			tenant, anchor.ID, key, name).Find(&lists).Error; err != nil {
			return err
		}
		for i := range lists {
			if err := tx.Where("tenant_id = ? AND trust_anchor_id = ? AND revocation_list_id = ?",
				tenant, anchor.ID, lists[i].ID).Delete(&crlEntryRow{}).Error; err != nil {
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
		return bumpRevocationRows(tx, anchor.ID, -int64(len(lists)))
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

// AuditLabel is the issuer's key hash: public, and what an auditor looks the list up by.
func (l DeviceRevocationList) AuditLabel() string { return l.IssuerKey }

// ─── freshness ──────────────────────────────────────────────────────────────

// Freshness of a stored list, worst first in severity order for an anchor.
const (
	RevocationStatusNone     = "NONE"
	RevocationStatusFresh    = "FRESH"
	RevocationStatusExpiring = "EXPIRING"
	RevocationStatusStale    = "STALE"
)

// RevocationListStatus is a list's freshness at now. STALE means the list is past nextUpdate plus
// the grace (the instant REQUIRE_CURRENT stops counting it as current); EXPIRING means three
// quarters of its validity have passed (the instant the operator should have pushed the next one);
// otherwise FRESH.
func RevocationListStatus(l *DeviceRevocationList, grace time.Duration, now time.Time) string {
	if now.After(l.NextUpdate.Add(grace)) {
		return RevocationStatusStale
	}
	validity := l.NextUpdate.Sub(l.ThisUpdate)
	if validity > 0 && !now.Before(l.ThisUpdate.Add(validity/4*3)) {
		return RevocationStatusExpiring
	}
	return RevocationStatusFresh
}

// RevocationAnchorStatus is the worst status over the anchor's lists, NONE when it has none.
func RevocationAnchorStatus(lists []DeviceRevocationList, grace time.Duration, now time.Time) string {
	rank := map[string]int{RevocationStatusNone: 0, RevocationStatusFresh: 1, RevocationStatusExpiring: 2, RevocationStatusStale: 3}
	worst := RevocationStatusNone
	for i := range lists {
		if s := RevocationListStatus(&lists[i], grace, now); rank[s] > rank[worst] {
			worst = s
		}
	}
	return worst
}

// ─── mode and grace ─────────────────────────────────────────────────────────

// revocationModeName is the stored mode for a GraphQL enum name.
func revocationModeFromEnum(name string) (x509trust.Mode, bool) {
	switch m := x509trust.Mode(name); m {
	case x509trust.ModeEntries, x509trust.ModeRequireCurrent:
		return m, true
	}
	return "", false
}

// parseRevocationGrace reads a Go duration for an anchor's grace: whole seconds, 0 to the maximum.
func parseRevocationGrace(s string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return 0, invalidAnchor("revocationStaleGrace must be a duration such as \"24h\" or \"90m\"")
	}
	if d < 0 || d > MaxRevocationStaleGrace || d%time.Second != 0 {
		return 0, invalidAnchor("revocationStaleGrace must be a whole number of seconds between 0s and %s", MaxRevocationStaleGrace)
	}
	return d, nil
}

// maxUncoveredIssuersReported is how many uncovered issuers a refusal names.
const maxUncoveredIssuersReported = 5

// requireCurrentCoverage is the guard on switching an anchor to REQUIRE_CURRENT (or shortening its
// grace while it is): the anchor must hold at least one current list, and every issuer on a chain
// a live credential is known to have used must be covered by one. Without it one click would
// refuse every certificate device of the anchor (revocation_unknown). Chains never seen (a new
// intermediate) are not knowable here and are refused at connect, as designed. Called with the
// anchor locked FOR UPDATE.
func requireCurrentCoverage(tx *gorm.DB, tenant string, anchor *DeviceTrustAnchor, grace time.Duration, now time.Time) error {
	var lists []DeviceRevocationList
	if err := tx.Where("tenant_id = ? AND trust_anchor_id = ?", tenant, anchor.ID).Find(&lists).Error; err != nil {
		return err
	}
	var current []DeviceRevocationList
	for _, l := range lists {
		if !now.After(l.NextUpdate.Add(grace)) {
			current = append(current, l)
		}
	}
	if len(current) == 0 {
		return invalidAnchor("revocationMode REQUIRE_CURRENT is refused: trust anchor %q holds no current revocation list, "+
			"so every certificate under it would be refused (revocation_unknown). Upload the CA's CRL first", anchor.Token)
	}

	// The issuers on live credentials' chains that no current list covers, asked of the database
	// and capped: the answer is only ever "which, up to a handful", so no fleet-sized set is ever
	// materialised while the anchor is locked. A list covers an issuer link when the name matches and
	// the key does (a legacy link with no key is matched by name alone). At most MaxListsPerAnchor
	// lists, so the predicate is bounded too.
	var links []DeviceCredentialChainLink
	live := tx.Model(&DeviceCredential{}).Select("id").Where("tenant_id = ? AND enabled = ?", tenant, true)
	q := tx.Select("issuer_key", "issuer_name").Distinct().
		Where("tenant_id = ? AND trust_anchor_id = ? AND credential_id IN (?)", tenant, anchor.ID, live)
	for _, l := range current {
		q = q.Where("NOT (issuer_name = ? AND (issuer_key IS NULL OR issuer_key = ?))", l.IssuerName, l.IssuerKey)
	}
	if err := q.Order("issuer_name").Limit(maxUncoveredIssuersReported + 1).Find(&links).Error; err != nil {
		return err
	}
	var missing []string
	for _, ln := range links {
		missing = append(missing, ln.IssuerName[:min(12, len(ln.IssuerName))])
	}
	if len(missing) > 0 {
		if len(missing) > maxUncoveredIssuersReported {
			missing = append(missing[:maxUncoveredIssuersReported], "…")
		}
		return invalidAnchor("revocationMode REQUIRE_CURRENT is refused: registered certificates under trust anchor %q "+
			"were issued by CAs with no current revocation list (issuer name hash %s), and would be refused (revocation_unknown). "+
			"Upload their CRLs first", anchor.Token, strings.Join(missing, ", "))
	}
	return nil
}

// applyRevocationSettings folds the revocation mode and grace of an anchor update onto the locked
// anchor row (the caller saves it). Switching to REQUIRE_CURRENT, or shortening the grace while in
// it, runs requireCurrentCoverage first, so the one click that would refuse the fleet is refused.
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
			return invalidAnchor("revocationMode cannot be cleared: send ENTRIES or REQUIRE_CURRENT")
		}
		m, ok := revocationModeFromEnum(*request.RevocationMode.Value)
		if !ok {
			return invalidAnchor("revocationMode must be ENTRIES or REQUIRE_CURRENT")
		}
		mode = m
		locked.RevocationMode = sql.NullString{String: string(m), Valid: true}
	}
	if request.RevocationStaleGrace.Set {
		if request.RevocationStaleGrace.Value == nil {
			locked.RevocationStaleGrace = sql.NullInt64{}
			grace = defaultGrace
		} else {
			g, err := parseRevocationGrace(*request.RevocationStaleGrace.Value)
			if err != nil {
				return err
			}
			grace = g
			locked.RevocationStaleGrace = sql.NullInt64{Int64: int64(g / time.Second), Valid: true}
		}
	}
	if mode == x509trust.ModeRequireCurrent && (oldMode != mode || oldGrace != grace) {
		return requireCurrentCoverage(tx, locked.TenantId, locked, grace, time.Now())
	}
	return nil
}

// CrlTooLargeRefusal is the refusal of a CRL over a size bound, for a door that checks the size
// before it hands the CRL on.
func CrlTooLargeRefusal(bound int) error {
	return listRefused(string(x509trust.RuleTooLarge), "at most %d bytes", bound)
}

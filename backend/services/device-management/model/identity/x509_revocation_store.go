// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

// The revocation store's transaction layer: every function here works on the transaction its
// caller (model's *Api) opened and passes in, and none opens one of its own. The lock order is the
// caller's to keep (revocationLockOrder in model).

import (
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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

// CrlEntryRow is DeviceRevokedCertificate for the bulk materialisation of a CRL's entries: the
// same table, but it opts OUT of the audit journal, so hundreds of thousands of derived rows are
// not journaled one by one. The list row carries the CRL's hash and entry count and IS journaled.
type CrlEntryRow DeviceRevokedCertificate

func (CrlEntryRow) TableName() string { return "device_revoked_certificates" }

// AuditExempt: a CRL entry is derived from the list the journal records.
func (CrlEntryRow) AuditExempt() bool { return true }

// LockCrlWriters serialises the CRL writes (upload, delete, and the delete of the anchor that
// discards them) of one anchor for the rest of the transaction. It is taken BEFORE the anchor row,
// so that an upload can stage and write its entries without holding that row: every writer of the
// lists, the issuer rows and the list entries passes through it, so none of them races another.
// A transaction-scoped advisory lock on PostgreSQL; the SQLite unit-test database has one writer
// at a time and nothing to take.
func LockCrlWriters(tx *gorm.DB, tenant string, anchorId uint) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", fmt.Sprintf("crl-writers:%s:%d", tenant, anchorId)).Error
}

// StoreRevocationList is the write half of an upload, run in the transaction that holds the
// anchor's CRL-writer lock (and, deliberately, not yet the anchor row). newList reports that a list
// was created rather than replaced, which is the caller's counter to bump under the anchor lock.
func StoreRevocationList(tx *gorm.DB, tenant string, anchor *DeviceTrustAnchor, rl *x509trust.RevocationList,
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
			return nil, false, ListRefused(RuleConflictingIssuer, "this anchor already holds a list for a CA that shares only its key or only its name with this signer")
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
			return nil, false, ListRefused(RuleCrlNumberNotNewer, "this issuer's highest accepted CRL number is %s, this list is %s",
				hw.String(), rl.Number.String())
		}
	}
	if current == nil && len(active) >= lim.MaxListsPerAnchor {
		return nil, false, limit.Exceeded("revocation lists per trust anchor", len(active)+1, lim.MaxListsPerAnchor)
	}
	if issuer == nil && len(issuers) >= lim.MaxListsPerAnchor*issuerRowsPerListFactor {
		return nil, false, ListRefused(RuleTooManyIssuers, "this anchor has recorded %d issuers; delete lists you no longer need only if they are not coming back", len(issuers))
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

	newly, removed, err := ApplyStagedEntries(tx, tenant, anchor.ID, list, keepNew)
	if err != nil {
		return nil, false, err
	}
	return &RevocationListPutResult{List: *list, NewlyRevoked: newly, Replaced: replaced, Removed: removed}, !replaced, nil
}

// maxUncoveredIssuersReported is how many uncovered issuers a refusal names.
const maxUncoveredIssuersReported = 5

// RequireCurrentCoverage is the guard on switching an anchor to REQUIRE_CURRENT (or shortening its
// grace while it is): the anchor must hold at least one current list, and every issuer on a chain
// a live credential is known to have used must be covered by one. Without it one click would
// refuse every certificate device of the anchor (revocation_unknown). Chains never seen (a new
// intermediate) are not knowable here and are refused at connect, as designed. Called with the
// anchor locked FOR UPDATE.
func RequireCurrentCoverage(tx *gorm.DB, tenant string, anchor *DeviceTrustAnchor, grace time.Duration, now time.Time) error {
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
		return InvalidAnchor("revocationMode REQUIRE_CURRENT is refused: trust anchor %q holds no current revocation list, "+
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
		return InvalidAnchor("revocationMode REQUIRE_CURRENT is refused: registered certificates under trust anchor %q "+
			"were issued by CAs with no current revocation list (issuer name hash %s), and would be refused (revocation_unknown). "+
			"Upload their CRLs first", anchor.Token, strings.Join(missing, ", "))
	}
	return nil
}

// MaxRevocationLookupKeys bounds the identities one connect asks about. A verified path holds at
// most x509trust.MaxPresentedCerts certificates, and a device can steer only how many verified
// paths exist, not make them more than a handful; a query bigger than this is a defect, and is
// refused (fail closed) rather than answered.
const MaxRevocationLookupKeys = rdb.MaxLookupKeys

// ReadRevocationState is ResolveX509Revocation over a given handle, so the in-transaction recheck
// of a registration reads through the transaction that holds the anchor lock.
func ReadRevocationState(db *gorm.DB, tenant string, anchor *DeviceTrustAnchor, keys x509trust.RevocationQuery,
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
	if len(keys.Fingerprints) > MaxRevocationLookupKeys || len(keys.Serials) > MaxRevocationLookupKeys ||
		len(keys.Issuers) > MaxRevocationLookupKeys {
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

// RecheckRevocationLocked is the connect check re-run INSIDE a registration's transaction, under
// the anchor's FOR SHARE lock: the gate in the callout ran before the transaction opened, and a
// revocation that committed in between must refuse the registration with nothing written. The
// anchor row passed is the one read under the lock, so a mode changed meanwhile is the mode used.
// It returns the *x509trust.ChainError of a refusal (ReasonOf revoked or revocation_unknown), or
// any other error, and every non-nil result refuses.
func RecheckRevocationLocked(tx *gorm.DB, tenant string, anchor *DeviceTrustAnchor, verified *x509trust.Verified,
	defaultGrace time.Duration) error {
	if verified == nil {
		return errors.New("a certificate registration needs the verified chain to re-check its revocation")
	}
	state, err := ReadRevocationState(tx, tenant, anchor, verified.RevocationKeys(), time.Now(), defaultGrace)
	if err != nil {
		return err
	}
	_, err = verified.ApplyRevocation(state)
	return err
}

// BumpRevocationRows adds delta to the anchor's revocation_rows counter. The caller holds the
// anchor FOR UPDATE.
func BumpRevocationRows(tx *gorm.DB, anchorId uint, delta int64) error {
	// The model carries the anchor's id, so the audit journal names the row it changed.
	return tx.Model(&DeviceTrustAnchor{Model: gorm.Model{ID: anchorId}}).
		Update("revocation_rows", gorm.Expr("COALESCE(revocation_rows, 0) + ?", delta)).Error
}

// ManualRevocationRows counts the manual revocation entries of the anchor.
func ManualRevocationRows(tx *gorm.DB, anchorId uint) (int64, error) {
	var n int64
	err := tx.Model(&DeviceRevokedCertificate{}).Where("trust_anchor_id = ? AND kind = ?", anchorId, RevokedKindManual).Count(&n).Error
	return n, err
}

// DiscardCrlRevocations deletes everything the anchor holds from CRLs, for good: the entries, the
// lists (soft-deleted ones included) and the issuer rows. Children first, because the foreign keys
// are RESTRICT. The caller holds the anchor FOR UPDATE and is deleting the anchor.
func DiscardCrlRevocations(tx *gorm.DB, tenant string, anchorId uint) error {
	if err := tx.Where("tenant_id = ? AND trust_anchor_id = ? AND kind = ?", tenant, anchorId, RevokedKindCRL).
		Delete(&CrlEntryRow{}).Error; err != nil {
		return err
	}
	if err := tx.Unscoped().Where("tenant_id = ? AND trust_anchor_id = ?", tenant, anchorId).
		Delete(&DeviceRevocationList{}).Error; err != nil {
		return err
	}
	return tx.Where("tenant_id = ? AND trust_anchor_id = ?", tenant, anchorId).Delete(&DeviceRevocationIssuer{}).Error
}

// RefuseIfEnforcedUnderOtherAnchors is the revoke-time guard for the per-anchor store: a revocation
// is enforced only for chains verified against the anchor it is recorded under. If the fingerprint
// pins a certificate credential, or sits on a recorded chain, under ANOTHER anchor of the tenant
// and not under this one, a "success" here would leave those devices connecting. That is refused,
// naming the anchor(s) whose store would enforce it. Called with the named anchor locked.
func RefuseIfEnforcedUnderOtherAnchors(tx *gorm.DB, tenant string, anchor *DeviceTrustAnchor, fp string) error {
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
	return InvalidAnchor("this certificate is used under trust anchor(s) %s, not under %q, and a revocation is enforced only "+
		"under the anchor it is recorded for; revoke it under %s", strings.Join(tokens, ", "), anchor.Token, strings.Join(tokens, " or "))
}

// LockTrustAnchorByToken reads the anchor with this token on tx, locking its row FOR UPDATE.
// Disable, delete and every write that binds something to an anchor serialize on it. A token
// that names nothing in the tenant is gorm.ErrRecordNotFound.
func LockTrustAnchorByToken(tx *gorm.DB, token string) (*DeviceTrustAnchor, error) {
	var locked DeviceTrustAnchor
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token = ?", token).First(&locked).Error; err != nil {
		return nil, err
	}
	return &locked, nil
}

// LockTrustAnchorShareByToken reads the anchor with this token on tx, locking its row FOR SHARE
// to the commit. A write that BINDS something to an anchor (a credential, a profile) holds it
// while it re-checks that the anchor is still enabled and then inserts, so a disable or a delete
// (FOR UPDATE) either finishes first, and the bind sees it, or waits for the bind and sees what
// it added. An anchor deleted meanwhile is gorm.ErrRecordNotFound.
func LockTrustAnchorShareByToken(tx *gorm.DB, token string) (*DeviceTrustAnchor, error) {
	var locked DeviceTrustAnchor
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("token = ?", token).First(&locked).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("trust anchor %q: %w", token, gorm.ErrRecordNotFound)
		}
		return nil, err
	}
	return &locked, nil
}

// LockTrustAnchorShareByID locks the anchor row FOR SHARE to the commit. An anchor already deleted
// is not an error: there is no revocation of it to race.
func LockTrustAnchorShareByID(tx *gorm.DB, id uint) error {
	var a DeviceTrustAnchor
	err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ?", id).Take(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}

// WriteChainLinks records the links of a credential just inserted, in the credential's own
// transaction. It is idempotent on (credential, depth).
func WriteChainLinks(tx *gorm.DB, credentialId, anchorId uint, chain []*x509.Certificate) error {
	links, err := ChainLinksOf(credentialId, anchorId, chain)
	if err != nil {
		return err
	}
	if len(links) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&links).Error
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

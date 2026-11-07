// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 🔴 THE KICK QUEUE OF A REVOCATION IS WRITTEN OUTSIDE THE ANCHOR LOCK.
//
// A revocation write holds its trust anchor row FOR UPDATE, and a certificate registration under
// the anchor takes it FOR SHARE inside the broker's authorization timeout, so whatever runs under
// that lock is time a registration waits. Finding every device a CA-level entry ends and
// upserting a pending_revocation_kicks row for each is O(devices) (2.2 s at 100 000 devices on local
// Postgres, against a 3 s callout lookup timeout). So the locked transaction (tx1) writes only a
// durable MARKER, O(1), and commits. A second transaction (tx2) that takes NO anchor lock and no
// advisory lock expands the marker into the per-device rows and deletes the marker in the same
// commit. The background drain expands any marker nobody expanded (a crash between tx1 and tx2, or
// a tx2 that failed), and the writer's own call reports ErrSessionsNotEnded while its marker
// stands, so a marker is a debt like any other.
//
// WHY EXPANDING OUTSIDE THE LOCK LOSES NOTHING (READ COMMITTED). Every statement of tx2 takes its
// own snapshot, and tx2 starts after tx1 committed. A session the revocation must end belongs to a
// credential that is in one of three states with respect to tx1:
//
//  1. Registered and COMMITTED before tx1 committed. Its credential and links are visible to every
//     statement of tx2, so tx2's device set contains it.
//  2. Registered but not yet committed when tx1 asked for the lock. A registration holds the
//     anchor FOR SHARE from its recheck to its commit and tx1 asked for FOR UPDATE, so tx1 WAITED
//     for it and committed after it: this is case 1.
//  3. Registering after tx1 committed. Its FOR SHARE recheck now reads the committed entry and
//     refuses it, so no credential, and therefore no session, is created.
//
// So tx2's device set is a SUPERSET of the set the locked transaction would have read. Under READ
// COMMITTED tx2 can also see credentials committed after tx1 (a registration of a different
// certificate, say), which only ADDS devices: one extra disconnect of a session that reconnects,
// never a missed one. The per-device rows are idempotent (an upsert on tenant, device and origin),
// so a tx2 that ran twice, or two expanders, converge on the same rows; the re-sweep already
// covers an admission that was in flight at the commit.
//
// A CREDENTIAL THAT LEAVES THE SET BETWEEN tx1 AND tx2 (found in review). The argument above covers
// credentials that ENTER the set. One that is deleted, or moved to another device, after tx1
// committed and before tx2 read would no longer name its device, and a credential delete queues no
// durable kick of its own, so the previous design's guarantee (the device was queued under the
// lock) would be lost. The two write paths that remove a certificate credential from a device
// (DeleteDeviceCredential and a re-point in UpdateDeviceCredential; a device delete and a reset
// queue their own durable kicks) therefore lock the credential's anchor FOR SHARE, like a
// registration, and in the same transaction queue the device's revocation-origin kick row when a
// marker of that anchor is on record (queueOwedRevocationKicksOnTx). With the anchor locked, such a
// write either committed before the revocation's tx1 (the credential was already gone then, which is
// the old design's outcome too) or after it, when the committed marker is visible and the row is
// written. Marker commit, expansion and these writes all converge on the same idempotent
// (tenant, device, origin) row.
//
// MIXED VERSIONS. During a rolling upgrade a pod from a release without this protocol may delete or
// move a certificate credential in the window between a revocation's tx1 and tx2: it neither checks the
// marker nor queues a durable kick, so that device is lost if the pod's own inline kick also fails. The
// window is the milliseconds between two statements of one call, and a loss needs both events; it is
// left, because an old pod cannot be made to honour a marker. (An old pod has no revocation of its own
// to write, so the gap is only the one a credential delete always had.)
//
// The lock order is unchanged: crl-writers advisory, anchor row, then queue rows. tx2 takes the
// marker row and then queue rows, and neither of the former.

// Marker kinds.
const (
	revocationMarkerManual = "MANUAL"
	revocationMarkerCRL    = "CRL"
)

// PendingRevocationExpansion records that a revocation write committed and its per-device kick rows
// have not been written yet. See the comment above for the protocol.
type PendingRevocationExpansion struct {
	TenantId      string `gorm:"primaryKey;not null;size:128"`
	TrustAnchorId uint   `gorm:"primaryKey;not null;autoIncrement:false"`
	// Generation tells two revocations of one anchor apart.
	Generation int64  `gorm:"primaryKey;not null;autoIncrement:false"`
	Kind       string `gorm:"not null;size:16"`
	Ca         bool   `gorm:"not null"`
	LinksWhole bool   `gorm:"not null"`
	// Fingerprint names a MANUAL entry; IssuerKey and IssuerName name the issuer of a CRL's.
	Fingerprint *string   `gorm:"size:64"`
	IssuerKey   *string   `gorm:"size:64"`
	IssuerName  *string   `gorm:"size:64"`
	RequestedAt time.Time `gorm:"not null"`
}

// AuditExempt: bookkeeping derived from a revocation the journal already records.
func (PendingRevocationExpansion) AuditExempt() bool { return true }

// PendingRevocationExpansionSerial is a serial a CRL upload newly revoked, kept with its marker's Generation
// until the marker is expanded.
type PendingRevocationExpansionSerial struct {
	TenantId      string `gorm:"primaryKey;not null;size:128"`
	TrustAnchorId uint   `gorm:"primaryKey;not null;autoIncrement:false"`
	Generation    int64  `gorm:"primaryKey;not null;autoIncrement:false"`
	Serial        string `gorm:"primaryKey;not null;size:64"`
}

// AuditExempt: bookkeeping, as for the marker.
func (PendingRevocationExpansionSerial) AuditExempt() bool { return true }

// revocationExpandHook, when set, runs at the start of every expansion, before it reads anything.
// A test pauses or fails the expansion here; it is nil in production.
var revocationExpandHook func() error

// revocationBeforeExpandHook, when set, runs before each marker's expansion transaction opens, so it
// holds no database lock. A test that writes concurrently with the window between a revocation's commit
// and its expansion uses it; nil in production.
var revocationBeforeExpandHook func()

// maxMarkersPerExpansion bounds the markers one expansion call handles.
var maxMarkersPerExpansion = 100

// writeRevocationMarkerOnTx records the marker in tx (the revocation's transaction, the anchor
// locked). It writes one row.
func writeRevocationMarkerOnTx(tx *gorm.DB, m PendingRevocationExpansion) error {
	if err := tx.Create(&m).Error; err != nil {
		return fmt.Errorf("recording the session kicks a revocation owes: %w", err)
	}
	return nil
}

// stashCrlRevocationSerials copies the serials the upload newly revoked (crlNewTable) into the
// durable table under a fresh generation and returns it. It runs BEFORE the anchor row is locked:
// a row costs index work, and the lock must cover only the marker.
func stashCrlRevocationSerials(tx *gorm.DB, tenant string, anchorId uint) (int64, error) {
	gen := rand.Int64()
	if err := tx.Exec("INSERT INTO pending_revocation_expansion_serials (tenant_id, trust_anchor_id, generation, serial) "+
		"SELECT ?, ?, ?, n.serial FROM "+crlNewTable+" n", tenant, anchorId, gen).Error; err != nil {
		return 0, fmt.Errorf("recording the serials a CRL newly revokes: %w", err)
	}
	return gen, nil
}

// expandMarkerOnTx turns one marker into its per-device kick rows and deletes it, all in tx. It is
// the one place the expansion happens, and it must never run in a transaction that holds an anchor
// lock.
func (api *Api) expandMarkerOnTx(tx *gorm.DB, m PendingRevocationExpansion) (int, error) {
	if revocationExpandHook != nil {
		if err := revocationExpandHook(); err != nil {
			return 0, err
		}
	}
	var tokens []string
	var err error
	switch m.Kind {
	case revocationMarkerManual:
		if m.Fingerprint == nil {
			return 0, errors.New("a manual revocation marker without a fingerprint")
		}
		tokens, _, err = revocationKickTargets(tx, m.TenantId, m.TrustAnchorId,
			RevocationDelta{Fingerprints: []string{*m.Fingerprint}, CA: m.Ca}, m.LinksWhole)
	case revocationMarkerCRL:
		if m.IssuerKey == nil || m.IssuerName == nil {
			return 0, errors.New("a CRL revocation marker without an issuer")
		}
		tokens, err = crlRevocationKickTargets(tx, m.TenantId, m.TrustAnchorId,
			x509trust.IssuerID{Key: *m.IssuerKey, Name: *m.IssuerName}, m.Generation, m.LinksWhole)
	default:
		return 0, fmt.Errorf("a revocation marker of unknown kind %q", m.Kind)
	}
	if err != nil {
		return 0, fmt.Errorf("finding the devices whose sessions a revocation ends: %w", err)
	}
	if err := api.queueRevocationKickRowsOnTx(tx, m.TenantId, m.TrustAnchorId, tokens); err != nil {
		return 0, err
	}
	if err := tx.Where("tenant_id = ? AND trust_anchor_id = ? AND generation = ?", m.TenantId, m.TrustAnchorId, m.Generation).
		Delete(&PendingRevocationExpansionSerial{}).Error; err != nil {
		return 0, err
	}
	if err := tx.Where("tenant_id = ? AND trust_anchor_id = ? AND generation = ?", m.TenantId, m.TrustAnchorId, m.Generation).
		Delete(&PendingRevocationExpansion{}).Error; err != nil {
		return 0, err
	}
	return len(tokens), nil
}

// markerKey names one marker.
type markerKey struct {
	anchor     uint
	generation int64
}

// expandRevocationMarkers expands the markers of tenant, those of one anchor when anchorId is not
// 0, each in its own transaction that holds no anchor lock. A marker another expander holds is
// WAITED for (FOR UPDATE), then found gone and skipped: the writer's call reports a revocation settled
// only when no marker IT LISTED stands, so a concurrent writer or the background drain expanding the
// same marker must not make it report "not yet" for the few milliseconds the other needs. Waiting
// cannot deadlock: every expander takes the marker row first and the queue rows after, in sorted
// order, and nothing that holds a queue row waits for a marker. It returns the markers it listed (the
// debts this call is answerable for) and every error after trying every marker it read.
//
// It lists in rounds of maxMarkersPerExpansion, NEWEST first, until a round comes back short or the
// bound is reached: the marker the caller itself just committed is the newest, so it is reached however
// many older ones stand (a backlog past the bound is the background drain's, which works through it
// pass by pass).
func (api *Api) expandRevocationMarkers(ctx context.Context, tenant string, anchorId uint) ([]markerKey, error) {
	if api.SessionKicker == nil {
		return nil, nil
	}
	db := api.RDB.DB(core.WithTenant(ctx, tenant))
	var listed []markerKey
	var errs []error
	for round := 0; round < maxExpansionRounds; round++ {
		q := db.Where("tenant_id = ?", tenant).Order("requested_at DESC, generation DESC").Limit(maxMarkersPerExpansion)
		if anchorId != 0 {
			q = q.Where("trust_anchor_id = ?", anchorId)
		}
		if len(listed) > 0 {
			// Past the ones already tried this call; a failed one is not retried in the same call.
			exclude := make([][]any, len(listed))
			for i, k := range listed {
				exclude[i] = []any{k.anchor, k.generation}
			}
			q = q.Where("(trust_anchor_id, generation) NOT IN ?", exclude)
		}
		var markers []PendingRevocationExpansion
		if err := q.Find(&markers).Error; err != nil {
			return listed, errors.Join(append(errs, fmt.Errorf("reading the pending revocation markers: %w", err))...)
		}
		for _, m := range markers {
			listed = append(listed, markerKey{m.TrustAnchorId, m.Generation})
			if revocationBeforeExpandHook != nil {
				revocationBeforeExpandHook()
			}
			err := db.Transaction(func(tx *gorm.DB) error {
				// Re-read under the row lock: another expander may have finished it since the list.
				lock := tx.Where("tenant_id = ? AND trust_anchor_id = ? AND generation = ?", tenant, m.TrustAnchorId, m.Generation)
				if tx.Dialector.Name() == "postgres" {
					lock = lock.Clauses(clause.Locking{Strength: "UPDATE"})
				}
				var held []PendingRevocationExpansion
				if err := lock.Find(&held).Error; err != nil {
					return err
				}
				if len(held) == 0 {
					return nil
				}
				_, err := api.expandMarkerOnTx(tx, held[0])
				return err
			})
			if err != nil {
				errs = append(errs, err)
			}
		}
		if len(markers) < maxMarkersPerExpansion {
			break
		}
	}
	return listed, errors.Join(errs...)
}

// maxExpansionRounds bounds the rounds of one expansion call. A variable so a test can shrink it.
var maxExpansionRounds = 20

// markersStanding reports whether any of the listed markers is still on record.
func (api *Api) markersStanding(ctx context.Context, tenant string, keys []markerKey) (bool, error) {
	for chunk := range slices.Chunk(keys, rdb.MaxLookupKeys) {
		pairs := make([][]any, len(chunk))
		for i, k := range chunk {
			pairs[i] = []any{k.anchor, k.generation}
		}
		var one []PendingRevocationExpansion
		if err := api.RDB.DB(core.WithTenant(ctx, tenant)).
			Where("tenant_id = ? AND (trust_anchor_id, generation) IN ?", tenant, pairs).Limit(1).Find(&one).Error; err != nil {
			return false, err
		}
		if len(one) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// tenantsOwingMarkers lists the tenants that hold an unexpanded revocation marker. Like
// tenantsOwingKicks it is a cross-tenant read of tenant ids only, under a system context.
func (api *Api) tenantsOwingMarkers(ctx context.Context) ([]string, error) {
	tenants := make([]string, 0)
	if err := api.RDB.DB(core.WithSystemContext(ctx)).Model(&PendingRevocationExpansion{}).
		Distinct().Order("tenant_id").Pluck("tenant_id", &tenants).Error; err != nil {
		return nil, err
	}
	return tenants, nil
}

// lockTrustAnchorShareByID locks the anchor row FOR SHARE to the commit. An anchor already deleted
// is not an error: there is no revocation of it to race.
func lockTrustAnchorShareByID(tx *gorm.DB, id uint) error {
	var a DeviceTrustAnchor
	err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ?", id).Take(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}

// queueOwedRevocationKicksOnTx queues, in tx, the revocation-origin kick of the given devices of the
// anchor when an unexpanded revocation marker of that anchor is on record. The caller holds the
// anchor FOR SHARE (see the comment on the marker protocol), and the rows are written after it in the
// lock order, taking only the queue rows the expansion also takes (in sorted order).
func (api *Api) queueOwedRevocationKicksOnTx(tx *gorm.DB, tenant string, anchorId uint, deviceTokens []string) error {
	if api.SessionKicker == nil {
		return nil
	}
	var one []PendingRevocationExpansion
	if err := tx.Where("tenant_id = ? AND trust_anchor_id = ?", tenant, anchorId).Limit(1).Find(&one).Error; err != nil {
		return err
	}
	if len(one) == 0 {
		return nil
	}
	return api.queueRevocationKickRowsOnTx(tx, tenant, anchorId, deviceTokens)
}

// markerGauge is the count of unexpanded revocation markers and the age of the oldest, for the gauge
// and alert that cover the kick queues.
func (api *Api) markerGauge(db *gorm.DB) (n int, age time.Duration, err error) {
	var count int64
	if err = db.Model(&PendingRevocationExpansion{}).Count(&count).Error; err != nil {
		return 0, 0, err
	}
	if count > 0 {
		var oldest []PendingRevocationExpansion
		if err = db.Order("requested_at ASC").Limit(1).Find(&oldest).Error; err != nil {
			return 0, 0, err
		}
		if len(oldest) == 1 {
			age = max(time.Since(oldest[0].RequestedAt), 0)
		}
	}
	return int(count), age, nil
}

// widenOwedMarkersOnTx is called, in the transaction that is about to DELETE a certificate credential
// (the anchor already locked FOR SHARE), before the delete: when the credential carries a link at
// depth 1 or more, an unexpanded marker of the anchor may have been going to widen to the whole anchor
// BECAUSE of that link (a bare-fingerprint revoke of an intermediate is CA-level only if some link at
// depth >= 1 names it), and the delete cascades the link away. So the anchor's markers are marked
// "links not whole", which makes their expansion cover the anchor: an over-kick of devices that
// reconnect, never a miss. Nothing is widened when the credential has no such link or no marker stands.
func (api *Api) widenOwedMarkersOnTx(tx *gorm.DB, tenant string, anchorId, credentialId uint) error {
	if api.SessionKicker == nil {
		return nil
	}
	var deep int64
	if err := tx.Model(&DeviceCredentialChainLink{}).
		Where("tenant_id = ? AND credential_id = ? AND depth >= 1", tenant, credentialId).Count(&deep).Error; err != nil {
		return err
	}
	if deep == 0 {
		return nil
	}
	return tx.Model(&PendingRevocationExpansion{}).
		Where("tenant_id = ? AND trust_anchor_id = ? AND links_whole = ?", tenant, anchorId, true).
		Update("links_whole", false).Error
}

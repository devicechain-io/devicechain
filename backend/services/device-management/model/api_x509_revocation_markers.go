// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 🔴 THE KICK QUEUE OF A REVOCATION IS WRITTEN OUTSIDE THE ANCHOR LOCK.
//
// A revocation write holds its trust anchor row FOR UPDATE, and a certificate registration under
// the anchor takes it FOR SHARE inside the broker's authorization timeout, so whatever runs under
// that lock is time a registration waits. Finding every device a CA-level entry ends and
// upserting a pending_session_kicks row for each is O(devices) (2.2 s at 100 000 devices on local
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
// The lock order is unchanged: crl-writers advisory, anchor row, then queue rows. tx2 takes the
// marker row and then queue rows, and neither of the former.

// Marker kinds.
const (
	revocationMarkerManual = "MANUAL"
	revocationMarkerCRL    = "CRL"
)

// PendingRevocationKick records that a revocation write committed and its per-device kick rows
// have not been written yet. See the comment above for the protocol.
type PendingRevocationKick struct {
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
func (PendingRevocationKick) AuditExempt() bool { return true }

// PendingRevocationSerial is a serial a CRL upload newly revoked, kept with its marker's Generation
// until the marker is expanded.
type PendingRevocationSerial struct {
	TenantId      string `gorm:"primaryKey;not null;size:128"`
	TrustAnchorId uint   `gorm:"primaryKey;not null;autoIncrement:false"`
	Generation    int64  `gorm:"primaryKey;not null;autoIncrement:false"`
	Serial        string `gorm:"primaryKey;not null;size:64"`
}

// AuditExempt: bookkeeping, as for the marker.
func (PendingRevocationSerial) AuditExempt() bool { return true }

// revocationExpandHook, when set, runs at the start of every expansion, before it reads anything.
// A test pauses or fails the expansion here; it is nil in production.
var revocationExpandHook func() error

// maxMarkersPerExpansion bounds the markers one expansion call handles.
const maxMarkersPerExpansion = 100

// writeRevocationMarkerOnTx records the marker in tx (the revocation's transaction, the anchor
// locked). It writes one row.
func writeRevocationMarkerOnTx(tx *gorm.DB, m PendingRevocationKick) error {
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
	if err := tx.Exec("INSERT INTO pending_revocation_serials (tenant_id, trust_anchor_id, generation, serial) "+
		"SELECT ?, ?, ?, n.serial FROM "+crlNewTable+" n", tenant, anchorId, gen).Error; err != nil {
		return 0, fmt.Errorf("recording the serials a CRL newly revokes: %w", err)
	}
	return gen, nil
}

// expandMarkerOnTx turns one marker into its per-device kick rows and deletes it, all in tx. It is
// the one place the expansion happens, and it must never run in a transaction that holds an anchor
// lock.
func (api *Api) expandMarkerOnTx(tx *gorm.DB, m PendingRevocationKick) (int, error) {
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
	if err := api.queueSessionKicksOnTx(tx, m.TenantId, revocationKickOrigin(m.TrustAnchorId), tokens); err != nil {
		return 0, err
	}
	if err := tx.Where("tenant_id = ? AND trust_anchor_id = ? AND generation = ?", m.TenantId, m.TrustAnchorId, m.Generation).
		Delete(&PendingRevocationSerial{}).Error; err != nil {
		return 0, err
	}
	if err := tx.Where("tenant_id = ? AND trust_anchor_id = ? AND generation = ?", m.TenantId, m.TrustAnchorId, m.Generation).
		Delete(&PendingRevocationKick{}).Error; err != nil {
		return 0, err
	}
	return len(tokens), nil
}

// expandRevocationMarkers expands the markers of tenant, those of one anchor when anchorId is not
// 0, each in its own transaction that holds no anchor lock. A marker another expander holds is
// skipped (FOR UPDATE SKIP LOCKED), not waited for. It returns every error after trying every
// marker it read.
func (api *Api) expandRevocationMarkers(ctx context.Context, tenant string, anchorId uint) error {
	if api.SessionKicker == nil {
		return nil
	}
	db := api.RDB.DB(core.WithTenant(ctx, tenant))
	q := db.Where("tenant_id = ?", tenant).Order("requested_at ASC, generation ASC").Limit(maxMarkersPerExpansion)
	if anchorId != 0 {
		q = q.Where("trust_anchor_id = ?", anchorId)
	}
	var markers []PendingRevocationKick
	if err := q.Find(&markers).Error; err != nil {
		return fmt.Errorf("reading the pending revocation markers: %w", err)
	}
	var errs []error
	for _, m := range markers {
		err := db.Transaction(func(tx *gorm.DB) error {
			// Re-read under the row lock: another expander may have finished it since the list.
			lock := tx.Where("tenant_id = ? AND trust_anchor_id = ? AND generation = ?", tenant, m.TrustAnchorId, m.Generation)
			if tx.Dialector.Name() == "postgres" {
				lock = lock.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
			}
			var held []PendingRevocationKick
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
	return errors.Join(errs...)
}

// revocationMarkersOwed reports whether tenant still holds a marker of the anchor.
func (api *Api) revocationMarkersOwed(ctx context.Context, tenant string, anchorId uint) (bool, error) {
	var one []PendingRevocationKick
	err := api.RDB.DB(core.WithTenant(ctx, tenant)).
		Where("tenant_id = ? AND trust_anchor_id = ?", tenant, anchorId).Limit(1).Find(&one).Error
	return len(one) > 0, err
}

// tenantsOwingMarkers lists the tenants that hold an unexpanded revocation marker. Like
// tenantsOwingKicks it is a cross-tenant read of tenant ids only, under a system context.
func (api *Api) tenantsOwingMarkers(ctx context.Context) ([]string, error) {
	tenants := make([]string, 0)
	if err := api.RDB.DB(core.WithSystemContext(ctx)).Model(&PendingRevocationKick{}).
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
	var one []PendingRevocationKick
	if err := tx.Where("tenant_id = ? AND trust_anchor_id = ?", tenant, anchorId).Limit(1).Find(&one).Error; err != nil {
		return err
	}
	if len(one) == 0 {
		return nil
	}
	return api.queueSessionKicksOnTx(tx, tenant, revocationKickOrigin(anchorId), deviceTokens)
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	"github.com/devicechain-io/dc-microservice/x509trust"
	"gorm.io/gorm"
)

// The set-based half of a CRL write. An upload's entries are staged in a table that lives for one
// transaction, and the stored list is made equal to the staged set by SQL: the Go side holds one
// batch of rows at a time and counts, never the entries. Writing the entries is the slow part of an
// upload, so it happens BEFORE the anchor row is locked.

// stageBatch is the staged rows per INSERT: three columns a row, far under PostgreSQL's 65 535
// parameters.
const stageBatch = 5000

// crlStageTable is the per-transaction table a CRL's entries are staged in.
const crlStageTable = "dc_crl_stage"

// crlStageRow is one staged entry.
type crlStageRow struct {
	Serial    string
	Reason    string
	RevokedAt time.Time
}

func (crlStageRow) TableName() string { return crlStageTable }

// AuditExempt: staging is scratch space for a write the journal records through the list row.
func (crlStageRow) AuditExempt() bool { return true }

// stageCrlEntries copies the CRL's entries into a TEMP table (invisible to every other session,
// dropped at commit or rollback), in batches. It takes no lock on the anchor. SQLite (the unit
// tests' database) has no ON COMMIT DROP, so the caller drops it explicitly (dropCrlStage).
func stageCrlEntries(tx *gorm.DB, entries []x509trust.RevokedEntry) error {
	if tx.Dialector.Name() == "postgres" {
		if err := tx.Exec("CREATE TEMP TABLE " + crlStageTable +
			" (serial varchar(64) PRIMARY KEY, reason varchar(32) NOT NULL, revoked_at timestamptz NOT NULL) ON COMMIT DROP").Error; err != nil {
			return err
		}
	} else {
		if err := tx.Exec("DROP TABLE IF EXISTS temp." + crlStageTable).Error; err != nil {
			return err
		}
		if err := tx.Exec("CREATE TEMP TABLE " + crlStageTable +
			" (serial varchar(64) PRIMARY KEY, reason varchar(32) NOT NULL, revoked_at datetime NOT NULL)").Error; err != nil {
			return err
		}
	}
	batch := make([]crlStageRow, 0, min(stageBatch, len(entries)))
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := tx.Create(&batch).Error
		batch = batch[:0]
		return err
	}
	for _, e := range entries {
		batch = append(batch, crlStageRow{Serial: e.Serial, Reason: string(e.Reason), RevokedAt: e.RevokedAt.UTC()})
		if len(batch) == stageBatch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if tx.Dialector.Name() == "postgres" {
		// A temp table has no statistics until it is analysed, and without them the planner joins it
		// to the entries by a plan that is quadratic in the list. One pass over the staged rows.
		return tx.Exec("ANALYZE " + crlStageTable).Error
	}
	return nil
}

func dropCrlStage(tx *gorm.DB) error {
	if tx.Dialector.Name() == "postgres" {
		return nil // ON COMMIT DROP
	}
	return tx.Exec("DROP TABLE IF EXISTS temp." + crlStageTable).Error
}

// applyStagedEntries makes the stored entries of the list equal the staged ones. Entries the staged
// set does not carry (or carries with another reason) are deleted, and staged entries the list does
// not hold are inserted from the staging table, so only the difference is written. newly counts
// entries that were not there before at all; a changed reason is not a new revocation. Every
// statement carries the tenant, the anchor, the list and the issuer identity, so each is also an
// index lookup of the connect read's own shape.
func applyStagedEntries(tx *gorm.DB, tenant string, anchorId uint, list *DeviceRevocationList) (newly, removed int, err error) {
	const (
		scopeE = "e.tenant_id = ? AND e.trust_anchor_id = ? AND e.revocation_list_id = ? AND e.kind = 'CRL' AND e.issuer_key = ? AND e.issuer_name = ?"
		scope  = "tenant_id = ? AND trust_anchor_id = ? AND revocation_list_id = ? AND kind = 'CRL' AND issuer_key = ? AND issuer_name = ?"
	)
	args := []any{tenant, anchorId, list.ID, list.IssuerKey, list.IssuerName}
	var n int64
	if err = tx.Raw("SELECT count(*) FROM "+crlStageTable+" s WHERE NOT EXISTS (SELECT 1 FROM device_revoked_certificates e WHERE "+
		scopeE+" AND e.serial = s.serial)", args...).Scan(&n).Error; err != nil {
		return 0, 0, err
	}
	newly = int(n)
	if err = tx.Raw("SELECT count(*) FROM device_revoked_certificates e WHERE "+scopeE+
		" AND NOT EXISTS (SELECT 1 FROM "+crlStageTable+" s WHERE s.serial = e.serial)", args...).Scan(&n).Error; err != nil {
		return 0, 0, err
	}
	removed = int(n)
	if err = tx.Exec("DELETE FROM device_revoked_certificates WHERE "+scope+
		" AND NOT EXISTS (SELECT 1 FROM "+crlStageTable+" s WHERE s.serial = device_revoked_certificates.serial AND s.reason = device_revoked_certificates.reason)",
		args...).Error; err != nil {
		return 0, 0, err
	}
	insertArgs := append(append([]any{}, args...), args...)
	if err = tx.Exec("INSERT INTO device_revoked_certificates (tenant_id, trust_anchor_id, kind, revocation_list_id, issuer_key, issuer_name, serial, reason, revoked_at, created_at) "+
		"SELECT CAST(? AS varchar(128)), CAST(? AS bigint), 'CRL', CAST(? AS bigint), CAST(? AS varchar(64)), CAST(? AS varchar(64)), s.serial, s.reason, s.revoked_at, CURRENT_TIMESTAMP "+
		"FROM "+crlStageTable+" s WHERE NOT EXISTS (SELECT 1 FROM device_revoked_certificates e WHERE "+scopeE+" AND e.serial = s.serial)",
		insertArgs...).Error; err != nil {
		return 0, 0, err
	}
	return newly, removed, nil
}

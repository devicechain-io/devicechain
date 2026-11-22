// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package attrprotocol is the one definition of the device attribute WRITER PROTOCOL marker
// that device-management's writers and the tenant purge executor (in user-management) share.
//
// A protocol transaction sets the transaction-local setting devicechain.attr_protocol to '1'.
// device-management sets it inside its per-device lock helper, so a writer cannot take the lock
// without marking or mark without the lock. Tenant purge is the one destructive exception: it
// deletes many devices' rows in one transaction and must take no per-device advisory lock (one
// per device would exhaust the lock table), so it marks once per purge transaction through
// RegisterPurge and nothing else.
//
// Nothing in this release enforces the marker. A later release installs a database guard that
// refuses an unmarked write of a device's attribute rows; every pod of the release before it
// must already mark, which is why the marker ships first. It lives in core so that the purge
// executor does not import a service module.
package attrprotocol

import (
	"errors"

	"github.com/devicechain-io/dc-microservice/tenantpurge"
	"gorm.io/gorm"
)

// Setting is the transaction-local setting a protocol transaction sets to "1".
const Setting = "devicechain.attr_protocol"

// MarkerSQL marks the current transaction. The third argument (is_local = true) scopes it to the
// transaction: it is gone after commit or rollback, and a ROLLBACK TO SAVEPOINT taken before it
// undoes it.
const MarkerSQL = "SELECT set_config('" + Setting + "', '1', true)"

// Schema and the tables whose rows the protocol covers, for RegisterPurge.
const (
	Schema           = "device-management"
	AttributesTable  = "entity_attributes"
	GenerationsTable = "entity_attribute_generations"
)

// ErrNotInTransaction refuses a mark on a handle that is not inside a transaction: there a
// transaction-local setting lasts one statement and marks nothing.
var ErrNotInTransaction = errors.New("the attribute writer protocol marker needs a transaction")

// Mark sets the marker on tx, which must be inside a transaction. On a database other than
// PostgreSQL (the SQLite unit fixtures) it does nothing.
func Mark(tx *gorm.DB) error {
	if tx == nil || tx.Statement == nil {
		return ErrNotInTransaction
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return ErrNotInTransaction
	}
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec(MarkerSQL).Error
}

// DevicesTable is the parent every protocol writer, and the configuration report path, holds a
// row lock on before it touches a device's child rows.
const DevicesTable = "devices"

// RegisterPurge makes a tenant purge plan that deletes the protocol's tables mark its
// transaction, and then lock the tenant's device rows, before any deletion. A plan that touches
// neither table is left unchanged; an existing BeforeDelete runs first.
//
// 🔴 PARENTS FIRST. The purge deletes tables in foreign-key order and then alphabetically, so
// the device-management children (device_configuration_*, ...) go before devices. A writer
// holds its device row (KEY SHARE, or FOR UPDATE for a delete) BEFORE it touches those
// children, so a purge that deleted a child row first and then waited for the device row could
// close a cycle with it (40P01). Locking the tenant's device rows FOR UPDATE, in id order, before
// the first DELETE makes the purge wait for every in-flight writer while it still holds no child
// row. Row locks are kept on the rows themselves, so this does not grow the lock table the way an
// advisory lock per device would.
func RegisterPurge(plan *tenantpurge.Plan) {
	if plan == nil {
		return
	}
	for _, entry := range plan.Entries {
		if entry.Table.Schema != Schema || (entry.Table.Name != AttributesTable && entry.Table.Name != GenerationsTable && entry.Table.Name != DevicesTable) {
			continue
		}
		previous := plan.BeforeDelete
		plan.BeforeDelete = func(tx *gorm.DB, tenant string) error {
			if previous != nil {
				if err := previous(tx, tenant); err != nil {
					return err
				}
			}
			if err := Mark(tx); err != nil {
				return err
			}
			return lockTenantDevices(tx, tenant)
		}
		return
	}
}

// lockTenantDevices takes every device row of tenant FOR UPDATE, in id order, inside the purge
// transaction. PostgreSQL only: the SQLite fixtures have a single writer.
func lockTenantDevices(tx *gorm.DB, tenant string) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	var ids []int64
	return tx.Raw(`SELECT id FROM "device-management".devices WHERE tenant_id = ? ORDER BY id FOR UPDATE`, tenant).Scan(&ids).Error
}

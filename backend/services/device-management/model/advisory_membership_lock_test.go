// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type membershipProbeDialect struct{ gorm.Dialector }

func (membershipProbeDialect) Name() string { return "postgres" }

func TestMembershipSharedChecksDatabaseOwnership(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	held, probes := false, 0
	// A unit-level database witness: exercise the real ownership decision with
	// both answers, while leaving the two-connection Postgres tests unmodified.
	require.NoError(t, api.RDB.Database.Callback().Row().Before("gorm:row").Register("test:membership-lock-probe", func(db *gorm.DB) {
		if !strings.Contains(db.Statement.SQL.String(), "FROM pg_locks") {
			return
		}
		probes++
		db.Statement.SQL.Reset()
		if held {
			db.Statement.SQL.WriteString("SELECT 1")
		} else {
			db.Statement.SQL.WriteString("SELECT 0")
		}
		db.Statement.Vars = nil
	}))
	defer api.RDB.Database.Callback().Row().Remove("test:membership-lock-probe")
	require.NoError(t, api.runDeviceAttributeTx(ctx, "set", func(tx *gorm.DB) error {
		require.NoError(t, lockDeviceAttributes(ctx, tx, 17))
		probe, cfg := *tx, *tx.Config
		probe.Config = &cfg
		probe.Config.Dialector = membershipProbeDialect{tx.Dialector}
		_, err := membershipWriterLockSQL(&probe, "device", 17)
		require.ErrorContains(t, err, "requires K_dev", "cached ownership alone is insufficient")
		held = true
		sql, err := membershipWriterLockSQL(&probe, "device", 17)
		require.NoError(t, err)
		require.Equal(t, "SELECT pg_advisory_xact_lock_shared(?)", sql)
		require.Equal(t, 2, probes)
		return nil
	}))
}

func TestMembershipWriterLockMode(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	require.NoError(t, api.runDeviceAttributeTx(ctx, "set", func(tx *gorm.DB) error {
		require.NoError(t, lockDeviceAttributes(ctx, tx, 17))
		sql, err := membershipWriterLockSQL(tx, "device", 17)
		require.NoError(t, err)
		require.Equal(t, "SELECT pg_advisory_xact_lock_shared(?)", sql)
		for _, family := range []string{"asset", "area", "customer"} {
			sql, err := membershipWriterLockSQL(tx, family, 17)
			require.NoError(t, err)
			require.Equal(t, "SELECT pg_advisory_xact_lock(?)", sql, family)
		}
		return nil
	}))
	// Non-device writers have no owner state at all and must remain exclusive.
	require.NoError(t, api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		for _, family := range []string{"asset", "area", "customer"} {
			sql, err := membershipWriterLockSQL(tx, family, 17)
			require.NoError(t, err)
			require.Equal(t, "SELECT pg_advisory_xact_lock(?)", sql, family)
		}
		return nil
	}))
}

func TestMembershipSharedRequiresDeviceOwner(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	check := func(tx *gorm.DB, id uint) {
		t.Helper()
		_, err := membershipWriterLockSQL(tx, "device", id)
		require.ErrorContains(t, err, "requires K_dev")
	}
	check(nil, 17)
	check(api.RDB.DB(ctx), 17)
	require.NoError(t, api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		check(tx, 17) // plain transaction, no owner state
		return nil
	}))
	require.NoError(t, api.runDeviceAttributeTx(ctx, "set", func(tx *gorm.DB) error {
		check(tx, 17) // runner alone is insufficient
		require.NoError(t, lockDeviceAttributes(ctx, tx, 17))
		check(tx, 18) // a different device's lock is insufficient
		// A state carried onto a pool handle is not a transaction-held lock.
		root := api.RDB.DB(context.WithValue(ctx, attributeTxContextKey{}, tx.Statement.Context.Value(attributeTxContextKey{})))
		check(root, 17)
		return nil
	}))
}

func TestCreatedDeviceMembershipOwner(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	_, err := lockCreatedDeviceForMembership(api.RDB.DB(ctx), 17)
	require.Error(t, err)
	require.NoError(t, api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		for _, id := range []uint{17, 18} {
			ownerTx, err := lockCreatedDeviceForMembership(tx, id)
			require.NoError(t, err)
			sql, err := membershipWriterLockSQL(ownerTx, "device", id)
			require.NoError(t, err)
			require.Equal(t, "SELECT pg_advisory_xact_lock_shared(?)", sql)
			_, err = membershipWriterLockSQL(ownerTx, "device", id+1)
			require.ErrorContains(t, err, "requires K_dev")
			// The proof neither leaks onto the original handle nor transfers to
			// another transaction which happens to carry the same context.
			_, err = membershipWriterLockSQL(tx, "device", id)
			require.ErrorContains(t, err, "requires K_dev")
			require.NoError(t, api.RDB.DB(ownerTx.Statement.Context).Transaction(func(other *gorm.DB) error {
				_, err := membershipWriterLockSQL(other, "device", id)
				require.ErrorContains(t, err, "requires K_dev")
				return nil
			}))
		}
		return nil
	}))
}

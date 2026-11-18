// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// SingletonResult translates a try-lock answer for callers with an injected seam.
// Read ran before err: TryAdvisoryLock returns true, fn(), so an error after
// running belongs to the callback and must retain its pass classification.
func SingletonResult(ran bool, err error, lockName string) error {
	if ran {
		return err
	}
	if err != nil {
		return fmt.Errorf("acquiring the %s lock: %w", lockName, err)
	}
	return core.ErrPassSkipped
}

// Plain passes use other pooled connections for their effects, which must be
// idempotent or CAS-guarded. Session passes run on the lock's own connection:
// use them when concurrent runners would be wrong, not merely wasteful.
//
// SingletonPass runs an idempotent or CAS-guarded pass under a nonblocking lock.
// The callback's statements run on other pooled connections.
func (rdb *RdbManager) SingletonPass(ctx context.Context, lockName string, fn func() error) error {
	ran, err := rdb.TryAdvisoryLock(ctx, AdvisoryLockKey(lockName), fn)
	return SingletonResult(ran, err, lockName)
}

// SingletonPassSession runs a pass on the nonblocking lock's own connection,
// so its database work lives and dies with that connection. The callback must
// not retain db and must restore any session settings before returning.
func (rdb *RdbManager) SingletonPassSession(ctx context.Context, lockName string, fn func(db *gorm.DB) error) error {
	ran, err := rdb.TryAdvisoryLockSession(ctx, AdvisoryLockKey(lockName), fn)
	return SingletonResult(ran, err, lockName)
}

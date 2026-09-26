// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// fenceTraceRecorder keeps the error gorm hands its logger for every statement that
// touches the fence table. It is a logger because the logger is exactly where the old
// shape of the fence read did its damage: every clear read reached it as a failure.
type fenceTraceRecorder struct {
	logger.Interface
	mu   sync.Mutex
	errs []error
}

func (r *fenceTraceRecorder) LogMode(logger.LogLevel) logger.Interface { return r }

func (r *fenceTraceRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), err error) {
	if sql, _ := fc(); strings.Contains(sql, FenceTable) {
		r.mu.Lock()
		r.errs = append(r.errs, err)
		r.mu.Unlock()
	}
}

func (r *fenceTraceRecorder) recorded() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...)
}

// A clear fence is the ordinary answer to the check made before every tenant write, so
// it must not reach the logger as a statement error. Read with Take, it did: "no fence"
// arrived as ErrRecordNotFound, and gorm's default logger printed a block for every
// transaction that wrote tenant data.
func TestAClearFenceReadIsNotAStatementError(t *testing.T) {
	rec := &fenceTraceRecorder{Interface: logger.Discard}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: rec})
	require.NoError(t, err)
	require.NoError(t, RegisterTenantScoping(db))
	require.NoError(t, RegisterTenantFence(db))
	require.NoError(t, RegisterAuditJournal(db))
	require.NoError(t, db.AutoMigrate(&PurgedTenant{}, &AuditEvent{}, &widget{}))
	before := len(rec.recorded())

	ctx := core.WithTenant(context.Background(), "acme")
	require.NoError(t, db.WithContext(ctx).Create(&widget{Name: "admitted"}).Error)

	got := rec.recorded()[before:]
	// Exactly one: a recorder that saw nothing must not pass for one that saw a clean read.
	require.Len(t, got, 1, "fence statements traced for one clear write")
	require.NoError(t, got[0], "the clear fence read reached the logger as an error")
}

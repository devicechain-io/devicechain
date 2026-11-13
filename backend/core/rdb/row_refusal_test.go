// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// A Row() read that a scoping callback refuses must hand back a row whose Scan returns the
// refusal. gorm skips its row callback on a handle carrying an error, so without help Row()
// returns a nil *sql.Row and Scan on that is a nil-pointer panic.
func TestARefusedRowReadReturnsAnErrorNotAPanic(t *testing.T) {
	db := newTestDB(t)
	ctxA := core.WithTenant(context.Background(), "A")
	if err := db.WithContext(ctxA).Create(&widget{Name: "a1"}).Error; err != nil {
		t.Fatal(err)
	}

	var n int
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Row().Scan panicked: %v", r)
			}
		}()
		err = db.WithContext(context.Background()).Model(&widget{}).Select("count(*)").Row().Scan(&n)
	}()
	if !errors.Is(err, core.ErrNoTenant) {
		t.Fatalf("want ErrNoTenant, got %v (n=%d)", err, n)
	}

	// The counterweight: a well-formed read is untouched.
	if err := db.WithContext(ctxA).Model(&widget{}).Select("count(*)").Row().Scan(&n); err != nil || n != 1 {
		t.Fatalf("well-formed Row read: n=%d err=%v", n, err)
	}
}

// A cancelled context must not hide the refusal behind "context canceled".
func TestARefusedRowReadReportsTheRefusalOnACancelledContext(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var n int
	err := db.WithContext(ctx).Model(&widget{}).Select("count(*)").Row().Scan(&n)
	if !errors.Is(err, core.ErrNoTenant) {
		t.Fatalf("want ErrNoTenant, got %v", err)
	}
}

// On a reused handle a refused Row() must not hand back the previous, already scanned, row.
func TestARefusedRowReadOnAReusedHandleReturnsTheRefusal(t *testing.T) {
	db := newTestDB(t)
	ctxA := core.WithTenant(context.Background(), "A")
	// A plain chain (clone 0) is reused by every call on it, so its Statement keeps the row the
	// first Row() produced.
	q := db.Model(&widget{}).Select("count(*)")
	q.Statement.Context = ctxA
	var n int
	if err := q.Row().Scan(&n); err != nil {
		t.Fatal(err)
	}
	q.Statement.Context = context.Background() // the same chain, now without a tenant
	err := q.Row().Scan(&n)
	if !errors.Is(err, core.ErrNoTenant) {
		t.Fatalf("want ErrNoTenant, got %v", err)
	}
}

// A refused Rows() read keeps gorm's behaviour: the error is on the handle, no row is built.
//
// The error alone cannot tell the callback's Rows() early-return from its absence (Rows() reports the
// refusal either way), so the test also looks at what the callback leaves in the statement: a
// *sql.Row in Dest means rowReturnsRefusal built a row for a read that wanted rows.
func TestARefusedRowsReadBuildsNoRow(t *testing.T) {
	db := newTestDB(t)
	// A plain chain, not a session: its Statement is the one the callbacks ran on, so it can be
	// inspected afterwards.
	q := db.Model(&widget{}).Select("count(*)")
	q.Statement.Context = context.Background() // no tenant: refused
	rows, err := q.Rows()
	if rows != nil {
		rows.Close()
	}
	if !errors.Is(err, core.ErrNoTenant) {
		t.Fatalf("want ErrNoTenant, got %v", err)
	}
	if row, ok := q.Statement.Dest.(*sql.Row); ok {
		t.Fatalf("a refused Rows() read left a built *sql.Row in the statement: %p", row)
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
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

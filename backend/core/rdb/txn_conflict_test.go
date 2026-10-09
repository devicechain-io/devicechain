// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsTransactionConflictRecognisesOnlyAbortedTransactions(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&pgconn.PgError{Code: "40P01"}, true},
		{&pgconn.PgError{Code: "40001"}, true},
		{fmt.Errorf("save: %w", &pgconn.PgError{Code: "40P01"}), true},
		{&pgconn.PgError{Code: "22001"}, false},
		{&pgconn.PgError{Code: "23505"}, false},
		{&pgconn.PgError{Code: "08006"}, false},
		{errors.New("deadlock detected"), false},
		{nil, false},
	} {
		if got := IsTransactionConflict(tc.err); got != tc.want {
			t.Errorf("IsTransactionConflict(%v) = %v; want %v", tc.err, got, tc.want)
		}
	}
}

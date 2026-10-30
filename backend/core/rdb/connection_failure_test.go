// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// IsConnectionFailure sorts errors by what they say about the connection, however wrapped.
func TestIsConnectionFailureRecognisesConnectionErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&pgconn.PgError{Code: "08006"}, true},
		{&pgconn.PgError{Code: "08001"}, true},
		{&pgconn.PgError{Code: "57P01"}, true},
		{&pgconn.PgError{Code: "57P03"}, true},
		{fmt.Errorf("wrapped: %w", driver.ErrBadConn), true},
		{io.ErrUnexpectedEOF, true},
		{context.DeadlineExceeded, true},
		{&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}, true},
		{&pgconn.PgError{Code: "22003"}, false},
		{&pgconn.PgError{Code: "40001"}, false},
		{&pgconn.PgError{Code: "57014"}, false},
		{fmt.Errorf("%w (tenant %q)", ErrTenantPurged, "gone"), false},
		{errors.New("some statement error"), false},
	} {
		if got := IsConnectionFailure(tc.err); got != tc.want {
			t.Errorf("IsConnectionFailure(%v) = %v; want %v", tc.err, got, tc.want)
		}
	}
}

// IsStoreRefusingWrites is the server saying it cannot take writes, and nothing a statement or
// a value did, and nothing that merely looks like a lost connection.
func TestIsStoreRefusingWritesRecognisesAStoreThatCannotTakeWrites(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&pgconn.PgError{Code: "53100"}, true}, // disk_full
		{&pgconn.PgError{Code: "53300"}, true}, // too_many_connections
		{&pgconn.PgError{Code: "25006"}, true}, // read_only_sql_transaction
		{&pgconn.PgError{Code: "58030"}, true}, // io_error
		{fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "53100"}), true},
		{&pgconn.PgError{Code: "53200"}, false}, // out_of_memory: can be one statement
		{&pgconn.PgError{Code: "53400"}, false}, // configuration_limit_exceeded: can be one statement
		{&pgconn.PgError{Code: "08006"}, false}, // a connection failure needs a probe
		{fmt.Errorf("wrapped: %w", driver.ErrBadConn), false},
		{&pgconn.PgError{Code: "22003"}, false},
		{&pgconn.PgError{Code: "23505"}, false},
		{&pgconn.PgError{Code: "40001"}, false},
		{&pgconn.PgError{Code: "42P01"}, false},
		{fmt.Errorf("%w (tenant %q)", ErrTenantPurged, "gone"), false},
		{errors.New("some statement error"), false},
		{nil, false},
	} {
		if got := IsStoreRefusingWrites(tc.err); got != tc.want {
			t.Errorf("IsStoreRefusingWrites(%v) = %v; want %v", tc.err, got, tc.want)
		}
	}
}

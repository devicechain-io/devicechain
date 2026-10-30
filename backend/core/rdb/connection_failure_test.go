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

// IsStoreUnavailable adds the server-side "cannot take this write" classes to the connection
// failures, and nothing that is a statement's or a value's fault.
func TestIsStoreUnavailableRecognisesAStoreThatCannotTakeWrites(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&pgconn.PgError{Code: "53100"}, true}, // disk_full
		{&pgconn.PgError{Code: "53200"}, true}, // out_of_memory
		{&pgconn.PgError{Code: "53300"}, true}, // too_many_connections
		{&pgconn.PgError{Code: "25006"}, true}, // read_only_sql_transaction
		{&pgconn.PgError{Code: "58030"}, true}, // io_error
		{fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "53100"}), true},
		{&pgconn.PgError{Code: "08006"}, true}, // everything a connection failure is
		{fmt.Errorf("wrapped: %w", driver.ErrBadConn), true},
		{&pgconn.PgError{Code: "22003"}, false}, // a value's fault
		{&pgconn.PgError{Code: "23505"}, false}, // a constraint
		{&pgconn.PgError{Code: "40001"}, false}, // serialization failure
		{&pgconn.PgError{Code: "42P01"}, false}, // a missing table
		{fmt.Errorf("%w (tenant %q)", ErrTenantPurged, "gone"), false},
		{errors.New("some statement error"), false},
		{nil, false},
	} {
		if got := IsStoreUnavailable(tc.err); got != tc.want {
			t.Errorf("IsStoreUnavailable(%v) = %v; want %v", tc.err, got, tc.want)
		}
	}
}

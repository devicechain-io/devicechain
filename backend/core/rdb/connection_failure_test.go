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

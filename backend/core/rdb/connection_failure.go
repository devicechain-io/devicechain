// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// IsConnectionFailure reports whether err says the database connection failed rather than
// that a statement was refused, so nothing in a batching writer's transaction can be blamed
// for it. It only decides how a failed batch is replayed — the part to blame set aside, or
// every message on its own — never a message's disposition, which its own per-message write
// decides. A connection failure it does not recognise costs extra transactions, not
// correctness.
func IsConnectionFailure(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, driver.ErrBadConn) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		pgconn.Timeout(err) {
		return true
	}
	var nerr net.Error
	if errors.As(err, &nerr) {
		return true
	}
	var cerr *pgconn.ConnectError
	if errors.As(err, &cerr) {
		return true
	}
	// Class 08 is connection exception; 57P01-57P05 are the server shutting down or
	// cancelling the session.
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		return strings.HasPrefix(pgerr.Code, "08") || strings.HasPrefix(pgerr.Code, "57P")
	}
	return false
}

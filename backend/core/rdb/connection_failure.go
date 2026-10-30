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

// IsStoreUnavailable reports whether err says the database cannot take a write RIGHT NOW,
// for a reason no redelivery of the same message can be blamed for and that clears when the
// operator (or the database) fixes the store: the connection failed (IsConnectionFailure),
// the server is out of a resource it needs to accept the write, it is read-only, or its
// storage reports an I/O fault.
//
// The classes, by SQLSTATE:
//
//   - 53xxx, insufficient resources: 53100 disk_full (the event-store incident this exists
//     for), 53200 out_of_memory, 53300 too_many_connections, 53400 configuration_limit_exceeded.
//   - 25006, read_only_sql_transaction: a replica, or a primary fenced by its operator.
//   - 58xxx, system error: 58030 io_error and its neighbours.
//
// A consumer uses this to HOLD a message instead of counting the failure against its
// delivery budget. A message abandoned because the database was full is data the platform
// acknowledged and then threw away, and the database coming back does not bring it back.
// This is deliberately a different question from rdb.IsConnectionFailure, which only decides
// how a failed batch is replayed.
func IsStoreUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if IsConnectionFailure(err) {
		return true
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		return strings.HasPrefix(pgerr.Code, "53") || strings.HasPrefix(pgerr.Code, "58") || pgerr.Code == "25006"
	}
	return false
}

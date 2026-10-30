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
// for it. On its own it decides only how a failed batch is replayed — the part to blame set
// aside, or every message on its own. It is never evidence that the STORE is down: a payload
// that crashes its backend looks exactly like a lost connection. A caller that treats it as an
// outage must confirm with a probe of the store first (see IsStoreRefusingWrites for the errors
// that need no confirmation). A connection failure it does not recognise costs extra
// transactions, not correctness.
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

// IsStoreRefusingWrites reports whether err is the database SAYING it cannot take writes right
// now, for a reason no redelivery of the same message can be blamed for and that clears when the
// operator fixes the store:
//
//   - 53100 disk_full (the event-store incident this exists for) and 53300 too_many_connections;
//   - 25006 read_only_sql_transaction: a replica, or a primary fenced by its operator;
//   - 58xxx, system error: 58030 io_error and its neighbours.
//
// These are statements about the SERVER, so they need no second opinion. 53200 out_of_memory and
// 53400 configuration_limit_exceeded are deliberately not here: one statement can exhaust either
// (a huge sort, a statement over a setting's limit) and the same bytes then fail on every
// delivery, which is the delivery budget's business, not an outage.
//
// A connection failure is NOT here, because IsConnectionFailure also matches what a payload that
// crashes its backend looks like. A caller that wants to hold on one asks the store first (a
// trivial query that succeeds when the store is up).
func IsStoreRefusingWrites(err error) bool {
	if err == nil {
		return false
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		return pgerr.Code == "53100" || pgerr.Code == "53300" || pgerr.Code == "25006" ||
			strings.HasPrefix(pgerr.Code, "58")
	}
	return false
}

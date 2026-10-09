// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// IsTransactionConflict reports whether err is PostgreSQL aborting a transaction because it
// lost a race with another one: a deadlock victim (40P01) or a serialization failure (40001).
// The server rolled the whole transaction back, so running it again from the start is safe,
// and usually succeeds because the transaction it collided with has since finished. It is
// deliberately narrower than "any transient error": a statement the database refuses on its
// merits (a value too long, a constraint) fails the same way every time.
func IsTransactionConflict(err error) bool {
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		return pgerr.Code == "40P01" || pgerr.Code == "40001"
	}
	return false
}

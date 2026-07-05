// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"errors"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// ErrChannelInUse is returned when a channel delete is refused because a routing
// rule still references it. The GraphQL layer surfaces it as a user error.
var ErrChannelInUse = errors.New("notification channel is still referenced by a policy rule and cannot be deleted")

// nullInt64OfInt32 adapts an optional GraphQL Int (int32) to a nullable bigint
// column; a nil pointer stores NULL.
func nullInt64OfInt32(v *int32) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*v), Valid: true}
}

// Api is the persistence-facing surface of the notification service (ADR-017): the
// per-tenant delivery channels (SMTP/webhook, with their write-only secrets), the
// routing policies that map alarm severities to channels + recipients, and the
// per-alarm notification state that the dispatcher (N.C) writes and the escalation
// scheduler (N.D) reads. Every method takes a context so the rdb tenant-scope
// callback can bind the caller's tenant; there is no cross-tenant surface.
type Api struct {
	RDB *rdb.RdbManager
}

// NewApi wraps an rdb manager as the notification persistence API.
func NewApi(rdb *rdb.RdbManager) *Api {
	return &Api{RDB: rdb}
}

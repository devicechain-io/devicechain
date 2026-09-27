// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
)

// ErrChannelInUse is returned when a channel delete is refused because a routing
// rule still references it. The GraphQL layer surfaces it as a user error carrying
// extensions.code REFERENCE_VIOLATION.
var ErrChannelInUse = integrity.NewRefusal(integrity.ClassReference, "notification channel is still referenced by a policy rule and cannot be deleted")

// ErrConflict is returned by UpdateNotificationPolicy when the caller passes the version
// it edited (expectedUpdatedAt) and the policy has moved on since — a concurrent edit.
// The caller should reload, reapply its change and send it again.
//
// 🔴 DESPITE THE NAME, IT MUST NEVER BECOME A conflict.Error. That type's code, CONFLICT,
// means "a value that must be unique is already in use", and a client may treat it as
// "already exists, carry on" — which, for a lost update, would report a save that never
// happened as done.
var ErrConflict = rdb.NewStaleWriteError("notification policy")

// Api is the persistence-facing surface of the notification service (ADR-017): the
// per-tenant delivery channels (SMTP/webhook, with their write-only secrets), the
// routing policies that map alarm severities to channels + recipients, and the
// per-alarm notification state that the dispatcher (N.C) writes and the escalation
// scheduler (N.D) reads. Every method takes a context so the rdb tenant-scope
// callback can bind the caller's tenant; there is no cross-tenant surface.
//
// Secrets is the envelope-encrypted secret store (ADR-059) holding each channel's
// write-only delivery secret. The channel write path Puts/Deletes through it and
// the read API's hasSecret is store.Exists; dispatch (the processor) Resolves the
// cleartext server-internal at delivery time. It is never nil in production; a unit
// test that does not exercise the secret path may leave it nil.
type Api struct {
	RDB     *rdb.RdbManager
	Secrets secrets.SecretStore
}

// NewApi wraps an rdb manager and the channel-secret store as the notification
// persistence API.
func NewApi(rdb *rdb.RdbManager, store secrets.SecretStore) *Api {
	return &Api{RDB: rdb, Secrets: store}
}

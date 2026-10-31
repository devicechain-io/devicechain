// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import "errors"

// ErrHoldReleased is what a consumer's store hold (core/storehold) returns when shutdown ended
// the hold: the message is still unacked and the next pod will get it. It is not a failure OF
// the message, so Dispose answers Retry for it on EVERY delivery, the last included -- a
// release must never be mistaken for a message that spent its budget and be lettered and acked.
var ErrHoldReleased = errors.New("messaging: store hold released at shutdown; message left unacked")

// Disposition is what a consumer does with a message whose handling FAILED. Dispose is the
// one place that decides it, so a consumer does not re-derive "is this the last attempt?"
// and, before that, "can any attempt succeed?" on its own.
type Disposition int

const (
	// Retry: leave the message UNACKED (never nak) so AckWait paces its redelivery.
	Retry Disposition = iota + 1
	// Exhausted: this was the final delivery and no redelivery follows. The consumer's own
	// give-up runs now — its dead letter, its failed-events report, or a counted drop — and
	// then it acks.
	Exhausted
	// TenantErased: the message's tenant has been deleted and this area's data erased, so
	// the write was refused, and it would be refused identically on every delivery. Ack it on
	// the delivery it arrived on, count it (core.ResultErased), and write NOTHING about it: no
	// dead letter and no failed-events copy, because a record of an erased tenant's payload
	// is exactly what the erasure removes, and retrying cannot succeed.
	TenantErased
)

// String names the disposition, for logs.
func (d Disposition) String() string {
	switch d {
	case Retry:
		return "retry"
	case Exhausted:
		return "exhausted"
	case TenantErased:
		return "tenant-erased"
	default:
		return "unknown"
	}
}

// tenantErased is implemented by an error that means the tenant's data in this area has been
// erased — the erasure fence's refusal, rdb.ErrTenantPurged. It is an interface rather than an
// import so that this package, which every stream reader links, does not pull in the database
// layer to recognise one of its errors.
type tenantErased interface{ TenantErased() bool }

// Dispose decides what a consumer does with msg after its handling failed with err:
//
//   - Retry, on ANY delivery, when err is (or wraps) ErrHoldReleased: shutdown ended a store hold
//     and the message was never judged.
//   - TenantErased, on ANY delivery, when err is (or wraps) the erasure fence's refusal. It
//     comes first, so the final delivery of an erased tenant's message is not lettered either.
//   - Exhausted on the final delivery (NumDelivered >= MaxDeliver).
//   - Retry otherwise.
//
// An UNREADABLE fence is not a refusal — it is a database that could not answer — and is
// retried like any other transient failure. A failure the consumer already knows to be
// deterministic (a payload no redelivery can fix) is the consumer's to recognise BEFORE it
// asks: only the consumer knows its own content errors.
//
// err must be non-nil: a nil error is a success, which has no disposition to decide, and
// asking about one is a defect in the caller, so it panics rather than answering Retry.
func Dispose(msg Message, err error) Disposition {
	if err == nil {
		panic("messaging: Dispose asked about a nil error; a handled message is acked, not disposed")
	}
	var erased tenantErased
	switch {
	case errors.Is(err, ErrHoldReleased):
		return Retry
	case errors.As(err, &erased) && erased.TenantErased():
		return TenantErased
	case msg.NumDelivered >= MaxDeliver:
		return Exhausted
	default:
		return Retry
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletter

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Exhausted describes the letter filed when a message runs out of deliveries.
type Exhausted struct {
	Summary    string
	Reference  string
	MaxPayload int // 0 = whole payload; positive values keep the first N bytes.
	// Lost is the message of the error line logged when the letter cannot be written, so each
	// consumer keeps its own searchable LOST sentence. Empty uses a generic one.
	Lost string
}

// Settle leaves Retry untouched (never nak), acks TenantErased without a letter,
// and writes an exhausted letter before acking, even if the write fails.
// A nil sink acks without a letter. err must be non-nil, as for messaging.Dispose.
// ctx must carry the tenant. A failed write is already counted by the sink.
// lettered reports WriteFor success, including intentional tenant-gate suppression,
// preserving callers' existing success-counter semantics.
func (s *Sink) Settle(ctx context.Context, msg messaging.Message, err error, x Exhausted) (d messaging.Disposition, lettered bool) {
	d = messaging.Dispose(msg, err)
	if d == messaging.Retry {
		return d, false
	}
	if d == messaging.Exhausted && s != nil {
		payload := msg.Value
		if x.MaxPayload > 0 && len(payload) > x.MaxPayload {
			payload = payload[:x.MaxPayload]
		}
		if werr := s.WriteFor(ctx, msg, Envelope{
			Reason: ReasonExhausted, Summary: x.Summary, Reference: x.Reference,
			Detail: err.Error(), OccurredAt: time.Now().UTC(), Payload: payload,
		}); werr != nil {
			logger := log.Ctx(ctx)
			if logger.GetLevel() == zerolog.Disabled {
				logger = &log.Logger
			}
			lost := x.Lost
			if lost == "" {
				lost = "LOST message: it could be neither applied nor dead-lettered."
			}
			logger.Error().Err(werr).Str("subject", msg.Subject).Str("reference", x.Reference).Msg(lost)
		} else {
			lettered = true
		}
	}
	msg.Ack()
	return d, lettered
}

// Result maps a failed message's disposition onto the processor result label.
func Result(d messaging.Disposition) string {
	switch d {
	case messaging.Retry:
		return core.ResultRetry
	case messaging.Exhausted:
		return core.ResultFailed
	case messaging.TenantErased:
		return core.ResultErased
	default:
		panic("deadletter: unknown disposition")
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmddownlink

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// HeaderTransport names the transport command-delivery resolved for a command's device, stamped
// on the device-commands message from the presence read its delivery gate already made. It is
// for ROUTING ONLY: an adapter uses it to leave a command that is not its own without opening
// the envelope, and it must never authorize anything. It is absent when command-delivery knew
// nothing about the device, and an absent header means "unknown", not "not mine".
const HeaderTransport = "Dc-Transport"

// HeaderCommandImpact is the impact (NORMAL, DISRUPTIVE or DESTRUCTIVE) of the command
// definition the device's profile gives the command, resolved by command-delivery at dispatch
// from the same owner that decides maintenance windows. It is absent when the lookup failed or
// timed out, and absent is UNKNOWN: an adapter must refuse anything that requires DISRUPTIVE or
// higher, never assume NORMAL.
const HeaderCommandImpact = "Dc-Command-Impact"

// HeaderCommandExpires is the instant after which command-delivery will no longer dispatch the
// command, as an RFC 3339 timestamp, stamped by command-delivery from the command's own expiry.
// An adapter that may hold a command for a while before it publishes (a rate wait) re-checks it
// before the wire, so a command is not actuated after the platform has already given up on it.
// It is absent when the command does not expire or the publisher predates the header, and absent
// means unknown: the claim command-delivery makes before the wire still refuses an expired row,
// so a missing header costs only the earlier refusal, never a late actuation.
const HeaderCommandExpires = "Dc-Command-Expires"

// DeliveryEnvelope is command-delivery's wire payload on the device-commands subject. Its fields
// and JSON tags MUST stay byte-identical to command-delivery/processor.deliveryEnvelope: the
// services agree on this shape with no shared type, which is why command-delivery pins it with a
// test against this definition.
type DeliveryEnvelope struct {
	Token       string           `json:"token"`
	DeviceToken string           `json:"deviceToken"`
	Name        string           `json:"name"`
	Payload     *json.RawMessage `json:"payload,omitempty"`

	// DispatchNonce names the claim this publish belongs to. It is quoted back when confirming
	// the dispatch before actuating (Claimer.ClaimDispatch), so a late or redelivered copy cannot
	// actuate a row that has since been re-armed, and when parking (Parker.Park), so a request
	// still in redelivery cannot hand back a row that has since been re-claimed and actuated.
	// Empty means the publisher stamped none: the command is then not actuated.
	DispatchNonce string `json:"dispatchNonce,omitempty"`
}

// Reach says how far a failed command got, as the reporting transport saw it. The values MUST
// match command-delivery/model.Reach.
const (
	// ReachUnanswered: the exchange ran and nothing came back. Recorded as TIMEOUT / UNKNOWN.
	ReachUnanswered = "UNANSWERED"
	// ReachUnsent: the transport refused the command before the wire. Recorded as FAILED /
	// NOT_DELIVERED, the proven-safe-to-retry class.
	ReachUnsent = "UNSENT"
)

// ResponseEnvelope is the outcome an adapter publishes on the command-responses subject. It must
// stay byte-identical to command-delivery/processor.responseEnvelope (apart from Payload, which
// that side reads as raw JSON and accepts a string for).
type ResponseEnvelope struct {
	CommandToken string  `json:"commandToken"`
	Success      bool    `json:"success"`
	Payload      *string `json:"payload,omitempty"`
	Error        *string `json:"error,omitempty"`

	// Reach is omitted on success and empty when the device itself answered no.
	Reach string `json:"reach,omitempty"`

	// DispatchNonce names the dispatch this outcome answers, and command-delivery REFUSES a
	// response that carries none. It is the nonce the adapter's own claim returned, never the
	// envelope's: the confirmation rotated it.
	DispatchNonce string `json:"dispatchNonce"`
}

// ResponseWriter is the one method an adapter publishes outcomes through.
//
// 🔴 WriteToDevice, NOT WriteMessages. command-responses is per-device: the responding device's
// token is a subject segment, and the tenant-wide method refuses that suffix at RUNTIME rather
// than failing to build. Narrowing the interface to the one correct method turns the mistake
// into a build failure.
type ResponseWriter interface {
	WriteToDevice(ctx context.Context, deviceToken string, msgs ...messaging.Message) error
}

// PublishResponse writes one outcome to the device's command-responses subject with a bounded
// LOCAL retry (attempts tries, backoff between them) and reports the last error on exhaustion.
//
// The tenant context is built fresh from the tenant string rather than taken from the caller's,
// so recording the outcome of an action that already ran is not aborted by a leadership change
// landing during the publish. Each attempt is still bounded by the messaging writer's own
// per-publish ceiling.
func PublishResponse(w ResponseWriter, tenant, deviceToken string, env ResponseEnvelope,
	attempts int, backoff time.Duration) error {
	if attempts < 1 {
		return fmt.Errorf("cmddownlink: publish response for command %q: attempts must be at least 1", env.CommandToken)
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("cmddownlink: encode response for command %q: %w", env.CommandToken, err)
	}
	tctx := core.WithTenant(context.Background(), tenant)
	for attempt := 0; attempt < attempts; attempt++ {
		if err = w.WriteToDevice(tctx, deviceToken, messaging.Message{Value: data}); err == nil {
			return nil
		}
		if attempt < attempts-1 {
			time.Sleep(backoff)
		}
	}
	return fmt.Errorf("cmddownlink: publish response for command %q: %w", env.CommandToken, err)
}

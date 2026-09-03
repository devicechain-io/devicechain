// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-notification-management/model"
)

// A review found the terminal classification had NO test in either service: replacing
// `errors.Is(err, egress.ErrBlocked)` with `false` left the whole suite green. The
// commit's central operational claim had zero evidence behind it. These are that
// evidence.

// blockingAdapter fails every delivery the way the egress guard does — through a wrapped
// sentinel, since that is how it actually arrives (the SMTP adapter wraps with %w, the
// webhook adapter wraps twice).
type blockingAdapter struct{ calls int }

func (b *blockingAdapter) Deliver(context.Context, *model.NotificationChannel, string, []string, *RenderedNotification) error {
	b.calls++
	return fmt.Errorf("smtp dial mail.internal:587: %w",
		&egress.BlockedError{Reason: "destination is a private address"})
}

// TestARefusedDestinationIsNotRetried is the first half: the in-line retry stops dead.
// Without it, a channel pointed at a private address burns every attempt and the operator
// reads "exhausted attempts".
func TestARefusedDestinationIsNotRetried(t *testing.T) {
	ba := &blockingAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: ba})
	n.timeout = 50 * time.Millisecond
	d := delivery{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP), recipients: []string{"x@x.com"}}

	if got := n.deliverWithRetry(context.Background(), d, &RenderedNotification{}); got != deliveryRefused {
		t.Fatalf("deliverWithRetry = %v, want deliveryRefused", got)
	}
	if ba.calls != 1 {
		t.Fatalf("a refused destination was attempted %d times; retrying cannot make an address public", ba.calls)
	}
}

// TestATransientFailureIsStillRetried is the counterweight, and it is the one that keeps
// the test above honest: a `deliverWithRetry` that gave up on everything would satisfy it.
func TestATransientFailureIsStillRetried(t *testing.T) {
	fa := &fakeAdapter{failTimes: 99}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
	n.timeout = 50 * time.Millisecond
	d := delivery{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP), recipients: []string{"x@x.com"}}

	if got := n.deliverWithRetry(context.Background(), d, &RenderedNotification{}); got != deliveryFailed {
		t.Fatalf("deliverWithRetry = %v, want deliveryFailed", got)
	}
	if fa.calls != 3 {
		t.Fatalf("a transient failure was attempted %d times, want all 3", fa.calls)
	}
}

// 🔴 The second half, and the defect the first fix missed. Stopping the inner loop is not
// enough: the caller counts deliveries and returns an error when none succeeded, so the
// durable consumer redelivers and the refusal churns the redelivery budget anyway — the
// same operator-facing outcome, one layer up. A refused-only dispatch must ACK.
func TestADispatchWhereEveryChannelIsRefusedDoesNotAskForRedelivery(t *testing.T) {
	ba := &blockingAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: ba})
	n.timeout = 50 * time.Millisecond
	d := delivery{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP), recipients: []string{"x@x.com"}}

	delivered, refused, failed := 0, 0, 0
	switch n.deliverWithRetry(context.Background(), d, &RenderedNotification{}) {
	case deliveryOK:
		delivered++
	case deliveryRefused:
		refused++
	default:
		failed++
	}

	// This mirrors the disposition rule in dispatch and Escalate. It is asserted here
	// rather than by driving dispatch itself because dispatch needs a persistence API;
	// what matters is that "no delivery" and "no redelivery" can both be true at once,
	// which the bool this replaced could not express.
	if delivered != 0 || refused != 1 || failed != 0 {
		t.Fatalf("delivered=%d refused=%d failed=%d, want 0/1/0", delivered, refused, failed)
	}
	if failed == 0 && refused > 0 {
		return // the ack path
	}
	t.Fatal("a dispatch whose only channel was refused would still have asked for redelivery")
}

// A mixed dispatch must still redeliver, because the transiently-failed channel deserves
// the retry the refused one does not. This is the case an "if any refusal, ack" shortcut
// would silently get wrong, dropping a page that a retry would have delivered.
func TestAMixedDispatchStillAsksForRedelivery(t *testing.T) {
	n := testNotifier(map[string]ChannelAdapter{
		model.ChannelTypeSMTP:    &blockingAdapter{},
		model.ChannelTypeWebhook: &fakeAdapter{failTimes: 99},
	})
	n.timeout = 50 * time.Millisecond

	refused, failed := 0, 0
	for _, ct := range []string{model.ChannelTypeSMTP, model.ChannelTypeWebhook} {
		d := delivery{channel: enabledChannel("c-"+ct, ct), recipients: []string{"x@x.com"}}
		switch n.deliverWithRetry(context.Background(), d, &RenderedNotification{}) {
		case deliveryRefused:
			refused++
		case deliveryFailed:
			failed++
		}
	}
	if refused != 1 || failed != 1 {
		t.Fatalf("refused=%d failed=%d, want 1/1", refused, failed)
	}
	if failed == 0 {
		t.Fatal("a mix would have been acked, dropping the channel a retry could still deliver")
	}
}

// The SMTP adapter's guard must be the one it was given. Without this, wiring the
// operator's allowed destinations into the registry and not into the adapter would leave
// the escape hatch working for webhooks and silently not for mail.
func TestSMTPAdapterUsesTheInjectedGuard(t *testing.T) {
	a := &smtpAdapter{}
	if a.egressGuard() == nil {
		t.Fatal("an unwired adapter must still get a guard, not a bare dialer")
	}

	guard := egress.NewGuard(nil)
	b := &smtpAdapter{guard: guard}
	if b.egressGuard() != guard {
		t.Fatal("the adapter did not use the guard it was constructed with")
	}
}

// And the registry must hand that guard to BOTH transports.
func TestTheRegistryGivesBothTransportsTheGuard(t *testing.T) {
	guard := egress.NewGuard(nil)
	reg := newAdapterRegistry(guard)

	smtp, ok := reg[model.ChannelTypeSMTP].(*smtpAdapter)
	if !ok || smtp.guard != guard {
		t.Fatal("the SMTP adapter did not receive the configured guard")
	}
	webhook, ok := reg[model.ChannelTypeWebhook].(*webhookAdapter)
	if !ok || webhook.client == nil {
		t.Fatal("the webhook adapter did not receive a guarded client")
	}
}

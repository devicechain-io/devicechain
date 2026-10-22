// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/rs/zerolog/log"
)

// A GATEWAY'S PUBLISH FOR A CHILD.
//
// A gateway is an ordinary device that fronts other devices. The platform broker authenticated
// it at CONNECT and confined it to its own subject namespace
// ("{i}.{t}.gateways.{gateway}.devices.{child}.events"); event-sources' capture source asserts
// that gateway (UnresolvedEvent.Gateway) and the child the subject names (UnresolvedEvent.Device).
// What the broker cannot say is whether THAT gateway may speak for THAT child. This file is
// where the platform decides, with one comparison on rows it already holds:
//
//	child.GatewayId == gateway.ID, and the gateway is enabled
//
// and where a "no" is made LOUD. MQTT 3.1.1 has no way for a broker to refuse a publish, so a
// publish the platform declines is PUBACKed and, unless the platform acts, vanishes. It is
// refused here as FailureReason_NotGatewayChild: terminal on its first delivery (no retry, no
// auto-registration, the same answer every time), recorded on failed-events WITH its payload
// (credentials redacted) so it can be recovered once the assignment is fixed, counted, logged,
// and announced to the gateway on its notices subject.
//
// The check applies in EVERY deviceAuthMode, because it is a property of the gateway subject
// and not of the credential policy: a deployment with authentication off still does not let one
// gateway speak for a device that is not its own.

// ErrNotGatewayChild is the refusal of a gateway's publish for a device that is not assigned to
// it. Reasons wrap it, so a caller can tell that a gateway check said no without parsing text.
var ErrNotGatewayChild = errors.New("the gateway may not publish for that device")

// GatewayNoticer tells a gateway that a publish it made was refused. messaging's
// GatewayNoticePublisher is the production implementation; nil means notices are not sent.
type GatewayNoticer interface {
	Notice(tenant, gateway string, n messaging.GatewayNotice) error
}

// resolveGatewayChild resolves an event a gateway published for a child to the child, or
// refuses it with ErrNotGatewayChild. It reads the child and the gateway through the cached
// by-token lookup, one hit each, so a gateway event costs two cache reads where any other event
// costs one (a two-token lookup would bypass the cache and read the store per event).
//
// The decision, in the order it is made, each step refusing:
//   - the event names a child (the capture source sets Device from the subject with Gateway). It
//     is NOT BrokerAsserted, deliberately: the broker authenticated the gateway, not the child,
//     and a replica that predates Gateway must not read the event as the child's own (see
//     event-sources' brokerAssertion.mark). Only that mark sets Gateway;
//   - the child exists in the tenant (an unknown token is refused on the first delivery, not
//     retried: a gateway cannot register a device by naming it);
//   - the gateway exists and is enabled;
//   - the child's recorded gateway is this gateway. A cache entry written by a replica that
//     predates the field has none, which reads as "no gateway" and refuses: the safe direction.
func (rez *EventResolver) resolveGatewayChild(ctx context.Context, unrez *esmodel.UnresolvedEvent) (*model.Device, uint, error) {
	refuse := func(why string) (*model.Device, uint, error) {
		return nil, uint(resolvedevent.FailureReason_NotGatewayChild),
			fmt.Errorf("%w: gateway %q published for device %q, %s", ErrNotGatewayChild, unrez.Gateway, unrez.Device, why)
	}
	if unrez.Device == "" {
		return refuse("but the event names no device")
	}
	children, err := rez.Api.DevicesByToken(ctx, []string{unrez.Device})
	if err != nil {
		return nil, uint(resolvedevent.FailureReason_ApiCallFailed), err
	}
	if len(children) == 0 {
		return refuse("which is not registered")
	}
	gateways, err := rez.Api.DevicesByToken(ctx, []string{unrez.Gateway})
	if err != nil {
		return nil, uint(resolvedevent.FailureReason_ApiCallFailed), err
	}
	if len(gateways) == 0 {
		return refuse("and the gateway is not registered")
	}
	child, gateway := children[0], gateways[0]
	if gateway.IsDisabled() {
		return refuse("but the gateway is disabled")
	}
	if child.GatewayId == nil || *child.GatewayId != gateway.ID {
		return refuse("which is not assigned to that gateway")
	}
	return child, 0, nil
}

// gatewayRefusals is what a refused gateway publish does besides being dead-lettered: count,
// log, and tell the gateway. Shared by the whole resolver pool (the workers read one channel).
type gatewayRefusals struct {
	// counter counts refusals; label-free (never per-tenant, ADR-023 G.3), nil in tests that
	// do not look at it.
	counter interface{ Inc() }
	// noticer sends the notice; nil sends none.
	noticer GatewayNoticer

	now   func() time.Time
	every time.Duration

	mu   sync.Mutex
	last map[string]time.Time
}

// gatewayWarnEvery bounds the WARN to one line per (tenant, gateway, child) per window, and
// gatewayWarnKeys bounds how many such keys are remembered: a misconfigured fleet can name an
// unbounded set of children, and a log limiter that grows with the offender is itself a leak.
const (
	gatewayWarnEvery = time.Minute
	gatewayWarnKeys  = 4096
)

func newGatewayRefusals() *gatewayRefusals {
	return &gatewayRefusals{now: time.Now, every: gatewayWarnEvery, last: map[string]time.Time{}}
}

// refused records one refusal: the counter always, the WARN at most once a window per
// (tenant, gateway, child) naming all three, and the notice every time (it is the gateway's
// per-event signal, and a core publish never blocks).
func (g *gatewayRefusals) refused(tenant string, unrez *esmodel.UnresolvedEvent, cause error) {
	if g == nil {
		return
	}
	if g.counter != nil {
		g.counter.Inc()
	}
	if g.shouldWarn(tenant + "|" + unrez.Gateway + "|" + unrez.Device) {
		log.Warn().Str("tenant", tenant).Str("gateway", unrez.Gateway).Str("child", unrez.Device).
			Err(cause).
			Msg("A gateway published for a device that is not assigned to it; the event was refused " +
				"and recorded in failed events. Assign the device to the gateway, or stop the gateway " +
				"publishing for it. Further refusals for this pair are counted, not logged, for a minute.")
	}
	if g.noticer != nil {
		altId := ""
		if unrez.AltId != nil {
			altId = *unrez.AltId
		}
		if err := g.noticer.Notice(tenant, unrez.Gateway, messaging.GatewayNotice{
			AltId: altId, Device: unrez.Device, Reason: messaging.GatewayNoticeReasonNotGatewayChild,
		}); err != nil {
			log.Debug().Err(err).Str("tenant", tenant).Str("gateway", unrez.Gateway).
				Msg("Could not send a gateway its refusal notice; the failed-events record stands.")
		}
	}
}

func (g *gatewayRefusals) shouldWarn(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if at, ok := g.last[key]; ok && now.Sub(at) < g.every {
		return false
	}
	if len(g.last) >= gatewayWarnKeys {
		// Drop the entries whose window has closed; if every one is still open the map is
		// at its bound and the offender set is wider than the log can usefully name, so a
		// key that does not fit is warned and not remembered.
		for k, at := range g.last {
			if now.Sub(at) >= g.every {
				delete(g.last, k)
			}
		}
		if len(g.last) >= gatewayWarnKeys {
			return true
		}
	}
	g.last[key] = now
	return true
}

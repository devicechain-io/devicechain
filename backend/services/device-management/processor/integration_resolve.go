// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/transport"
)

// AN INBOUND INTEGRATION'S EVENT.
//
// A LoRaWAN network server or a webhook delivers uplinks over HTTP with one bearer token for
// the integration, not one credential per device. event-sources' integration route
// authenticates the sender, finds the device by its alias, and publishes the event with the
// integration's token and the bearer token SEALED beside it (UnresolvedEvent.Integration,
// SealedIntegrationToken). This file is where the platform decides whether to believe that:
//
//  1. the sealed token opens for THIS tenant and THIS integration (credential.IntegrationSealKind),
//  2. the integration authenticates with it (the credential Checker, KindIntegration),
//  3. the integration is enabled,
//  4. the device the event names is of a type in the integration's scope.
//
// Every step refuses, and the event is trusted only after all four. The route has already done
// the same checks, and nothing here relies on that: its caches can be stale (a token rotated, an
// integration disabled, a device moved out of scope inside the cache window), and this is the
// authority, run for every event, so a revocation takes effect at the next event.
//
// 🔴 THE CHECK APPLIES IN EVERY deviceAuthMode. An integration event carries no device
// credential, so 'disabled' and 'optional' would otherwise let any event naming a device
// through; the integration's own authentication is the only authority such an event has, and a
// deployment with device authentication off still does not let a sender reach a device its
// integration is not scoped to.
//
// 🔴 THE MARKER IS THE ONLY AUTHORITY, so an event that also claims another one is refused
// rather than resolved by whichever is checked first: a broker assertion, a gateway, a
// transport mark or a device credential on an integration event means a producer is confused or
// forging, and neither is a case for picking a winner.

// errIntegrationEvent builds the refusal text of an integration event. It names the integration
// (a slug, not a secret) and never the token.
func errIntegrationEvent(integration, why string) error {
	return fmt.Errorf("integration %q: %s", integration, why)
}

// resolveIntegrationEvent resolves an event an inbound integration delivered to its device,
// or refuses it. See the block comment above for the rules.
func (rez *EventResolver) resolveIntegrationEvent(ctx context.Context, unrez *esmodel.UnresolvedEvent) (*model.Device, uint, error) {
	unauthenticated := uint(resolvedevent.FailureReason_Unauthenticated)
	if unrez.Integration == "" || unrez.SealedIntegrationToken == nil {
		return nil, unauthenticated, errors.New("an integration event must carry the integration and its sealed token")
	}
	if unrez.BrokerAsserted || unrez.AuthenticatedTransport || unrez.Gateway != "" ||
		unrez.CredentialType != nil || unrez.CredentialId != nil || unrez.CredentialSecret != nil ||
		unrez.SealedCredentialId != nil || unrez.SealedCredentialSecret != nil {
		return nil, unauthenticated, errIntegrationEvent(unrez.Integration, "an integration event carries no other authority")
	}
	// The source stamp is the one command delivery classifies a device's transport by, so an
	// integration event that is stamped as anything else would be commandable (or would corrupt
	// another transport's presence rows). It must be exactly "integration:{token}".
	if want := transport.Qualify(transport.Integration, unrez.Integration); unrez.Source != want {
		return nil, unauthenticated, errIntegrationEvent(unrez.Integration, "the event's source is not the integration's")
	}
	// What an integration delivers is readings. A marked event of any other type does not
	// inherit the trust, so a future emit path for a more dangerous type cannot silently get it.
	if unrez.EventType != eventtype.Measurement {
		return nil, unauthenticated, errIntegrationEvent(unrez.Integration, "an integration may deliver measurements only")
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" || rez.SecretTransit == nil {
		return nil, unauthenticated, errIntegrationEvent(unrez.Integration, "the sealed token cannot be opened")
	}
	token, err := rez.SecretTransit.OpenToken(tenant, credential.IntegrationSealKind(unrez.Integration), unrez.SealedIntegrationToken)
	if err != nil || token == "" {
		return nil, unauthenticated, errIntegrationEvent(unrez.Integration, "the sealed token was not sealed for this tenant and integration")
	}
	integration, err := rez.Api.AuthenticateIntegration(ctx, unrez.Integration, token)
	switch {
	case err == nil:
	case errors.Is(err, model.ErrIntegrationAuthFailed):
		return nil, unauthenticated, errIntegrationEvent(unrez.Integration, "authentication failed")
	case errors.Is(err, model.ErrIntegrationDisabled):
		return nil, unauthenticated, errIntegrationEvent(unrez.Integration, "the integration is disabled")
	default:
		// The database, or no integration key wired: says nothing about the token, so it is
		// the retryable kind of failure, never read as a rejection.
		return nil, uint(resolvedevent.FailureReason_ApiCallFailed), err
	}
	matches, err := rez.Api.DevicesByToken(ctx, []string{unrez.Device})
	if err != nil {
		return nil, uint(resolvedevent.FailureReason_ApiCallFailed), err
	}
	if len(matches) == 0 {
		return nil, uint(resolvedevent.FailureReason_DeviceNotFound),
			fmt.Errorf("event device token %q is not registered", unrez.Device)
	}
	if !integration.ScopeHas(matches[0].DeviceTypeId) {
		return nil, unauthenticated, errIntegrationEvent(unrez.Integration, "the device's type is not in the integration's scope")
	}
	return matches[0], 0, nil
}

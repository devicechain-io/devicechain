// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-event-sources/adapter"
)

// The Sparkplug session machine decodes the wire format into the shared adapter's
// protocol-neutral shapes (adapter.Sample / adapter.PresenceEvent) and hands them to the
// shared Registrar/Emitter/Ingester/Reconciler (ADR-044 register + emit, ADR-067
// presence + failover reconcile). Only the two per-protocol identifiers below reach the
// adapter — Sparkplug's device-token prefix and dedup-id namespace — and this package is
// the single place they are pinned.
const (
	// tokenPrefix labels every auto-derived Sparkplug device token (ADR-042 grammar:
	// alphanumeric-first) so an operator recognizes a Sparkplug-origin device.
	tokenPrefix = "sp-"
	// dedupPrefix namespaces every emitted JetStream dedup id so a Sparkplug id can
	// never collide with another protocol's in the shared InboundEvents dedup window.
	dedupPrefix = "sp"
)

// Type aliases let the Sparkplug session machine and Client wiring keep naming these
// shapes locally while they live in the shared adapter — identical types, no conversion.
// Only the shapes host actually names are aliased (the constructors below return the
// adapter's pointer types directly).
type (
	Sample         = adapter.Sample
	PresenceEvent  = adapter.PresenceEvent
	AssertedDevice = adapter.AssertedDevice
	IngestPolicy   = adapter.IngestPolicy
	IngestMetrics  = adapter.IngestMetrics
	Ingester       = adapter.Ingester
	Publication    = adapter.Publication
	Reconciler     = adapter.Reconciler
)

// NewRegistrar binds the shared registrar with Sparkplug's token prefix, so callers (and
// the wiring test) never repeat it. tenantDeleted is the ADR-077 lifecycle gate (nil
// disables it); see adapter.NewRegistrar.
func NewRegistrar(client adapter.GraphQLClient, graphqlURL string, tenantDeleted func(string) bool) *adapter.Registrar {
	return adapter.NewRegistrar(client, graphqlURL, tokenPrefix, tenantDeleted)
}

// registrarResolver adapts the shared registrar to TokenResolver.
type registrarResolver struct{ r *adapter.Registrar }

// ResolveToken returns the token of an already-registered device. It never registers one: a device
// that is not (or no longer) ingestible is simply not addressable by a command. The registrar's
// cache holds the answer for any device a birth has just resolved, so this is not a round trip in
// the ordinary case.
func (x registrarResolver) ResolveToken(ctx context.Context, tenant, externalId string, policy IngestPolicy) (string, bool, error) {
	token, outcome, err := x.r.Resolve(ctx, tenant, externalId, policy)
	if err != nil {
		return "", false, err
	}
	return token, outcome.Ingestible(), nil
}

// NewTokenResolver exposes a registrar to Client.SetTokenResolver.
func NewTokenResolver(r *adapter.Registrar) TokenResolver { return registrarResolver{r: r} }

// NewEmitter binds the shared emitter with Sparkplug's dedup-id prefix.
//
// authenticatedTransport=true: a Sparkplug publisher authenticates at the MQTT
// BROKER connection, so emitted events carry no per-event credential and the
// resolver trusts their device token under deviceAuthMode=required. NOTE this is
// BROKER-level, not per-device: the device token is derived from the publisher's
// MQTT topic (group/node/device), which Sparkplug does not authenticate per-device
// — so `required` does NOT close intra-tenant device-token spoofing for Sparkplug
// (it still does for HTTP/MQTT credential paths). Cross-tenant is still closed
// (connection-scoped tenancy). Real per-device Sparkplug auth is a tracked gap.
func NewEmitter(writer adapter.EventWriter, now func() time.Time) *adapter.Emitter {
	return adapter.NewEmitter(writer, now, dedupPrefix, true)
}

// NewReconciler and NewIngester carry no protocol prefix; they are thin pass-throughs so
// all four adapter constructors read consistently at the Sparkplug wiring site.
func NewReconciler(client adapter.GraphQLClient, deviceStateURL string) *adapter.Reconciler {
	return adapter.NewReconciler(client, deviceStateURL)
}

func NewIngester(registrar *adapter.Registrar, emitter *adapter.Emitter, metrics adapter.IngestMetrics) *adapter.Ingester {
	return adapter.NewIngester(registrar, emitter, metrics)
}

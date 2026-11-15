// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-event-sources/processor"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/auth"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/stretchr/testify/require"
)

// A DEVICE'S OWN UPLINK IS WRITTEN TO THE UPLINK STREAM ONLY, SEALED, AND WITH NO AUTHORITY BUT ITS
// OWN CREDENTIAL. This asserts the publish the device route is wired to; the route's own tests hand
// it a publisher of their own.

func deviceUplinkEvent() *model.UnresolvedEvent {
	typ, id, secret := "MQTT_BASIC", "meter-1", "hunter2"
	now := time.Now().UTC()
	return &model.UnresolvedEvent{
		Device: "meter-1", OccurredTime: now, ProcessedTime: now, EventType: eventtype.IntegrationUplink,
		CredentialType: &typ, CredentialId: &id, CredentialSecret: &secret,
	}
}

func TestADeviceUplinkIsWrittenToTheUplinkStreamSealedAndUnmarked(t *testing.T) {
	sealingFixture(t)
	savedInbound, savedUplinks := InboundEventsWriter, IntegrationUplinksWriter
	t.Cleanup(func() { InboundEventsWriter, IntegrationUplinksWriter = savedInbound, savedUplinks })
	inbound, uplinks := &recordingWriter{}, &recordingWriter{}
	InboundEventsWriter, IntegrationUplinksWriter = inbound, uplinks

	e := deviceUplinkEvent()
	// A producer that tried to smuggle authority in: every one is cleared.
	e.Integration, e.SealedIntegrationToken, e.IntegrationIdentifier = "cs", []byte{1}, "dev"
	e.AuthenticatedTransport = true

	require.NoError(t, onDeviceUplinkDecoded("http", "acme", e, &model.UnresolvedIntegrationUplinkPayload{Frame: []byte{1, 2}, MaxReadings: 3}, ""))
	require.Zero(t, inbound.count(), "a device uplink must never reach inbound-events")
	require.Equal(t, 1, uplinks.count())

	got, err := esproto.UnmarshalUnresolvedEvent(uplinks.msgs[0].Value)
	require.NoError(t, err)
	require.Equal(t, eventtype.IntegrationUplink, got.EventType)
	require.Equal(t, "http", got.Source)
	require.Empty(t, got.Integration)
	require.Nil(t, got.SealedIntegrationToken)
	require.Empty(t, got.IntegrationIdentifier)
	require.False(t, got.AuthenticatedTransport)
	require.Nil(t, got.CredentialSecret, "the secret as sent never crosses the stream")
	require.NotEmpty(t, got.SealedCredentialSecret)
	require.NotContains(t, string(uplinks.msgs[0].Value), "hunter2")
}

// The device-facing publish for every other path still refuses the carrier type: the new function
// is the only door.
func TestTheGenericDevicePublishStillRefusesTheCarrier(t *testing.T) {
	sealingFixture(t)
	_, _, ok := inboundEventMessage("http", "acme", deviceUplinkEvent(), &model.UnresolvedIntegrationUplinkPayload{}, "")
	require.False(t, ok)
}

func TestADeviceUplinkWriteFailureIsReturnedNotSwallowed(t *testing.T) {
	sealingFixture(t)
	saved := IntegrationUplinksWriter
	t.Cleanup(func() { IntegrationUplinksWriter = saved })
	payload := &model.UnresolvedIntegrationUplinkPayload{MaxReadings: 1}

	IntegrationUplinksWriter = nil
	require.Error(t, onDeviceUplinkDecoded("http", "acme", deviceUplinkEvent(), payload, ""))

	IntegrationUplinksWriter = &recordingWriter{err: messaging.ErrStreamBackpressure}
	require.ErrorIs(t, onDeviceUplinkDecoded("http", "acme", deviceUplinkEvent(), payload, ""), messaging.ErrStreamBackpressure)

	// A message that cannot be built is the route's 500, not a success.
	IntegrationUplinksWriter = &recordingWriter{}
	require.ErrorIs(t, onDeviceUplinkDecoded("http", "", deviceUplinkEvent(), payload, ""), processor.ErrEventNotBuilt)
}

// 🔴 THE ROUTE IS OFF UNLESS THE OPERATOR TURNS IT ON. It publishes an uplink with no integration
// mark, which a device-management from before the route refuses; so during a rolling upgrade an
// old resolver would record as failed a request the route had already answered 202. Off is the
// default and builds no route at all (the source then answers 404).
func TestTheDeviceUplinkRouteIsOffUnlessTheOperatorEnablesIt(t *testing.T) {
	require.Nil(t, buildDeviceUplinkRoute(config.HttpIngest{}, nil))
	require.Nil(t, buildDeviceUplinkRoute(config.HttpIngest{DeviceUplinks: false, HeaderTimeoutSeconds: 5}, nil))
	// The paired positive: enabling it reaches the part that needs the instance (a nil Microservice
	// here), so the guard above is what stopped the others.
	require.Panics(t, func() { buildDeviceUplinkRoute(config.HttpIngest{DeviceUplinks: true}, nil) })
}

// A device's raw payload over MQTT is written to the uplink stream with the broker's mark kept and
// the capture sequence as its dedup id (so a redelivered capture message is stored once), with no
// credential and no integration authority.
func TestAnMqttDeviceUplinkKeepsTheBrokersMarkAndItsDedupID(t *testing.T) {
	sealingFixture(t)
	savedInbound, savedUplinks := InboundEventsWriter, IntegrationUplinksWriter
	t.Cleanup(func() { InboundEventsWriter, IntegrationUplinksWriter = savedInbound, savedUplinks })
	inbound, uplinks := &recordingWriter{}, &recordingWriter{}
	InboundEventsWriter, IntegrationUplinksWriter = inbound, uplinks

	now := time.Now().UTC()
	e := &model.UnresolvedEvent{Device: "meter-1", OccurredTime: now, ProcessedTime: now,
		EventType: eventtype.IntegrationUplink, BrokerAsserted: true}
	dedup := processor.DedupID("acme", 77)
	require.NoError(t, onDeviceUplinkDecoded("gw", "acme", e, &model.UnresolvedIntegrationUplinkPayload{Frame: []byte{9}, MaxReadings: 1}, dedup))

	require.Zero(t, inbound.count(), "never inbound-events")
	require.Equal(t, 1, uplinks.count())
	require.Equal(t, dedup, uplinks.msgs[0].DedupID)
	got, err := esproto.UnmarshalUnresolvedEvent(uplinks.msgs[0].Value)
	require.NoError(t, err)
	require.True(t, got.BrokerAsserted, "the broker authenticated the device")
	require.Nil(t, got.CredentialType)
	require.Empty(t, got.Integration)
	require.False(t, got.AuthenticatedTransport)
}

func TestDeviceUplinkRequiresEndpointAndServiceSecret(t *testing.T) {
	saved := Microservice
	t.Cleanup(func() { Microservice = saved })
	Microservice = &core.Microservice{FunctionalArea: "event-sources"}
	infra := &Microservice.InstanceConfiguration.Infrastructure
	infra.DeviceManagement = mscfg.DeviceManagementConfiguration{Hostname: "devices", Port: 8080}
	infra.ServiceAuth.Secret = "secret"
	peers := func() *svcclient.Peers {
		return svcclient.NewPeers(*infra, Microservice.FunctionalArea)
	}
	// No user-management guard is added: this site historically builds the client lazily.
	require.NotNil(t, newDeviceUplinkRoute(peers()))
	infra.DeviceManagement.Port = 0
	require.Nil(t, newDeviceUplinkRoute(peers()))
	infra.DeviceManagement.Port = 8080
	infra.DeviceManagement.Hostname = ""
	require.Nil(t, newDeviceUplinkRoute(peers()))
	infra.DeviceManagement.Hostname = "devices"
	infra.ServiceAuth.Secret = ""
	require.Nil(t, newDeviceUplinkRoute(peers()))
}

// Exercise the wired client through both targets: its mint identity, internal address and
// shared token cache must survive the peer migration together.
func TestCommandPullWiringPreservesItsMintAndSharedClient(t *testing.T) {
	savedMs, savedConfig := Microservice, Configuration
	t.Cleanup(func() { Microservice, Configuration = savedMs, savedConfig })
	Microservice = &core.Microservice{FunctionalArea: "event-sources"}
	Configuration = &config.EventSourcesConfiguration{}
	var dmCalls, cdCalls atomic.Int32
	peer := func(calls *atomic.Int32, body string) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/graphql" || r.Header.Get("Authorization") != "Bearer service-token" ||
				r.Header.Get(auth.ServiceTenantHeader) != "acme" {
				http.Error(w, "unexpected peer request", http.StatusBadRequest)
				return
			}
			calls.Add(1)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(server.Close)
		return server
	}
	dm := peer(&dmCalls, `{"data":{"authenticateCommandChannel":{"deviceToken":"device-1","commandChannel":"HTTP_PULL"}}}`)
	cd := peer(&cdCalls, `{"data":{"pullCommands":[]}}`)
	mints := make(chan auth.ServiceTokenRequest, 2)
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != auth.ServiceTokenPath || r.Header.Get(auth.ServiceSecretHeader) != "secret" {
			http.Error(w, "unexpected mint request", http.StatusBadRequest)
			return
		}
		var request auth.ServiceTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mints <- request
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "service-token", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	}))
	t.Cleanup(mint.Close)
	port := func(server *httptest.Server) uint32 {
		u, err := url.Parse(server.URL)
		require.NoError(t, err)
		n, err := strconv.ParseUint(u.Port(), 10, 32)
		require.NoError(t, err)
		return uint32(n)
	}
	infra := &Microservice.InstanceConfiguration.Infrastructure
	infra.ServiceAuth.Secret = "secret"
	infra.UserManagement = mscfg.UserManagementConfiguration{Hostname: "127.0.0.1", Port: port(dm), InternalPort: port(mint)}
	infra.DeviceManagement = mscfg.DeviceManagementConfiguration{Hostname: "127.0.0.1", Port: port(dm)}
	infra.CommandDelivery = mscfg.CommandDeliveryConfiguration{Hostname: "127.0.0.1", Port: port(cd)}
	route := buildCommandPullRoute(svcclient.NewPeers(*infra, Microservice.FunctionalArea))
	require.NotNil(t, route)
	device, err := route.Backend.Authenticate(context.Background(), "acme", "access-token")
	require.NoError(t, err)
	require.NotNil(t, device)
	_, err = route.Backend.Pull(context.Background(), "acme", device.Token, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, dmCalls.Load())
	require.EqualValues(t, 1, cdCalls.Load())
	require.Len(t, mints, 1, "both targets must share one client's token cache")
	request := <-mints
	require.Equal(t, "event-sources", request.Subject)
	require.Equal(t, []string{string(auth.DeviceRead), string(auth.CommandClaim)}, request.Authorities)
}

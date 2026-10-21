// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	esconfig "github.com/devicechain-io/dc-event-sources/config"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	dctest "github.com/devicechain-io/dc-microservice/test"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// kickBroker is a real nats-server with the shape the platform deploys: an APP account holding
// the service login and the devices, a SYS account holding the system login, and the MQTT
// gateway. The device-management auth callout is not part of it (that is another module): what
// is under test here is that a REQUEST on the wire ends a REAL session through the system
// account, and that the service refuses to run without the login that makes this possible.
func kickBroker(t *testing.T) (cfg mscfg.NatsConfiguration, mqttPort int) {
	t.Helper()
	dir := dctest.JetStreamStoreDir(t)
	conf := filepath.Join(dir, "nats.conf")
	require.NoError(t, os.WriteFile(conf, []byte(fmt.Sprintf(`
listen: "127.0.0.1:-1"
server_name: "kick-test"
jetstream { store_dir: %q }
mqtt { listen: "127.0.0.1:-1" }
accounts {
  APP: { jetstream: enabled, users: [ {user: dc_service, password: svc} ] }
  SYS: { users: [ {user: dc_sys, password: sys} ] }
}
system_account: SYS
`, dir)), 0o600))
	opts, err := natsserver.ProcessConfigFile(conf)
	require.NoError(t, err)
	opts.NoLog, opts.NoSigs = true, true
	srv, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second), "embedded broker not ready")
	t.Cleanup(srv.Shutdown)
	addr, ok := srv.Addr().(*net.TCPAddr)
	require.True(t, ok)
	cfg = mscfg.NatsConfiguration{Hostname: "127.0.0.1", Port: uint32(addr.Port)}
	cfg.Auth.User, cfg.Auth.Password = "dc_service", "svc"
	cfg.Auth.SysUser, cfg.Auth.SysPassword = "dc_sys", "sys"
	return cfg, dctest.MQTTPort(t, srv)
}

// withKickService installs the service state startSessionKick reads, and restores it.
func withKickService(t *testing.T, cfg mscfg.NatsConfiguration, gatewaySource string) {
	t.Helper()
	savedMs, savedGateway, savedConfig := Microservice, GatewaySourceId, Configuration
	t.Cleanup(func() {
		stopSessionKick()
		Microservice, GatewaySourceId, Configuration = savedMs, savedGateway, savedConfig
	})
	Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats = cfg
	GatewaySourceId = gatewaySource
	Configuration = &esconfig.EventSourcesConfiguration{}
	Configuration.ApplyDefaults()
}

func dataConn(t *testing.T, cfg mscfg.NatsConfiguration) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(fmt.Sprintf("nats://%s:%d", cfg.Hostname, cfg.Port), nats.UserInfo(cfg.Auth.User, cfg.Auth.Password))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

func device(t *testing.T, mqttPort int, clientID string) mqtt.Client {
	t.Helper()
	opts := mqtt.NewClientOptions().AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", mqttPort)).
		SetClientID(clientID).SetUsername("dc_service").SetPassword("svc").SetAutoReconnect(false)
	cl := mqtt.NewClient(opts)
	tok := cl.Connect()
	require.True(t, tok.WaitTimeout(20*time.Second))
	require.NoError(t, tok.Error())
	return cl
}

// A request on the wire ends the device's real broker session through the system account, and
// the requester is told so. Negative control: without startSessionKick's subscription the same
// request finds no responder (second half); without the kick call the session stays connected.
func TestARequestOnTheWireEndsTheDevicesRealSession(t *testing.T) {
	cfg, mqttPort := kickBroker(t)
	withKickService(t, cfg, "mqtt1")
	nc := dataConn(t, cfg)
	requester := messaging.NewSessionKickRequester(nc, "inst-1")
	requester.Attempts, requester.Backoff, requester.AttemptTimeout = 2, 10*time.Millisecond, 3*time.Second

	// Before the service listens: nobody answers, and that is an ERROR.
	err := requester.KickDeviceSessions(context.Background(), "acme", messaging.SessionKickDeviceDisabled, []string{"sensor-001"})
	require.ErrorIs(t, err, messaging.ErrSessionKickUnavailable)
	require.Contains(t, err.Error(), "no service is answering")

	require.NoError(t, startSessionKickOn(context.Background(), func() *nats.Conn { return nc }))

	victim := device(t, mqttPort, "inst-1:acme:sensor-001")
	bystander := device(t, mqttPort, "inst-1:acme:sensor-002")
	require.NoError(t, requester.KickDeviceSessions(context.Background(), "acme", messaging.SessionKickDeviceDisabled, []string{"sensor-001"}))
	require.Eventually(t, func() bool { return !victim.IsConnected() }, 10*time.Second, 25*time.Millisecond,
		"the device's session was not ended")
	require.True(t, bystander.IsConnected(), "another device's session was ended")

	// A device with no session is a confirmed success, not an error.
	require.NoError(t, requester.KickDeviceSessions(context.Background(), "acme", messaging.SessionKickDeviceDisabled, []string{"sensor-404"}))
}

// A malformed request is answered with a reason, never dropped (a dropped one reads as a
// timeout and is retried), and never reaches the broker.
func TestAMalformedKickRequestIsAnsweredNotDropped(t *testing.T) {
	cfg, _ := kickBroker(t)
	withKickService(t, cfg, "mqtt1")
	nc := dataConn(t, cfg)
	require.NoError(t, startSessionKickOn(context.Background(), func() *nats.Conn { return nc }))
	msg, err := nc.Request(messaging.SessionKickSubject("inst-1"), []byte(`{"tenant":"acme","tokens":[]}`), 5*time.Second)
	require.NoError(t, err)
	require.Contains(t, string(msg.Data), `"error"`)
	require.NotContains(t, string(msg.Data), `"ok":true`)
}

// FAIL CLOSED AT START. Where the broker runs the device auth callout, device-management asks
// this service to end sessions, so without a system-account login it does not start, whether
// or not it reads a platform-broker source (a device holds a session for its commands too). A
// broker with no callout (auth off) mints no sessions of ours, and the service starts.
func TestTheServiceRefusesToStartWithoutTheLoginThatEndsSessions(t *testing.T) {
	cfg, _ := kickBroker(t)
	cfg.Auth.SysUser, cfg.Auth.SysPassword = "", ""
	cfg.Auth.CalloutIssuerSeed = "SAcalloutseedplaceholder"

	for _, gateway := range []string{"mqtt1", ""} {
		withKickService(t, cfg, gateway)
		err := startSessionKickOn(context.Background(), func() *nats.Conn { return nil })
		require.ErrorIs(t, err, errNoSystemLoginForSessionKick, "gateway source %q", gateway)
	}

	cfg.Auth.CalloutIssuerSeed = ""
	withKickService(t, cfg, "mqtt1")
	require.NoError(t, startSessionKickOn(context.Background(), func() *nats.Conn { return nil }),
		"a broker with no device callout has no sessions of ours, even with a platform-broker source")
	require.Nil(t, sessionKick)
}

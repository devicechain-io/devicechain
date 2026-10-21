// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"fmt"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// These run against a REAL nats-server: what is claimed is what the broker does with a kick
// request and with the connection list, not what a hand-built reply says.

func awaitDisconnected(t *testing.T, cl mqtt.Client, what string) {
	t.Helper()
	require.Eventually(t, func() bool { return !cl.IsConnected() }, 10*time.Second, 25*time.Millisecond, what)
}

func TestKickEndsEveryLiveSessionOfTheDeviceAndNoOneElses(t *testing.T) {
	srv, mqttPort := broker(t)
	sys := sysConn(t, srv)

	plain := connectDevice(t, mqttPort, "inst-1:acme:sensor-001")
	second := connectDevice(t, mqttPort, "inst-1:acme:sensor-001:b") // a discriminated concurrent session
	other := connectDevice(t, mqttPort, "inst-1:acme:sensor-002")    // another device of the tenant
	crossTenant := connectDevice(t, mqttPort, "inst-1:globex:sensor-001")
	crossInstance := connectDevice(t, mqttPort, "inst-2:acme:sensor-001")
	// A prefix-sharing token must not be caught by a prefix match: "sensor-0011".
	neighbour := connectDevice(t, mqttPort, "inst-1:acme:sensor-0011")

	n, err := KickDevices(context.Background(), NewRequester(sys), testInstance, "acme", []string{"sensor-001"}, time.Second)
	require.NoError(t, err)
	require.Equal(t, 2, n, "the plain and the discriminated session")

	awaitDisconnected(t, plain, "the device's plain session was not ended")
	awaitDisconnected(t, second, "the device's discriminated session was not ended")
	for name, cl := range map[string]mqtt.Client{
		"another device of the tenant": other, "the same token in another tenant": crossTenant,
		"the same device id in another instance": crossInstance, "a token that merely starts with it": neighbour,
	} {
		require.True(t, cl.IsConnected(), "%s was kicked", name)
	}
}

func TestKickOfADeviceWithNoSessionIsASuccessOfZero(t *testing.T) {
	srv, _ := broker(t)
	n, err := KickDevices(context.Background(), NewRequester(sysConn(t, srv)), testInstance, "acme",
		[]string{"sensor-404"}, time.Second)
	require.NoError(t, err)
	require.Zero(t, n)
}

// A device that reconnects after the kick is let straight back in by a broker with no callout:
// the kick ends sessions, it does not ban. The refusal of a revoked credential is the
// callout's, against the database. Pinned here so nobody reads the kick as a ban.
func TestAKickedDeviceCanReconnectUntilTheCalloutRefusesIt(t *testing.T) {
	srv, mqttPort := broker(t)
	cl := connectDevice(t, mqttPort, "inst-1:acme:sensor-001")
	_, err := KickDevices(context.Background(), NewRequester(sysConn(t, srv)), testInstance, "acme",
		[]string{"sensor-001"}, time.Second)
	require.NoError(t, err)
	awaitDisconnected(t, cl, "not kicked")
	again := connectDevice(t, mqttPort, "inst-1:acme:sensor-001")
	require.True(t, again.IsConnected())
}

// kickFakeRequester answers the three calls a kick makes from tables, so the fail-closed branches
// the real broker will not produce on demand can be driven.
type kickFakeRequester struct {
	servers  []string // ids that answer the ping
	expected int      // what they claim the cluster size is
	connz    map[string]string
	kick     func(subject string) string
}

func (f *kickFakeRequester) Gather(_ context.Context, _ string, _ []byte, _ time.Duration) ([][]byte, error) {
	var out [][]byte
	for _, id := range f.servers {
		out = append(out, []byte(fmt.Sprintf(`{"server":{"id":%q},"statsz":{"active_servers":%d}}`, id, f.expected)))
	}
	return out, nil
}

func (f *kickFakeRequester) RequestWithContext(_ context.Context, subject string, _ []byte) (*nats.Msg, error) {
	for id, body := range f.connz {
		if subject == fmt.Sprintf(sysServerDirect, id, "CONNZ") {
			return &nats.Msg{Data: []byte(body)}, nil
		}
	}
	if f.kick != nil {
		return &nats.Msg{Data: []byte(f.kick(subject))}, nil
	}
	return nil, fmt.Errorf("unexpected request %s", subject)
}

func connzWith(clientID string, cid int) string {
	return fmt.Sprintf(`{"server":{"id":"S1"},"data":{"total":1,"num_connections":1,"connections":[{"cid":%d,"mqtt_client":%q}]}}`,
		cid, clientID)
}

// FAIL CLOSED: a cluster only partly consulted cannot say a device has no session left.
func TestKickRefusesToReportSuccessOnAnIncompleteCluster(t *testing.T) {
	f := &kickFakeRequester{servers: []string{"S1"}, expected: 2, connz: map[string]string{"S1": connzWith("inst-1:acme:other", 7)}}
	_, err := KickDevices(context.Background(), f, testInstance, "acme", []string{"sensor-001"}, time.Millisecond)
	require.Error(t, err)
	require.Contains(t, err.Error(), "1 of 2")
}

func TestKickReportsABrokerThatRefusesTheKick(t *testing.T) {
	f := &kickFakeRequester{servers: []string{"S1"}, expected: 1,
		connz: map[string]string{"S1": connzWith("inst-1:acme:sensor-001", 7)},
		kick:  func(string) string { return `{"error":{"description":"permission denied"}}` }}
	n, err := KickDevices(context.Background(), f, testInstance, "acme", []string{"sensor-001"}, time.Millisecond)
	require.Error(t, err)
	require.Zero(t, n, "a refused kick is not counted")
}

func TestKickTreatsAConnectionThatIsAlreadyGoneAsEnded(t *testing.T) {
	f := &kickFakeRequester{servers: []string{"S1"}, expected: 1,
		connz: map[string]string{"S1": connzWith("inst-1:acme:sensor-001", 7)},
		kick:  func(string) string { return `{"error":{"description":"no such client or leafnode id"}}` }}
	_, err := KickDevices(context.Background(), f, testInstance, "acme", []string{"sensor-001"}, time.Millisecond)
	require.NoError(t, err)
}

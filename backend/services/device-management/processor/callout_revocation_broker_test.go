// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/devicechain-io/dc-microservice/natsauth"
	dctest "github.com/devicechain-io/dc-microservice/test"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// REVOCATION, END TO END ON ONE BROKER: the auth callout, the system-account kick, and the
// device's reconnect, together. Each half has its own tests (the callout here, the kick in
// event-sources/presence against a real broker); what neither can show alone is the property
// the pair exists for: once a device's events carry no credential, a revoked credential stops
// a session ONLY because the kick ends it and the callout then refuses the reconnect, while a
// session on another, still-valid credential of the same device comes straight back.
//
// The kick is made the way event-sources makes it (CONNZ on the server, then KICK by
// connection id) over a dc_sys login held to natsauth's allow list, so the list is proven
// enough for it on a broker that also runs the callout.

const revokeClientID = "inst-1:acme-corp:sensor-001"

// startCalloutServerWithSys is startCalloutServer plus the system-account login, listed in
// auth_users as the infrastructure module lists it and held to the same allow list.
func startCalloutServerWithSys(t *testing.T, creds natsauth.Credentials) (*natsserver.Server, int) {
	t.Helper()
	serviceHash, err := bcrypt.GenerateFromPassword([]byte(creds.ServicePassword), bcrypt.MinCost)
	require.NoError(t, err)
	q := func(subjects []string) string {
		out := make([]string, 0, len(subjects))
		for _, s := range subjects {
			out = append(out, strconv.Quote(s))
		}
		return strings.Join(out, ", ")
	}
	dir := dctest.JetStreamStoreDir(t)
	conf := filepath.Join(dir, "nats.conf")
	body := fmt.Sprintf(`
		port: -1
		server_name: "callout-revoke"
		jetstream { store_dir: %q }
		mqtt { host: "127.0.0.1", port: -1 }
		system_account: SYS
		accounts {
		  APP { jetstream: enabled, users: [{user: %q, password: %q}] }
		  SYS { users: [{user: %q, password: "sys", permissions: { publish: { allow: [%s] }, subscribe: { allow: [%s] } }}] }
		}
		authorization {
		  timeout: 5
		  auth_callout { issuer: %q, auth_users: [%q, %q], account: "APP" }
		}
	`, filepath.Join(dir, "js"), natsauth.ServiceUser, string(serviceHash),
		natsauth.SysUser, q(natsauth.SysPublishAllow()), q(natsauth.SysSubscribeAllow()),
		creds.IssuerPublic, natsauth.ServiceUser, natsauth.SysUser)
	require.NoError(t, os.WriteFile(conf, []byte(body), 0o600))
	opts, err := natsserver.ProcessConfigFile(conf)
	require.NoError(t, err)
	opts.NoLog, opts.NoSigs = true, true
	srv, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)
	return srv, dctest.MQTTPort(t, srv)
}

// revocableApi resolves any credential id of acme-corp to sensor-001 unless it was revoked,
// as the database does once a disable has committed.
type revocableApi struct {
	mu       sync.Mutex
	revoked  map[string]bool
	expiries map[string]time.Time
}

func (r *revocableApi) auth(_ context.Context, p *model.PresentedCredential) (*model.Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.revoked[p.CredentialId] {
		return nil, errors.New("credential disabled")
	}
	d := &model.Device{}
	d.Token = "sensor-001"
	if exp, ok := r.expiries[p.CredentialId]; ok {
		d.ConnectCredentialExpiresAt = sql.NullTime{Time: exp, Valid: true}
	}
	return d, nil
}

func (r *revocableApi) revoke(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revoked[id] = true
}

// startRevocationBroker runs the broker and the real callout responder over it.
func startRevocationBroker(t *testing.T, api *revocableApi) (*natsserver.Server, int) {
	t.Helper()
	creds, err := natsauth.GenerateCredentials()
	require.NoError(t, err)
	srv, mqttPort := startCalloutServerWithSys(t, creds)
	nc, err := nats.Connect(srv.ClientURL(), nats.UserInfo(natsauth.ServiceUser, creds.ServicePassword))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	r := mustResponder(t, nc, fakeAuthApi{authFn: api.auth}, testChecker(t, credentialtest.NewStore()), creds.IssuerSeed, nil)
	require.NoError(t, r.Start())
	t.Cleanup(func() { _ = r.Stop() })
	return srv, mqttPort
}

// liveDevice connects like firmware does: it reconnects on its own after losing the session,
// so "it stays disconnected" can only mean the broker keeps refusing it.
func liveDevice(t *testing.T, port int, clientID, username string) mqtt.Client {
	t.Helper()
	opts := mqtt.NewClientOptions().AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", port)).
		SetClientID(clientID).SetUsername(username).SetPassword("s3cret").
		SetAutoReconnect(true).SetConnectRetry(false).
		SetMaxReconnectInterval(200 * time.Millisecond).SetConnectTimeout(5 * time.Second)
	c := mqtt.NewClient(opts)
	tok := c.Connect()
	require.True(t, tok.WaitTimeout(15*time.Second))
	require.NoError(t, tok.Error(), "%s must connect", username)
	t.Cleanup(func() { c.Disconnect(100) })
	return c
}

// kickDevice ends every session of revokeClientID (plain and discriminated) the way
// event-sources does, and returns how many it ended.
func kickDevice(t *testing.T, srv *natsserver.Server) int {
	t.Helper()
	sys, err := nats.Connect(srv.ClientURL(), nats.UserInfo(natsauth.SysUser, "sys"))
	require.NoError(t, err)
	defer sys.Close()
	msg, err := sys.Request(fmt.Sprintf("$SYS.REQ.SERVER.%s.CONNZ", srv.ID()), []byte(`{"limit":1024}`), 5*time.Second)
	require.NoError(t, err)
	var reply struct {
		Data struct {
			Connections []struct {
				Cid        uint64 `json:"cid"`
				MQTTClient string `json:"mqtt_client"`
			} `json:"connections"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(msg.Data, &reply))
	kicked := 0
	for _, c := range reply.Data.Connections {
		if c.MQTTClient != revokeClientID && !strings.HasPrefix(c.MQTTClient, revokeClientID+":") {
			continue
		}
		_, err := sys.Request(fmt.Sprintf("$SYS.REQ.SERVER.%s.KICK", srv.ID()), []byte(fmt.Sprintf(`{"cid":%d}`, c.Cid)), 5*time.Second)
		require.NoError(t, err)
		kicked++
	}
	return kicked
}

func TestARevokedCredentialsSessionEndsOnlyThroughTheKickAndTheOtherCredentialsSurvives(t *testing.T) {
	api := &revocableApi{revoked: map[string]bool{}}
	srv, port := startRevocationBroker(t, api)

	thief := liveDevice(t, port, revokeClientID, "acme-corp:stolen")
	owner := liveDevice(t, port, revokeClientID+":b", "acme-corp:rotated")

	api.revoke("stolen")
	// 🔑 THE HOLE THE KICK CLOSES, SHOWN FIRST: the revocation is committed and the thief's
	// session is untouched by it. An event on it would carry no credential to re-check.
	time.Sleep(time.Second)
	require.True(t, thief.IsConnectionOpen(), "nothing but the kick ends an open session; if this fails the test proves nothing")

	require.Equal(t, 2, kickDevice(t, srv), "both of the device's sessions are ended")
	require.Eventually(t, func() bool { return !thief.IsConnectionOpen() }, 10*time.Second, 25*time.Millisecond,
		"the revoked credential's session was not ended")
	require.Eventually(t, owner.IsConnectionOpen, 10*time.Second, 25*time.Millisecond,
		"the session on the still-valid credential must come back after the kick")
	// The thief keeps trying (auto-reconnect every 200 ms) and the callout keeps refusing.
	require.Never(t, thief.IsConnectionOpen, 2*time.Second, 50*time.Millisecond,
		"the revoked credential reconnected: the callout did not re-run against it")
}

// The clamp, on the wire: the broker itself closes a session whose JWT the callout clamped to
// the credential's expiry, with no kick, and the reconnect is refused because the credential
// now has no time left. The control is the same device on a credential with no expiry.
func TestTheBrokerEndsASessionAtItsCredentialsExpiry(t *testing.T) {
	api := &revocableApi{revoked: map[string]bool{}, expiries: map[string]time.Time{}}
	_, port := startRevocationBroker(t, api)
	// The expiry is set only once the broker is up, so a slow start (the race detector)
	// cannot eat the credential's remaining life before the device connects.
	api.mu.Lock()
	api.expiries["short"] = time.Now().Add(4 * time.Second)
	api.mu.Unlock()
	expiring := liveDevice(t, port, revokeClientID, "acme-corp:short")
	lasting := liveDevice(t, port, revokeClientID+":b", "acme-corp:lasting")

	require.Eventually(t, func() bool { return !expiring.IsConnectionOpen() }, 10*time.Second, 50*time.Millisecond,
		"the session outlived its credential's expiry")
	require.Never(t, expiring.IsConnectionOpen, 2*time.Second, 50*time.Millisecond,
		"an expired credential reconnected")
	require.True(t, lasting.IsConnectionOpen(), "a credential with no expiry keeps its session")
}

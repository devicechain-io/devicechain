// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/natsauth"
	dctest "github.com/devicechain-io/dc-microservice/test"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
)

// The MQTT client-id pin, against a REAL broker.
//
// # 🔴 What the unit tests cannot see, and why it is the whole risk
//
// callout_test.go drives authorize() directly, so it proves the comparison denies
// what it should. It proves NOTHING about the value being compared. The required
// id is checked against req.ClientInformation.MQTT, which nats-server fills in from
// its own client state — and if that field arrived empty for an MQTT connect, every
// single device on the platform would be refused, while every unit test in this
// package stayed green because they all set the field themselves.
//
// That is not a hypothetical shape of mistake: a test that constructs the input it
// then asserts on is measuring its own fixture. So this one hands the value to a
// real nats-server over a real MQTT CONNECT and lets the real responder read it
// back, which is the only arrangement in which "the server populates this field
// before it consults the callout" is a fact rather than a reading of its source.
//
// The negative control is in the same test and is the reason it can fail: the
// correct id must CONNECT. Without it, a responder that refused everything — the
// exact failure an empty ClientInformation.MQTT would cause — would satisfy every
// refusal assertion perfectly.

// startCalloutServer runs an embedded nats-server configured exactly the way the
// deployed one is (deploy/opentofu/modules/nats): an APP account holding the static
// service login, and an auth_callout delegating everything else. It returns the
// server and the gateway's MQTT port.
//
// authTimeout is the authorization block's timeout — how long the server waits for a
// callout reply before refusing the connection. It is a parameter rather than a
// constant because callout_race_test.go needs a window it can outlast without adding
// five seconds to the suite.
func startCalloutServer(t *testing.T, creds natsauth.Credentials, authTimeout time.Duration) (*natsserver.Server, int) {
	t.Helper()

	mqttPort := dctest.FreeTCPPort(t)

	// A config FILE rather than a hand-built Options struct, because the shape under
	// test is the deployed one. Options assembled in Go can express an arrangement
	// the config parser would never produce, and the question here is whether the
	// production arrangement carries the client id — not whether some arrangement does.
	dir := t.TempDir()
	conf := filepath.Join(dir, "nats.conf")
	body := fmt.Sprintf(`
		port: -1
		server_name: "callout-e2e"
		jetstream { store_dir: %q }
		mqtt { host: "127.0.0.1", port: %d }
		system_account: SYS
		accounts {
		  APP { jetstream: enabled, users: [{user: %q, password: %q}] }
		  SYS {}
		}
		authorization {
		  timeout: %v
		  auth_callout { issuer: %q, auth_users: [%q], account: "APP" }
		}
	`, filepath.Join(dir, "js"), mqttPort,
		natsauth.ServiceUser, creds.ServicePasswordBcrypt,
		authTimeout.Seconds(),
		creds.IssuerPublic, natsauth.ServiceUser)
	if err := os.WriteFile(conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	opts, err := natsserver.ProcessConfigFile(conf)
	if err != nil {
		t.Fatalf("process nats config: %v", err)
	}
	opts.NoLog, opts.NoSigs = true, true
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("new nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(20 * time.Second) {
		t.Fatal("embedded nats server not ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv, mqttPort
}

// startCalloutBroker is startCalloutServer plus the service connection the responder
// answers on, at the deployed authorization timeout.
func startCalloutBroker(t *testing.T, creds natsauth.Credentials) (*nats.Conn, int) {
	t.Helper()
	srv, mqttPort := startCalloutServer(t, creds, 5*time.Second)

	// The responder's own connection presents the static service credential, which
	// auth_users exempts from the callout — otherwise it would have to answer the
	// request authorizing itself.
	nc, err := nats.Connect(srv.ClientURL(),
		nats.UserInfo(natsauth.ServiceUser, creds.ServicePassword))
	if err != nil {
		t.Fatalf("service connect: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc, mqttPort
}

// mqttConnectAsDevice attempts one MQTT CONNECT and reports whether the broker
// accepted it.
func mqttConnectAsDevice(t *testing.T, port int, clientID, username, password string) error {
	t.Helper()
	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", port))
	opts.SetClientID(clientID)
	opts.SetUsername(username)
	opts.SetPassword(password)
	// This Uplink owns the outcome: a retrying client would turn a refusal into a
	// timeout and lose which of the two happened.
	opts.SetConnectRetry(false)
	opts.SetAutoReconnect(false)
	opts.SetConnectTimeout(10 * time.Second)
	c := mqtt.NewClient(opts)
	tok := c.Connect()
	if !tok.WaitTimeout(15 * time.Second) {
		return fmt.Errorf("connect did not settle")
	}
	if err := tok.Error(); err != nil {
		return err
	}
	t.Cleanup(func() { c.Disconnect(100) })
	return nil
}

func TestCalloutPinsTheMqttClientIdAgainstARealBroker(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	nc, mqttPort := startCalloutBroker(t, creds)

	api := fakeAuthApi{authFn: func(context.Context, *model.PresentedCredential) (*model.Device, error) {
		d := &model.Device{}
		d.Token = "sensor-001"
		return d, nil
	}}
	r := NewCalloutResponder(nc, api, creds.IssuerSeed, "inst-1", nil)
	if err := r.Start(); err != nil {
		t.Fatalf("start responder: %v", err)
	}
	t.Cleanup(func() { _ = r.Stop() })

	// 🔑 THE NEGATIVE CONTROL, AND IT RUNS FIRST. Every assertion below it is a
	// refusal, and refusals are what a broken responder produces for free. If the
	// server did not hand the callout the client id, this is the line that fails —
	// which is exactly the fact the unit tests cannot establish.
	const requiredID = "inst-1:acme-corp:sensor-001"
	if err := mqttConnectAsDevice(t, mqttPort, requiredID, "acme-corp:dev1", "s3cret"); err != nil {
		t.Fatalf("the required client id %q was refused by a real broker (%v). If this is the "+
			"only failure here, the server did not populate the MQTT client id in the "+
			"authorization request and the callout is now refusing every device.", requiredID, err)
	}

	// 🔑 THE SUFFIXED FORM, HELD CONCURRENTLY WITH THE PLAIN ONE. This is the case
	// that decided the rule: requiring an EXACT id would give a device one session,
	// so a second connection would evict its first — and the connection above is
	// still open, so if the suffix were refused, or if the broker treated the two as
	// one session, this line is where it shows. That failure is invisible to a unit
	// test, which never has two live sessions to collide.
	if err := mqttConnectAsDevice(t, mqttPort, requiredID+":sub", "acme-corp:dev1", "s3cret"); err != nil {
		t.Fatalf("a device's own second session %q was refused (%v): firmware that publishes on "+
			"one connection and subscribes on another cannot connect", requiredID+":sub", err)
	}

	// And the id the platform does not issue is refused at CONNECT — before
	// nats-server looks a session up, so the takeover never happens rather than
	// being undone.
	// ONE refusal, not a table. By now the admission rule is proven (in
	// core/messaging, where a case is free) and the client id is proven to reach the
	// callout at all (above), so extra rows here buy nothing but a real MQTT CONNECT
	// each. What this single case still earns is that a denial reaches the WIRE — the
	// responder's refusal actually ends the connection rather than being swallowed.
	const anotherTenants = "inst-1:other-corp:sensor-001"
	if err := mqttConnectAsDevice(t, mqttPort, anotherTenants, "acme-corp:dev1", "s3cret"); err == nil {
		t.Fatalf("client id %q was accepted by the broker; the pin is not in force on the "+
			"real CONNECT path", anotherTenants)
	}
}

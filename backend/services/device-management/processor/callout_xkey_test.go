// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/nats-io/jwt/v2"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// The auth callout, ENCRYPTED, against the real responder and a real broker.
//
// Without auth_callout.xkey the authorization request (it carries the device's CONNECT password)
// crosses $SYS.REQ.USER.AUTH in plaintext, readable by any login in the APP account. These tests
// run device-management's own responder, over MQTT, and look at what an eavesdropper holding the
// shared service login can see; and at what happens when the responder and the broker disagree
// about encryption, which is the state a rolling upgrade passes through.

const (
	xkeyClientID = "inst-1:acme-corp:sensor-001"
	xkeyUsername = "acme-corp:dev1"
	xkeyPassword = "s3cret"
)

type xkeyHarness struct {
	srv       *natsserver.Server
	mqttPort  int
	responder *CalloutResponder
	seen      func() [][]byte
}

// startXKeyHarness runs a broker (encrypting to creds.XKeyPublic when brokerEncrypts) and a
// responder (holding the xkey seed when responderHoldsSeed), with an eavesdropper subscribed as the
// shared service login. counter, when non-nil, counts plaintext requests the responder answered.
func startXKeyHarness(t *testing.T, creds natsauth.Credentials, brokerEncrypts, responderHoldsSeed bool, counter prometheus.Counter) *xkeyHarness {
	t.Helper()
	xkeyPublic := ""
	if brokerEncrypts {
		xkeyPublic = creds.XKeyPublic
	}
	srv, mqttPort := startCalloutServerEncrypted(t, creds, 5*time.Second, xkeyPublic)
	connect := func() *nats.Conn {
		nc, err := nats.Connect(srv.ClientURL(), nats.UserInfo(natsauth.ServiceUser, creds.ServicePassword))
		if err != nil {
			t.Fatalf("service connect: %v", err)
		}
		t.Cleanup(nc.Close)
		return nc
	}

	// The eavesdropper, before anything connects.
	var mu sync.Mutex
	var payloads [][]byte
	listener := connect()
	sub, err := listener.Subscribe(AuthCalloutSubject, func(m *nats.Msg) {
		mu.Lock()
		defer mu.Unlock()
		payloads = append(payloads, append([]byte(nil), m.Data...))
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Flush(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	api := fakeAuthApi{secret: xkeyPassword, authFn: func(context.Context, *model.PresentedCredential) (*model.Device, error) {
		d := &model.Device{}
		d.Token = "sensor-001"
		return d, nil
	}}
	r := mustResponder(t, connect(), api, testChecker(t, credentialtest.NewStore()), creds.IssuerSeed, nil)
	if responderHoldsSeed {
		if _, err := r.WithRequestEncryption(creds.XKeySeed, creds.XKeyPublic); err != nil {
			t.Fatal(err)
		}
	}
	if counter != nil {
		r.Configure(0, 0, 0, &CalloutMetrics{
			InFlight:          prometheus.NewGauge(prometheus.GaugeOpts{Name: "in_flight_test"}),
			RefusedBusy:       prometheus.NewCounter(prometheus.CounterOpts{Name: "refused_busy_test_total"}),
			PlaintextRequests: counter,
		})
	}
	if err := r.Start(); err != nil {
		t.Fatalf("start responder: %v", err)
	}
	t.Cleanup(func() { _ = r.Stop() })
	return &xkeyHarness{srv: srv, mqttPort: mqttPort, responder: r, seen: func() [][]byte {
		mu.Lock()
		defer mu.Unlock()
		return append([][]byte(nil), payloads...)
	}}
}

// readable reports whether a payload the eavesdropper captured decodes as an authorization request,
// and the password in it when it does.
func readable(payload []byte) (string, bool) {
	rc, err := jwt.DecodeAuthorizationRequestClaims(string(payload))
	if err != nil {
		return "", false
	}
	return rc.AuthorizationRequest.ConnectOptions.Password, true
}

func TestAnEncryptedCalloutStillAuthenticatesDevicesAndHidesTheirCredentials(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		name := "unencrypted (negative control)"
		if encrypted {
			name = "encrypted"
		}
		t.Run(name, func(t *testing.T) {
			creds, err := natsauth.GenerateCredentials()
			if err != nil {
				t.Fatal(err)
			}
			h := startXKeyHarness(t, creds, encrypted, true, nil)

			// The control inside the test: the right credential connects and the wrong one is
			// refused THROUGH the real responder. Without it "unreadable" would be satisfied by a
			// broker that sent nothing, and a refusal by one that refused everything.
			if err := mqttConnectAsDevice(t, h.mqttPort, xkeyClientID, xkeyUsername, xkeyPassword); err != nil {
				t.Fatalf("the right credential was refused (%v)", err)
			}
			if err := mqttConnectAsDevice(t, h.mqttPort, xkeyClientID+":bad", xkeyUsername, "wrong"); err == nil {
				t.Fatal("a wrong credential connected")
			}

			deadline := time.Now().Add(5 * time.Second)
			for len(h.seen()) < 2 && time.Now().Before(deadline) {
				time.Sleep(25 * time.Millisecond)
			}
			captured := h.seen()
			if len(captured) < 2 {
				t.Fatalf("the eavesdropper was shown %d requests, want both connect attempts", len(captured))
			}
			var sawPassword bool
			for _, p := range captured {
				pw, ok := readable(p)
				if encrypted {
					if ok || pw != "" {
						t.Fatal("an eavesdropper holding the shared service login read a device password off the callout subject")
					}
					continue
				}
				sawPassword = sawPassword || pw == xkeyPassword
			}
			if !encrypted && !sawPassword {
				t.Fatal("negative control: with the xkey OFF the eavesdropper must see the password, " +
					"or the encrypted run proves nothing")
			}
		})
	}
}

// 🔑 THE ROLLING UPGRADE'S FIRST HALF: the responder holds the xkey seed while the broker does not
// encrypt yet. It must keep serving, and say that it is answering plaintext.
func TestAResponderWithTheSeedStillServesABrokerThatDoesNotEncryptYet(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "plaintext_test_total"})
	h := startXKeyHarness(t, creds, false, true, counter)

	if err := mqttConnectAsDevice(t, h.mqttPort, xkeyClientID, xkeyUsername, xkeyPassword); err != nil {
		t.Fatalf("the device was refused by a new responder on a broker that has not started encrypting: %v", err)
	}
	if got := testutil.ToFloat64(counter); got < 1 {
		t.Errorf("the plaintext request was answered without being counted (%v)", got)
	}
}

// And the other half, the one the ordering exists to avoid: a broker that encrypts and a responder
// that cannot read it. Every device is refused. This is what fail closed looks like, and it is
// why the broker is told to encrypt only after the responder can read it.
func TestAResponderWithoutTheSeedServesNoOneOnAnEncryptedBroker(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	h := startXKeyHarness(t, creds, true, false, nil)
	if err := mqttConnectAsDevice(t, h.mqttPort, xkeyClientID, xkeyUsername, xkeyPassword); err == nil {
		t.Fatal("a device connected through a responder that cannot have read its request")
	}
}

func TestTheResponderRefusesASeedThatIsNotTheBrokersKey(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	otherSeed, _, err := natsauth.GenerateXKey()
	if err != nil {
		t.Fatal(err)
	}
	r := mustResponder(t, nil, fakeAuthApi{}, testChecker(t, credentialtest.NewStore()), creds.IssuerSeed, nil)

	if _, err := r.WithRequestEncryption(otherSeed, creds.XKeyPublic); !errors.Is(err, errXKeyMismatch) {
		t.Errorf("a seed from another mint was accepted for the broker's key: %v", err)
	}
	if _, err := r.WithRequestEncryption(creds.IssuerSeed, ""); !errors.Is(err, natsauth.ErrNotAnXKeySeed) {
		t.Errorf("the issuer's account seed was accepted as a curve seed: %v", err)
	}
	if _, err := r.WithRequestEncryption(creds.XKeySeed, creds.XKeyPublic); err != nil {
		t.Errorf("the matching seed was refused: %v", err)
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/nats-io/jwt/v2"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	dctest "github.com/devicechain-io/dc-microservice/test"
)

// What the callout subject carries, and who can read it.
//
// A device's CONNECT password rides inside the authorization request the broker publishes on
// $SYS.REQ.USER.AUTH in the APP account. Every platform service holds a login in that account, so
// without auth_callout.xkey any of them can read the request, either by subscribing to the subject
// or by capturing it into a JetStream stream and reading it back later. These tests run an
// embedded nats-server (the version go.mod requires) configured the way the infrastructure module
// configures it, and look at what an eavesdropper holding the shared service login actually gets.
//
// Every claim has its negative control in the same table: the same body, run against a broker
// with the xkey OFF, must show the password. Without that, "unreadable" could just mean "the
// eavesdropper never received anything".

const (
	eavesdropPassword = "hunter2-device-secret"
	eavesdropUser     = "acme:dev-1"
)

func TestXKeyHeaderMatchesTheServer(t *testing.T) {
	require.Equal(t, natsserver.AuthRequestXKeyHeader, AuthRequestXKeyHeader,
		"the header the responder looks for must be the one the server stamps")
}

func TestGenerateXKeyIsACurveKeyPair(t *testing.T) {
	seed, pub, err := GenerateXKey()
	require.NoError(t, err)
	require.True(t, nkeys.IsValidPublicCurveKey(pub), "public key %q", pub)
	got, err := XKeyPublicFromSeed(seed)
	require.NoError(t, err)
	require.Equal(t, pub, got)

	// An ACCOUNT seed is a valid nkey and must not pass for a curve seed: it would fail later and
	// obscurely, inside the box, on the first request.
	creds, err := GenerateCredentials()
	require.NoError(t, err)
	_, err = XKeyPublicFromSeed(creds.IssuerSeed)
	require.ErrorIs(t, err, ErrNotAnXKeySeed)
	_, err = NewXKeyCodec(creds.IssuerSeed)
	require.ErrorIs(t, err, ErrNotAnXKeySeed)
	_, err = XKeyPublicFromSeed("")
	require.ErrorIs(t, err, ErrNotAnXKeySeed)
}

func TestWithXKeyIntroducesOnceAndNeverReplaces(t *testing.T) {
	base, err := GenerateCredentials()
	require.NoError(t, err)
	require.NotEmpty(t, base.XKeySeed)
	require.NotEmpty(t, base.XKeyPublic)

	// Reused: the same seed and the key derived from it, never a fresh one.
	again, err := base.WithXKey(base.XKeySeed)
	require.NoError(t, err)
	require.Equal(t, base.XKeySeed, again.XKeySeed)
	require.Equal(t, base.XKeyPublic, again.XKeyPublic)

	// Introduced: an absent seed is minted, and only an absent one.
	introduced, err := Credentials{IssuerSeed: base.IssuerSeed}.WithXKey("")
	require.NoError(t, err)
	require.NotEmpty(t, introduced.XKeySeed)
	require.NotEqual(t, base.XKeySeed, introduced.XKeySeed)

	// A seed that is PRESENT and wrong is an error, not a reason to mint: minting over it would
	// hide a corrupted store and swap the key out from under a broker already using it.
	_, err = base.WithXKey(base.IssuerSeed)
	require.ErrorIs(t, err, ErrNotAnXKeySeed)
	_, err = base.WithXKey("garbage")
	require.Error(t, err)
}

func TestCredentialsFromDeployedLeavesTheXKeyToTheCaller(t *testing.T) {
	// CredentialsFromDeployed rebuilds what the running instance holds; the xkey has its own
	// reuse-or-introduce step (WithXKey) so a deployment without one is not an error there.
	minted, err := GenerateCredentials()
	require.NoError(t, err)
	got, err := CredentialsFromDeployed(minted.IssuerSeed, minted.ServicePassword, minted.SysPassword, DeployedHashes{})
	require.NoError(t, err)
	require.Empty(t, got.XKeySeed)
}

// calloutBroker starts a broker with an auth callout, optionally encrypted. The shared service
// login is exempt from the callout and is what the responder AND the eavesdropper hold.
func calloutBroker(t *testing.T, issuerPublic, xkeyPublic string) (*natsserver.Server, int) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("svc"), bcrypt.MinCost)
	require.NoError(t, err)
	xkey := ""
	if xkeyPublic != "" {
		xkey = fmt.Sprintf("xkey: %q,", xkeyPublic)
	}
	dir := dctest.JetStreamStoreDir(t)
	conf := filepath.Join(dir, "nats.conf")
	require.NoError(t, os.WriteFile(conf, []byte(fmt.Sprintf(`
port: -1
server_name: "xkey-test"
jetstream { store_dir: %q }
mqtt { host: "127.0.0.1", port: -1 }
system_account: SYS
accounts {
  APP { jetstream: enabled, users: [{user: %q, password: %q}] }
  SYS {}
}
authorization {
  timeout: 5
  auth_callout { issuer: %q, %s auth_users: [%q], account: "APP" }
}
`, filepath.Join(dir, "js"), ServiceUser, string(hash), issuerPublic, xkey, ServiceUser)), 0o600))
	opts, err := natsserver.ProcessConfigFile(conf)
	require.NoError(t, err)
	opts.NoLog, opts.NoSigs = true, true
	srv, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second), "embedded broker not ready")
	t.Cleanup(srv.Shutdown)
	return srv, dctest.MQTTPort(t, srv)
}

func serviceConn(t *testing.T, srv *natsserver.Server) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(srv.ClientURL(), nats.UserInfo(ServiceUser, "svc"))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// startResponder answers authorization requests the way device-management does, minus the
// credential store: the one right password is granted, every other is refused. codec is nil for
// an unencrypted callout.
func startResponder(t *testing.T, srv *natsserver.Server, creds Credentials, codec *XKeyCodec) {
	t.Helper()
	nc := serviceConn(t, srv)
	sub, err := nc.QueueSubscribe("$SYS.REQ.USER.AUTH", "responders", func(msg *nats.Msg) {
		data, serverKey := msg.Data, msg.Header.Get(AuthRequestXKeyHeader)
		if serverKey != "" {
			var err error
			if data, err = codec.Open(msg.Data, serverKey); err != nil {
				return
			}
		}
		rc, err := jwt.DecodeAuthorizationRequestClaims(string(data))
		if err != nil {
			return
		}
		req := rc.AuthorizationRequest
		userJWT, errMsg := "", "device authentication failed"
		if req.ConnectOptions.Password == eavesdropPassword {
			userJWT, err = SignDeviceUserJWT(creds.IssuerSeed, req.UserNkey, "inst-1", "acme", "dev-1", time.Now(), time.Hour)
			if err != nil {
				return
			}
			errMsg = ""
		}
		resp, err := EncodeAuthResponse(creds.IssuerSeed, req.Server.ID, req.UserNkey, userJWT, errMsg)
		if err != nil {
			return
		}
		out := []byte(resp)
		if serverKey != "" {
			if out, err = codec.Seal(out, serverKey); err != nil {
				return
			}
		}
		_ = msg.Respond(out)
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	t.Cleanup(func() { _ = sub.Unsubscribe() })
}

func deviceConnect(t *testing.T, port int, password string) error {
	t.Helper()
	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", port))
	opts.SetClientID("inst-1:acme:dev-1")
	opts.SetUsername(eavesdropUser)
	opts.SetPassword(password)
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

// captured is one message an eavesdropper saw on the callout subject.
type captured struct {
	data      []byte
	serverKey string
}

// plaintextPassword returns the device's password if the captured payload is a readable
// authorization request, and "" if it is not.
func (c captured) plaintextPassword() string {
	rc, err := jwt.DecodeAuthorizationRequestClaims(string(c.data))
	if err != nil {
		return ""
	}
	return rc.AuthorizationRequest.ConnectOptions.Password
}

// eavesdrop subscribes to the callout subject as the shared service login.
func eavesdrop(t *testing.T, srv *natsserver.Server) func() []captured {
	t.Helper()
	nc := serviceConn(t, srv)
	var mu sync.Mutex
	var seen []captured
	sub, err := nc.Subscribe("$SYS.REQ.USER.AUTH", func(msg *nats.Msg) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, captured{data: append([]byte(nil), msg.Data...), serverKey: msg.Header.Get(AuthRequestXKeyHeader)})
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return func() []captured {
		mu.Lock()
		defer mu.Unlock()
		return append([]captured(nil), seen...)
	}
}

// captureCalloutStream is the stream variant: the eavesdropper does not need to be listening when a
// device connects. It creates a stream over the subject (no_ack, so the requests' reply subject
// does not make the stream answer them) and reads what was persisted back out.
func captureCalloutStream(t *testing.T, srv *natsserver.Server) func() []captured {
	t.Helper()
	js, err := serviceConn(t, srv).JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{
		Name: "callout_capture", Subjects: []string{"$SYS.REQ.USER.AUTH"}, NoAck: true, Storage: nats.MemoryStorage,
	})
	require.NoError(t, err, "a login in the APP account can create a stream over the callout subject; "+
		"that is why a subscribe deny does not protect it and the payload itself must be encrypted")
	return func() []captured {
		var out []captured
		for seq := uint64(1); ; seq++ {
			m, err := js.GetMsg("callout_capture", seq)
			if err != nil {
				return out
			}
			out = append(out, captured{data: m.Data, serverKey: m.Header.Get(AuthRequestXKeyHeader)})
		}
	}
}

func TestACalloutEavesdropperReadsCiphertextOnlyWhenTheXKeyIsOn(t *testing.T) {
	for _, tc := range []struct {
		name string
		look func(*testing.T, *natsserver.Server) func() []captured
	}{
		{"subscribed to the callout subject", eavesdrop},
		{"captured the callout subject into a stream", captureCalloutStream},
	} {
		for _, xkeyOn := range []bool{false, true} {
			name := fmt.Sprintf("%s/xkey=%v", tc.name, xkeyOn)
			t.Run(name, func(t *testing.T) {
				creds, err := GenerateCredentials()
				require.NoError(t, err)
				xkeyPublic := ""
				var codec *XKeyCodec
				if xkeyOn {
					xkeyPublic = creds.XKeyPublic
					codec, err = NewXKeyCodec(creds.XKeySeed)
					require.NoError(t, err)
				}
				srv, mqttPort := calloutBroker(t, creds.IssuerPublic, xkeyPublic)
				seen := tc.look(t, srv)
				startResponder(t, srv, creds, codec)

				// The control inside the test: the right credential connects, the wrong one is
				// refused. A responder that refused everything would otherwise satisfy every
				// refusal below, and an eavesdropper that was never sent anything would satisfy
				// "unreadable".
				require.NoError(t, deviceConnect(t, mqttPort, eavesdropPassword), "the right credential must connect")
				require.Error(t, deviceConnect(t, mqttPort, "not-the-password"), "a wrong credential must be refused")

				var got []captured
				require.Eventually(t, func() bool { got = seen(); return len(got) >= 2 }, 5*time.Second, 50*time.Millisecond,
					"the eavesdropper must have been shown both connect attempts")

				readable := 0
				sawRight := false
				for _, c := range got {
					pw := c.plaintextPassword()
					if xkeyOn {
						require.Empty(t, pw, "an eavesdropper read a device password off the callout subject")
						require.NotEmpty(t, c.serverKey, "an encrypted request names the server key that sealed it")
						require.False(t, bytes.HasPrefix(c.data, []byte("eyJ")), "the payload is still a bare JWT")
						require.False(t, bytes.Contains(c.data, []byte(eavesdropPassword)))
						// And the eavesdropper's own curve key cannot open it either.
						other, _, err := GenerateXKey()
						require.NoError(t, err)
						oc, err := NewXKeyCodec(other)
						require.NoError(t, err)
						_, err = oc.Open(c.data, c.serverKey)
						require.Error(t, err)
						continue
					}
					if pw != "" {
						readable++
					}
					sawRight = sawRight || pw == eavesdropPassword
				}
				if !xkeyOn {
					require.NotZero(t, readable, "negative control: with the xkey OFF the same eavesdrop must read a device "+
						"password, or the encrypted run proves nothing")
					require.True(t, sawRight, "negative control: the plaintext callout shows the right password")
				}
			})
		}
	}
}

func TestACalloutResponderWithoutTheSeedCannotServeAnEncryptedBroker(t *testing.T) {
	// The fail-closed shape this change relies on: a responder that cannot open the requests
	// gets no device in. (device-management additionally refuses to START in that state.)
	creds, err := GenerateCredentials()
	require.NoError(t, err)
	srv, mqttPort := calloutBroker(t, creds.IssuerPublic, creds.XKeyPublic)

	otherSeed, _, err := GenerateXKey()
	require.NoError(t, err)
	wrong, err := NewXKeyCodec(otherSeed)
	require.NoError(t, err)
	startResponder(t, srv, creds, wrong)

	require.Error(t, deviceConnect(t, mqttPort, eavesdropPassword),
		"a responder holding the wrong curve seed must not authorize anyone")
}

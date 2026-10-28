// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// pinnedFixture is the real-broker x509 fixture with an issuer pin over a real system-account
// connection (the dc_sys login, under the real SysPublishAllow / SysSubscribeAllow lists the
// fixture's broker is configured with).
type pinnedFixture struct {
	*x509Fixture
	sys       *nats.Conn
	issuerPin *IssuerPin
}

func newPinnedFixture(t *testing.T) *pinnedFixture {
	t.Helper()
	f := newX509Fixture(t)
	ms := &core.Microservice{InstanceId: "inst-1", FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	sys, err := nats.Connect(f.srv.ClientURL(), nats.UserInfo(natsauth.SysUser, "sys"))
	require.NoError(t, err)
	t.Cleanup(sys.Close)
	pf := &pinnedFixture{x509Fixture: f, sys: sys}
	pin, err := NewIssuerPin(IssuerPinConfig{
		Gather:    NewSystemGather(sys, 2*time.Second),
		Connected: sys.IsConnected,
		// Small, so the forgeries below each find the pin free to re-ping and are counted as
		// forgeries (issuer_unpinned); the rate-limited case has its own subtest.
		MinRefreshInterval: time.Millisecond,
		Metrics:            NewIssuerPinMetrics(ms),
	})
	require.NoError(t, err)
	pf.issuerPin = pin
	f.resp.WithIssuerPin(pin)
	pin.Start(context.Background())
	t.Cleanup(pin.Stop)
	return pf
}

// forgedRequest hands the responder an authorization request for a certificate connect that
// carries a VALID chain for a registered device, signed by signer and naming serverID as its
// sender, and returns whether it granted. Driven through handle with the real reply path.
//
// It is handed to the responder directly because a live broker never lets it get there: the
// publish-deny on the callout subject (TestNoClientInTheCalloutAccountCanPublishARequest)
// refuses the publish. The pin is the second defence, for the day that deny is wrong, so this
// is the case it exists for: a request that is otherwise perfect.
func (f *x509Fixture) forgedRequest(t *testing.T, signer nkeys.KeyPair, serverID string, device string, leaf *testCert) bool {
	t.Helper()
	req := testRequestFromClient(t, certUser(), "", clientIDOf(device))
	req.TLS = &jwt.ClientTLS{Certs: pemsOf(leaf)}
	req.Server.ID = serverID
	return f.handleToken(t, signedBy(t, req, signer))
}

// signedBy encodes req as a request JWT signed by signer.
func signedBy(t *testing.T, req jwt.AuthorizationRequest, signer nkeys.KeyPair) string {
	t.Helper()
	claims := jwt.NewAuthorizationRequestClaims(req.UserNkey)
	claims.AuthorizationRequest = req
	claims.Expires = time.Now().Add(time.Minute).Unix()
	tok, err := claims.Encode(signer)
	require.NoError(t, err)
	return tok
}

// resignedBy swaps a request token's issuer for signer's public key and signs it with signer:
// the one thing jwt's encoder will not do, which is what a forger who is not a server would send.
func resignedBy(t *testing.T, tok string, signer nkeys.KeyPair) string {
	t.Helper()
	parts := strings.Split(tok, ".")
	require.Len(t, parts, 3)
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(raw, &claims))
	pub, err := signer.PublicKey()
	require.NoError(t, err)
	claims["iss"] = pub
	raw, err = json.Marshal(claims)
	require.NoError(t, err)
	signing := parts[0] + "." + base64.RawURLEncoding.EncodeToString(raw)
	sig, err := signer.Sign([]byte(signing))
	require.NoError(t, err)
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// admit are the callout_x509_total results that mean a session was granted.
var admit = []string{x509ResultOK, x509ResultJitCreated, x509ResultJitAdopted, x509ResultJitRenewed, x509ResultJitReplayed}

// handleToken runs one request JWT through the responder's handler and reports whether it was granted.
func (f *x509Fixture) handleToken(t *testing.T, tok string) bool {
	t.Helper()
	nc := f.resp.conn
	msg := &nats.Msg{Subject: AuthCalloutSubject, Reply: nc.NewRespInbox(), Header: nats.Header{}, Data: []byte(tok)}
	sub, err := nc.SubscribeSync(msg.Reply)
	require.NoError(t, err)
	defer sub.Unsubscribe()
	require.NoError(t, nc.Flush())
	f.resp.handle(msg)
	out, err := sub.NextMsg(2 * time.Second)
	if err != nil {
		return false // dropped without an answer: the broker times the connect out
	}
	resp, err := jwt.DecodeAuthorizationResponseClaims(string(out.Data))
	require.NoError(t, err)
	return resp.Jwt != "" && resp.Error == ""
}

// 🔑 THE PIN, END TO END ON A REAL BROKER. A genuine certificate connect (the broker's own request,
// signed by its own server key) is granted; a request forged by a publisher that is not a server,
// by one that borrows a live server's id without its key, and by a server-shaped key that is not
// live, is refused. Control: with the pin removed, the same forgery is granted, which is what
// proves the refusal comes from the pin and not from something else in the request.
func TestIssuerPinOnARealBroker(t *testing.T) {
	f := newPinnedFixture(t)
	root := newTestCert(t, nil, certOpts{cn: "Maker Root", ca: true})
	f.anchor("maker", root)
	f.device("sensor-001")
	leaf := newTestCert(t, root, certOpts{cn: "SN-0001"})
	f.pin("c-1", "sensor-001", "maker", leaf)

	t.Run("the premise: the live server set holds the server id the broker signs its requests with", func(t *testing.T) {
		require.Contains(t, f.issuerPin.Members(), f.srv.ID())
	})

	t.Run("a genuine certificate connect is granted", func(t *testing.T) {
		before := f.counts()
		c, err := f.connect(clientIDOf("sensor-001"), certUser(), "", *presenting(leaf))
		require.NoError(t, err, "the broker's own request, signed by its own key, was refused by the pin")
		require.True(t, c.IsConnectionOpen())
		f.moved(before, x509ResultOK)
	})

	user, err := nkeys.CreateUser()
	require.NoError(t, err)
	stranger, err := nkeys.CreateServer()
	require.NoError(t, err)
	strangerID, err := stranger.PublicKey()
	require.NoError(t, err)

	t.Run("a forged request signed by a non-server key is refused", func(t *testing.T) {
		// jwt refuses to encode a request with a non-server key, so the token is built by hand:
		// a real server-signed token with its issuer swapped for a user nkey and re-signed.
		userID, _ := user.PublicKey()
		req := testRequestFromClient(t, certUser(), "", clientIDOf("sensor-001"))
		req.TLS = &jwt.ClientTLS{Certs: pemsOf(leaf)}
		req.Server.ID = userID
		tok := resignedBy(t, signedBy(t, req, stranger), user)
		_, decodeErr := jwt.DecodeAuthorizationRequestClaims(tok)
		require.Error(t, decodeErr, "jwt now decodes a request with a non-server issuer: the pin is its only guard")
		before := f.counts()
		require.False(t, f.handleToken(t, tok), "a request signed by a user nkey was granted")
		after := f.counts()
		for _, r := range admit {
			require.Equal(t, before[r], after[r], "an admit result moved for a forged request: %s", r)
		}
	})

	t.Run("a forged request borrowing a live server's id but signed by another key is refused", func(t *testing.T) {
		before := f.counts()
		require.False(t, f.forgedRequest(t, stranger, f.srv.ID(), "sensor-001", leaf),
			"naming a live server as sender, without its key, was granted")
		f.moved(before, x509ResultIssuerUnpinned)
	})

	t.Run("a server-shaped key that is not a live server is refused", func(t *testing.T) {
		before := f.counts()
		require.False(t, f.forgedRequest(t, stranger, strangerID, "sensor-001", leaf))
		f.moved(before, x509ResultIssuerUnpinned)
	})

	t.Run("a forgery inside the re-ping interval is refused as pending, not as unpinned", func(t *testing.T) {
		f.issuerPin.minGap = time.Hour
		defer func() { f.issuerPin.minGap = time.Millisecond }()
		// Spend the one allowed ping, then the next unknown issuer is inside the interval.
		f.issuerPin.refresh(context.Background(), true)
		before := f.counts()
		require.False(t, f.forgedRequest(t, stranger, strangerID, "sensor-001", leaf))
		f.moved(before, x509ResultIssuerPending)
	})

	t.Run("negative control: with the pin removed the same forgery is granted", func(t *testing.T) {
		f.resp.WithIssuerPin(nil)
		defer f.resp.WithIssuerPin(f.issuerPin)
		require.True(t, f.forgedRequest(t, stranger, strangerID, "sensor-001", leaf),
			"the control did not grant: the forgeries above were refused by something other than the pin")
	})

	t.Run("a password connect is not pinned", func(t *testing.T) {
		// The pin is for the grant that needs no secret. A token or password connect from an
		// unpinned issuer goes through its own credential check, unchanged: a VALID credential
		// is granted, so the test fails if the pin ever reaches this path.
		_, err := f.api.CreateDeviceCredential(f.ctx, &model.DeviceCredentialCreateRequest{
			Token: "tok-pin", DeviceToken: "sensor-001", CredentialType: string(model.CredentialAccessToken),
			CredentialId: "tok-pin-0123456789", Enabled: true})
		require.NoError(t, err)
		req := testRequestFromClient(t, x509Tenant+":tok-pin-0123456789", "", clientIDOf("sensor-001"))
		before := f.counts()
		jwtOut, why := f.resp.authorizeRequest(req, true, strangerID)
		require.Empty(t, why, "a valid token connect from an unpinned issuer was refused: the pin reached a non-certificate path")
		require.NotEmpty(t, jwtOut)
		require.Equal(t, before, f.counts(), "no certificate result may move for a token connect")
	})

	t.Run("with the system connection down every certificate connect is refused", func(t *testing.T) {
		f.sys.Close()
		before := f.counts()
		_, err := f.connect(clientIDOf("sensor-001"), certUser(), "", *presenting(leaf))
		require.Error(t, err, "a certificate connect was granted with the system connection down")
		f.moved(before, x509ResultPinUnavailable)
	})
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509sim

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eclipse/paho.mqtt.golang/packets"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/x509trust"
)

// seen is what a broker observed on one CONNECT.
type seen struct {
	username, password, clientID string
	chain                        []*x509.Certificate
}

// wireBroker is a TLS listener that REQUESTS a client certificate without verifying it (the
// platform broker does the same and leaves verification to its auth callout), reads the MQTT
// CONNECT and accepts it. It proves what the simulator PRESENTS, not what the platform decides.
func wireBroker(t *testing.T) (broker, serverCAFile string, out chan seen) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "broker"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	parsed, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	serverCAFile = filepath.Join(t.TempDir(), "server.pem")
	require.NoError(t, os.WriteFile(serverCAFile, CertPEM(parsed), 0o600))

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		ClientAuth:   tls.RequestClientCert, MinVersion: tls.VersionTLS12,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	out = make(chan seen, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				tc := c.(*tls.Conn)
				_ = tc.SetDeadline(time.Now().Add(10 * time.Second))
				if err := tc.Handshake(); err != nil {
					return
				}
				p, err := packets.ReadPacket(tc)
				if err != nil {
					return
				}
				cp, ok := p.(*packets.ConnectPacket)
				if !ok {
					return
				}
				out <- seen{cp.Username, string(cp.Password), cp.ClientIdentifier, tc.ConnectionState().PeerCertificates}
				ack := packets.NewControlPacket(packets.Connack).(*packets.ConnackPacket)
				_ = ack.Write(tc)
				_, _ = packets.ReadPacket(tc) // hold until the client disconnects
			}()
		}
	}()
	return "ssl://" + ln.Addr().String(), serverCAFile, out
}

func TestConnectPresentsCertificateUsernameAndClientID(t *testing.T) {
	broker, serverCA, out := wireBroker(t)
	st, err := OpenOrMint(t.TempDir(), Options{Prefix: "w-", Pinned: 1, JIT: 1})
	require.NoError(t, err)

	fleet, err := Connect(context.Background(), ConnectConfig{
		Broker: broker, InstanceId: "dcinst", Tenant: "acme", ServerCAFile: serverCA,
	}, st, st.Manifest.Devices)
	require.NoError(t, err)
	t.Cleanup(fleet.Close)
	c, r := fleet.Summary(KindPinned)
	require.Equal(t, 1, c)
	require.Zero(t, r)
	c, _ = fleet.Summary(KindJIT)
	require.Equal(t, 1, c, "a JIT certificate is presented exactly like a pinned one")

	got := map[string]seen{}
	for i := 0; i < 2; i++ {
		select {
		case s := <-out:
			got[s.clientID] = s
		case <-time.After(10 * time.Second):
			t.Fatal("broker saw fewer than two connects")
		}
	}
	for _, e := range st.Manifest.Devices {
		want, err := messaging.DeviceClientID("dcinst", "acme", e.Token)
		require.NoError(t, err)
		s, ok := got[want]
		require.True(t, ok, "client id %s", want)
		require.Equal(t, "acme:@x509", s.username)
		require.Empty(t, s.password)
		require.Len(t, s.chain, 1)
		require.Equal(t, e.ExternalId, s.chain[0].Subject.CommonName)
		require.Equal(t, e.Fingerprint, x509trust.Fingerprint(s.chain[0]))
		_, err = x509trust.VerifyClientChain(st.CA.Cert, s.chain, time.Now())
		require.NoError(t, err, "what went over the wire must verify against the CA")
	}
}

func TestConnectRefusesAPlaintextBroker(t *testing.T) {
	st, err := OpenOrMint(t.TempDir(), Options{Pinned: 1})
	require.NoError(t, err)
	_, err = Connect(context.Background(), ConnectConfig{Broker: "tcp://127.0.0.1:1883", InstanceId: "i", Tenant: "t"}, st, st.Manifest.Devices)
	require.ErrorContains(t, err, "TLS scheme")
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509sim

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// Username is the MQTT username that selects the certificate path: "{tenant}:@x509". The '@' is
// outside the token grammar, so it can never collide with a credential id.
func Username(tenant string) string { return tenant + ":@x509" }

// ClientID is the MQTT client id a certificate device must present: the standard device client id
// for its token. The token is the one derived from the CN (DeviceToken), for pinned and JIT alike.
func ClientID(instanceId, tenant string, e Entry) (string, error) {
	return messaging.DeviceClientID(instanceId, tenant, e.Token)
}

// ConnectConfig addresses the broker.
type ConnectConfig struct {
	// Broker is the gateway, which must be a TLS scheme: ssl://host:port or tls://host:port.
	Broker     string
	InstanceId string
	Tenant     string
	// ServerCAFile is a PEM bundle that verifies the broker's certificate; empty uses the system
	// roots. ServerInsecure skips that verification (a local bring-up's self-signed gateway).
	ServerCAFile   string
	ServerInsecure bool
	// ServerName overrides the name the broker certificate is verified against.
	ServerName     string
	ConnectTimeout time.Duration
	KeepAlive      time.Duration
}

// Result is one device's connect outcome. An error is a result, not a failure: a JIT certificate
// is expected to be refused until the registration path exists.
type Result struct {
	Entry     Entry
	ClientID  string
	Connected bool
	Err       string
}

// Fleet is a set of connected (or refused) certificate devices.
type Fleet struct {
	mu      sync.Mutex
	clients []mqtt.Client
	Results []Result
}

// Connect dials one MQTT session per entry, each presenting its own client certificate, with
// username "{tenant}:@x509", no password and the derived client id.
func Connect(ctx context.Context, cfg ConnectConfig, st *State, entries []Entry) (*Fleet, error) {
	u, err := url.Parse(cfg.Broker)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("x509sim: broker %q is not a URL", cfg.Broker)
	}
	switch u.Scheme {
	case "ssl", "tls", "tcps", "wss":
	default:
		return nil, fmt.Errorf("x509sim: broker %q: a client certificate needs a TLS scheme (ssl://, tls://, wss://), not %q", cfg.Broker, u.Scheme)
	}
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	if cfg.KeepAlive == 0 {
		cfg.KeepAlive = 30 * time.Second
	}
	var roots *x509.CertPool
	if cfg.ServerCAFile != "" {
		b, err := os.ReadFile(cfg.ServerCAFile)
		if err != nil {
			return nil, err
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(b) {
			return nil, fmt.Errorf("x509sim: %s holds no certificate", cfg.ServerCAFile)
		}
	}
	serverName := cfg.ServerName
	if serverName == "" {
		serverName = u.Hostname()
	}

	f := &Fleet{}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return f, err
		}
		pair, err := st.Pair(e)
		if err != nil {
			return f, fmt.Errorf("device %q: %w", e.Token, err)
		}
		clientID, err := ClientID(cfg.InstanceId, cfg.Tenant, e)
		if err != nil {
			return f, err
		}
		opts := mqtt.NewClientOptions()
		opts.AddBroker(cfg.Broker)
		opts.SetClientID(clientID)
		opts.SetUsername(Username(cfg.Tenant))
		opts.SetCleanSession(true)
		opts.SetAutoReconnect(false)
		opts.SetConnectRetry(false)
		opts.SetConnectTimeout(cfg.ConnectTimeout)
		opts.SetKeepAlive(cfg.KeepAlive)
		opts.SetTLSConfig(&tls.Config{
			MinVersion:         tls.VersionTLS12,
			Certificates:       []tls.Certificate{pair},
			RootCAs:            roots,
			ServerName:         serverName,
			InsecureSkipVerify: cfg.ServerInsecure, //nolint:gosec // the operator's recorded trust decision for a self-signed gateway
		})
		c := mqtt.NewClient(opts)
		res := Result{Entry: e, ClientID: clientID}
		tok := c.Connect()
		if !tok.WaitTimeout(cfg.ConnectTimeout + time.Second) {
			res.Err = "connect timed out"
		} else if err := tok.Error(); err != nil {
			res.Err = err.Error()
		} else {
			res.Connected = true
			f.mu.Lock()
			f.clients = append(f.clients, c)
			f.mu.Unlock()
		}
		f.Results = append(f.Results, res)
	}
	return f, nil
}

// Close disconnects every open session.
func (f *Fleet) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.clients {
		c.Disconnect(250)
	}
	f.clients = nil
}

// Summary counts results by outcome for one kind.
func (f *Fleet) Summary(k Kind) (connected, refused int) {
	for _, r := range f.Results {
		if r.Entry.Kind != k {
			continue
		}
		if r.Connected {
			connected++
		} else {
			refused++
		}
	}
	return
}

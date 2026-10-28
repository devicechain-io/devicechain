// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package smoke checks a running DeviceChain broker from outside: that its MQTT listener requests
// client certificates, that a device presenting one completes TLS and its chain reaches the auth
// callout, and that a device presenting none — the token and password fleets — connects exactly as
// before.
//
// It is shared by the module's own tests, which run it against an embedded server started the way
// main starts one, and by cmd/brokersmoke, which runs it against the built image in a container
// (hack/broker-image-smoke.sh). Neither reaches the broker image: ko builds the module's root
// package only.
//
// The responder here is deliberately not device-management. It admits TokenUser (a token device,
// with or without a certificate) and X509User when — and only when — the request carries a client
// certificate, and records what each request carried. Whether a chain is TRUSTED is the callout's
// decision and is not tested here; what is tested is that the broker delivers it.
package smoke

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

const (
	tenant = "acme"
	// TokenUser is a token device's MQTT username.
	TokenUser = tenant + ":tok-1"
	// X509User is a certificate device's MQTT username.
	X509User    = tenant + ":@x509"
	serviceUser = "dc-smoke-service"
	appAccount  = "APP"
	calloutSubj = "$SYS.REQ.USER.AUTH"
	xkeyHeader  = "Nats-Server-Xkey"
	// BigChainBytes is the DER size of the large presented chain: the device path's ceiling.
	BigChainBytes = 16 * 1024
)

// Secrets is what Prepare writes for the clients and the responder (and the broker's config).
type Secrets struct {
	IssuerSeed      string `json:"issuerSeed"`
	IssuerPublic    string `json:"issuerPublic"`
	XKeySeed        string `json:"xkeySeed"`
	XKeyPublic      string `json:"xkeyPublic"`
	ServicePassword string `json:"servicePassword"`
}

// PrepareOptions shape the broker config Prepare writes.
type PrepareOptions struct {
	// ConfDir is the directory as the BROKER sees it, for the paths inside nats.conf. Empty means
	// the prepare directory itself, made absolute.
	ConfDir string
	// SANs are the names the broker's certificate is valid for. Empty means localhost and
	// 127.0.0.1.
	SANs []string
	// PidFile, StoreDir: pid_file and the JetStream store_dir. Empty means the chart's pid path
	// and /tmp/brokersmoke-js.
	PidFile, StoreDir string
	// ClientPort, MQTTPort, HTTPPort: 0 means 4222, 1883 and 8222; -1 asks for a random port.
	ClientPort, MQTTPort, HTTPPort int
	// MQTTClientCA gives the MQTT listener's tls block a ca_file (the server CA), as the chart
	// does with the instance CA. nats-server turns it into the listener's ClientCAs, which a
	// request-only listener would send as an acceptable-CA list. Off by default, which is the shape
	// the earlier tests ran against and why they could not see it.
	MQTTClientCA bool
}

func orDefault(v, d int) int {
	if v == 0 {
		return d
	}
	return v
}

// Prepare mints a server PKI, device certificates, the callout keys and a nats.conf into dir.
func Prepare(dir string, o PrepareOptions) error {
	if dir == "" {
		return errors.New("prepare: a directory is required")
	}
	if len(o.SANs) == 0 {
		o.SANs = []string{"localhost", "127.0.0.1"}
	}
	if o.PidFile == "" {
		o.PidFile = "/var/run/nats/nats.pid"
	}
	if o.StoreDir == "" {
		o.StoreDir = "/tmp/brokersmoke-js"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if o.ConfDir == "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		o.ConfDir = filepath.ToSlash(abs)
	}
	if err := mintPKI(dir, o.SANs, BigChainBytes); err != nil {
		return fmt.Errorf("mint PKI: %w", err)
	}
	sec, err := mintSecrets()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(sec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.json"), b, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "nats.conf"), []byte(natsConf(o, sec)), 0o644)
}

func mintSecrets() (Secrets, error) {
	akp, err := nkeys.CreateAccount()
	if err != nil {
		return Secrets{}, err
	}
	seed, _ := akp.Seed()
	pub, _ := akp.PublicKey()
	xkp, err := nkeys.CreateCurveKeys()
	if err != nil {
		return Secrets{}, err
	}
	xseed, _ := xkp.Seed()
	xpub, _ := xkp.PublicKey()
	pw := make([]byte, 16)
	if _, err := rand.Read(pw); err != nil {
		return Secrets{}, err
	}
	return Secrets{
		IssuerSeed: string(seed), IssuerPublic: pub,
		XKeySeed: string(xseed), XKeyPublic: xpub,
		ServicePassword: hex.EncodeToString(pw),
	}, nil
}

// natsConf is the deployed broker's shape, cut down: TLS on the client and MQTT listeners from the
// same certificate, an APP account with one static service login, an auth_callout (with xkey)
// delegating every other connect, and JetStream for MQTT QoS 1 sessions. The lame duck timings are
// the shortest the server accepts, so a lame duck shutdown can be watched in a test.
func natsConf(o PrepareOptions, sec Secrets) string {
	p := func(name string) string { return o.ConfDir + "/" + name }
	return fmt.Sprintf(`server_name: brokersmoke
port: %d
http_port: %d
pid_file: %q
lame_duck_grace_period: 1s
lame_duck_duration: 30s
tls {
  cert_file: %q
  key_file: %q
  ca_file: %q
}
mqtt {
  port: %d
  tls {
    cert_file: %q
    key_file: %q%s
  }
}
jetstream {
  store_dir: %q
}
accounts {
  APP { jetstream: enabled, users: [{user: %q, password: %q}] }
  SYS {}
}
system_account: SYS
authorization {
  timeout: 5
  auth_callout {
    issuer: %q
    xkey: %q
    auth_users: [%q]
    account: APP
  }
}
`, orDefault(o.ClientPort, 4222), orDefault(o.HTTPPort, 8222), o.PidFile,
		p("server.crt"), p("server.key"), p("server-ca.crt"),
		orDefault(o.MQTTPort, 1883), p("server.crt"), p("server.key"), mqttCA(o, p),
		o.StoreDir, serviceUser, sec.ServicePassword, sec.IssuerPublic, sec.XKeyPublic, serviceUser)
}

func mqttCA(o PrepareOptions, p func(string) string) string {
	if !o.MQTTClientCA {
		return ""
	}
	return fmt.Sprintf("\n    ca_file: %q", p("server-ca.crt"))
}

// LoadSecrets reads what Prepare wrote.
func LoadSecrets(dir string) (Secrets, error) {
	var sec Secrets
	b, err := os.ReadFile(filepath.Join(dir, "secrets.json"))
	if err != nil {
		return sec, err
	}
	return sec, json.Unmarshal(b, &sec)
}

func serverRoots(dir string) (*x509.CertPool, error) {
	b, err := os.ReadFile(filepath.Join(dir, "server-ca.crt"))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, errors.New("server-ca.crt holds no certificate")
	}
	return pool, nil
}

// ---------------------------------------------------------------------------
// the responder

// Record is what one authorization request carried.
type Record struct {
	Username       string `json:"username"`
	ClientID       string `json:"clientId"`
	TLS            bool   `json:"tls"`
	TLSVersion     string `json:"tlsVersion,omitempty"`
	Certs          int    `json:"certs"`
	VerifiedChains int    `json:"verifiedChains"`
	ChainDERBytes  int    `json:"chainDerBytes"`
	ClaimBytes     int    `json:"claimBytes"`
	WireBytes      int    `json:"wireBytes"`
	Admitted       bool   `json:"admitted"`
}

// Responder answers the broker's authorization requests and keeps what each carried.
type Responder struct {
	mu      sync.Mutex
	records []Record
	// Sink, when set, is handed every record as it is made.
	Sink func(Record)
}

func (r *Responder) add(rec Record) {
	r.mu.Lock()
	r.records = append(r.records, rec)
	sink := r.Sink
	r.mu.Unlock()
	if sink != nil {
		sink(rec)
	}
}

// Last returns the newest record for username, or false.
func (r *Responder) Last(username string) (Record, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.records) - 1; i >= 0; i-- {
		if r.records[i].Username == username {
			return r.records[i], true
		}
	}
	return Record{}, false
}

// Count is how many requests have been answered.
func (r *Responder) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.records)
}

// ConnectService connects as the static service login, retrying for up to a minute while the broker
// starts.
func ConnectService(dir, url string, sec Secrets) (*nats.Conn, error) {
	roots, err := serverRoots(dir)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		nc, err := nats.Connect(url, nats.UserInfo(serviceUser, sec.ServicePassword),
			nats.Secure(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}), nats.Timeout(5*time.Second))
		if err == nil || time.Now().After(deadline) {
			return nc, err
		}
		time.Sleep(time.Second)
	}
}

// StartResponder subscribes to the callout subject on nc.
func StartResponder(nc *nats.Conn, sec Secrets) (*Responder, error) {
	r := &Responder{}
	issuer, err := nkeys.FromSeed([]byte(sec.IssuerSeed))
	if err != nil {
		return nil, err
	}
	xkp, err := nkeys.FromCurveSeed([]byte(sec.XKeySeed))
	if err != nil {
		return nil, err
	}
	_, err = nc.Subscribe(calloutSubj, func(msg *nats.Msg) {
		data, sealTo := msg.Data, msg.Header.Get(xkeyHeader)
		if sealTo != "" {
			plain, err := xkp.Open(msg.Data, sealTo)
			if err != nil {
				return
			}
			data = plain
		}
		rc, err := jwt.DecodeAuthorizationRequestClaims(string(data))
		if err != nil {
			return
		}
		req := rc.AuthorizationRequest
		rec := Record{
			Username:   req.ConnectOptions.Username,
			ClientID:   req.ClientInformation.MQTT,
			ClaimBytes: len(data),
			WireBytes:  len(msg.Data),
		}
		if req.TLS != nil {
			rec.TLS = true
			rec.TLSVersion = req.TLS.Version
			rec.Certs = len(req.TLS.Certs)
			rec.VerifiedChains = len(req.TLS.VerifiedChains)
			for _, p := range req.TLS.Certs {
				if blk, _ := pem.Decode([]byte(p)); blk != nil {
					rec.ChainDERBytes += len(blk.Bytes)
				}
			}
		}
		rec.Admitted = rec.Username == TokenUser || (rec.Username == X509User && rec.Certs > 0)

		resp := jwt.NewAuthorizationResponseClaims(req.UserNkey)
		resp.Audience = req.Server.ID
		if rec.Admitted {
			uc := jwt.NewUserClaims(req.UserNkey)
			uc.Audience = appAccount
			uc.Expires = time.Now().Add(time.Hour).Unix()
			if resp.Jwt, err = uc.Encode(issuer); err != nil {
				return
			}
		} else {
			resp.Error = "denied"
		}
		out, err := resp.Encode(issuer)
		if err != nil {
			return
		}
		body := []byte(out)
		if sealTo != "" {
			if body, err = xkp.Seal(body, sealTo); err != nil {
				return
			}
		}
		r.add(rec)
		_ = msg.Respond(body)
	})
	if err != nil {
		return nil, err
	}
	return r, nc.Flush()
}

// ---------------------------------------------------------------------------
// clients

// ClientTLS trusts the broker and, when certName is set, presents <certName>.crt/.key. The pair is
// assembled by hand rather than with tls.X509KeyPair so that the wrong-key case can be built: the
// standard loader refuses a key that does not match the certificate, which is the mismatch the
// broker must refuse in the handshake.
func ClientTLS(dir, certName string) (*tls.Config, error) {
	roots, err := serverRoots(dir)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	if certName == "" {
		return cfg, nil
	}
	certPEMs, err := os.ReadFile(filepath.Join(dir, certName+".crt"))
	if err != nil {
		return nil, err
	}
	keyPEMb, err := os.ReadFile(filepath.Join(dir, certName+".key"))
	if err != nil {
		return nil, err
	}
	var cert tls.Certificate
	for rest := certPEMs; ; {
		var blk *pem.Block
		if blk, rest = pem.Decode(rest); blk == nil {
			break
		}
		cert.Certificate = append(cert.Certificate, blk.Bytes)
	}
	kb, _ := pem.Decode(keyPEMb)
	if kb == nil {
		return nil, fmt.Errorf("%s.key holds no PEM block", certName)
	}
	if cert.PrivateKey, err = x509.ParsePKCS8PrivateKey(kb.Bytes); err != nil {
		return nil, err
	}
	cfg.Certificates = []tls.Certificate{cert}
	return cfg, nil
}

// Connect is one Go paho session: CONNECT, a QoS 1 publish (which the broker acknowledges only
// through JetStream, so it is a real session and not just a CONNACK), DISCONNECT.
func Connect(addr, user, clientID string, cfg *tls.Config) error {
	o := mqtt.NewClientOptions().
		AddBroker("ssl://" + addr).
		SetClientID(clientID).
		SetUsername(user).
		SetTLSConfig(cfg).
		SetConnectTimeout(10 * time.Second).
		SetAutoReconnect(false).
		SetConnectRetry(false)
	c := mqtt.NewClient(o)
	t := c.Connect()
	if !t.WaitTimeout(20 * time.Second) {
		return errors.New("connect timed out")
	}
	if err := t.Error(); err != nil {
		return err
	}
	defer c.Disconnect(250)
	p := c.Publish("smoke/"+clientID, 1, false, "brokersmoke")
	if !p.WaitTimeout(20 * time.Second) {
		return errors.New("QoS 1 publish timed out")
	}
	return p.Error()
}

// RandomID is a fresh MQTT client id.
func RandomID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "smoke-" + hex.EncodeToString(b)
}

// Probe reports whether the listener at addr sends a CertificateRequest — the same observation the
// broker's own gauge is built on, made from outside.
func Probe(dir, addr string) (bool, error) {
	cfg, err := ClientTLS(dir, "")
	if err != nil {
		return false, err
	}
	requested := false
	cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		requested = true
		return &tls.Certificate{}, nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, err
	}
	cfg.ServerName = host
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, cfg)
	if err != nil {
		return false, err
	}
	_ = conn.Close()
	return requested, nil
}

// AcceptableCAs returns how many issuers the listener's CertificateRequest names. A client that
// honours the list withholds a certificate its issuer does not match, so anything but 0 turns
// away every device whose CA is not one of them.
func AcceptableCAs(dir, addr string) (int, error) {
	cfg, err := ClientTLS(dir, "")
	if err != nil {
		return 0, err
	}
	n := -1
	cfg.GetClientCertificate = func(cri *tls.CertificateRequestInfo) (*tls.Certificate, error) {
		n = len(cri.AcceptableCAs)
		return &tls.Certificate{}, nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, err
	}
	cfg.ServerName = host
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, cfg)
	if err != nil {
		return 0, err
	}
	_ = conn.Close()
	if n < 0 {
		return 0, errors.New("the listener sent no CertificateRequest")
	}
	return n, nil
}

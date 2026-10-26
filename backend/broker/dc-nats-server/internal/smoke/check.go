// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package smoke

import (
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// Step is one assertion of Check, with what it observed.
type Step struct {
	Name string
	Err  error
	Note string
}

// Failed counts the steps that did not pass.
func Failed(steps []Step) int {
	n := 0
	for _, s := range steps {
		if s.Err != nil {
			n++
		}
	}
	return n
}

// Check runs the responder and the Go clients against the broker at natsURL / mqttAddr and returns
// one Step per assertion. expect says whether the listener should be requesting client
// certificates: true for this module's broker, false for the upstream image, which is the negative
// control — run with expect=true against upstream, Check must report failures.
func Check(dir, natsURL, mqttAddr string, expect bool) ([]Step, error) {
	sec, err := LoadSecrets(dir)
	if err != nil {
		return nil, err
	}
	nc, err := ConnectService(dir, natsURL, sec)
	if err != nil {
		return nil, fmt.Errorf("service connect: %w", err)
	}
	defer nc.Close()
	r, err := StartResponder(nc, sec)
	if err != nil {
		return nil, err
	}
	c := checker{dir: dir, addr: mqttAddr, expect: expect, r: r, nc: nc}
	c.run("the MQTT listener sends a CertificateRequest", c.probe)
	c.run("a token device presenting no certificate connects and publishes", c.tokenNoCert)
	c.certCase("a certificate device completes TLS and its certificate reaches the callout", X509User, "device", 1, 1)
	c.certCase("a 5-certificate chain of 16 KiB reaches the callout whole", X509User, "big", 5, BigChainBytes)
	c.certCase("a token device that also presents a certificate still connects on its token", TokenUser, "device", 1, 1)
	c.run("a certificate signed with the wrong key fails the handshake and never reaches the callout", c.wrongKey)
	return c.steps, nil
}

type checker struct {
	dir, addr string
	expect    bool
	r         *Responder
	nc        *nats.Conn
	steps     []Step
}

func (c *checker) run(name string, f func() (string, error)) {
	note, err := f()
	c.steps = append(c.steps, Step{Name: name, Err: err, Note: note})
}

func (c *checker) probe() (string, error) {
	var req bool
	var err error
	// The MQTT listener comes up after JetStream; allow the time a broker start takes.
	for deadline := time.Now().Add(60 * time.Second); ; {
		if req, err = Probe(c.dir, c.addr); err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		return "", err
	}
	if req != c.expect {
		return "", fmt.Errorf("certificate_request=%v, want %v", req, c.expect)
	}
	return fmt.Sprintf("certificate_request=%v", req), nil
}

func (c *checker) tokenNoCert() (string, error) {
	cfg, err := ClientTLS(c.dir, "")
	if err != nil {
		return "", err
	}
	if err := Connect(c.addr, TokenUser, RandomID(), cfg); err != nil {
		return "", err
	}
	rec, ok := c.r.Last(TokenUser)
	if !ok {
		return "", errors.New("the callout saw no request for the token device")
	}
	if rec.Certs != 0 {
		return "", fmt.Errorf("the callout saw %d certificates from a device that sent none", rec.Certs)
	}
	return fmt.Sprintf("callout: tls=%v certs=0 claim=%dB", rec.TLS, rec.ClaimBytes), nil
}

// certCase connects as user presenting cert, and checks what reached the callout.
func (c *checker) certCase(name, user, cert string, wantCerts, minDER int) {
	c.run(name, func() (string, error) {
		cfg, err := ClientTLS(c.dir, cert)
		if err != nil {
			return "", err
		}
		before := c.r.Count()
		connErr := Connect(c.addr, user, RandomID(), cfg)
		rec, ok := c.r.Last(user)
		if c.r.Count() == before {
			ok = false
		}
		if !c.expect {
			// Upstream requests no certificate, so none is sent: the certificate user is refused
			// for carrying none, and a token user connects on its token with no chain attached.
			if ok && rec.Certs != 0 {
				return "", fmt.Errorf("the callout saw %d certificates from a listener that requests none", rec.Certs)
			}
			if user == X509User {
				if connErr == nil {
					return "", errors.New("connected although the listener requests no certificate")
				}
				return fmt.Sprintf("refused, no certificate reached the callout (%v)", connErr), nil
			}
			if connErr != nil {
				return "", connErr
			}
			return "connected on its token; no certificate reached the callout", nil
		}
		if connErr != nil {
			return "", connErr
		}
		if !ok {
			return "", errors.New("the callout saw no request")
		}
		if rec.Certs != wantCerts || rec.VerifiedChains != 0 {
			return "", fmt.Errorf("the callout saw certs=%d verifiedChains=%d, want certs=%d verifiedChains=0", rec.Certs, rec.VerifiedChains, wantCerts)
		}
		if rec.ChainDERBytes < minDER {
			return "", fmt.Errorf("the callout saw %d bytes of chain DER, want at least %d", rec.ChainDERBytes, minDER)
		}
		return fmt.Sprintf("callout: TLS %s certs=%d verifiedChains=0 chainDER=%dB claim=%dB wire=%dB (max_payload %dB)",
			rec.TLSVersion, rec.Certs, rec.ChainDERBytes, rec.ClaimBytes, rec.WireBytes, c.nc.MaxPayload()), nil
	})
}

func (c *checker) wrongKey() (string, error) {
	cfg, err := ClientTLS(c.dir, "wrongkey")
	if err != nil {
		return "", err
	}
	before := c.r.Count()
	connErr := Connect(c.addr, X509User, RandomID(), cfg)
	if !c.expect {
		// Upstream asks for no certificate, so none is sent and none is checked; the connect
		// is refused by the callout for carrying no certificate instead.
		if connErr == nil {
			return "", errors.New("connected although the listener requests no certificate")
		}
		return fmt.Sprintf("refused, by the callout (%v)", connErr), nil
	}
	if connErr == nil {
		return "", errors.New("connected with a certificate whose key it does not hold")
	}
	if c.r.Count() != before {
		return "", errors.New("the callout was consulted for a connection whose handshake should have failed")
	}
	return fmt.Sprintf("refused in the handshake (%v)", connErr), nil
}

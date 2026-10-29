// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package smoke

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Fleet is a set of held token-device MQTT sessions and a count of how many have been lost.
type Fleet struct {
	clients []mqtt.Client
	lost    atomic.Int32
	closeFn func()
	// FirstLoss is the error of the first session lost, empty while none has been.
	firstLoss atomic.Value
}

// HoldFleet runs the callout responder against natsURL and opens n token sessions on mqttAddr,
// keeping them open. Auto-reconnect is off, so a drop is counted and not papered over.
func HoldFleet(dir, natsURL, mqttAddr string, n int) (*Fleet, error) {
	sec, err := LoadSecrets(dir)
	if err != nil {
		return nil, err
	}
	svc, err := ConnectService(dir, natsURL, sec)
	if err != nil {
		return nil, fmt.Errorf("service connect: %w", err)
	}
	if _, err := StartResponder(svc, sec); err != nil {
		svc.Close()
		return nil, err
	}
	cfg, err := ClientTLS(dir, "")
	if err != nil {
		svc.Close()
		return nil, err
	}
	f := &Fleet{}
	f.closeFn = func() {
		for _, c := range f.clients {
			c.Disconnect(0)
		}
		svc.Close()
	}
	for i := 0; i < n; i++ {
		o := mqtt.NewClientOptions().
			AddBroker("ssl://" + mqttAddr).
			SetClientID(RandomID()).
			SetUsername(TokenUser).
			SetTLSConfig(cfg).
			SetConnectTimeout(10 * time.Second).
			SetAutoReconnect(false).
			SetConnectRetry(false).
			SetConnectionLostHandler(func(_ mqtt.Client, err error) {
				if f.lost.Add(1) == 1 {
					f.firstLoss.Store(err.Error())
				}
			})
		c := mqtt.NewClient(o)
		tok := c.Connect()
		if !tok.WaitTimeout(20 * time.Second) {
			f.Close()
			return nil, errors.New("connect timed out")
		}
		if tok.Error() != nil {
			f.Close()
			return nil, fmt.Errorf("connect %d: %w", i, tok.Error())
		}
		f.clients = append(f.clients, c)
	}
	return f, nil
}

// Lost is how many sessions have been lost since they were opened.
func (f *Fleet) Lost() int { return int(f.lost.Load()) }

// FirstLoss is why the first session was lost, or "".
func (f *Fleet) FirstLoss() string {
	s, _ := f.firstLoss.Load().(string)
	return s
}

// Publish sends a QoS 1 message on the first session that is still open; the broker acknowledges
// it only through JetStream, so success means the session works end to end.
func (f *Fleet) Publish() error {
	for _, c := range f.clients {
		if c.IsConnectionOpen() {
			p := c.Publish("fleet/after", 1, false, "x")
			if !p.WaitTimeout(20 * time.Second) {
				return errors.New("QoS 1 publish timed out")
			}
			return p.Error()
		}
	}
	return errors.New("no session is open")
}

// Close disconnects every session and the responder's connection.
func (f *Fleet) Close() { f.closeFn() }

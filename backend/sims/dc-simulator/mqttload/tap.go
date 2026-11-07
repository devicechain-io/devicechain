// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package mqttload

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// TapConfig is how the read-back tap reaches the target broker. The tap is a subscriber, the
// part of a real system that consumes what devices publish: what it receives is the evidence of
// delivery the PUBACK is checked against.
type TapConfig struct {
	Broker      string
	TLS         bool
	TLSInsecure bool
	// TLSServerName overrides the name the broker's certificate is verified against.
	TLSServerName string
	ClientID      string
	Username      string
	Password      string
	// Filter is the subscription filter; it must cover every device's publish topic.
	Filter string
	// ConnectTimeout bounds the connect and the subscribe.
	ConnectTimeout time.Duration
}

// TapStats is what the tap saw, for the report.
type TapStats struct {
	// Received is every PUBLISH delivered to the tap, Unique the distinct (device, instant)
	// identities among them.
	Received int64 `json:"received"`
	Unique   int64 `json:"unique"`
	// Duplicates is deliveries of an identity already seen (QoS 1 is at-least-once). They are
	// not counted twice, and not a loss.
	Duplicates int64 `json:"duplicates"`
	// Unparsed is deliveries whose body was not an event naming a device and an RFC 3339
	// occurredTime: traffic that is not the run's.
	Unparsed int64 `json:"unparsed"`
	// ConnectionLosses is how often the tap's own session dropped. A tap that dropped may have
	// missed deliveries the broker never retried, so the run cannot name that a loss of the
	// target's.
	ConnectionLosses int64  `json:"connectionLosses"`
	Filter           string `json:"filter"`
}

type tapKey struct {
	device string
	micros int64
}

// Tap subscribes to the target's device topics and records the identity (device, occurredTime
// in microseconds) of every event delivered.
type Tap struct {
	cfg    TapConfig
	client mqtt.Client

	mu     sync.Mutex
	seen   map[tapKey]struct{}
	byDev  map[string][]int64
	all    []int64 // every unique instant, for window counts
	closed atomic.Bool

	received, duplicates, unparsed, losses atomic.Int64
}

// NewTap connects the tap and subscribes it, returning only once the broker has acknowledged the
// subscription: a tap that is not yet subscribed would read the first events as lost.
func NewTap(ctx context.Context, cfg TapConfig) (*Tap, error) {
	if cfg.Filter == "" {
		return nil, fmt.Errorf("the tap has no topic filter")
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = DefaultConnectTimeout
	}
	broker, err := ResolveBroker(cfg.Broker, cfg.TLS)
	if err != nil {
		return nil, err
	}
	t := &Tap{cfg: cfg, seen: map[tapKey]struct{}{}, byDev: map[string][]int64{}}

	opts := mqtt.NewClientOptions()
	opts.AddBroker(broker)
	opts.SetClientID(cfg.ClientID)
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
	}
	opts.SetPassword(cfg.Password)
	// A clean session: leftovers queued for this client id by an earlier run are not this run's
	// traffic. A drop mid-run is NOT recovered silently; it is counted and fails the run.
	opts.SetCleanSession(true)
	opts.SetAutoReconnect(false)
	opts.SetConnectRetry(false)
	opts.SetOrderMatters(false)
	opts.SetKeepAlive(DefaultKeepAlive)
	opts.SetConnectTimeout(cfg.ConnectTimeout)
	if cfg.TLS {
		opts.SetTLSConfig(&tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: cfg.TLSInsecure, //nolint:gosec // the operator's recorded trust decision
			ServerName:         tapServerName(broker, cfg.TLSServerName),
		})
	}
	opts.SetConnectionLostHandler(func(_ mqtt.Client, _ error) {
		if !t.closed.Load() {
			t.losses.Add(1)
		}
	})
	t.client = mqtt.NewClient(opts)

	wait := func(tok mqtt.Token, what string) error {
		select {
		case <-tok.Done():
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2*cfg.ConnectTimeout + 5*time.Second):
			return fmt.Errorf("tap %s: no answer", what)
		}
		if err := tok.Error(); err != nil {
			return fmt.Errorf("tap %s: %w", what, err)
		}
		return nil
	}
	if err := wait(t.client.Connect(), "connect"); err != nil {
		return nil, err
	}
	sub := t.client.Subscribe(cfg.Filter, 1, t.onMessage)
	if err := wait(sub, "subscribe"); err != nil {
		t.Close()
		return nil, err
	}
	// A broker may grant less than asked, or refuse (0x80): either is a tap that will hear
	// nothing, which a run would then report as total loss.
	if st, ok := sub.(*mqtt.SubscribeToken); ok {
		for f, code := range st.Result() {
			if code == 0x80 {
				t.Close()
				return nil, fmt.Errorf("tap subscribe: the broker refused the subscription to %q (is the tap user allowed to read it?)", f)
			}
			if code != 1 {
				t.Close()
				return nil, fmt.Errorf("tap subscribe: the broker granted QoS %d for %q, not the QoS 1 the verdict is defined on", code, f)
			}
		}
	}
	return t, nil
}

func tapServerName(broker, override string) string {
	if override != "" {
		return override
	}
	u, err := url.Parse(broker) // ResolveBroker returned a parseable URL
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func (t *Tap) onMessage(_ mqtt.Client, m mqtt.Message) {
	t.received.Add(1)
	var body struct {
		Device       string `json:"device"`
		OccurredTime string `json:"occurredTime"`
	}
	if err := json.Unmarshal(m.Payload(), &body); err != nil || body.Device == "" {
		t.unparsed.Add(1)
		return
	}
	at, err := time.Parse(time.RFC3339Nano, body.OccurredTime)
	if err != nil {
		t.unparsed.Add(1)
		return
	}
	k := tapKey{device: body.Device, micros: at.UnixMicro()}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, dup := t.seen[k]; dup {
		t.duplicates.Add(1)
		return
	}
	t.seen[k] = struct{}{}
	t.byDev[k.device] = append(t.byDev[k.device], k.micros)
	t.all = append(t.all, k.micros)
}

// CountBetween is how many distinct events were delivered with an instant in [start, end].
func (t *Tap) CountBetween(start, end time.Time) int64 {
	lo, hi := start.UnixMicro(), end.UnixMicro()
	t.mu.Lock()
	defer t.mu.Unlock()
	var n int64
	for _, us := range t.all {
		if us >= lo && us <= hi {
			n++
		}
	}
	return n
}

// DeviceBetween lists the distinct instants delivered for device in [start, end], oldest first.
func (t *Tap) DeviceBetween(device string, start, end time.Time) []int64 {
	lo, hi := start.UnixMicro(), end.UnixMicro()
	t.mu.Lock()
	var out []int64
	for _, us := range t.byDev[device] {
		if us >= lo && us <= hi {
			out = append(out, us)
		}
	}
	t.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Stats is what the tap has seen so far.
func (t *Tap) Stats() TapStats {
	t.mu.Lock()
	unique := int64(len(t.seen))
	t.mu.Unlock()
	return TapStats{
		Received: t.received.Load(), Unique: unique, Duplicates: t.duplicates.Load(),
		Unparsed: t.unparsed.Load(), ConnectionLosses: t.losses.Load(), Filter: t.cfg.Filter,
	}
}

// Close disconnects the tap.
func (t *Tap) Close() {
	if t.closed.Swap(true) {
		return
	}
	if t.client != nil && t.client.IsConnectionOpen() {
		t.client.Disconnect(250)
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// These two tests drive the REAL runLoop over a REAL paho client against a REAL broker
// that refuses a subscription the way a customer broker does: an ACL that denies read on
// one group, answered with SUBACK 0x80. The refusal is the broker's behaviour, so a fake
// that returns an error from a seam would only confirm that our idea of a refusal agrees
// with itself. The embedded NATS server's MQTT gateway is used because it enforces
// per-user subscribe permissions and answers a denied filter with exactly that code.

const (
	refusalHostUser = "sparkplug-host"
	refusalHostPass = "host-secret"
	refusalObsUser  = "observer"
	refusalObsPass  = "observer-secret"
)

// startRefusingBroker runs an MQTT broker on which refusalHostUser may subscribe to
// everything except the Sparkplug group "denied", and refusalObsUser may do anything.
func startRefusingBroker(t *testing.T) string {
	t.Helper()
	mqttPort := dctest.FreeTCPPort(t)
	// NATS maps an MQTT topic onto a subject by turning '/' into '.' and a literal '.'
	// into "//", and subscribes a trailing "/#" filter on both the parent subject and its
	// ">" wildcard. Both are denied, so the whole group filter is refused.
	denied := []string{"spBv1//0.denied", "spBv1//0.denied.>"}
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:       "127.0.0.1",
		Port:       -1,
		JetStream:  true, // the MQTT gateway is built on JetStream
		StoreDir:   t.TempDir(),
		ServerName: "sparkplug-refusal-test",
		MQTT:       natsserver.MQTTOpts{Host: "127.0.0.1", Port: mqttPort},
		Users: []*natsserver.User{
			{
				Username: refusalHostUser, Password: refusalHostPass,
				Permissions: &natsserver.Permissions{
					Subscribe: &natsserver.SubjectPermission{Allow: []string{">"}, Deny: denied},
				},
			},
			{Username: refusalObsUser, Password: refusalObsPass},
		},
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(15*time.Second), "embedded broker not ready")
	t.Cleanup(srv.Shutdown)
	return fmt.Sprintf("tcp://127.0.0.1:%d", mqttPort)
}

// watchState subscribes an unrestricted observer to the host's STATE topic and returns
// every STATE payload it sees, retained or live, in arrival order.
func watchState(t *testing.T, url, stateTopic string) func() []State {
	t.Helper()
	opts := mqtt.NewClientOptions()
	opts.AddBroker(url)
	opts.SetClientID("state-observer")
	opts.SetUsername(refusalObsUser)
	opts.SetPassword(refusalObsPass)
	obs := mqtt.NewClient(opts)
	tok := obs.Connect()
	require.True(t, tok.WaitTimeout(15*time.Second), "observer connect timed out")
	require.NoError(t, tok.Error())
	t.Cleanup(func() { obs.Disconnect(100) })

	var mu sync.Mutex
	var seen []State
	//subconfirm:ok the observer's subscription is confirmed on the next line
	sub := obs.Subscribe(stateTopic, 1, func(_ mqtt.Client, m mqtt.Message) {
		st, err := ParseState(m.Payload())
		if err != nil {
			t.Errorf("unparseable STATE payload %q: %v", m.Payload(), err)
			return
		}
		mu.Lock()
		seen = append(seen, st)
		mu.Unlock()
	})
	require.True(t, sub.WaitTimeout(15*time.Second), "observer subscribe timed out")
	require.NoError(t, sub.Error())
	return func() []State {
		mu.Lock()
		defer mu.Unlock()
		return append([]State(nil), seen...)
	}
}

func refusalClient(url string, groups []string, failures prometheus.Counter) *Client {
	return NewClient(
		config.SparkplugSource{Tenant: "acme", HostId: "refusal-host", Groups: groups},
		Broker{URL: url, ClientID: "refusal-host-client", Username: refusalHostUser, Password: refusalHostPass},
		nil, nil, Metrics{SubscribeFailures: failures},
	)
}

// A broker that refuses ONE of the host's groups must never see the host announce
// ONLINE, and the host must keep retrying (with backoff) rather than settle into a
// half-subscribed session.
//
// Before, the refusal was logged and skipped: ONLINE went out for every group, so the
// refused group's edge nodes flushed their store-and-forward buffers into a
// subscription that did not exist, and the reconcile probe declared that group's
// devices disconnected for staying silent.
func TestARefusedGroupOnARealBrokerNeverAnnouncesOnline(t *testing.T) {
	url := startRefusingBroker(t)
	states := watchState(t, url, StateTopic("refusal-host"))

	failures := prometheus.NewCounter(prometheus.CounterOpts{Name: "subscribe_failures_total"})
	c := refusalClient(url, []string{"good", "denied"}, failures)
	// Record the backoff runLoop chooses, and wait a token 10ms instead of it, so three
	// sessions fit in a test.
	var bmu sync.Mutex
	var backoffs []time.Duration
	c.backoffSleep = func(ctx context.Context, d time.Duration) bool {
		bmu.Lock()
		backoffs = append(backoffs, d)
		bmu.Unlock()
		return sleep(ctx, 10*time.Millisecond)
	}
	c.Connect()
	t.Cleanup(c.Stop)

	// Three abandoned sessions prove the host RETRIES after abandoning one, not that it
	// gave up after the first refusal.
	require.Eventually(t, func() bool { return testutil.ToFloat64(failures) >= 3 },
		15*time.Second, 5*time.Millisecond,
		"the broker refused a group, but the host did not count and retry abandoned sessions")

	// ...and that it BACKS OFF between them. The connection itself succeeds every time, so
	// a backoff reset on connect (where it used to be) would re-dial a refusing broker at
	// the 1s floor forever.
	bmu.Lock()
	got := append([]time.Duration(nil), backoffs...)
	bmu.Unlock()
	require.GreaterOrEqual(t, len(got), 2)
	require.Equal(t, []time.Duration{time.Second, 2 * time.Second}, got[:2],
		"abandoned sessions must back off exponentially, like failed connects")

	for _, st := range states() {
		require.False(t, st.Online,
			"the host announced ONLINE while the broker refused one of its groups; that group's edge "+
				"nodes would flush their buffered data into a subscription that does not exist")
	}
}

// The counterweight: with every group granted, the same host on the same broker DOES
// announce ONLINE and counts no failure. Without it, a host that never announced
// anything at all would satisfy the test above.
func TestAllGroupsGrantedOnARealBrokerAnnouncesOnline(t *testing.T) {
	url := startRefusingBroker(t)
	states := watchState(t, url, StateTopic("refusal-host"))

	failures := prometheus.NewCounter(prometheus.CounterOpts{Name: "subscribe_failures_total"})
	c := refusalClient(url, []string{"good", "also-good"}, failures)
	c.Connect()
	t.Cleanup(c.Stop)

	require.Eventually(t, func() bool {
		for _, st := range states() {
			if st.Online {
				return true
			}
		}
		return false
	}, 15*time.Second, 20*time.Millisecond, "a host whose every group was granted never announced ONLINE")
	require.Equal(t, float64(0), testutil.ToFloat64(failures), "a granted session counted a subscribe failure")
}

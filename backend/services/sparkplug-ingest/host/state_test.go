// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStateRoundTripIs30Form pins the Sparkplug 3.0 STATE body: a JSON object
// with online + timestamp, NOT the 2.2 bare "ONLINE"/"OFFLINE" string. A parser
// that accepted the old string form would silently mis-read presence.
func TestStateRoundTripIs30Form(t *testing.T) {
	b, err := State{Online: true, Timestamp: 1_700_000_000_123}.Marshal()
	require.NoError(t, err)
	assert.JSONEq(t, `{"online":true,"timestamp":1700000000123}`, string(b))

	got, err := ParseState(b)
	require.NoError(t, err)
	assert.True(t, got.Online)
	assert.Equal(t, int64(1_700_000_000_123), got.Timestamp)
}

// TestParseStateFailsClosed pins the fail-closed contract: only a complete
// {online,timestamp} object parses. The sharp cases are {} / null / a
// missing field — each decodes silently into State{false,0} with a plain
// Unmarshal, which would be read as OFFLINE@epoch (a false liveness reading).
// The 2.2 bare-string and unknown/trailing forms round it out.
func TestParseStateFailsClosed(t *testing.T) {
	reject := map[string]string{
		"bare 2.2 string": `ONLINE`,
		"empty object":    `{}`,
		"null":            `null`,
		"missing online":  `{"timestamp":1700000000123}`,
		"missing ts":      `{"online":true}`,
		"unknown field":   `{"online":true,"timestamp":1,"extra":2}`,
		"trailing data":   `{"online":true,"timestamp":1}{"online":false,"timestamp":2}`,
	}
	for name, body := range reject {
		t.Run(name, func(t *testing.T) {
			_, err := ParseState([]byte(body))
			require.Error(t, err)
		})
	}

	// The counterweight: a real online:false must be accepted and distinguished
	// from an absent field (so a genuine OFFLINE is not lost as "malformed").
	got, err := ParseState([]byte(`{"online":false,"timestamp":5}`))
	require.NoError(t, err)
	assert.False(t, got.Online)
	assert.Equal(t, int64(5), got.Timestamp)
}

// TestStateTopicIsOutsideGroupTree pins B4: the Host STATE topic is
// spBv1.0/STATE/{host_id}, NOT under any {group}. A wrong topic means edge nodes
// never observe host presence — a silent total failure this test guards.
func TestStateTopicIsOutsideGroupTree(t *testing.T) {
	assert.Equal(t, "spBv1.0/STATE/devicechain", StateTopic("devicechain"))
}

// TestSubscriptionsFollowGroups pins the subscribe topology: no configured
// groups ⇒ the all-groups wildcard; configured groups ⇒ one scoped filter each,
// never the wildcard (which would over-subscribe past the intended groups).
func TestSubscriptionsFollowGroups(t *testing.T) {
	clk := func() time.Time { return time.UnixMilli(0) }
	all := NewClient(config.SparkplugSource{Tenant: "t", HostId: "h"}, Broker{}, clk, Metrics{})
	assert.Equal(t, []string{"spBv1.0/#"}, all.subscriptions())

	scoped := NewClient(config.SparkplugSource{Tenant: "t", HostId: "h", Groups: []string{"plant-a", "plant-b"}}, Broker{}, clk, Metrics{})
	assert.Equal(t, []string{"spBv1.0/plant-a/#", "spBv1.0/plant-b/#"}, scoped.subscriptions())
}

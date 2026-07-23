// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// These tests guard the SHIPPED Sparkplug wiring — that host.NewRegistrar/host.NewEmitter
// inject exactly "sp-" / "sp" into the shared adapter. The adapter's own suite proves the
// prefix parameter is honoured; this proves THIS binary passes the right value. Without
// it, a typo in the tokenPrefix/dedupPrefix const (adapter.go) ships silently: every newly
// minted device token changes (breaking the auto-register unique-index collision) or every
// emitted dedup id changes (so a failover re-derivation duplicates presence events in the
// JetStream window). The expected strings are the same goldens the adapter suite pins.

// fakeWireWriter captures the durable writes so the test can read the injected DedupID.
type fakeWireWriter struct{ msgs []messaging.Message }

func (w *fakeWireWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	w.msgs = append(w.msgs, msgs...)
	return nil
}

// fakeCreateGQL answers a lookup-miss then echoes the create request's token, so a
// Resolve auto-registers and returns the DERIVED token — exercising the injected prefix.
type fakeCreateGQL struct{}

func (fakeCreateGQL) Query(_ context.Context, _, _, query string, vars map[string]any, out any) error {
	var data any
	if strings.Contains(query, "devicesByExternalId") {
		data = map[string]any{"devicesByExternalId": []map[string]any{}}
	} else {
		token := vars["request"].(map[string]any)["token"]
		data = map[string]any{"createDevice": map[string]any{"token": token}}
	}
	b, _ := json.Marshal(data)
	return json.Unmarshal(b, out)
}

func TestSparkplugWiringInjectsTokenPrefix(t *testing.T) {
	r := NewRegistrar(fakeCreateGQL{}, "url")
	tok, outcome, err := r.Resolve(context.Background(), "acme", "plant-a/node-3/dev-2",
		IngestPolicy{AutoRegister: true, DeviceTypeToken: "t"})
	require.NoError(t, err)
	assert.Equal(t, adapter.ResolveCreated, outcome)
	// The golden token for "plant-a/node-3/dev-2" under the "sp-" prefix (the same golden
	// the adapter suite pins). A typoed tokenPrefix const reddens here.
	assert.Equal(t, "sp-plant-a-node-3-dev-2-f059b7804d5d", tok,
		"host.NewRegistrar must inject the Sparkplug token prefix \"sp-\"")
}

func TestSparkplugWiringInjectsDedupPrefix(t *testing.T) {
	w := &fakeWireWriter{}
	e := NewEmitter(w, func() time.Time { return time.Unix(1_700_000_000, 0) })
	ev := PresenceEvent{
		ExternalId: "plant-a/n1", Connected: true, Reason: "birth",
		SessionId: 1_700_000_000_123456789, OccurredAt: time.Unix(1_700_000_000, 0),
	}
	require.NoError(t, e.EmitPresence(context.Background(), "acme", "sparkplug:h1", "sp-dev-abc", ev))
	require.Len(t, w.msgs, 1)
	// The golden connected-presence dedup id under the "sp" prefix. A typoed dedupPrefix
	// const reddens here.
	assert.Equal(t, "sp1gip9ahddziq7", w.msgs[0].DedupID,
		"host.NewEmitter must inject the Sparkplug dedup prefix \"sp\"")
}

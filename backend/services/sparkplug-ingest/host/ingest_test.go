// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// --- fakes ------------------------------------------------------------------

// fakeGraphQL stands in for the svcclient device-management client. Its responder
// decides the reply per operation (lookup vs create), and it decodes that reply into
// `out` exactly as svcclient does (json round-trip), so the registrar's response
// handling is exercised for real.
type fakeGraphQL struct {
	mu        sync.Mutex
	lookups   int
	creates   int
	responder func(query string, vars map[string]any) (any, error)
}

func (f *fakeGraphQL) Query(_ context.Context, _, _ string, query string, vars map[string]any, out any) error {
	f.mu.Lock()
	if strings.Contains(query, "devicesByExternalId") {
		f.lookups++
	} else {
		f.creates++
	}
	f.mu.Unlock()
	data, err := f.responder(query, vars)
	if err != nil {
		return err
	}
	if out != nil && data != nil {
		b, _ := json.Marshal(data)
		return json.Unmarshal(b, out)
	}
	return nil
}

func lookupHit(token string) any {
	return map[string]any{"devicesByExternalId": []map[string]any{{"token": token}}}
}
func lookupMiss() any { return map[string]any{"devicesByExternalId": []map[string]any{}} }
func createHit(token string) any {
	return map[string]any{"createDevice": map[string]any{"token": token}}
}

// fakeWriter captures the durable writes (and the context, to prove tenant scoping).
type fakeWriter struct {
	mu   sync.Mutex
	ctxs []context.Context
	msgs []messaging.Message
	err  error
}

func (w *fakeWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	w.ctxs = append(w.ctxs, ctx)
	w.msgs = append(w.msgs, msgs...)
	return nil
}

// --- Registrar --------------------------------------------------------------

func TestRegistrarCachesResolvedToken(t *testing.T) {
	gql := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return lookupHit("dev-1"), nil }}
	r := NewRegistrar(gql, "url")

	tok, outcome, err := r.Resolve(context.Background(), "acme", "g/n", IngestPolicy{})
	require.NoError(t, err)
	assert.Equal(t, "dev-1", tok)
	assert.Equal(t, resolveFound, outcome)

	// Second resolve is served from cache — no second lookup.
	tok2, _, err := r.Resolve(context.Background(), "acme", "g/n", IngestPolicy{})
	require.NoError(t, err)
	assert.Equal(t, "dev-1", tok2)
	assert.Equal(t, 1, gql.lookups, "a cached resolution must not re-hit device-management")
}

func TestRegistrarDropsUnknownWhenAutoRegisterOff(t *testing.T) {
	gql := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return lookupMiss(), nil }}
	r := NewRegistrar(gql, "url")

	tok, outcome, err := r.Resolve(context.Background(), "acme", "g/n", IngestPolicy{AutoRegister: false})
	require.NoError(t, err)
	assert.Equal(t, "", tok)
	assert.Equal(t, resolveDropped, outcome)
	assert.Equal(t, 0, gql.creates, "auto-register off must never create a device")
}

func TestRegistrarAutoRegistersUnknownDevice(t *testing.T) {
	var createdRequest map[string]any
	gql := &fakeGraphQL{responder: func(query string, vars map[string]any) (any, error) {
		if strings.Contains(query, "devicesByExternalId") {
			return lookupMiss(), nil
		}
		createdRequest, _ = vars["request"].(map[string]any)
		return createHit(createdRequest["token"].(string)), nil
	}}
	r := NewRegistrar(gql, "url")

	tok, outcome, err := r.Resolve(context.Background(), "acme", "plant-a/node-3", IngestPolicy{AutoRegister: true, DeviceTypeToken: "sp-node"})
	require.NoError(t, err)
	assert.Equal(t, resolveCreated, outcome)
	// The created device carries the DERIVED (grammar-safe) token, the raw external
	// id, and the source's device type.
	assert.Equal(t, DeriveDeviceToken("plant-a/node-3"), tok)
	assert.Equal(t, DeriveDeviceToken("plant-a/node-3"), createdRequest["token"])
	assert.Equal(t, "plant-a/node-3", createdRequest["externalId"])
	assert.Equal(t, "sp-node", createdRequest["deviceTypeToken"])
}

// TestRegistrarSwallowsCreateRaceViaReLookup pins §5: when create fails because a
// concurrent burst already registered the device, the registrar re-looks-up and
// uses the winner rather than surfacing an error.
func TestRegistrarSwallowsCreateRaceViaReLookup(t *testing.T) {
	lookups := 0
	gql := &fakeGraphQL{responder: func(query string, _ map[string]any) (any, error) {
		if strings.Contains(query, "devicesByExternalId") {
			lookups++
			if lookups == 1 {
				return lookupMiss(), nil // first lookup: absent
			}
			return lookupHit("winner-token"), nil // re-lookup after the failed create: the race winner
		}
		return nil, errors.New("ERROR: duplicate key value violates unique constraint")
	}}
	r := NewRegistrar(gql, "url")

	tok, outcome, err := r.Resolve(context.Background(), "acme", "g/n", IngestPolicy{AutoRegister: true, DeviceTypeToken: "t"})
	require.NoError(t, err)
	assert.Equal(t, "winner-token", tok)
	assert.Equal(t, resolveFound, outcome)
}

func TestRegistrarReturnsTransportErrorForRetry(t *testing.T) {
	gql := &fakeGraphQL{responder: func(string, map[string]any) (any, error) {
		return nil, errors.New("svcclient: call url: connection refused")
	}}
	r := NewRegistrar(gql, "url")

	_, _, err := r.Resolve(context.Background(), "acme", "g/n", IngestPolicy{})
	assert.Error(t, err, "a transport error must be returned so the caller can retry")
}

// --- Emitter ----------------------------------------------------------------

// TestEmitterProducesResolverReadableEvent is the durable-emit pin: the bytes the
// emitter writes must round-trip through the event-sources unmarshaller (the exact
// contract the device-management resolver reads), carry the resolved token, the
// stringified measurement at its own timestamp, a NIL AltId and an empty DedupID
// (the at-least-once posture), a non-zero ProcessedTime (a zero time marshals to
// year 0001, not an error), and be published under the connection's tenant.
func TestEmitterProducesResolverReadableEvent(t *testing.T) {
	w := &fakeWriter{}
	e := NewEmitter(w, fixedNow)

	ts := int64(1_700_000_000_123)
	err := e.Emit(context.Background(), "acme", "sparkplug:h1", "dev-1", []Sample{{Name: "temperature", Value: 21.5, Time: ts}})
	require.NoError(t, err)
	require.Len(t, w.msgs, 1)

	msg := w.msgs[0]
	assert.Equal(t, "", msg.DedupID, "external-broker source publishes DedupID='' (HTTP-ingest posture)")
	assert.Equal(t, []byte("dev-1"), msg.Key)

	ev, err := esproto.UnmarshalUnresolvedEvent(msg.Value)
	require.NoError(t, err, "the emitted bytes must round-trip through the resolver's decoder")
	assert.Equal(t, "dev-1", ev.Device)
	assert.Equal(t, "sparkplug:h1", ev.Source)
	assert.Equal(t, esmodel.Measurement, ev.EventType)
	assert.Nil(t, ev.AltId, "AltId must be nil so event-management's dedup index does not collapse distinct messages")
	assert.False(t, ev.ProcessedTime.IsZero(), "ProcessedTime must be a real time, not the zero value")
	assert.False(t, ev.OccurredTime.IsZero())

	payload, ok := ev.Payload.(*esmodel.UnresolvedMeasurementsPayload)
	require.True(t, ok)
	require.Len(t, payload.Entries, 1)
	assert.Equal(t, "21.5", payload.Entries[0].Measurements["temperature"])
	require.NotNil(t, payload.Entries[0].OccurredTime)
	assert.Equal(t, time.UnixMilli(ts).UTC().Format(time.RFC3339Nano), *payload.Entries[0].OccurredTime)

	// The tenant flows from the connection into the write context (SP3a invariant) —
	// never parsed from the Sparkplug topic.
	require.Len(t, w.ctxs, 1)
	tenant, ok := core.TenantFromContext(w.ctxs[0])
	assert.True(t, ok)
	assert.Equal(t, "acme", tenant)
}

// --- Ingester ---------------------------------------------------------------

func newIngestMetrics() (IngestMetrics, func(string) float64) {
	emitted := prometheus.NewCounter(prometheus.CounterOpts{Name: "emitted"})
	registered := prometheus.NewCounter(prometheus.CounterOpts{Name: "registered"})
	dropped := prometheus.NewCounter(prometheus.CounterOpts{Name: "dropped"})
	m := IngestMetrics{MeasurementsEmitted: emitted, DevicesRegistered: registered, UnknownDropped: dropped}
	read := func(which string) float64 {
		switch which {
		case "emitted":
			return testutil.ToFloat64(emitted)
		case "registered":
			return testutil.ToFloat64(registered)
		default:
			return testutil.ToFloat64(dropped)
		}
	}
	return m, read
}

func TestIngesterEmitsForAKnownDevice(t *testing.T) {
	gql := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return lookupHit("dev-1"), nil }}
	w := &fakeWriter{}
	m, read := newIngestMetrics()
	ing := NewIngester(NewRegistrar(gql, "url"), NewEmitter(w, fixedNow), m)

	err := ing.Ingest(context.Background(), "acme", IngestPolicy{Source: "s"}, "g/n", []Sample{{Name: "t", Value: 1, Time: 1}})
	require.NoError(t, err)
	assert.Len(t, w.msgs, 1)
	assert.Equal(t, float64(1), read("emitted"))
	assert.Equal(t, float64(0), read("dropped"))
}

func TestIngesterDropsUnknownAndCountsIt(t *testing.T) {
	gql := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return lookupMiss(), nil }}
	w := &fakeWriter{}
	m, read := newIngestMetrics()
	ing := NewIngester(NewRegistrar(gql, "url"), NewEmitter(w, fixedNow), m)

	err := ing.Ingest(context.Background(), "acme", IngestPolicy{AutoRegister: false}, "g/n", []Sample{{Name: "t", Value: 1, Time: 1}, {Name: "u", Value: 2, Time: 1}})
	require.NoError(t, err, "a definitive drop is handled, not an error")
	assert.Empty(t, w.msgs, "an unknown device with auto-register off must emit nothing")
	assert.Equal(t, float64(2), read("dropped"), "drop counts the samples")
	assert.Equal(t, float64(0), read("emitted"))
}

func TestIngesterCountsRegistrationThenEmits(t *testing.T) {
	gql := &fakeGraphQL{responder: func(query string, vars map[string]any) (any, error) {
		if strings.Contains(query, "devicesByExternalId") {
			return lookupMiss(), nil
		}
		return createHit(vars["request"].(map[string]any)["token"].(string)), nil
	}}
	w := &fakeWriter{}
	m, read := newIngestMetrics()
	ing := NewIngester(NewRegistrar(gql, "url"), NewEmitter(w, fixedNow), m)

	err := ing.Ingest(context.Background(), "acme", IngestPolicy{Source: "s", AutoRegister: true, DeviceTypeToken: "t"}, "g/n", []Sample{{Name: "t", Value: 1, Time: 1}})
	require.NoError(t, err)
	assert.Equal(t, float64(1), read("registered"))
	assert.Len(t, w.msgs, 1)
}

func TestIngesterReturnsRetryableErrors(t *testing.T) {
	// Resolve failure is retryable.
	gqlErr := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return nil, errors.New("dm down") }}
	w := &fakeWriter{}
	m, _ := newIngestMetrics()
	ing := NewIngester(NewRegistrar(gqlErr, "url"), NewEmitter(w, fixedNow), m)
	err := ing.Ingest(context.Background(), "acme", IngestPolicy{}, "g/n", []Sample{{Name: "t", Value: 1, Time: 1}})
	assert.Error(t, err)
	assert.Empty(t, w.msgs)

	// Emit failure is retryable.
	gqlOK := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return lookupHit("dev-1"), nil }}
	wErr := &fakeWriter{err: errors.New("nats down")}
	ing2 := NewIngester(NewRegistrar(gqlOK, "url"), NewEmitter(wErr, fixedNow), m)
	err = ing2.Ingest(context.Background(), "acme", IngestPolicy{}, "g/n", []Sample{{Name: "t", Value: 1, Time: 1}})
	assert.Error(t, err)
}

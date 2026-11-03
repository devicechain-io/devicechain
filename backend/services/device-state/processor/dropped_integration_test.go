// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The dropped-measurement record against a real PostgreSQL, end to end: the LA-04 payload
// {"status_text":"ok","battery":50} off the resolved-event wire leaves one battery reading and
// one drop row (status_text, undeclared_non_numeric, "ok", 1), readable through the real
// schema, with the same customer-scope visibility as the device's latest readings.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./processor/ -run Dropped -v
package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	dsgraphql "github.com/devicechain-io/dc-device-state/graphql"
	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
)

// la04Event is the event device-management publishes for the LA-04 payload against a profile
// that declares neither key, owned by owners.
func la04Event(t *testing.T, device string, at time.Time, text string, owners ...string) messaging.Message {
	t.Helper()
	anchors := make([]resolvedevent.ResolvedAnchor, 0, len(owners))
	for i, o := range owners {
		typ, tok, _ := strings.Cut(o, ":")
		anchors = append(anchors, resolvedevent.ResolvedAnchor{AnchorType: typ, AnchorToken: tok, RelationshipId: uint(i + 1)})
	}
	event := &resolvedevent.ResolvedEvent{
		Source: mqttTestSource, SourceDeviceToken: device, Anchors: anchors, EventType: eventtype.Measurement,
		OccurredTime: at, ProcessedTime: at,
		Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
			OccurredTime: at,
			Entries:      []resolvedevent.ResolvedMeasurementEntry{{Name: "battery", Value: "50"}},
			Dropped: []resolvedevent.DroppedMeasurementEntry{
				{Name: "status_text", Reason: resolvedevent.DropUndeclaredNonNumeric, Value: text}}}}},
	}
	encoded, err := resolvedevent.MarshalResolvedEvent(event)
	require.NoError(t, err)
	return messaging.Message{Subject: scopeEventsSubj, Value: encoded}
}

type droppedView struct {
	Rows []struct {
		Name      string
		Reason    string
		LastValue string
		Count     int
	} `json:"droppedMeasurements"`
	States []struct {
		DeviceToken             string
		DroppedMeasurementCount int
	} `json:"deviceStatesByDeviceToken"`
	Latest []struct{ Name string } `json:"latestMeasurements"`
}

func readDropped(t *testing.T, ctx context.Context, device string) droppedView {
	t.Helper()
	schema := gqlcore.MustParseSchema(dsgraphql.SchemaContent, &dsgraphql.SchemaResolver{})
	res := schema.Exec(ctx, `query($d: String!) {
		droppedMeasurements(deviceToken: $d) { name reason lastValue count }
		deviceStatesByDeviceToken(deviceTokens: [$d]) { deviceToken droppedMeasurementCount }
		latestMeasurements(deviceToken: $d) { name }
	}`, "", map[string]any{"d": device})
	require.Empty(t, res.Errors)
	var out droppedView
	require.NoError(t, json.Unmarshal(res.Data, &out))
	return out
}

func TestLA04PayloadLeavesOneReadingAndOneDropRow(t *testing.T) {
	sp, _, ctx := scopeFixture(t)
	t0 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	// Once per message path, once through the batch.
	sp.mergeOne(context.Background(), la04Event(t, "pump-1", t0, "ok", scopeAcmeAnchor))
	got := readDropped(t, asViewer(ctx), "pump-1")
	require.Len(t, got.Rows, 1)
	require.Equal(t, "status_text", got.Rows[0].Name)
	require.Equal(t, "UNDECLARED_NON_NUMERIC", got.Rows[0].Reason)
	require.Equal(t, "ok", got.Rows[0].LastValue)
	require.Equal(t, 1, got.Rows[0].Count)
	require.Equal(t, 1, got.States[0].DroppedMeasurementCount, "the badge counts rows")
	require.Len(t, got.Latest, 1)
	require.Equal(t, "battery", got.Latest[0].Name, "battery is stored, status_text is not")

	sp.mergeBatch(context.Background(), []pendingMerge{
		admitted(t, sp, la04Event(t, "pump-1", t0.Add(time.Second), "fault", scopeAcmeAnchor)),
		admitted(t, sp, la04Event(t, "pump-1", t0.Add(2*time.Second), "ok", scopeAcmeAnchor)),
	})
	got = readDropped(t, asViewer(ctx), "pump-1")
	require.Len(t, got.Rows, 1)
	require.Equal(t, 3, got.Rows[0].Count)
	require.Equal(t, "ok", got.Rows[0].LastValue)
	require.Equal(t, 1, got.States[0].DroppedMeasurementCount, "still one row, not three events")
}

// last_value has the visibility of LatestMeasurement.value: the owning customer and the
// unscoped operator see it, another customer, another tenant and a caller without state:read
// do not, and it follows the device when ownership moves.
func TestTheDropRecordIsVisibleToTheReadersOfTheLatestReadings(t *testing.T) {
	sp, op, ctx := scopeFixture(t)
	t0 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	sp.mergeOne(context.Background(), la04Event(t, "pump-1", t0, "secret-ish", scopeAcmeAnchor))

	require.Len(t, readDropped(t, asViewer(ctx), "pump-1").Rows, 1, "control: the operator reads it")
	require.Len(t, readDropped(t, asViewer(ctx, scopeAcmeAnchor), "pump-1").Rows, 1, "the owning customer reads it")
	require.Empty(t, readDropped(t, asViewer(ctx, scopeGlobexAnchor), "pump-1").Rows, "another customer does not")
	require.Empty(t, readDropped(t, asViewer(ctx, scopeGlobexAnchor), "pump-1").States, "nor its badge")
	require.Empty(t, readDropped(t, asViewer(core.WithTenant(ctx, "B"), scopeAcmeAnchor), "pump-1").Rows,
		"another tenant naming the same anchor does not")

	// The gate is state:read, as for the latest readings.
	schema := gqlcore.MustParseSchema(dsgraphql.SchemaContent, &dsgraphql.SchemaResolver{})
	noState := auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.DeviceRead)}})
	res := schema.Exec(noState, `query { droppedMeasurements(deviceToken: "pump-1") { name } }`, "", nil)
	require.Len(t, res.Errors, 1)
	require.Equal(t, "FORBIDDEN", res.Errors[0].Extensions["code"])
	require.NotContains(t, string(res.Data), "status_text")

	// The device is sold to Globex with no further event: the record moves with it.
	op.handle(context.Background(), ownershipFact(t, "pump-1", t0.Add(time.Minute), scopeGlobexAnchor))
	require.Empty(t, readDropped(t, asViewer(ctx, scopeAcmeAnchor), "pump-1").Rows)
	require.Len(t, readDropped(t, asViewer(ctx, scopeGlobexAnchor), "pump-1").Rows, 1)
}

// The bound on a real PostgreSQL: a 33rd distinct (key, reason) evicts the oldest last_time, the
// eviction is reported once the batch has committed, and another device is untouched.
func TestTheDropRecordIsBoundedOnPostgres(t *testing.T) {
	sp, _, ctx := scopeFixture(t)
	t0 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	var evicted int64
	sp.Api.(*model.Api).OnDroppedEvicted = func(n int64) { evicted += n }

	api := sp.Api.(*model.Api)
	var inputs []model.DroppedMeasurementInput
	for i := 0; i <= model.MaxDroppedMeasurementsPerDevice; i++ {
		inputs = append(inputs, model.DroppedMeasurementInput{Name: fmt.Sprintf("k%02d", i),
			Reason: resolvedevent.DropUndeclaredNonNumeric, Value: "v", OccurredTime: t0.Add(time.Duration(i) * time.Second)})
	}
	require.NoError(t, api.MergeProjectionBatch(context.Background(), []model.ProjectionUpdate{
		{Tenant: scopeTenant, DeviceToken: "chatty", OccurredAt: t0, Dropped: inputs},
		{Tenant: scopeTenant, DeviceToken: "quiet", OccurredAt: t0, Dropped: inputs[:1]},
	}))
	rows, err := api.DroppedMeasurementsByDeviceToken(ctx, "chatty")
	require.NoError(t, err)
	require.Len(t, rows, model.MaxDroppedMeasurementsPerDevice)
	for _, r := range rows {
		require.NotEqual(t, "k00", r.Name, "the oldest last_time is evicted")
	}
	require.EqualValues(t, 1, evicted)
	quiet, err := api.DroppedMeasurementsByDeviceToken(ctx, "quiet")
	require.NoError(t, err)
	require.Len(t, quiet, 1)

	// Negative control: a device at the bound that only refreshes a key it already has loses nothing.
	require.NoError(t, api.MergeDroppedMeasurements(ctx, "chatty", inputs[5:6]))
	rows, err = api.DroppedMeasurementsByDeviceToken(ctx, "chatty")
	require.NoError(t, err)
	require.Len(t, rows, model.MaxDroppedMeasurementsPerDevice)
	require.EqualValues(t, 1, evicted)
}

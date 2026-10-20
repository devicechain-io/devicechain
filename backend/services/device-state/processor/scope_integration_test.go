// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Members limited to part of the tenant (ADR-081) against the live-state projection, end to
// end: a resolved event off the wire stamps the device's owners, a device ownership-changed
// fact restamps them with no event, and the scoped reads are answered through the real
// schema. On a real PostgreSQL, because the scope predicate is an array overlap sqlite
// cannot evaluate.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./processor/ -run Scope -v
package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	dsgraphql "github.com/devicechain-io/dc-device-state/graphql"
	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/stretchr/testify/require"
)

const (
	scopeTenant       = "A"
	scopeEventsSubj   = "instance1.A.resolved-events"
	scopeOwnersSubj   = "instance1.A.device-ownership"
	scopeAcmeAnchor   = "customer:acme"
	scopeGlobexAnchor = "customer:globex"
)

func scopeEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// scopeFixture is a device-state on a real PostgreSQL, migrated by the service's own chain,
// with the projection's tables emptied, and a request context carrying its Api.
func scopeFixture(t *testing.T) (*StateProcessor, *OwnershipProcessor, context.Context) {
	t.Helper()
	port, err := strconv.Atoi(scopeEnv("DC_IT_PGPORT", "5432"))
	require.NoError(t, err)
	host, user, pass := scopeEnv("DC_IT_PGHOST", "localhost"), scopeEnv("DC_IT_PGUSER", "postgres"),
		scopeEnv("DC_IT_PGPASSWORD", "postgres")
	require.NoError(t, rdbtest.EnsureDatabase(context.Background(), host, port, user, pass, "dcstatescope", ""))
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: "dcstatescope", FunctionalArea: "device-state"},
		Migrations:   model.Migrations,
		InstanceConfig: config.DatastoreConfiguration{Type: "postgres95", Configuration: map[string]interface{}{
			"hostname": host, "port": port, "username": user, "password": pass,
		}},
	}
	require.NoError(t, mgr.ExecuteInitialize(context.Background()))
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	sys := mgr.Database.WithContext(core.WithSystemContext(context.Background()))
	for _, table := range []string{"device_states", "latest_measurements", "latest_locations"} {
		require.NoError(t, sys.Exec(fmt.Sprintf(`TRUNCATE TABLE "device-state".%q`, table)).Error)
	}
	api := model.NewApi(mgr)
	sp := NewStateProcessor(deviceStateMicroservice, nil, core.NewNoOpLifecycleCallbacks(), api,
		nil, nil)
	op := &OwnershipProcessor{Api: api}
	ctx := core.WithTenant(context.Background(), scopeTenant)
	ctx = context.WithValue(ctx, gqlcore.ContextRdbKey, mgr)
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, api)
	return sp, op, ctx
}

// asViewer is a member holding the read-only state and position authorities, limited to
// anchors when any are given and unscoped otherwise.
func asViewer(ctx context.Context, anchors ...string) context.Context {
	return auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess,
		Authorities:  []string{string(auth.StateRead), string(auth.LocationRead)},
		ScopeAnchors: anchors})
}

// ownedEvent is a resolved event exactly as device-management publishes it: a measurement
// and a position for device, resolved at processed with the device's owners as anchors.
func ownedEvent(t *testing.T, device string, kind eventtype.EventType, occurred, processed time.Time,
	value string, owners ...string) messaging.Message {
	t.Helper()
	anchors := make([]resolvedevent.ResolvedAnchor, 0, len(owners))
	for i, o := range owners {
		typ, tok, _ := strings.Cut(o, ":")
		anchors = append(anchors, resolvedevent.ResolvedAnchor{AnchorType: typ, AnchorToken: tok, RelationshipId: uint(i + 1)})
	}
	event := &resolvedevent.ResolvedEvent{
		Source:            mqttTestSource,
		SourceDeviceToken: device,
		Anchors:           anchors,
		EventType:         kind,
		OccurredTime:      occurred,
		ProcessedTime:     processed,
	}
	switch kind {
	case eventtype.Measurement:
		event.Payload = &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
			OccurredTime: occurred, Entries: []resolvedevent.ResolvedMeasurementEntry{{Name: "temp", Value: value}}}}}
	case eventtype.Location:
		event.Payload = &resolvedevent.ResolvedLocationsPayload{Entries: []resolvedevent.ResolvedLocationEntry{{
			OccurredTime: occurred, Latitude: str(value), Longitude: str("-87.5")}}}
	}
	encoded, err := resolvedevent.MarshalResolvedEvent(event)
	require.NoError(t, err)
	return messaging.Message{Subject: scopeEventsSubj, Value: encoded}
}

// ownershipFact is a device ownership-changed fact exactly as device-management publishes it.
func ownershipFact(t *testing.T, device string, changed time.Time, owners ...string) messaging.Message {
	t.Helper()
	encoded, err := dmproto.MarshalDeviceOwnershipChangedEvent(&dmmodel.DeviceOwnershipChangedEvent{
		DeviceToken: device, Anchors: owners, ChangedTime: changed})
	require.NoError(t, err)
	return messaging.Message{Subject: scopeOwnersSubj, Value: encoded}
}

// sees is what a caller reads of the live view: the devices whose state it is answered
// with (by token and by search), and the measurement and position it reads for each.
type sees struct {
	ByToken  []string
	Searched []string
	Temp     map[string]float64
	Lat      map[string]float64
}

func liveView(t *testing.T, ctx context.Context, devices ...string) sees {
	t.Helper()
	schema := gqlcore.MustParseSchema(dsgraphql.SchemaContent, &dsgraphql.SchemaResolver{})
	exec := func(q string, vars map[string]any, out any) {
		t.Helper()
		res := schema.Exec(ctx, q, "", vars)
		require.Empty(t, res.Errors, "%s", q)
		require.NoError(t, json.Unmarshal(res.Data, out))
	}
	tokens := make([]any, len(devices))
	for i, d := range devices {
		tokens[i] = d
	}
	got := sees{ByToken: []string{}, Searched: []string{}, Temp: map[string]float64{}, Lat: map[string]float64{}}

	var byToken struct {
		DeviceStatesByDeviceToken []struct{ DeviceToken string } `json:"deviceStatesByDeviceToken"`
	}
	exec(`query($t: [String!]!) { deviceStatesByDeviceToken(deviceTokens: $t) { deviceToken } }`,
		map[string]any{"t": tokens}, &byToken)
	for _, s := range byToken.DeviceStatesByDeviceToken {
		got.ByToken = append(got.ByToken, s.DeviceToken)
	}
	var searched struct {
		DeviceStates struct {
			Results    []struct{ DeviceToken string } `json:"results"`
			Pagination struct{ TotalRecords int }     `json:"pagination"`
		} `json:"deviceStates"`
	}
	exec(`query { deviceStates(criteria: {pageNumber: 1, pageSize: 100}) { results { deviceToken } pagination { totalRecords } } }`,
		nil, &searched)
	for _, s := range searched.DeviceStates.Results {
		got.Searched = append(got.Searched, s.DeviceToken)
	}
	require.Equal(t, len(got.Searched), searched.DeviceStates.Pagination.TotalRecords,
		"the page's total must count only the rows the caller may see")
	for _, d := range devices {
		var m struct {
			LatestMeasurements []struct {
				Name  string
				Value float64
			} `json:"latestMeasurements"`
			LatestLocation *struct{ Latitude float64 } `json:"latestLocation"`
		}
		exec(`query($d: String!) { latestMeasurements(deviceToken: $d) { name value } latestLocation(deviceToken: $d) { latitude } }`,
			map[string]any{"d": d}, &m)
		for _, r := range m.LatestMeasurements {
			got.Temp[d] = r.Value
		}
		if m.LatestLocation != nil {
			got.Lat[d] = m.LatestLocation.Latitude
		}
	}
	var locs struct {
		LatestLocations []struct{ DeviceToken string } `json:"latestLocations"`
	}
	exec(`query($t: [String!]!) { latestLocations(deviceTokens: $t) { deviceToken } }`, map[string]any{"t": tokens}, &locs)
	batch := make([]string, 0)
	for _, l := range locs.LatestLocations {
		batch = append(batch, l.DeviceToken)
	}
	single := make([]string, 0)
	for d := range got.Lat {
		single = append(single, d)
	}
	sort.Strings(batch)
	sort.Strings(single)
	require.Equal(t, single, batch, "the batch position door must answer what the single one does")
	sort.Strings(got.ByToken)
	sort.Strings(got.Searched)
	return got
}

func nothing() sees {
	return sees{ByToken: []string{}, Searched: []string{}, Temp: map[string]float64{}, Lat: map[string]float64{}}
}

// 🔑 THE SLICE'S TEST. A device moves from Acme to Globex and reports nothing afterwards:
// its live state, its latest readings and its position move with it, on the ownership
// fact alone. Before the move Acme's member sees it and Globex's does not; after it, the
// other way round. The unscoped operator, the negative control, sees it throughout.
func TestReassignmentMovesLiveStateBetweenCustomers(t *testing.T) {
	sp, op, ctx := scopeFixture(t)
	t0 := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	sp.mergeOne(context.Background(), ownedEvent(t, "pump-1", eventtype.Measurement, t0, t0.Add(time.Second), "21.5", scopeAcmeAnchor))
	sp.mergeOne(context.Background(), ownedEvent(t, "pump-1", eventtype.Location, t0, t0.Add(time.Second), "41.5", scopeAcmeAnchor))
	sp.mergeOne(context.Background(), ownedEvent(t, "valve-9", eventtype.Measurement, t0, t0.Add(time.Second), "3", scopeGlobexAnchor))

	pump := sees{ByToken: []string{"pump-1"}, Searched: []string{"pump-1"},
		Temp: map[string]float64{"pump-1": 21.5}, Lat: map[string]float64{"pump-1": 41.5}}
	valve := sees{ByToken: []string{"valve-9"}, Searched: []string{"valve-9"},
		Temp: map[string]float64{"valve-9": 3}, Lat: map[string]float64{}}
	both := sees{ByToken: []string{"pump-1", "valve-9"}, Searched: []string{"pump-1", "valve-9"},
		Temp: map[string]float64{"pump-1": 21.5, "valve-9": 3}, Lat: map[string]float64{"pump-1": 41.5}}

	require.Equal(t, both, liveView(t, asViewer(ctx), "pump-1", "valve-9"), "control: the operator sees both devices")
	require.Equal(t, pump, liveView(t, asViewer(ctx, scopeAcmeAnchor), "pump-1", "valve-9"))
	require.Equal(t, valve, liveView(t, asViewer(ctx, scopeGlobexAnchor), "pump-1", "valve-9"))

	// The move. No event follows it.
	op.handle(context.Background(), ownershipFact(t, "pump-1", t0.Add(time.Minute), scopeGlobexAnchor))

	require.Equal(t, both, liveView(t, asViewer(ctx), "pump-1", "valve-9"), "control: the operator still sees both")
	require.Equal(t, nothing(), liveView(t, asViewer(ctx, scopeAcmeAnchor), "pump-1", "valve-9"),
		"Acme no longer owns the pump, so its live view must not show it")
	require.Equal(t, both, liveView(t, asViewer(ctx, scopeGlobexAnchor), "pump-1", "valve-9"),
		"Globex owns the pump now, and sees its state with no new event")
}

// 🔴 THE ORDERING THE SLICE TURNS ON. The resolved events and the ownership facts are two
// streams, and nothing orders one against the other: an event device-management resolved
// BEFORE the move can reach device-state AFTER the fact, still carrying the old owner. It
// must not hand the device back. The event's data still lands (it is a real reading); its
// stamp does not. An event resolved after the move carries the new owner and changes
// nothing; a later move is applied; an older fact redelivered is not.
func TestALateEventResolvedBeforeTheMoveDoesNotHandTheDeviceBack(t *testing.T) {
	sp, op, ctx := scopeFixture(t)
	t0 := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	moved := t0.Add(time.Minute)
	sp.mergeOne(context.Background(), ownedEvent(t, "pump-1", eventtype.Measurement, t0, t0, "1", scopeAcmeAnchor))
	op.handle(context.Background(), ownershipFact(t, "pump-1", moved, scopeGlobexAnchor))

	// Received (and so resolved) half a minute before the move, delivered after its fact.
	sp.mergeOne(context.Background(), ownedEvent(t, "pump-1", eventtype.Measurement, t0.Add(30*time.Second),
		t0.Add(30*time.Second), "2", scopeAcmeAnchor))
	require.Equal(t, nothing(), liveView(t, asViewer(ctx, scopeAcmeAnchor), "pump-1"),
		"an event resolved before the move must not restore the old owner")
	require.Equal(t, map[string]float64{"pump-1": 2}, liveView(t, asViewer(ctx, scopeGlobexAnchor), "pump-1").Temp,
		"the late event's reading is still the device's latest")

	// Received just AFTER the move, inside the settle window, but resolved from a cache that
	// had not yet dropped the old owner (or stamped by a pod whose clock runs ahead): it must
	// not hand the device back either.
	sp.mergeOne(context.Background(), ownedEvent(t, "pump-1", eventtype.Measurement, moved.Add(10*time.Second),
		moved.Add(10*time.Second), "2", scopeAcmeAnchor))
	require.Equal(t, nothing(), liveView(t, asViewer(ctx, scopeAcmeAnchor), "pump-1"),
		"an event received inside the settle window must not restore the old owner")

	// The same through the batch path, which folds and writes in SQL.
	sp.mergeBatch(context.Background(), []pendingMerge{admitted(t, sp, ownedEvent(t, "pump-1", eventtype.Measurement,
		t0.Add(40*time.Second), t0.Add(40*time.Second), "3", scopeAcmeAnchor))})
	require.Equal(t, nothing(), liveView(t, asViewer(ctx, scopeAcmeAnchor), "pump-1"),
		"the batch path must apply the same ordering")

	// Redelivery of an older fact (Acme, from before the move) is refused.
	op.handle(context.Background(), ownershipFact(t, "pump-1", t0.Add(10*time.Second), scopeAcmeAnchor))
	require.Equal(t, nothing(), liveView(t, asViewer(ctx, scopeAcmeAnchor), "pump-1"),
		"an older ownership fact must not overwrite a newer one")

	// A second move, back to Acme, is applied.
	op.handle(context.Background(), ownershipFact(t, "pump-1", moved.Add(time.Hour), scopeAcmeAnchor))
	require.Equal(t, []string{"pump-1"}, liveView(t, asViewer(ctx, scopeAcmeAnchor), "pump-1").ByToken)
	require.Equal(t, nothing(), liveView(t, asViewer(ctx, scopeGlobexAnchor), "pump-1"))

	// A fact can be LOST (it is at-most-once): the next event resolved well after the last
	// applied fact repairs the stamp on its own. Here the device was sold to Globex and the
	// fact never arrived.
	repairedAt := moved.Add(2 * time.Hour)
	sp.mergeOne(context.Background(), ownedEvent(t, "pump-1", eventtype.Measurement, repairedAt, repairedAt, "4", scopeGlobexAnchor))
	require.Equal(t, nothing(), liveView(t, asViewer(ctx, scopeAcmeAnchor), "pump-1"))
	require.Equal(t, map[string]float64{"pump-1": 4}, liveView(t, asViewer(ctx, scopeGlobexAnchor), "pump-1").Temp)
}

// A member with a scope that names nobody sees nothing; a device owned by nobody is seen
// by no scoped member; a member of another tenant naming the same anchor sees nothing of
// this tenant's.
func TestUnownedAndForeignStateIsInvisibleToScopedMembers(t *testing.T) {
	sp, op, ctx := scopeFixture(t)
	t0 := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	sp.mergeOne(context.Background(), ownedEvent(t, "pump-1", eventtype.Measurement, t0, t0, "1", scopeAcmeAnchor))
	sp.mergeOne(context.Background(), ownedEvent(t, "loose-1", eventtype.Measurement, t0, t0, "1"))
	op.handle(context.Background(), ownershipFact(t, "pump-2", t0, scopeAcmeAnchor)) // no row: nothing to stamp

	require.Equal(t, []string{"loose-1", "pump-1"}, liveView(t, asViewer(ctx), "pump-1", "loose-1").ByToken)
	require.Equal(t, []string{"pump-1"}, liveView(t, asViewer(ctx, scopeAcmeAnchor), "pump-1", "loose-1", "pump-2").ByToken)
	require.Equal(t, nothing(), liveView(t, asViewer(ctx, "customer:initech"), "pump-1", "loose-1"))
	other := core.WithTenant(ctx, "B")
	require.Equal(t, nothing(), liveView(t, asViewer(other, scopeAcmeAnchor), "pump-1", "loose-1"))

	// A device sold to nobody leaves every scoped view.
	op.handle(context.Background(), ownershipFact(t, "pump-1", t0.Add(time.Minute)))
	require.Equal(t, nothing(), liveView(t, asViewer(ctx, scopeAcmeAnchor), "pump-1"))
}

// 🔴 location:read STAYS OUTSIDE THE BASELINE FOR A SCOPED MEMBER TOO. Opting the position
// doors in to scoped callers must not turn state:read into a position grant: a member of
// Acme holding the read-only baseline is refused the position of a device Acme owns.
func TestScopedViewerWithoutLocationReadSeesNoPosition(t *testing.T) {
	sp, _, ctx := scopeFixture(t)
	t0 := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	sp.mergeOne(context.Background(), ownedEvent(t, "pump-1", eventtype.Location, t0, t0, "41.5", scopeAcmeAnchor))
	member := auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.StateRead), string(auth.DeviceRead), string(auth.EventRead)}, ScopeAnchors: []string{scopeAcmeAnchor}})
	schema := gqlcore.MustParseSchema(dsgraphql.SchemaContent, &dsgraphql.SchemaResolver{})
	for _, q := range []string{
		`query { latestLocation(deviceToken: "pump-1") { latitude } }`,
		`query { latestLocations(deviceTokens: ["pump-1"]) { latitude } }`,
	} {
		res := schema.Exec(member, q, "", nil)
		require.Len(t, res.Errors, 1, q)
		require.Equal(t, "FORBIDDEN", res.Errors[0].Extensions["code"], q)
		require.NotContains(t, string(res.Data), "41.5", q)
	}
	// ...while the same member reads the device's state.
	res := schema.Exec(member, `query { deviceStatesByDeviceToken(deviceTokens: ["pump-1"]) { deviceToken } }`, "", nil)
	require.Empty(t, res.Errors)
	require.Contains(t, string(res.Data), "pump-1")
}

// admitted is a message through admission, ready for mergeBatch.
func admitted(t *testing.T, sp *StateProcessor, msg messaging.Message) pendingMerge {
	t.Helper()
	p, ok := sp.admit(context.Background(), msg)
	require.True(t, ok)
	return p
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	dmgraphql "github.com/devicechain-io/dc-device-management/graphql"
	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmprocessor "github.com/devicechain-io/dc-device-management/processor"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/auth"
	dccore "github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// These tests drive the geofence wiring through the REAL seams on both sides of the service
// boundary: device-management's own Api mints the fence-set version and its own NATS publisher
// marshals the fact; this service's own consumer decodes it, its own projection holds it, and its
// own fan-out evaluates a compiled containment rule against it. The only things faked are the
// transports themselves (a capture writer standing in for the broker, an in-process schema
// execution standing in for the HTTP hop) — never a step that this slice is claiming to have built.

// ── device-management side ───────────────────────────────────────────────────────────────────

// newFenceDmApi builds a real, sqlite-backed device-management Api holding just the geofence
// tables. Everything the tests assert about fence sets — version minting, snapshot freezing,
// tenant scoping — is that Api's real behaviour, not a stub's.
func newFenceDmApi(t *testing.T) *dmmodel.Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&dmmodel.GeoFence{}, &dmmodel.GeoFenceSetVersion{}, &dmmodel.GeoFenceGeometryBlob{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return dmmodel.NewApi(&rdb.RdbManager{Database: db})
}

// fenceBox is a POLYGON_2D envelope for an axis-aligned lon/lat box, in the platform's stored
// geometry shape.
func fenceBox(lonMin, latMin, lonMax, latMax float64) string {
	raw, err := json.Marshal(map[string]any{
		"kind": geofence.KindPolygon2D,
		"geometry": map[string]any{
			"type": "Polygon",
			"coordinates": [][][2]float64{{
				{lonMin, latMin}, {lonMax, latMin}, {lonMax, latMax}, {lonMin, latMax}, {lonMin, latMin},
			}},
		},
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// fenceFactWriter captures the bytes device-management's real publisher would have written to the
// geofence-set subject, so a test can hand them to this service's real consumer unchanged.
type fenceFactWriter struct {
	payloads [][]byte
}

func (w *fenceFactWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	for _, m := range msgs {
		w.payloads = append(w.payloads, m.Value)
	}
	return nil
}
func (w *fenceFactWriter) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}
func (w *fenceFactWriter) HandleResponse(error) {}

// fenceFactMsg wraps a captured fact payload as a consumed message on the tenant's geofence-set
// subject — the subject shape the consumer parses the tenant out of.
func fenceFactMsg(tenant string, payload []byte, ack messaging.Acknowledger) messaging.Message {
	return messaging.NewConsumedMessage("dc."+tenant+"."+streams.GeoFenceSet, payload, 0, nil, ack)
}

// ── the fetch path, over device-management's REAL GraphQL schema ─────────────────────────────

// schemaFenceSource resolves frozen fence sets by EXECUTING this service's own queries against
// device-management's real parsed schema and resolvers, then decoding the response through this
// service's own response types.
//
// It stands in for the service-token HTTP hop and for nothing else: the query strings, the schema,
// the resolvers, the authority gate, the tenant scoping, the JSON field mapping and the geometry
// compile are all the production ones. That is deliberate — every one of those is a place the two
// services can disagree without anything failing to build, and a hand-written fixture response
// would measure the fixture instead.
type schemaFenceSource struct {
	t   *testing.T
	api *dmmodel.Api
	// versionsAsked records every version resolved through the source, so a test can prove the
	// preview really went to the archive rather than answering from a set it already had.
	versionsAsked []int32
	// pagesServed / bytesServed record how many GraphQL responses the source produced and how
	// large the largest one was, so a test about the response-size cap can show it actually paged
	// rather than trusting that a big fence set was big.
	pagesServed  int
	bytesServed  int
	largestBytes int
	// refusals counts responses this source refused for exceeding the read cap, so a test can
	// prove the halving retry was actually provoked rather than assuming a large fixture
	// reached it.
	refusals int
}

// dmContext builds the request context device-management's GraphQL layer expects: the Api, the
// caller's tenant, and the device:read authority the fence doors gate on. The tenant is what the
// service-token client would carry in its tenant header.
func (s *schemaFenceSource) dmContext(tenant string) context.Context {
	ctx := dccore.WithTenant(context.Background(), tenant)
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, s.api)
	return auth.WithClaims(ctx, &auth.Claims{Tenant: tenant, Authorities: []string{string(auth.DeviceRead)}})
}

// exec runs one query against device-management's schema and unmarshals its data into out. It
// satisfies fenceSetExec, which is what lets the two methods below run the PRODUCTION paging
// loops — the page walk, the stitching and the completeness check are the shipped code here, not
// a test reimplementation of it, so a defect in the loop fails these tests.
//
// It also records every page it serves, so a test can prove a set really was read in more than
// one round trip rather than assuming the cap it is about was ever approached.
func (s *schemaFenceSource) exec(_ context.Context, tenant, query string, vars map[string]any, out any) error {
	s.t.Helper()
	s.pagesServed++
	schema := gqlcore.MustParseSchema(dmgraphql.SchemaContent, &dmgraphql.SchemaResolver{})
	resp := schema.Exec(s.dmContext(tenant), query, "", vars)
	if len(resp.Errors) > 0 {
		return resp.Errors[0]
	}
	s.bytesServed += len(resp.Data)
	if len(resp.Data) > s.largestBytes {
		s.largestBytes = len(resp.Data)
	}
	// 🔴 IT ENFORCES THE READ CAP, because the cap is half of what is under test. The
	// production transport is svcclient, which refuses a response over MaxResponseBytes and
	// says so as ErrResponseTooLarge; a test transport that happily returned 1.5 MB would let
	// a paging loop that cannot survive the cap pass every test in this package. Standing in
	// for the HTTP hop means standing in for its limits too, not just its shape.
	if len(resp.Data) > svcclient.MaxResponseBytes {
		s.refusals++
		return fmt.Errorf("%w: %d bytes", svcclient.ErrResponseTooLarge, len(resp.Data))
	}
	return json.Unmarshal(resp.Data, out)
}

func (s *schemaFenceSource) FenceSetAt(ctx context.Context, tenant string, version int32) (*geofence.FenceSet, error) {
	s.versionsAsked = append(s.versionsAsked, version)
	return fetchSnapshotAt(ctx, s.exec, tenant, version)
}

func (s *schemaFenceSource) CurrentFenceSet(ctx context.Context, tenant string) (*geofence.FenceSet, error) {
	return fetchCurrentSnapshot(ctx, s.exec, tenant)
}

var (
	_ runtime.FenceSetSource        = (*schemaFenceSource)(nil)
	_ runtime.CurrentFenceSetSource = (*schemaFenceSource)(nil)
)

// ── event-processing side ────────────────────────────────────────────────────────────────────

// fenceRuleReg builds a registry with one threshold rule whose leaf is pure containment of the
// named fence — compiled through the production compiler, so RequiresPosition and the CEL leaf are
// whatever a real authored rule would get.
func fenceRuleReg(t *testing.T, tenant, profileVersion, fenceToken string) *runtime.RuleRegistry {
	t.Helper()
	cr, err := rules0.Compile(rules0.Rule{
		ID:   runtime.ComposeRuleID(tenant, "inyard"),
		Name: "in the yard",
		Type: rules0.TypeThreshold,
		When: rules0.Condition{CEL: `geo.inFence("` + fenceToken + `")`},
	}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile fence rule: %v", err)
	}
	return runtime.NewRuleRegistry([]runtime.ScopedRule{
		{Tenant: tenant, ProfileVersionToken: profileVersion, Compiled: cr},
	})
}

// locatedMsg builds a consumed resolved LOCATION message stamped with a fence-set version.
func locatedMsg(t *testing.T, seq uint64, tenant, device, profileVersion string, fenceSetVersion int32,
	lon, lat float64, ack messaging.Acknowledger) messaging.Message {
	t.Helper()
	occurred := testBase.Add(time.Duration(seq) * time.Second)
	lonS := strconv.FormatFloat(lon, 'f', -1, 64)
	latS := strconv.FormatFloat(lat, 'f', -1, 64)
	ev := &dmmodel.ResolvedEvent{
		Source:              "mqtt1",
		SourceDeviceToken:   device,
		ProfileVersionToken: profileVersion,
		OccurredTime:        occurred,
		ProcessedTime:       occurred,
		EventType:           esmodel.Location,
		FenceSetVersion:     fenceSetVersion,
		Payload: &dmmodel.ResolvedLocationsPayload{Entries: []dmmodel.ResolvedLocationEntry{
			{Latitude: &latS, Longitude: &lonS},
		}},
	}
	b, err := dmproto.MarshalResolvedEvent(ev)
	if err != nil {
		t.Fatalf("marshal resolved event: %v", err)
	}
	m := messaging.NewConsumedMessage("dc."+tenant+".resolved-events", b, 0, nil, ack)
	m.StreamSeq = seq
	return m
}

// newFenceProcessor builds a processor with a live fence projection, a fence rule, and an engine
// restored from an empty store — i.e. the real DETECT loop's collaborators, driven by hand.
func newFenceProcessor(t *testing.T, reg *runtime.RuleRegistry, w *captureWriter,
	source runtime.CurrentFenceSetSource) *ResolvedEventsProcessor {
	t.Helper()
	rp := &ResolvedEventsProcessor{
		Store: newTestStore(t),
		cfg: Config{
			PartitionId:        "singleton",
			CheckpointEvents:   100,
			CheckpointInterval: time.Hour,
			TickInterval:       time.Hour,
			Clock:              detectcore.RealClock{},
		},
		registry:     reg,
		publisher:    runtime.NewPublisher(w, reg, (*detectMetrics)(nil)),
		clock:        detectcore.RealClock{},
		procCtx:      context.Background(),
		fenceUpdates: make(chan fenceUpdate, 8),
		FenceSets:    source,
	}
	if err := rp.restore(context.Background()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	return rp
}

// drainFenceUpdates applies whatever the consumer marshalled onto the loop, standing in for the
// loop's own `case fu := <-rp.fenceUpdates` branch. It applies through the real applyFenceSet, so
// what lands in the view is what the running loop would install.
func drainFenceUpdates(rp *ResolvedEventsProcessor) int {
	n := 0
	for {
		select {
		case fu := <-rp.fenceUpdates:
			rp.applyFenceSet(fu)
			n++
		default:
			return n
		}
	}
}

// ── 1. the live feed, end to end ─────────────────────────────────────────────────────────────

// A fence authored in device-management is evaluable by the DETECT engine, with nothing between
// the two but the fact.
//
// The chain is entirely real: Api.CreateGeoFence mints the version and freezes the set →
// GeoFenceSetWriter marshals the fact → the consumer decodes and compiles it → the loop installs it
// → a resolved location event stamped with that version fires the rule and publishes a derived
// event. Before the fact arrives the same event fires NOTHING, which is the control: it proves the
// firing afterwards came from the fact and not from a projection that was already populated.
func TestFenceEditReachesTheDetectEngineEndToEnd(t *testing.T) {
	ctx := context.Background()
	api := newFenceDmApi(t)
	dmCtx := dccore.WithTenant(ctx, "acme")
	factWriter := &fenceFactWriter{}
	api.GeoFenceSetPublisher = dmprocessor.NewGeoFenceSetWriter(factWriter, &fenceFactWriter{}, 0, nil, nil)

	w := &captureWriter{}
	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), w, nil)
	rp.fenceView = runtime.NewFenceSetView()

	// CONTROL: with the projection empty, an event stamped version 1 cannot be evaluated at all.
	rp.handle(locatedMsg(t, 1, "acme", "d1", "p@1", 1, 0.5, 0.5, &fakeAck{}))
	rp.checkpoint(ctx)
	if w.writes != 0 {
		t.Fatalf("control: a fence rule fired with NOTHING in the projection (%d derived writes); "+
			"the rest of this test would prove nothing", w.writes)
	}

	// Author the fence. This is the whole live path: mint → freeze → publish.
	if _, err := api.CreateGeoFence(dmCtx, &dmmodel.GeoFenceCreateRequest{
		Token: "yard", Geometry: fenceBox(0, 0, 1, 1)}); err != nil {
		t.Fatalf("create geofence: %v", err)
	}
	if len(factWriter.payloads) != 1 {
		t.Fatalf("device-management published %d fence-set facts, want 1 — the live feed is not "+
			"connected at the producing end", len(factWriter.payloads))
	}

	// Consume the fact exactly as the running consumer does, then let the loop install it.
	ack := &fakeAck{}
	if !rp.handleFenceSetFact(fenceFactMsg("acme", factWriter.payloads[0], ack)) {
		t.Fatal("handleFenceSetFact reported shutdown")
	}
	if drainFenceUpdates(rp) != 1 {
		t.Fatal("the consumer marshalled no fence set onto the loop")
	}
	if ack.acks != 1 {
		t.Errorf("the fact was acked %d times, want 1", ack.acks)
	}
	if held := rp.fenceView.RetainedVersions("acme"); len(held) != 1 || held[0] != 1 {
		t.Fatalf("the projection holds versions %v, want [1]", held)
	}

	// The SAME event now fires: the device is inside the yard that was just authored.
	rp.handle(locatedMsg(t, 2, "acme", "d1", "p@1", 1, 0.5, 0.5, &fakeAck{}))
	rp.checkpoint(ctx)
	if w.writes != 1 {
		t.Fatalf("after the fence fact reached the projection the rule published %d derived events, want 1",
			w.writes)
	}
}

// A fence EDIT reaches the engine too, and the engine keeps answering the superseded version
// correctly. The pair matters: an implementation that replaced the tenant's set on every fact
// (rather than filing it by version) would pass the first half and fail the second, and that is
// precisely the determinism break the version stamp exists to prevent.
func TestFenceEditsAreFiledByVersionNotReplaced(t *testing.T) {
	ctx := context.Background()
	api := newFenceDmApi(t)
	dmCtx := dccore.WithTenant(ctx, "acme")
	factWriter := &fenceFactWriter{}
	api.GeoFenceSetPublisher = dmprocessor.NewGeoFenceSetWriter(factWriter, &fenceFactWriter{}, 0, nil, nil)

	if _, err := api.CreateGeoFence(dmCtx, &dmmodel.GeoFenceCreateRequest{
		Token: "yard", Geometry: fenceBox(0, 0, 1, 1)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Move the yard far away — version 2.
	if _, err := api.UpdateGeoFence(dmCtx, "yard", &dmmodel.GeoFenceCreateRequest{
		Token: "yard", Geometry: fenceBox(10, 10, 11, 11)}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(factWriter.payloads) != 2 {
		t.Fatalf("device-management published %d facts, want 2 (create + edit)", len(factWriter.payloads))
	}

	w := &captureWriter{}
	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), w, nil)
	rp.fenceView = runtime.NewFenceSetView()
	for _, payload := range factWriter.payloads {
		if !rp.handleFenceSetFact(fenceFactMsg("acme", payload, &fakeAck{})) {
			t.Fatal("handleFenceSetFact reported shutdown")
		}
	}
	drainFenceUpdates(rp)

	// A device standing still at (0.5, 0.5): inside the OLD yard, far outside the new one.
	rp.handle(locatedMsg(t, 1, "acme", "d1", "p@1", 1, 0.5, 0.5, &fakeAck{}))
	rp.checkpoint(ctx)
	if w.writes != 1 {
		t.Fatalf("an event stamped with the SUPERSEDED version 1 published %d derived events, want 1: "+
			"it must still be evaluated against the geometry that was live when it happened", w.writes)
	}

	// The same position stamped with version 2 is OUTSIDE the moved yard. It is sent from a
	// DIFFERENT device so the series is fresh: a non-match on a series that has never raised emits
	// no edge at all, where re-using d1 would emit a resolve and make "something was published"
	// ambiguous between the two.
	before := w.writes
	rp.handle(locatedMsg(t, 2, "acme", "d2", "p@1", 2, 0.5, 0.5, &fakeAck{}))
	rp.checkpoint(ctx)
	if w.writes != before {
		t.Errorf("an event stamped with version 2 fired against the MOVED yard (%d → %d writes)",
			before, w.writes)
	}
}

// A fact claiming to BE version 0 is dropped and acked, never filed. 0 is the reserved "this
// tenant had never created a fence" stamp, which the projection answers as a known-EMPTY set
// without consulting its map; filing a producer-invented set under it would overwrite knowledge
// with a claim. The control is the well-formed fact in the same test, which IS filed.
func TestFenceSetFactWithANonPositiveVersionIsDropped(t *testing.T) {
	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), &captureWriter{}, nil)
	rp.fenceView = runtime.NewFenceSetView()

	bad, err := dmmodel.MarshalGeoFenceSetMintedEvent(&dmmodel.GeoFenceSetMintedEvent{Version: 0})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ack := &fakeAck{}
	if !rp.handleFenceSetFact(fenceFactMsg("acme", bad, ack)) {
		t.Fatal("handleFenceSetFact reported shutdown")
	}
	if n := drainFenceUpdates(rp); n != 0 {
		t.Errorf("a version-0 fact was marshalled onto the loop %d times, want 0", n)
	}
	if ack.acks != 1 {
		t.Errorf("a poison fact must be acked so it stops redelivering; acks=%d", ack.acks)
	}

	// Control: a well-formed fact from the same path IS filed.
	good, err := dmmodel.MarshalGeoFenceSetMintedEvent(&dmmodel.GeoFenceSetMintedEvent{
		Version: 1, Fences: []dmmodel.GeoFenceSnapshotRef{{Token: "yard", Geometry: []byte(fenceBox(0, 0, 1, 1))}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !rp.handleFenceSetFact(fenceFactMsg("acme", good, &fakeAck{})) {
		t.Fatal("handleFenceSetFact reported shutdown")
	}
	if n := drainFenceUpdates(rp); n != 1 {
		t.Fatalf("control: a well-formed fact was marshalled onto the loop %d times, want 1", n)
	}
}

// ── 2. the startup reconcile ─────────────────────────────────────────────────────────────────

// The startup reconcile seeds the projection from device-management's archive, so a processor that
// has received no fact at all can still evaluate a fence rule.
//
// This is the half a live feed alone cannot supply: the fact stream carries only changes from now
// on, so without this a restarted engine is blind to every existing fence until somebody happens to
// edit one. The processor here consumes NO fact — if the reconcile does not run, nothing else can
// populate the view.
func TestStartFenceViewSeedsFromTheArchive(t *testing.T) {
	ctx := context.Background()
	api := newFenceDmApi(t)
	dmCtx := dccore.WithTenant(ctx, "acme")
	if _, err := api.CreateGeoFence(dmCtx, &dmmodel.GeoFenceCreateRequest{
		Token: "yard", Geometry: fenceBox(0, 0, 1, 1)}); err != nil {
		t.Fatalf("create: %v", err)
	}

	src := &schemaFenceSource{t: t, api: api}
	w := &captureWriter{}
	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), w, src)
	if err := rp.startFenceView(ctx); err != nil {
		t.Fatalf("startFenceView: %v", err)
	}
	if rp.fenceView == nil {
		t.Fatal("a wired source must build the projection")
	}
	if held := rp.fenceView.RetainedVersions("acme"); len(held) != 1 || held[0] != 1 {
		t.Fatalf("the reconcile seeded versions %v, want [1]", held)
	}

	// And it is genuinely evaluable, not merely present.
	rp.handle(locatedMsg(t, 1, "acme", "d1", "p@1", 1, 0.5, 0.5, &fakeAck{}))
	rp.checkpoint(ctx)
	if w.writes != 1 {
		t.Errorf("a seeded fence set did not evaluate: %d derived writes, want 1", w.writes)
	}
}

// With no source wired the projection stays nil, which is the honest state: every containment call
// then reports an unresolvable fence set (counted) instead of the invisible lie a view seeded with
// nothing would tell.
func TestStartFenceViewWithNoSourceLeavesTheProjectionDisabled(t *testing.T) {
	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), &captureWriter{}, nil)
	if err := rp.startFenceView(context.Background()); err != nil {
		t.Fatalf("startFenceView: %v", err)
	}
	if rp.fenceView != nil {
		t.Errorf("no fence source must leave the projection nil, got %v", rp.fenceView)
	}
}

// A processor that RESTARTS still evaluates a fence rule correctly, having consumed no fact.
//
// The first processor learns the fence from the live fact; the second is a fresh object with an
// empty projection — exactly what a pod restart produces, since the projection is memory and the
// fact stream's already-acked history does not redeliver. Only the reconcile can make the second
// one work.
func TestRestartedProcessorStillEvaluatesAFenceRule(t *testing.T) {
	ctx := context.Background()
	api := newFenceDmApi(t)
	dmCtx := dccore.WithTenant(ctx, "acme")
	factWriter := &fenceFactWriter{}
	api.GeoFenceSetPublisher = dmprocessor.NewGeoFenceSetWriter(factWriter, &fenceFactWriter{}, 0, nil, nil)
	if _, err := api.CreateGeoFence(dmCtx, &dmmodel.GeoFenceCreateRequest{
		Token: "yard", Geometry: fenceBox(0, 0, 1, 1)}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Incarnation 1: learns the fence from the live fact and fires.
	src := &schemaFenceSource{t: t, api: api}
	w1 := &captureWriter{}
	first := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), w1, src)
	first.fenceView = runtime.NewFenceSetView()
	if !first.handleFenceSetFact(fenceFactMsg("acme", factWriter.payloads[0], &fakeAck{})) {
		t.Fatal("handleFenceSetFact reported shutdown")
	}
	drainFenceUpdates(first)
	first.handle(locatedMsg(t, 1, "acme", "d1", "p@1", 1, 0.5, 0.5, &fakeAck{}))
	first.checkpoint(ctx)
	if w1.writes != 1 {
		t.Fatalf("control (pre-restart): %d derived writes, want 1", w1.writes)
	}

	// Incarnation 2: a brand-new processor. Nothing carries over but device-management's archive.
	w2 := &captureWriter{}
	second := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), w2, src)
	if err := second.startFenceView(ctx); err != nil {
		t.Fatalf("startFenceView after restart: %v", err)
	}
	second.handle(locatedMsg(t, 1, "acme", "d1", "p@1", 1, 0.5, 0.5, &fakeAck{}))
	second.checkpoint(ctx)
	if w2.writes != 1 {
		t.Errorf("a restarted processor published %d derived events for an event inside the fence, want 1: "+
			"it is blind to every existing fence until somebody edits one", w2.writes)
	}
}

// The reconcile reads only the tenants that actually have a fence-referencing rule. A tenant whose
// rules never call inFence cannot produce a containment miss, so seeding it would be a cross-service
// round trip per restart that buys nothing.
func TestReconcileSeedsOnlyFenceRuleTenants(t *testing.T) {
	thr := 80.0
	plain, err := rules0.Compile(rules0.Rule{
		ID:   runtime.ComposeRuleID("globex", "hot"),
		Name: "hot",
		Type: rules0.TypeThreshold,
		When: rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &thr},
	}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	fenceRules := fenceRuleReg(t, "acme", "p@1", "yard")
	fenceRules.Upsert(runtime.ScopedRule{Tenant: "globex", ProfileVersionToken: "p@1", Compiled: plain})

	tenants := fenceRules.TenantsWithFenceRules()
	if len(tenants) != 1 || tenants[0] != "acme" {
		t.Errorf("TenantsWithFenceRules() = %v, want [acme]: only a rule that calls inFence needs a "+
			"fence set, and globex's threshold rule does not", tenants)
	}
}

// ── 3. a version the projection does not hold: both halves ───────────────────────────────────

// THE HOT PATH. An event naming a version the projection does not hold is a COUNTED eval error and
// feeds the engine nothing — it must never become the answer "outside", which for a Duration rule
// cancels an in-flight hold and for every kind reads as a healthy, quiet rule.
//
// The control is the same rule, same device, same position, stamped with the version the projection
// DOES hold: it fires. Without it, "nothing was published" would pass against an engine that never
// ran at all.
func TestHotPathMissIsACountedErrorNotOutside(t *testing.T) {
	ctx := context.Background()
	reg := fenceRuleReg(t, "acme", "p@1", "yard")
	w := &captureWriter{}
	rp := newFenceProcessor(t, reg, w, nil)
	rp.fenceView = runtime.NewFenceSetView()
	rp.fenceView.Put("acme", boxFenceSetFor(2, "yard", 0, 0, 1, 1))

	// Stamped 1 — superseded and never seeded. The device IS inside the box the view holds under
	// version 2, so an implementation that reached for "the tenant's current set" would fire here.
	missEvent := locatedEvent(t, "d1", "p@1", 1, 0.5, 0.5)

	// The resolution itself must answer NOTHING, not an empty set. Both produce an eval error for a
	// rule that names a fence, so the counter alone cannot tell them apart — but they are different
	// claims: nil says "this fence set is unknowable", while a known-empty set says "the tenant had
	// no fences at that version", which is a statement about history nobody here can make.
	if got := rp.fenceView.ForEvent("acme", missEvent); got != nil {
		t.Fatalf("a version the projection does not hold resolved to a set (version %d, %d fences); "+
			"a miss must be nil so it reads as unknowable rather than as 'there were no fences'",
			got.Version(), got.Len())
	}

	miss := rp.registry.Plan(1, "acme", missEvent, testBase, nil, rp.fenceView.ForEvent("acme", missEvent))
	if len(miss.Events) != 0 {
		t.Errorf("an unresolvable fence set fed %d events to the engine, want 0", len(miss.Events))
	}
	if miss.EvalErrors != 1 {
		t.Errorf("eval errors = %d, want 1: a miss must be COUNTED, not silently answered 'outside'",
			miss.EvalErrors)
	}

	// Through the whole processor: the message is applied and acked, and nothing is published.
	rp.handle(locatedMsg(t, 1, "acme", "d1", "p@1", 1, 0.5, 0.5, &fakeAck{}))
	rp.checkpoint(ctx)
	if w.writes != 0 {
		t.Errorf("an event naming an unheld fence-set version published %d derived events, want 0", w.writes)
	}

	// CONTROL: the held version answers, and the rule fires.
	hit := rp.registry.Plan(2, "acme", locatedEvent(t, "d1", "p@1", 2, 0.5, 0.5),
		testBase, nil, rp.fenceView.ForEvent("acme", locatedEvent(t, "d1", "p@1", 2, 0.5, 0.5)))
	if len(hit.Events) != 1 || !hit.Events[0].Match || hit.EvalErrors != 0 {
		t.Fatalf("control: the held version did not evaluate (%d events, errors %d)",
			len(hit.Events), hit.EvalErrors)
	}
	rp.handle(locatedMsg(t, 2, "acme", "d1", "p@1", 2, 0.5, 0.5, &fakeAck{}))
	rp.checkpoint(ctx)
	if w.writes != 1 {
		t.Fatalf("control: the held version published %d derived events, want 1", w.writes)
	}
}

// THE PREVIEW/REPLAY PATH. The same unheld version resolves correctly — through the source, off the
// loop — and evaluates against the geometry that was live THEN.
//
// The fence has been moved since, so the two versions disagree about the answer: version 1 says the
// device is inside, version 2 says it is outside. Resolving version 1 and getting "inside" is
// therefore a statement that the historical set was really fetched, not that any fence set was.
func TestPreviewResolvesAVersionTheProjectionDoesNotHold(t *testing.T) {
	ctx := context.Background()
	api := newFenceDmApi(t)
	dmCtx := dccore.WithTenant(ctx, "acme")
	if _, err := api.CreateGeoFence(dmCtx, &dmmodel.GeoFenceCreateRequest{
		Token: "yard", Geometry: fenceBox(0, 0, 1, 1)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := api.UpdateGeoFence(dmCtx, "yard", &dmmodel.GeoFenceCreateRequest{
		Token: "yard", Geometry: fenceBox(10, 10, 11, 11)}); err != nil {
		t.Fatalf("update: %v", err)
	}

	src := &schemaFenceSource{t: t, api: api}
	reg := fenceRuleReg(t, "acme", "p@1", "yard")

	// The live projection holds only the CURRENT version, as the reconcile would have left it.
	view := runtime.NewFenceSetView()
	current, err := src.CurrentFenceSet(ctx, "acme")
	if err != nil {
		t.Fatalf("current fence set: %v", err)
	}
	view.Put("acme", current)
	if got := view.For("acme", 1); got != nil {
		t.Fatal("precondition: the projection must NOT hold version 1 for this test to mean anything")
	}

	// The off-loop resolver — what a preview or a replay uses — reaches the archive.
	loader := runtime.NewLoadingFenceSets(src, "acme")
	historical := loader.Resolve(ctx, 1)
	if historical == nil {
		t.Fatal("the preview path could not resolve version 1 from device-management's archive")
	}
	if len(src.versionsAsked) != 1 || src.versionsAsked[0] != 1 {
		t.Errorf("versions read from the archive = %v, want [1]", src.versionsAsked)
	}

	ev := locatedEvent(t, "d1", "p@1", 1, 0.5, 0.5)
	res := reg.Plan(1, "acme", ev, testBase, nil, historical)
	if len(res.Events) != 1 || !res.Events[0].Match {
		t.Fatalf("previewing version 1 did not put the device inside the yard as it stood THEN "+
			"(%d events, errors %d)", len(res.Events), res.EvalErrors)
	}

	// CONTROL: the same position against the CURRENT set is outside, so the "inside" above is a
	// statement about the historical geometry rather than about any fence set at all.
	now := reg.Plan(2, "acme", locatedEvent(t, "d1", "p@1", current.Version(), 0.5, 0.5),
		testBase, nil, current)
	if len(now.Events) != 1 || now.Events[0].Match {
		t.Fatalf("control: the CURRENT set should place the device outside the moved yard "+
			"(%d events, matched %v)", len(now.Events), len(now.Events) > 0 && now.Events[0].Match)
	}
}

// ── helpers shared by the miss/preview tests ─────────────────────────────────────────────────

// locatedEvent builds a resolved LOCATION event stamped with a fence-set version, carrying one fix.
func locatedEvent(t *testing.T, device, profileVersion string, fenceSetVersion int32, lon, lat float64) *dmmodel.ResolvedEvent {
	t.Helper()
	lonS := strconv.FormatFloat(lon, 'f', -1, 64)
	latS := strconv.FormatFloat(lat, 'f', -1, 64)
	return &dmmodel.ResolvedEvent{
		SourceDeviceToken:   device,
		ProfileVersionToken: profileVersion,
		OccurredTime:        testBase,
		EventType:           esmodel.Location,
		FenceSetVersion:     fenceSetVersion,
		Payload: &dmmodel.ResolvedLocationsPayload{Entries: []dmmodel.ResolvedLocationEntry{
			{Latitude: &latS, Longitude: &lonS},
		}},
	}
}

// boxFenceSetFor builds a compiled fence set of one box, for the tests that seed the projection
// directly rather than through a fact.
func boxFenceSetFor(version int32, token string, lonMin, latMin, lonMax, latMax float64) *geofence.FenceSet {
	return geofence.NewFenceSet(version, []geofence.SnapshotFence{
		{Token: token, Geometry: json.RawMessage(fenceBox(lonMin, latMin, lonMax, latMax))},
	})
}

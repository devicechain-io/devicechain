// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmprocessor "github.com/devicechain-io/dc-device-management/processor"
	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/auth"
	dccore "github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/glebarez/sqlite"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// The fact reconcile tests drive BOTH sides of the service boundary through their real code:
// device-management's own Api writes the rows and its own NATS writers marshal the facts; this
// service's own consumers persist the facts that are delivered, its own reconcile client reads
// device-management's REAL GraphQL schema and resolvers in-process, and its own stores, registry,
// engine, dead-man armer and attribute view hold the result. The only things faked are the two
// transports — a capture writer standing in for the broker (so a test chooses which facts to
// "lose"), and an in-process schema execution standing in for the HTTP hop.

// Rule definitions used across the reconcile tests.
const (
	hotRule     = `{"name":"hot","type":"threshold","when":{"metric":"temp","op":"gt","threshold":30}}`
	deadRule    = `{"name":"dead","type":"absence","timeout":"10s"}`
	dynamicRule = `{"name":"hot","type":"threshold","when":{"metric":"temp","op":"gt","thresholdAttr":"tempLimit"}}`
)

// schemaFactSource answers the reconcile client's queries: user-management's tenant listing from a
// static list, and device-management's three doors by EXECUTING them against device-management's
// real parsed schema, in-process, over the Api the test wrote through. It enforces the service
// client's response cap exactly as the HTTP hop would.
type schemaFactSource struct {
	t       *testing.T
	api     *dmmodel.Api
	tenants []string
	client  *deviceManagementFactsClient

	mu sync.Mutex
	// calls counts requests per door ("tenantTokens", "activeProfileRules", ...).
	calls map[string]int
	// failOn, when set, is asked before each request; a non-nil answer fails that request.
	failOn func(door string, call int) error
	// hold, when non-nil, blocks every request until closed — a sweep held in flight.
	hold chan struct{}
	// sweeps counts tenant listings, i.e. sweeps begun.
	sweeps atomic.Int64
}

func newSchemaFactSource(t *testing.T, api *dmmodel.Api, tenants ...string) *schemaFactSource {
	s := &schemaFactSource{t: t, api: api, tenants: tenants, calls: map[string]int{}}
	s.client = &deviceManagementFactsClient{transport: s.exec, listScope: "instance"}
	return s
}

// doorOf names the door a query addresses.
func doorOf(query string) string {
	for _, door := range []string{"tenantTokens", "activeProfileRules", "deviceRosterPage", "deviceThresholdAttributePage"} {
		if strings.Contains(query, door) {
			return door
		}
	}
	return "unknown"
}

func (s *schemaFactSource) callsTo(door string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[door]
}

func (s *schemaFactSource) exec(ctx context.Context, svc factService, tenant, query string, vars map[string]any, out any) error {
	door := doorOf(query)
	s.mu.Lock()
	s.calls[door]++
	call := s.calls[door]
	failOn, hold := s.failOn, s.hold
	s.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if failOn != nil {
		if err := failOn(door, call); err != nil {
			return err
		}
	}
	if svc == factUserManagement {
		if door != "tenantTokens" {
			return fmt.Errorf("user-management does not serve %q", door)
		}
		s.sweeps.Add(1)
		raw, _ := json.Marshal(map[string]any{"tenantTokens": s.tenants})
		return json.Unmarshal(raw, out)
	}
	resp := dmSchema().Exec(schemaDmContext(s.api, tenant), query, "", wireVariables(s.t, vars))
	if len(resp.Errors) > 0 {
		return resp.Errors[0]
	}
	if len(resp.Data) > svcclient.MaxResponseBytes {
		return fmt.Errorf("%w: %d bytes", svcclient.ErrResponseTooLarge, len(resp.Data))
	}
	return json.Unmarshal(resp.Data, out)
}

// singleConnection pins an in-memory SQLite database to ONE connection. Each connection to
// ":memory:" is a separate, empty database, so the moment two goroutines — a consumer or a sweep
// and the test — query at once, the pool opens a second connection that has no tables at all.
func singleConnection(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
}

// schemaDmContext is the request context device-management's GraphQL layer builds for a
// service-token call: the Api, the tenant from the token's tenant header, and device:read.
func schemaDmContext(api *dmmodel.Api, tenant string) context.Context {
	ctx := dccore.WithTenant(context.Background(), tenant)
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, api)
	return auth.WithClaims(ctx, &auth.Claims{Tenant: tenant, Authorities: []string{string(auth.DeviceRead)}})
}

// dmWorld is device-management's side: a real Api and the three fact writers over capture
// transports, so a test decides which facts reach this service.
type dmWorld struct {
	t      *testing.T
	api    *dmmodel.Api
	ctx    context.Context
	rules  *fenceFactWriter
	roster *fenceFactWriter
	attrs  *fenceFactWriter
}

func newDmWorld(t *testing.T) *dmWorld {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	singleConnection(t, db)
	if err := db.AutoMigrate(&dmmodel.Device{}, &dmmodel.DeviceType{}, &dmmodel.DeviceProfile{},
		&dmmodel.DeviceProfileVersion{}, &dmmodel.MetricDefinition{}, &dmmodel.CommandDefinition{},
		&dmmodel.DetectionRule{}, &dmmodel.DetectionRuleScopeRef{}, &dmmodel.EntityAttribute{},
		&dmmodel.EntityGroupMembership{}, &dmmodel.EntityGroupFacetRef{}, &dmmodel.EntityRelationship{},
		&dmmodel.EntityRelationshipType{}, &dmmodel.Alarm{}, &dmmodel.DeviceCredential{}, &dmmodel.DeviceReplacement{}); err != nil {
		t.Fatalf("migrate device-management: %v", err)
	}
	w := &dmWorld{t: t, api: dmmodel.NewApi(&rdb.RdbManager{Database: db}),
		ctx:   dccore.WithTenant(context.Background(), "acme"),
		rules: &fenceFactWriter{}, roster: &fenceFactWriter{}, attrs: &fenceFactWriter{}}
	w.api.DetectionRulesPublishedPublisher = dmprocessor.NewDetectionRulesPublishedWriter(w.rules, nil)
	w.api.DeviceRosterPublisher = dmprocessor.NewDeviceRosterWriter(w.roster, nil)
	w.api.DeviceAttributePublisher = dmprocessor.NewDeviceAttributeWriter(w.attrs, nil)
	return w
}

// profile creates a profile carrying the given enabled rules (token → definition).
func (w *dmWorld) profile(token string, rules map[string]string) {
	w.t.Helper()
	if _, err := w.api.CreateDeviceProfile(w.ctx, &dmmodel.DeviceProfileCreateRequest{Token: token}); err != nil {
		w.t.Fatalf("create profile: %v", err)
	}
	for ruleToken, def := range rules {
		w.rule(token, ruleToken, def)
	}
}

// rule adds (or replaces the definition of) one enabled rule on a profile's draft.
func (w *dmWorld) rule(profileToken, ruleToken, def string) {
	w.t.Helper()
	profiles, err := w.api.DeviceProfilesByToken(w.ctx, []string{profileToken})
	if err != nil || len(profiles) != 1 {
		w.t.Fatalf("profile %q: %v", profileToken, err)
	}
	db := func() *gorm.DB { return w.api.RDB.DB(w.ctx) }
	var existing dmmodel.DetectionRule
	if db().Where("device_profile_id = ? AND token = ?", profiles[0].ID, ruleToken).Limit(1).Find(&existing).RowsAffected == 1 {
		if err := db().Model(&existing).UpdateColumn("definition", datatypes.JSON(def)).Error; err != nil {
			w.t.Fatalf("update rule: %v", err)
		}
		return
	}
	dr := &dmmodel.DetectionRule{DeviceProfileId: profiles[0].ID, Definition: datatypes.JSON(def), Enabled: true}
	dr.Token = ruleToken
	if err := db().Create(dr).Error; err != nil {
		w.t.Fatalf("create rule: %v", err)
	}
}

func (w *dmWorld) publish(profileToken string) {
	w.t.Helper()
	if _, err := w.api.PublishDeviceProfile(w.ctx, profileToken, nil, nil, "test"); err != nil {
		w.t.Fatalf("publish %q: %v", profileToken, err)
	}
}

func (w *dmWorld) rollback(profileToken string, version int32) {
	w.t.Helper()
	if _, err := w.api.RollbackDeviceProfile(w.ctx, profileToken, version); err != nil {
		w.t.Fatalf("rollback %q to %d: %v", profileToken, version, err)
	}
}

func (w *dmWorld) deviceType(token, profileToken string) {
	w.t.Helper()
	req := &dmmodel.DeviceTypeCreateRequest{Token: token}
	if profileToken != "" {
		req.ProfileToken = &profileToken
	}
	if _, err := w.api.CreateDeviceType(w.ctx, req); err != nil {
		w.t.Fatalf("create type: %v", err)
	}
}

func (w *dmWorld) device(token, typeToken string) *dmmodel.Device {
	w.t.Helper()
	d, err := w.api.CreateDevice(w.ctx, &dmmodel.DeviceCreateRequest{Token: token, DeviceTypeToken: typeToken})
	if err != nil {
		w.t.Fatalf("create device: %v", err)
	}
	return d
}

func (w *dmWorld) deleteDevice(token string) {
	w.t.Helper()
	if _, err := w.api.DeleteDevice(w.ctx, token); err != nil {
		w.t.Fatalf("delete device: %v", err)
	}
}

func (w *dmWorld) setAttr(device, scope, key, value string) {
	w.t.Helper()
	if _, err := w.api.SetEntityAttribute(w.ctx, &dmmodel.EntityAttributeSetRequest{EntityType: "device",
		Entity: device, Scope: scope, AttrKey: key, ValueType: string(dmmodel.AttributeValueDouble), Value: &value}); err != nil {
		w.t.Fatalf("set attribute: %v", err)
	}
}

// activeSince reads a profile's stored activation instant back from device-management.
func (w *dmWorld) activeSince(profileToken string) time.Time {
	w.t.Helper()
	profiles, err := w.api.DeviceProfilesByToken(w.ctx, []string{profileToken})
	if err != nil || len(profiles) != 1 || !profiles[0].ActiveSince.Valid {
		w.t.Fatalf("profile %q has no stored activation instant: %v", profileToken, err)
	}
	return profiles[0].ActiveSince.Time
}

// reconcileRig is this service's side: a processor over one sqlite database holding every
// projection, a real registry, engine, dead-man armer and attribute view, real metrics, and the
// reconcile client pointed at device-management's schema.
type reconcileRig struct {
	t       *testing.T
	dm      *dmWorld
	src     *schemaFactSource
	rp      *ResolvedEventsProcessor
	derived *captureWriter
	clock   *detectcore.ManualClock
	// writes counts statements against this service's projections that changed at least one row.
	writes  atomic.Int64
	seq     uint64
	metrics *DetectMetrics
}

func newReconcileRig(t *testing.T, dm *dmWorld) *reconcileRig {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	singleConnection(t, db)
	if err := db.AutoMigrate(&model.DetectRule{}, &model.ProfileActive{}, &model.DeviceRoster{},
		&model.DeviceAttribute{}, &model.DeviceAttributeDeletion{}, &model.DetectSnapshot{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	rig := &reconcileRig{t: t, dm: dm, derived: &captureWriter{}}
	count := func(tx *gorm.DB) {
		if tx.Error == nil && tx.RowsAffected > 0 && tx.Statement.Table != "detect_snapshots" {
			rig.writes.Add(1)
		}
	}
	_ = db.Callback().Create().After("gorm:create").Register("test:count_create", count)
	_ = db.Callback().Update().After("gorm:update").Register("test:count_update", count)
	_ = db.Callback().Delete().After("gorm:delete").Register("test:count_delete", count)
	mgr := &rdb.RdbManager{Database: db}

	rig.src = newSchemaFactSource(t, dm.api, "acme")
	rig.metrics = NewDetectMetrics(&dccore.Microservice{InstanceId: "test", FunctionalArea: "event-processing"})
	// Past every instant device-management mints during the test, so nothing is left for "the
	// fact may be in flight" (factSettle) unless a test moves it back.
	rig.clock = detectcore.NewManualClock(time.Now().Add(time.Hour))
	reg := runtime.NewRuleRegistry(nil)
	rig.rp = &ResolvedEventsProcessor{
		Store: model.NewSnapshotStore(mgr),
		cfg: Config{PartitionId: "singleton", CheckpointEvents: 1000, CheckpointInterval: time.Hour,
			TickInterval: time.Hour, Clock: rig.clock},
		registry:           reg,
		publisher:          runtime.NewPublisher(rig.derived, reg, (*DetectMetrics)(nil)),
		clock:              rig.clock,
		metrics:            rig.metrics,
		procCtx:            context.Background(),
		ruleUpdates:        make(chan ruleUpdate, 64),
		armUpdates:         make(chan armUpdate, 256),
		attrUpdates:        make(chan attrUpdate, 256),
		fenceUpdates:       make(chan fenceUpdate, 8),
		factAbsent:         newAbsentSeen(),
		RuleStore:          model.NewDetectRuleStore(mgr),
		ProfileActiveStore: model.NewProfileActiveStore(mgr),
		RosterStore:        model.NewDeviceRosterStore(mgr),
		AttributeStore:     model.NewDeviceAttributeStore(mgr),
		DeviceManagement:   rig.src.client,
	}
	ctx := context.Background()
	if err := rig.rp.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if err := rig.rp.startAttributeView(ctx); err != nil {
		t.Fatalf("attribute view: %v", err)
	}
	if err := rig.rp.startDeadmanGate(ctx); err != nil {
		t.Fatalf("dead-man gate: %v", err)
	}
	if err := rig.rp.reconcileDeadmanArming(ctx); err != nil {
		t.Fatalf("dead-man arming: %v", err)
	}
	return rig
}

// pump plays the single-writer loop's part: it applies everything the consumers and the sweep
// handed to the loop.
func (r *reconcileRig) pump() {
	for {
		select {
		case upd := <-r.rp.ruleUpdates:
			r.rp.applyRuleUpdate(upd)
		case au := <-r.rp.armUpdates:
			r.rp.applyArmRecheck(au)
		case at := <-r.rp.attrUpdates:
			r.rp.applyAttrRecheck(at)
		default:
			return
		}
	}
}

// deliverRules hands the device-management facts at the given capture indices to this service's
// real consumers, then pumps. A fact at no given index is a lost one. deliverRoster and
// deliverAttrs do the same for the other two streams.
func (r *reconcileRig) deliverRules(indices ...int) {
	r.t.Helper()
	for _, i := range indices {
		msg := messaging.NewConsumedMessage("dc.acme."+streams.DetectionRulesPublished, r.dm.rules.payloads[i], 0, nil, &fakeAck{})
		if !r.rp.handleRuleFact(msg, true) {
			r.t.Fatalf("rule fact %d was not consumed", i)
		}
	}
	r.pump()
}

func (r *reconcileRig) deliverRoster(indices ...int) {
	r.t.Helper()
	for _, i := range indices {
		msg := messaging.NewConsumedMessage("dc.acme."+streams.DeviceRoster, r.dm.roster.payloads[i], 0, nil, &fakeAck{})
		if !r.rp.handleRosterFact(msg, true) {
			r.t.Fatalf("roster fact %d was not consumed", i)
		}
	}
	r.pump()
}

func (r *reconcileRig) deliverAttrs(indices ...int) {
	r.t.Helper()
	for _, i := range indices {
		msg := messaging.NewConsumedMessage("dc.acme."+streams.DeviceAttribute, r.dm.attrs.payloads[i], 0, nil, &fakeAck{})
		if !r.rp.handleAttributeFact(msg) {
			r.t.Fatalf("attribute fact %d was not consumed", i)
		}
	}
	r.pump()
}

// sweepDeadline bounds one rig sweep. A sweep over these fixtures takes well under a second; one
// that has not finished in this long is stuck, and the test fails rather than hanging until go
// test's own timeout.
const sweepDeadline = time.Minute

// sweep runs one fact reconcile the way the ticker does — through startFactReconcile, joined —
// applying what it hands the loop WHILE it runs, as the loop would. Draining only after the join
// would deadlock a sweep that signals more than the channels buffer (a mass tombstone), which is
// exactly the regression a partial-walk bug produces: it must fail fast, not hang CI.
func (r *reconcileRig) sweep() {
	r.t.Helper()
	r.rp.startFactReconcile()
	done := make(chan struct{})
	go func() { r.rp.readerWG.Wait(); close(done) }()
	deadline := time.NewTimer(sweepDeadline)
	defer deadline.Stop()
	for {
		select {
		case upd := <-r.rp.ruleUpdates:
			r.rp.applyRuleUpdate(upd)
		case au := <-r.rp.armUpdates:
			r.rp.applyArmRecheck(au)
		case at := <-r.rp.attrUpdates:
			r.rp.applyAttrRecheck(at)
		case <-done:
			r.pump()
			return
		case <-deadline.C:
			r.t.Fatalf("a fact sweep did not finish within %s", sweepDeadline)
		}
	}
}

// measure feeds one resolved temperature reading for a device under a profile version, checkpoints,
// and returns how many derived events that published.
func (r *reconcileRig) measure(device, profileVersion, value string) int {
	r.t.Helper()
	r.seq++
	before := r.derived.writes
	r.rp.handle(measuredMsg(r.t, r.seq, "acme", device, profileVersion, "temp", value, &fakeAck{}))
	r.rp.checkpoint(context.Background())
	return r.derived.writes - before
}

// repairs and failures read the reconcile counters.
func (r *reconcileRig) repairs(p reconcileProjection) float64 {
	return testutil.ToFloat64(r.metrics.factRepairs.WithLabelValues(string(p)))
}

func (r *reconcileRig) failures(p reconcileProjection) float64 {
	return testutil.ToFloat64(r.metrics.factFailures.WithLabelValues(string(p)))
}

// counters snapshots every repairs and failures series.
func (r *reconcileRig) counters() map[string]float64 {
	out := map[string]float64{}
	for _, p := range repairProjections {
		out["repairs/"+string(p)] = r.repairs(p)
	}
	for _, p := range failureProjections {
		out["failures/"+string(p)] = r.failures(p)
	}
	return out
}

// projection dumps every row of this service's four projections for the tenant, comparable with
// deep equality (gorm-managed UpdatedAt zeroed; instants normalised to UTC).
type projectionDump struct {
	Rules      []model.DetectRule
	Actives    []model.ProfileActive
	Roster     []model.DeviceRoster
	Attributes []model.DeviceAttribute
}

func (r *reconcileRig) dump() projectionDump {
	r.t.Helper()
	ctx := context.Background()
	var d projectionDump
	var err error
	if d.Rules, err = r.rp.RuleStore.LoadTenant(ctx, "acme"); err != nil {
		r.t.Fatal(err)
	}
	if d.Actives, err = r.rp.ProfileActiveStore.LoadTenant(ctx, "acme"); err != nil {
		r.t.Fatal(err)
	}
	if d.Roster, err = r.rp.RosterStore.LoadTenant(ctx, "acme"); err != nil {
		r.t.Fatal(err)
	}
	if d.Attributes, err = r.rp.AttributeStore.LoadTenant(ctx, "acme"); err != nil {
		r.t.Fatal(err)
	}
	for i := range d.Rules {
		d.Rules[i].UpdatedAt = time.Time{}
	}
	for i := range d.Actives {
		d.Actives[i].UpdatedAt = time.Time{}
		d.Actives[i].PublishedAt = d.Actives[i].PublishedAt.UTC()
	}
	for i := range d.Roster {
		d.Roster[i].UpdatedAt = time.Time{}
		d.Roster[i].ExpectedSince = d.Roster[i].ExpectedSince.UTC()
		d.Roster[i].LastEventAt = d.Roster[i].LastEventAt.UTC()
	}
	for i := range d.Attributes {
		d.Attributes[i].UpdatedAt = time.Time{}
		d.Attributes[i].LastEventAt = d.Attributes[i].LastEventAt.UTC()
	}
	return d
}

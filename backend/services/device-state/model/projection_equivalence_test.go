// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/presence"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A BATCH MUST LEAVE WHAT MERGING ITS EVENTS ONE AT A TIME LEAVES. Each case below runs the
// same updates two ways, on two databases seeded identically:
//
//   - A, one at a time: MergeDeviceState per update, then the latest values through
//     referenceMergeLatest* — the read-lock-compare-write LOOPS this service ran before the
//     conditional upsert, kept here as the reference. The latest-value half of A is
//     therefore NOT the code under test: a defect in the upsert or in the coalescing (a
//     guard that lets an equal time overwrite, a tie kept by last arrival) shows up as a
//     difference, instead of being shared by both sides and passing.
//   - B, the batch: ONE MergeProjectionBatch.
//
// and then compares every row of all three tables, field by field. The device-state half of
// A does share its rule with B (applyEvent). That is deliberate — it is the ONE definition
// of the rule — and it is pinned on its own by api_test.go, demotion_projection_test.go and
// latest_location_test.go, which run MergeDeviceState against expected VALUES. What this
// file adds is that folding several events over one row in memory, in one transaction,
// ends where re-reading the row between them does.
//
// It runs on SQLite here and on PostgreSQL under the integration tag
// (projection_equivalence_integration_test.go), because SQLite compares these time columns
// as text and PostgreSQL as timestamps, and a pass on one says nothing about the other.

// newSQLiteProjectionApi is an Api over a fresh in-memory SQLite database of its own, with the
// tenant-scope callbacks and all three projection tables migrated from the live models.
func newSQLiteProjectionApi(t *testing.T, name string) *Api {
	t.Helper()
	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_", "#", "_").Replace(t.Name()+"-"+name) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if sqldb, err := db.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&DeviceState{}, &LatestMeasurement{}, &LatestLocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

// referenceMergeLatestMeasurements is the per-key loop MergeLatestMeasurements ran before
// the conditional upsert: for each reading, lock the row, create it if absent, overwrite it
// only when the reading is strictly newer. Times are compared at stored precision, the rule
// both paths now share.
func referenceMergeLatestMeasurements(api *Api, ctx context.Context, token string, inputs []LatestMeasurementInput) error {
	if len(inputs) == 0 {
		return nil
	}
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		for _, in := range inputs {
			at := storedTime(in.OccurredTime)
			found := &LatestMeasurement{}
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("device_token = ? AND name = ?", token, in.Name).First(found).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if err := tx.Create(&LatestMeasurement{DeviceToken: token, Name: in.Name, Value: in.Value,
					Classifier: in.Classifier, Unit: in.Unit, DataType: in.DataType, OccurredTime: at}).Error; err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			if at.After(found.OccurredTime) {
				found.Value, found.Classifier, found.Unit, found.DataType, found.OccurredTime =
					in.Value, in.Classifier, in.Unit, in.DataType, at
				if err := tx.Save(found).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// referenceMergeLatestLocations is the same loop for positions.
func referenceMergeLatestLocations(api *Api, ctx context.Context, token string, inputs []LatestLocationInput) error {
	if len(inputs) == 0 {
		return nil
	}
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		for _, in := range inputs {
			at := storedTime(in.OccurredTime)
			found := &LatestLocation{}
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("device_token = ?", token).First(found).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if err := tx.Create(&LatestLocation{DeviceToken: token, Latitude: in.Latitude, Longitude: in.Longitude,
					Elevation: in.Elevation, Accuracy: in.Accuracy, Speed: in.Speed, Heading: in.Heading,
					OccurredTime: at}).Error; err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			if at.After(found.OccurredTime) {
				found.Latitude, found.Longitude, found.Elevation = in.Latitude, in.Longitude, in.Elevation
				found.Accuracy, found.Speed, found.Heading, found.OccurredTime = in.Accuracy, in.Speed, in.Heading, at
				if err := tx.Save(found).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// mergeOneAtATime is path A for one update.
func mergeOneAtATime(api *Api, u ProjectionUpdate) error {
	ctx := core.WithTenant(context.Background(), u.Tenant)
	if _, err := api.MergeDeviceState(ctx, u.DeviceToken, u.OccurredAt, u.Presence, u.Identity); err != nil {
		return err
	}
	if err := referenceMergeLatestMeasurements(api, ctx, u.DeviceToken, u.Measurements); err != nil {
		return err
	}
	return referenceMergeLatestLocations(api, ctx, u.DeviceToken, u.Locations)
}

// projectionRows is every row of the three tables, keyed and stripped of what the two paths
// legitimately differ in (ids, row timestamps), with times in UTC.
func projectionRows(t *testing.T, api *Api) []string {
	t.Helper()
	ctx := core.WithSystemContext(context.Background())
	var states []DeviceState
	var readings []LatestMeasurement
	var fixes []LatestLocation
	db := api.RDB.DB(ctx)
	if err := db.Find(&states).Error; err != nil {
		t.Fatalf("read device_states: %v", err)
	}
	if err := db.Find(&readings).Error; err != nil {
		t.Fatalf("read latest_measurements: %v", err)
	}
	if err := db.Find(&fixes).Error; err != nil {
		t.Fatalf("read latest_locations: %v", err)
	}
	nt := func(n sql.NullTime) string {
		if !n.Valid {
			return "null"
		}
		return n.Time.UTC().Format(time.RFC3339Nano)
	}
	nf := func(n sql.NullFloat64) string {
		if !n.Valid {
			return "null"
		}
		return fmt.Sprint(n.Float64)
	}
	ps := func(p *string) string {
		if p == nil {
			return "nil"
		}
		return *p
	}
	pu := func(p *uint) string {
		if p == nil {
			return "nil"
		}
		return fmt.Sprint(*p)
	}
	var out []string
	for _, s := range states {
		out = append(out, fmt.Sprintf("state %s/%s ext=%q src=%q active=%v conn=%s disc=%s act=%s alarm=%s timeout=%d presence=%s session=%d ptime=%s",
			s.TenantId, s.DeviceToken, s.ExternalId, s.Source, s.Active, nt(s.LastConnectTime), nt(s.LastDisconnectTime),
			nt(s.LastActivityTime), nt(s.InactivityAlarmTime), s.InactivityTimeout, s.PresenceSource, s.SessionId, nt(s.PresenceTime)))
	}
	for _, r := range readings {
		out = append(out, fmt.Sprintf("reading %s/%s/%s value=%s class=%s unit=%s type=%s at=%s",
			r.TenantId, r.DeviceToken, r.Name, nf(r.Value), pu(r.Classifier), ps(r.Unit), ps(r.DataType),
			r.OccurredTime.UTC().Format(time.RFC3339Nano)))
	}
	for _, f := range fixes {
		out = append(out, fmt.Sprintf("fix %s/%s lat=%s lon=%s el=%s acc=%s spd=%s hdg=%s at=%s",
			f.TenantId, f.DeviceToken, nf(f.Latitude), nf(f.Longitude), nf(f.Elevation), nf(f.Accuracy), nf(f.Speed),
			nf(f.Heading), f.OccurredTime.UTC().Format(time.RFC3339Nano)))
	}
	sort.Strings(out)
	return out
}

// equivalenceCase is one scripted sequence: seed runs on both databases (one at a time), and
// then updates run one at a time on A and as one batch on B.
type equivalenceCase struct {
	name    string
	seed    []ProjectionUpdate
	sweepAt time.Time // when set, the inactivity sweep runs at this time after the seed
	updates []ProjectionUpdate
	// want, when set, is a row the result must contain — the scripted expectation, so a
	// case asserts a VALUE and not only agreement.
	want []string
}

// assertEquivalent runs c on two fresh databases from newApi and compares them.
func assertEquivalent(t *testing.T, newApi func(t *testing.T, name string) *Api, c equivalenceCase) {
	t.Helper()
	a, b := newApi(t, "a"), newApi(t, "b")
	for _, api := range []*Api{a, b} {
		for _, u := range c.seed {
			if err := mergeOneAtATime(api, u); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
		if !c.sweepAt.IsZero() {
			if _, err := api.SweepInactive(core.WithSystemContext(context.Background()), c.sweepAt); err != nil {
				t.Fatalf("sweep: %v", err)
			}
		}
	}
	for _, u := range c.updates {
		if err := mergeOneAtATime(a, u); err != nil {
			t.Fatalf("one at a time: %v", err)
		}
	}
	if err := b.MergeProjectionBatch(context.Background(), c.updates); err != nil {
		t.Fatalf("batch: %v", err)
	}
	ra, rb := projectionRows(t, a), projectionRows(t, b)
	if strings.Join(ra, "\n") != strings.Join(rb, "\n") {
		t.Errorf("the batch left a different projection.\none at a time:\n  %s\nbatch:\n  %s",
			strings.Join(ra, "\n  "), strings.Join(rb, "\n  "))
	}
	for _, w := range c.want {
		found := false
		for _, r := range rb {
			if strings.Contains(r, w) {
				found = true
			}
		}
		if !found {
			t.Errorf("no row contains %q; rows:\n  %s", w, strings.Join(rb, "\n  "))
		}
	}
}

var eqT0 = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return eqT0.Add(d) }

func data(tenant, tok string, when time.Time) ProjectionUpdate {
	return ProjectionUpdate{Tenant: tenant, DeviceToken: tok, OccurredAt: when}
}

func claim(tenant, tok string, when time.Time, c presence.Claim, session uint64) ProjectionUpdate {
	return ProjectionUpdate{Tenant: tenant, DeviceToken: tok, OccurredAt: when,
		Presence: &PresenceTransition{Claim: c, SessionId: session, OccurredAt: when}}
}

func reading(tenant, tok string, when time.Time, name string, v float64) ProjectionUpdate {
	u := data(tenant, tok, when)
	u.Measurements = []LatestMeasurementInput{{Name: name, Value: sql.NullFloat64{Float64: v, Valid: true}, OccurredTime: when}}
	return u
}

func fix(tenant, tok string, when time.Time, lat float64) ProjectionUpdate {
	u := data(tenant, tok, when)
	u.Locations = []LatestLocationInput{{Latitude: sql.NullFloat64{Float64: lat, Valid: true},
		Longitude: sql.NullFloat64{Float64: -81.5, Valid: true}, OccurredTime: when}}
	return u
}

func withIdentity(u ProjectionUpdate, ext string) ProjectionUpdate {
	u.Identity = DeviceIdentity{ExternalId: ext, Source: "mqtt1"}
	return u
}

// equivalenceCases is the scripted table. Each row names what it is there to catch.
func equivalenceCases() []equivalenceCase {
	const A, B = "tenant-a", "tenant-b"
	return []equivalenceCase{
		{name: "data events out of order on an inferred device",
			updates: []ProjectionUpdate{data(A, "d1", at(2*time.Minute)), data(A, "d1", at(0)), data(A, "d1", at(time.Minute))},
			want:    []string{"act=2026-09-20T12:02:00Z"}},
		{name: "an equal-time data event resurrects a swept device (not older)",
			seed:    []ProjectionUpdate{data(A, "d1", at(0))},
			sweepAt: at(2 * time.Hour),
			updates: []ProjectionUpdate{data(A, "d1", at(0)), data(A, "d1", at(-time.Minute))},
			want:    []string{"active=true"}},
		{name: "connect, data, disconnect, reconnect on a new session, then a late disconnect of the old one",
			updates: []ProjectionUpdate{
				claim(A, "d1", at(0), presence.ClaimConnected, 1),
				data(A, "d1", at(time.Minute)),
				claim(A, "d1", at(2*time.Minute), presence.ClaimDisconnected, 1),
				claim(A, "d1", at(3*time.Minute), presence.ClaimConnected, 2),
				claim(A, "d1", at(4*time.Minute), presence.ClaimDisconnected, 1),
			},
			want: []string{"active=true", "session=2"}},
		{name: "a first-ever disconnect creates the row dead, then data does not resurrect it",
			updates: []ProjectionUpdate{claim(A, "d1", at(0), presence.ClaimDisconnected, 5), data(A, "d1", at(time.Minute))},
			want:    []string{"active=false", "presence=ASSERTED"}},
		{name: "a demotion of a device with no row, then data",
			updates: []ProjectionUpdate{claim(A, "d1", at(0), presence.ClaimDemoted, 3), data(A, "d1", at(time.Minute))},
			want:    []string{"presence=INFERRED"}},
		{name: "a demotion after a connect, then a late echo from the released session",
			updates: []ProjectionUpdate{
				claim(A, "d1", at(0), presence.ClaimConnected, 4),
				claim(A, "d1", at(time.Minute), presence.ClaimDemoted, 4),
				claim(A, "d1", at(30*time.Second), presence.ClaimConnected, 4),
			},
			want: []string{"presence=INFERRED"}},
		{name: "identity: empty, then set, then empty again keeps the set value",
			updates: []ProjectionUpdate{
				withIdentity(data(A, "d1", at(0)), ""),
				withIdentity(data(A, "d1", at(time.Minute)), "ext-1"),
				withIdentity(data(A, "d1", at(2*time.Minute)), ""),
			},
			want: []string{`ext="ext-1"`}},
		{name: "one name at t1, t0, t1: the first t1 reading stays",
			updates: []ProjectionUpdate{
				reading(A, "d1", at(time.Minute), "temp", 1), reading(A, "d1", at(0), "temp", 0), reading(A, "d1", at(time.Minute), "temp", 2),
			},
			want: []string{"reading tenant-a/d1/temp value=1 "}},
		{name: "a stored reading is not replaced by one with an equal time",
			seed:    []ProjectionUpdate{reading(A, "d1", at(0), "temp", 10)},
			updates: []ProjectionUpdate{reading(A, "d1", at(0), "temp", 11)},
			want:    []string{"reading tenant-a/d1/temp value=10 "}},
		// The database keeps microseconds, so two readings inside one microsecond are equal
		// once stored, and the first stored stays — in both orders. Compared as sent, the
		// first would let an OLDER reading replace a newer one whose extra digits the
		// database had dropped, and the second would let a redelivery-shaped tie overwrite.
		{name: "an older reading inside the same microsecond does not replace the stored one",
			seed:    []ProjectionUpdate{reading(A, "d1", at(1300*time.Nanosecond), "temp", 10)},
			updates: []ProjectionUpdate{reading(A, "d1", at(1100*time.Nanosecond), "temp", 11)},
			want:    []string{"reading tenant-a/d1/temp value=10 "}},
		{name: "a later reading inside the same microsecond does not replace the stored one",
			seed:    []ProjectionUpdate{reading(A, "d1", at(1100*time.Nanosecond), "temp", 10)},
			updates: []ProjectionUpdate{reading(A, "d1", at(1300*time.Nanosecond), "temp", 11)},
			want:    []string{"reading tenant-a/d1/temp value=10 "}},
		{name: "several names across events for two interleaved devices",
			updates: []ProjectionUpdate{
				reading(A, "d1", at(0), "temp", 1), reading(A, "d2", at(0), "temp", 5),
				reading(A, "d1", at(time.Minute), "hum", 2), reading(A, "d2", at(-time.Minute), "temp", 4),
				reading(A, "d1", at(2*time.Minute), "temp", 3), reading(A, "d2", at(time.Minute), "hum", 6),
			},
			want: []string{"reading tenant-a/d1/temp value=3 ", "reading tenant-a/d2/temp value=5 "}},
		{name: "fixes at t1 then t0: t1 stays",
			updates: []ProjectionUpdate{fix(A, "d1", at(time.Minute), 28.75), fix(A, "d1", at(0), 28.5)},
			want:    []string{"fix tenant-a/d1 lat=28.75 "}},
		{name: "two tenants reusing one device token stay apart",
			updates: []ProjectionUpdate{
				reading(A, "d1", at(0), "temp", 1), reading(B, "d1", at(time.Minute), "temp", 2),
				claim(B, "d1", at(2*time.Minute), presence.ClaimDisconnected, 1), data(A, "d1", at(3*time.Minute)),
			},
			want: []string{"state tenant-a/d1 ", "state tenant-b/d1 ", "reading tenant-b/d1/temp value=2 "}},
		{name: "an existing device and a new one in one batch",
			seed:    []ProjectionUpdate{reading(A, "old", at(0), "temp", 1)},
			updates: []ProjectionUpdate{reading(A, "new", at(time.Minute), "temp", 2), reading(A, "old", at(time.Minute), "temp", 3)},
			want:    []string{"reading tenant-a/new/temp value=2 ", "reading tenant-a/old/temp value=3 "}},
	}
}

func TestABatchLeavesWhatMergingOneAtATimeLeaves(t *testing.T) {
	for _, c := range equivalenceCases() {
		t.Run(c.name, func(t *testing.T) { assertEquivalent(t, newSQLiteProjectionApi, c) })
	}
}

// randomUpdates is one generated sequence: up to 12 events over 3 devices in 2 tenants,
// mixing plain data, readings, fixes and every presence claim, with times drawn from a small
// set (so ties are common) that includes sub-microsecond offsets.
func randomUpdates(r *rand.Rand) []ProjectionUpdate {
	tenants := []string{"tenant-a", "tenant-b"}
	devices := []string{"d1", "d2", "d3"}
	offsets := []time.Duration{0, 1100 * time.Nanosecond, 1300 * time.Nanosecond, time.Second, time.Minute, 2 * time.Minute}
	claims := []presence.Claim{presence.ClaimConnected, presence.ClaimDisconnected, presence.ClaimDemoted}
	n := 1 + r.Intn(12)
	out := make([]ProjectionUpdate, 0, n)
	for i := 0; i < n; i++ {
		tenant, tok := tenants[r.Intn(len(tenants))], devices[r.Intn(len(devices))]
		when := eqT0.Add(offsets[r.Intn(len(offsets))])
		var u ProjectionUpdate
		switch r.Intn(4) {
		case 0:
			u = data(tenant, tok, when)
		case 1:
			u = reading(tenant, tok, when, []string{"temp", "hum"}[r.Intn(2)], float64(r.Intn(100)))
		case 2:
			u = fix(tenant, tok, when, float64(r.Intn(90)))
		default:
			u = claim(tenant, tok, when, claims[r.Intn(len(claims))], uint64(1+r.Intn(3)))
		}
		if r.Intn(3) == 0 {
			u = withIdentity(u, []string{"", "ext-1", "ext-2"}[r.Intn(3)])
		}
		out = append(out, u)
	}
	return out
}

// 200 generated sequences, from a fixed seed so a failure reproduces; the seed and the
// sequence number are in every failure's name.
func TestGeneratedBatchesLeaveWhatMergingOneAtATimeLeaves(t *testing.T) {
	const seed = 20260927
	r := rand.New(rand.NewSource(seed))
	for i := 0; i < 200; i++ {
		c := equivalenceCase{name: fmt.Sprintf("seed%d-seq%03d", seed, i), updates: randomUpdates(r)}
		if r.Intn(2) == 0 {
			c.seed = randomUpdates(r)
		}
		t.Run(c.name, func(t *testing.T) { assertEquivalent(t, newSQLiteProjectionApi, c) })
	}
}

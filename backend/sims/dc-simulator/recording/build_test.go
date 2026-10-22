// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

var t0 = time.Date(2026, 10, 15, 18, 0, 0, 0, time.UTC)

type fakePlatform struct {
	rows  []Row
	fixes []Fix
}

func (f *fakePlatform) Measurements(_ context.Context, device string, from, to time.Time) ([]Row, error) {
	var out []Row
	for _, r := range f.rows {
		if r.Device == device && !r.At.Before(from) && !r.At.After(to) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakePlatform) Locations(_ context.Context, device string, from, to time.Time) ([]Fix, error) {
	var out []Fix
	for _, x := range f.fixes {
		if x.Device == device && !x.At.Before(from) && !x.At.After(to) {
			out = append(out, x)
		}
	}
	return out, nil
}

func ptr(v float64) *float64 { return &v }

func line(v any) string {
	b, _ := json.Marshal(v)
	return string(b) + "\n"
}

// A run of 20 minutes: hauler-01 reports fuel every 10 s and a position every 10 s, and
// publishes engine_hours too (which the recording drops).
func fixture() (*fakePlatform, Options) {
	p := &fakePlatform{}
	var log strings.Builder
	for s := 0; s <= 1200; s += 10 {
		at := t0.Add(time.Duration(s) * time.Second)
		fuel := 90 - float64(s)/100
		p.rows = append(p.rows, Row{Device: "sp-hauler-01", Name: "fuel_pct", At: at, Value: fuel},
			Row{Device: "sp-hauler-01", Name: "engine_hours", At: at, Value: 100})
		log.WriteString(line(sim.PublishLogEntry{Device: "sp-hauler-01", Type: "Measurement", Time: at.Format(time.RFC3339Nano),
			Values: map[string]string{"fuel_pct": jsonNum(fuel), "engine_hours": "100"}}))
		lat := 39 + float64(s)/1e6
		p.fixes = append(p.fixes, Fix{Device: "sp-hauler-01", At: at, Latitude: lat, Longitude: -117, Heading: ptr(90), Speed: ptr(4)})
		log.WriteString(line(sim.PublishLogEntry{Device: "sp-hauler-01", Type: "Location", Time: at.Format(time.RFC3339Nano),
			Values: map[string]string{"latitude": jsonNum(lat), "longitude": "-117", "heading": "90", "speed": "4"}}))
	}
	raised := t0.Add(5 * time.Minute).Format(time.RFC3339Nano)
	cleared := t0.Add(15 * time.Minute).Format(time.RFC3339Nano)
	var alarms strings.Builder
	for s := -10; s <= 1210; s += 5 {
		at := t0.Add(time.Duration(s) * time.Second)
		a := WatchedAlarm{Token: "tok-1", Originator: "sp-hauler-01", AlarmKey: "low-fuel", MetricKey: "fuel_pct", Severity: "MAJOR", State: "CLEARED"}
		switch {
		case s >= 900:
			a.State, a.RaisedTime, a.ClearedTime = "CLEARED", &raised, &cleared
		case s >= 300:
			a.State, a.RaisedTime = "ACTIVE", &raised
		default:
			old := t0.Add(-time.Hour).Format(time.RFC3339Nano)
			a.RaisedTime, a.ClearedTime = &old, &old
		}
		alarms.WriteString(line(AlarmWatch{RequestedAt: at.Add(-100 * time.Millisecond).Format(time.RFC3339Nano),
			AnsweredAt: at.Format(time.RFC3339Nano), Total: 1, Alarms: []WatchedAlarm{a}}))
	}
	m := sim.NewSitepulse(1, sim.Load{}).Manifest()
	return p, Options{
		From: t0, To: t0.Add(20 * time.Minute), LiveFrom: t0.Add(15 * time.Minute), HistoryEvery: time.Minute,
		Devices: m.Expand(m.Seed), Instance: "guide", Tenant: "sim-sitepulse", PlatformVersion: "dev", GitSha: "abcdef1",
		BoardPath: "board.json", BoardCommit: strings.Repeat("a", 40), BoardBytes: []byte("{}"),
		PublishLog: []byte(log.String()), PublishLogName: "published.ndjson", Alarms: []byte(alarms.String()), AlarmsName: "alarms.ndjson",
	}
}

func jsonNum(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestBuildKeepsCheckedRowsThinsTheHistoryAndFoldsTheAlarms(t *testing.T) {
	p, o := fixture()
	rec, rep, err := Build(context.Background(), p, o)
	if err != nil {
		t.Fatal(err)
	}
	if rep.PlatformRows != 2*121 || rep.MissingOnServer != 0 || rep.PublishedInWindow != 121 {
		t.Fatalf("report %+v", rep)
	}
	// History (before 15 min): one fuel row a minute, 15 of them; then every row, 31 of them.
	if got := len(rec.Measurements.T); got != 15+31 {
		t.Fatalf("kept %d rows, want 46", got)
	}
	for i, n := range rec.Measurements.N {
		if BoardMeasurements[n] != "fuel_pct" {
			t.Fatalf("row %d is %s; engine_hours must be dropped", i, BoardMeasurements[n])
		}
	}
	// Positions: the last one before the showcase start, then every one after it.
	if got := len(rec.Locations.T); got != 1+31 || rec.Locations.T[0] != 890_000 {
		t.Fatalf("kept %d positions starting at %v", got, rec.Locations.T)
	}
	// Alarms: the answer before the start as a snapshot, then the raise and the clear.
	if len(rec.Alarms.Snapshots) != 1 || rec.Alarms.Snapshots[0].Alarms[0].State != "CLEARED" {
		t.Fatalf("snapshots %+v", rec.Alarms.Snapshots)
	}
	ev := rec.Alarms.Events
	if len(ev) != 2 || ev[0].State != "ACTIVE" || ev[0].TMs != 300_000 || ev[1].State != "CLEARED" || ev[1].TMs != 900_000 || ev[0].ID != "alarm-1" {
		t.Fatalf("events %+v", ev)
	}
	if rec.Chapters[0]["tMs"] != int64(900_000) {
		t.Fatalf("chapter %+v", rec.Chapters)
	}
	var buf bytes.Buffer
	if err := Write(&buf, rec); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	want := []string{"alarms", "board", "build", "channels", "chapters", "clock", "converter", "devices", "durationMs", "formatVersion",
		"instance", "kind", "locations", "measurements", "platformVersion", "runId", "sourceHashes", "startedAtUtc", "tenant"}
	if len(doc) != len(want) {
		t.Fatalf("the document has keys %v", doc)
	}
	for _, k := range want {
		if _, ok := doc[k]; !ok {
			t.Fatalf("the document has no %q", k)
		}
	}
}

func refused(t *testing.T, err error, want string) {
	t.Helper()
	var r *Refusal
	if !errors.As(err, &r) || !strings.Contains(err.Error(), want) {
		t.Fatalf("want a refusal mentioning %q, got %v", want, err)
	}
}

func TestBuildRefusesWhatTheDevicesNeverSent(t *testing.T) {
	p, o := fixture()
	p.rows = append(p.rows, Row{Device: "sp-hauler-01", Name: "fuel_pct", At: t0.Add(5*time.Second + time.Microsecond), Value: 1})
	_, _, err := Build(context.Background(), p, o)
	refused(t, err, "never sent")

	p, o = fixture()
	p.rows[0].Value = 12.5
	_, _, err = Build(context.Background(), p, o)
	refused(t, err, "the device sent")

	p, o = fixture()
	p.fixes[3].Latitude += 0.001
	_, _, err = Build(context.Background(), p, o)
	refused(t, err, "not the one the device sent")
}

func TestBuildRefusesAnAlarmHistoryItCannotVouchFor(t *testing.T) {
	p, o := fixture()
	o.From = t0.Add(-time.Hour)
	o.LiveFrom = t0
	_, _, err := Build(context.Background(), p, o)
	refused(t, err, "no alarm answer")

	p, o = fixture()
	lines := strings.Split(strings.TrimSpace(string(o.Alarms)), "\n")
	o.Alarms = []byte(strings.Join(append(lines[:50], lines[70:]...), "\n"))
	_, _, err = Build(context.Background(), p, o)
	refused(t, err, "gap")
}

// rewriteAlarms applies edit to every watched alarm of every answer.
func rewriteAlarms(t *testing.T, o *Options, edit func(*WatchedAlarm)) {
	t.Helper()
	var out strings.Builder
	for _, l := range strings.Split(strings.TrimSpace(string(o.Alarms)), "\n") {
		var a AlarmWatch
		if err := json.Unmarshal([]byte(l), &a); err != nil {
			t.Fatal(err)
		}
		for i := range a.Alarms {
			edit(&a.Alarms[i])
		}
		out.WriteString(line(a))
	}
	o.Alarms = []byte(out.String())
}

// The alarm table's Value column is the reading that raised the alarm: the value the device
// sent for its metric at the raise instant. An alarm raised before the run has none.
func TestBuildCarriesTheReadingThatRaisedEachAlarm(t *testing.T) {
	p, o := fixture()
	rec, _, err := Build(context.Background(), p, o)
	if err != nil {
		t.Fatal(err)
	}
	ev := rec.Alarms.Events
	if len(ev) != 2 || ev[0].Val == nil || *ev[0].Val != 87 || ev[1].Val == nil || *ev[1].Val != 87 {
		t.Fatalf("the raise and the clear must carry the raising reading 87: %+v", ev)
	}
	if v := rec.Alarms.Snapshots[0].Alarms[0].Val; v != nil {
		t.Fatalf("an alarm raised before the run carries a value the run never sent: %v", *v)
	}
	var buf bytes.Buffer
	if err := Write(&buf, rec); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"val":87`) || strings.Count(buf.String(), `"val"`) != 2 {
		t.Fatalf("the document must write val on the two alarms that have one")
	}

	// The platform's own lastValue agreeing with the publish log is accepted.
	p, o = fixture()
	rewriteAlarms(t, &o, func(a *WatchedAlarm) {
		if a.State != "CLEARED" || a.ClearedTime != nil && strings.HasPrefix(*a.ClearedTime, "2026-10-15T18:15") {
			a.LastValue = ptr(87)
		}
	})
	if _, _, err := Build(context.Background(), p, o); err != nil {
		t.Fatalf("an agreeing lastValue was refused: %v", err)
	}
	// ...and a pre-run alarm's lastValue is kept out, not refused.
	p, o = fixture()
	rewriteAlarms(t, &o, func(a *WatchedAlarm) {
		if strings.HasPrefix(*a.RaisedTime, "2026-10-15T17:") {
			a.LastValue = ptr(3)
		}
	})
	if rec, _, err := Build(context.Background(), p, o); err != nil || rec.Alarms.Snapshots[0].Alarms[0].Val != nil {
		t.Fatalf("a pre-run alarm: %v", err)
	}
}

func TestBuildRefusesAnAlarmValueTheDevicesNeverSent(t *testing.T) {
	p, o := fixture()
	rewriteAlarms(t, &o, func(a *WatchedAlarm) {
		if a.State == "ACTIVE" {
			a.LastValue = ptr(86)
		}
	})
	_, _, err := Build(context.Background(), p, o)
	refused(t, err, "was raised by 86; the device sent 87")

	// Raised while the devices were publishing, at an instant they sent nothing.
	p, o = fixture()
	odd := t0.Add(5*time.Minute + 5*time.Second).Format(time.RFC3339Nano)
	rewriteAlarms(t, &o, func(a *WatchedAlarm) {
		if a.State == "ACTIVE" {
			a.RaisedTime, a.LastValue = &odd, ptr(87)
		}
	})
	_, _, err = Build(context.Background(), p, o)
	refused(t, err, "the device sent no fuel_pct")
}

// A measurement the board aggregates keeps every row of its history: thinning it would move
// the Avg and Peak a stat card shows.
func TestBuildKeepsAFullRateMeasurementUnthinned(t *testing.T) {
	p, o := fixture()
	o.FullRate = []string{"fuel_pct"}
	rec, _, err := Build(context.Background(), p, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(rec.Measurements.T); got != 121 {
		t.Fatalf("kept %d rows, want all 121", got)
	}
	p, o = fixture()
	o.FullRate = []string{"engine_hours"}
	_, _, err = Build(context.Background(), p, o)
	refused(t, err, "not a measurement the board shows")
}

// Every field a watched alarm records must be one the watch ASKS for: a field the query
// leaves out decodes as its zero value (a missing lastValue reads as "no value") and the
// recording silently loses it.
func TestTheWatchAsksForEveryFieldItRecords(t *testing.T) {
	sel := alarmsQuery[strings.Index(alarmsQuery, "results {")+len("results {"):]
	asked := map[string]bool{}
	for _, f := range strings.Fields(sel[:strings.Index(sel, "}")]) {
		asked[f] = true
	}
	typ := reflect.TypeOf(WatchedAlarm{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if !asked[name] {
			t.Errorf("WatchedAlarm.%s (%q) is recorded but the alarm query does not ask for it", typ.Field(i).Name, name)
		}
		delete(asked, name)
	}
	if len(asked) != 0 {
		t.Errorf("the alarm query asks for fields nothing records: %v", asked)
	}
}

// The recording carries what each kind's profile declares: the crusher plant declares no fuel, so a
// replayed fleet table says it does not report fuel rather than that it sent none.
func TestBuildCarriesEachKindsDeclaredMeasurements(t *testing.T) {
	p, o := fixture()
	m := sim.NewSitepulse(1, sim.Load{}).Manifest()
	o.Declared = DeclaredByType(m)
	rec, _, err := Build(context.Background(), p, o)
	if err != nil {
		t.Fatal(err)
	}
	machine := []string{sim.SitepulseFuelKey, sim.SitepulseEngineTempKey, sim.SitepulseEngineHoursKey, sim.SitepulsePayloadKey, sim.SitepulseTyreKey}
	want := map[string][]string{
		"Hauler": machine, "Loader": machine, "Dozer": machine,
		"Plant": {sim.SitepulseThroughputKey, sim.SitepulsePlantRunningKey},
	}
	if !reflect.DeepEqual(rec.DeclaredMetrics, want) {
		t.Fatalf("declaredMetrics = %v, want %v", rec.DeclaredMetrics, want)
	}
	var buf bytes.Buffer
	if err := Write(&buf, rec); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"declaredMetrics":{"Dozer":`) {
		t.Fatalf("the written recording carries no declaredMetrics: %.400s", buf.String())
	}

	// Without declarations, none is written: a player then reads every device as declaring all.
	o.Declared = nil
	if rec, _, err = Build(context.Background(), p, o); err != nil || rec.DeclaredMetrics != nil {
		t.Fatalf("no declarations: err %v", err)
	}

	// A type that declares nothing, and a kind whose devices disagree, are refused.
	o.Declared = DeclaredByType(m)
	o.Declared[sim.SitepulsePlantTypeToken] = nil
	if _, _, err := Build(context.Background(), p, o); err == nil || !strings.Contains(err.Error(), "declares no measurements") {
		t.Fatalf("a type declaring nothing: err %v", err)
	}
	o.Declared = DeclaredByType(m)
	o.Devices = append([]sim.DeviceInstance(nil), o.Devices...)
	for i, d := range o.Devices {
		if d.ExternalId == "SP-HL-0002" {
			o.Devices[i].DeviceTypeToken = sim.SitepulsePlantTypeToken
		}
	}
	if _, _, err := Build(context.Background(), p, o); err == nil || !strings.Contains(err.Error(), "do not all declare") {
		t.Fatalf("a kind that disagrees: err %v", err)
	}
}

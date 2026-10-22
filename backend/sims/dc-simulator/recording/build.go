// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// FormatVersion and FormatKind are the board-recording format this writes: version 2, the
// one with positions (frontend/packages/dashboards/src/recording.ts is the reader, and the
// contract).
const (
	FormatVersion = 2
	FormatKind    = "sitepulse-board-recording"
	ProducerName  = "sitepulse-record"
	// ProducerVersion is this producer's own version, in converter.version. 2: alarms carry
	// the reading that raised them (`val`), and FullRate measurements are never thinned. 3: the
	// recording carries what each kind's profile declares (`declaredMetrics`).
	//
	// The committed showcase recording (frontend/testdata/board-replay/sitepulse-showcase.json)
	// was written by version 2 and its `declaredMetrics` added afterwards from DeclaredByType over
	// the same sitepulse manifest, which is exactly what version 3 writes; its converter.version
	// still says 2, because version 2 is what read its rows back from the platform.
	ProducerVersion = 3
)

// BoardMeasurements are the measurements the sitepulse board shows, and the only ones a
// recording carries (engine_hours is published and read by nothing on the board).
var BoardMeasurements = []string{
	sim.SitepulseEngineTempKey,
	sim.SitepulseFuelKey,
	sim.SitepulsePayloadKey,
	sim.SitepulsePlantRunningKey,
	sim.SitepulseThroughputKey,
	sim.SitepulseTyreKey,
}

// DefaultFullRate is what sitepulse-record build keeps at full rate unless told otherwise:
// the plant's throughput, which the sitepulse board's Avg and Peak stat cards aggregate.
const DefaultFullRate = sim.SitepulseThroughputKey

// Refusal is a reason the recording must not be written. Never caught and continued past.
type Refusal struct{ msg string }

func (r *Refusal) Error() string { return r.msg }

func refuse(format string, args ...any) error { return &Refusal{fmt.Sprintf(format, args...)} }

// Row is one stored reading read back from the platform.
type Row struct {
	Device string
	Name   string
	At     time.Time
	Value  float64
}

// Fix is one stored position read back from the platform.
type Fix struct {
	Device              string
	At                  time.Time
	Latitude, Longitude float64
	Heading, Speed      *float64
}

// Platform is what Build reads from the running instance: the stored measurements and
// positions of one device over a window. Behind an interface so the checks can be tested
// without one.
type Platform interface {
	Measurements(ctx context.Context, device string, from, to time.Time) ([]Row, error)
	Locations(ctx context.Context, device string, from, to time.Time) ([]Fix, error)
}

// Options is everything a recording is made from, besides the platform.
type Options struct {
	From, To time.Time
	// LiveFrom is where the showcase starts (a chapter): everything before it is the
	// history behind it.
	LiveFrom time.Time
	// HistoryEvery thins the readings and positions before LiveFrom to one per device and
	// measurement in each step of this length (the first of each); after LiveFrom every
	// row is kept. Zero keeps every row.
	HistoryEvery time.Duration
	// FullRate names the measurements whose history is never thinned: the ones the board
	// AGGREGATES over its span (a stat card's Avg or Peak). A thinned history would change
	// those figures: it drops the peak reading, and it weights the average towards the
	// unthinned tail. The board-replay tests hold the committed recording to this.
	FullRate []string

	Devices []sim.DeviceInstance
	// Declared is what each device TYPE's profile declares, by type token (DeclaredByType). The
	// recording carries it per kind as `declaredMetrics`, which is what lets a replayed fleet
	// table say "does not report this" for the crusher's fuel. Nil writes none, and a player
	// then treats every device as declaring everything.
	Declared        map[string][]string
	Instance        string
	Tenant          string
	PlatformVersion string
	GitSha          string

	BoardPath   string
	BoardCommit string
	BoardBytes  []byte

	PublishLog     []byte
	PublishLogName string
	Alarms         []byte
	AlarmsName     string
}

// Report says what a build kept and what it checked.
type Report struct {
	PlatformRows, KeptRows             int
	PlatformFixes, KeptFixes           int
	PublishedInWindow, MissingOnServer int
	AlarmAnswers, AlarmEvents          int
}

type published struct {
	values map[string]string
}

func micro(t time.Time) int64 { return t.UnixMicro() }

func parsePublishLog(raw []byte) (map[string]published, error) {
	out := map[string]published{}
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e sim.PublishLogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, refuse("publish log line %d is not JSON: %v", n, err)
		}
		at, err := time.Parse(time.RFC3339Nano, e.Time)
		if err != nil {
			return nil, refuse("publish log line %d: %v", n, err)
		}
		out[key(e.Device, e.Type, at)] = published{values: e.Values}
	}
	return out, sc.Err()
}

func key(device, eventType string, at time.Time) string {
	return device + "|" + eventType + "|" + strconv.FormatInt(micro(at), 10)
}

// sameNumber says whether a stored value is the published string's number. The store keeps
// a double, so the published decimal parses to exactly the double stored; a position's
// columns are rounded at the store, so they get a tolerance.
func sameNumber(sent string, stored float64, tolerance float64) bool {
	v, err := strconv.ParseFloat(sent, 64)
	if err != nil {
		return false
	}
	if tolerance == 0 {
		return v == stored
	}
	return math.Abs(v-stored) <= tolerance
}

// The column tolerances: a stored coordinate keeps 1e-7 of a degree and more; a heading or
// a speed is kept to four places.
const (
	coordinateTolerance = 1e-7
	columnTolerance     = 1e-3
)

type recDevice struct {
	ID    string `json:"id"`
	Token string `json:"token"`
	Kind  string `json:"kind"`
}

type recAlarm struct {
	ID     string `json:"id"`
	Dev    string `json:"dev"`
	Key    string `json:"key"`
	Metric string `json:"metric"`
	State  string `json:"state"`
	Sev    string `json:"sev"`
	Occ    string `json:"occ"`
	Ack    *bool  `json:"ack,omitempty"`
	// Val is the reading that raised the alarm (the alarm table's Value column): the value
	// the device SENT for the alarm's metric at its raise instant, from the publish log.
	// Absent when this run sent no reading at that instant (an alarm raised before the run).
	Val *float64 `json:"val,omitempty"`
}

type recAlarmEvent struct {
	recAlarm
	TMs int64 `json:"tMs"`
}

type recSnapshot struct {
	TMs         int64      `json:"tMs"`
	RequestedAt int64      `json:"requestedAtMs"`
	Total       int        `json:"total"`
	Alarms      []recAlarm `json:"alarms"`
}

// Recording is the document written, field for field the format's.
type Recording struct {
	Kind            string              `json:"kind"`
	FormatVersion   int                 `json:"formatVersion"`
	RunID           string              `json:"runId"`
	StartedAtUtc    string              `json:"startedAtUtc"`
	DurationMs      int64               `json:"durationMs"`
	PlatformVersion string              `json:"platformVersion"`
	Instance        string              `json:"instance"`
	Tenant          string              `json:"tenant"`
	Build           map[string]string   `json:"build"`
	Clock           []map[string]any    `json:"clock"`
	Devices         []recDevice         `json:"devices"`
	DeclaredMetrics map[string][]string `json:"declaredMetrics,omitempty"`
	Channels        map[string]any      `json:"channels"`
	Board           map[string]string   `json:"board"`
	SourceHashes    map[string]string   `json:"sourceHashes"`
	Converter       map[string]any      `json:"converter"`
	Chapters        []map[string]any    `json:"chapters"`
	Measurements    struct {
		D []int     `json:"d"`
		N []int     `json:"n"`
		T []int64   `json:"t"`
		V []float64 `json:"v"`
		S []int     `json:"s"`
	} `json:"measurements"`
	Alarms struct {
		Snapshots []recSnapshot   `json:"snapshots"`
		Events    []recAlarmEvent `json:"events"`
	} `json:"alarms"`
	Locations struct {
		D   []int      `json:"d"`
		T   []int64    `json:"t"`
		Lat []float64  `json:"lat"`
		Lon []float64  `json:"lon"`
		Hdg []*float64 `json:"hdg"`
		Spd []*float64 `json:"spd"`
	} `json:"locations"`
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

var hexCommit = func(s string, min int) bool {
	if len(s) < min || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func kindOf(externalID string) string {
	switch {
	case strings.HasPrefix(externalID, "SP-HL-"):
		return "Hauler"
	case strings.HasPrefix(externalID, "SP-LD-"):
		return "Loader"
	case strings.HasPrefix(externalID, "SP-DZ-"):
		return "Dozer"
	case strings.HasPrefix(externalID, "SP-PL-"):
		return "Plant"
	}
	return ""
}

// DeclaredByType is what each device type of a manifest declares: its profile's metric keys, in
// the profile's order, by device type token. The manifest is what bootstrapped those profiles,
// so it is the vocabulary the platform's active profile versions hold for the run.
func DeclaredByType(m sim.SimManifest) map[string][]string {
	byProfile := map[string][]string{}
	for _, p := range m.Profiles {
		for _, metric := range p.Metrics {
			byProfile[p.Token] = append(byProfile[p.Token], metric.Key)
		}
	}
	out := map[string][]string{}
	for _, dt := range m.DeviceTypes {
		out[dt.Token] = byProfile[dt.ProfileToken]
	}
	return out
}

// declare writes the declarations per kind. Every device's type must declare something, and every
// device of a kind the same names: the format keys a declaration by kind, and a kind whose devices
// disagreed would state one of them falsely.
func declare(rec *Recording, devices []sim.DeviceInstance, byType map[string][]string) error {
	if byType == nil {
		return nil
	}
	out := map[string][]string{}
	for _, d := range devices {
		names := byType[d.DeviceTypeToken]
		if len(names) == 0 {
			return refuse("%s: its device type %q declares no measurements", d.ExternalId, d.DeviceTypeToken)
		}
		k := kindOf(d.ExternalId)
		if have, ok := out[k]; ok {
			if !slices.Equal(have, names) {
				return refuse("the %s devices do not all declare the same measurements (%v, %v)", k, have, names)
			}
			continue
		}
		out[k] = append([]string(nil), names...)
	}
	rec.DeclaredMetrics = out
	return nil
}

// Build reads the window back from the platform, checks every row against the publish
// log, folds the watched alarm answers into the format's history, and returns the
// recording. Anything that cannot be made honest is a Refusal.
func Build(ctx context.Context, p Platform, o Options) (*Recording, Report, error) {
	var rep Report
	if !o.From.Before(o.To) {
		return nil, rep, refuse("the window is empty: %s .. %s", o.From, o.To)
	}
	if o.LiveFrom.Before(o.From) || !o.LiveFrom.Before(o.To) {
		return nil, rep, refuse("the showcase start %s is not inside the window", o.LiveFrom)
	}
	if !hexCommit(o.BoardCommit, 40) || !hexCommit(o.GitSha, 7) {
		return nil, rep, refuse("the board commit must be a full lowercase hex commit id and the git sha a hex one")
	}
	sent, err := parsePublishLog(o.PublishLog)
	if err != nil {
		return nil, rep, err
	}
	ms := func(t time.Time) int64 { return t.Sub(o.From).Milliseconds() }

	rec := &Recording{
		Kind:            FormatKind,
		FormatVersion:   FormatVersion,
		RunID:           "run-" + o.From.UTC().Format("20060102T150405Z"),
		StartedAtUtc:    o.From.UTC().Format("2006-01-02T15:04:05.000Z"),
		DurationMs:      ms(o.To),
		PlatformVersion: o.PlatformVersion,
		Instance:        o.Instance,
		Tenant:          o.Tenant,
		Build:           map[string]string{"gitSha": o.GitSha, "sdkCommit": o.GitSha},
		Clock:           []map[string]any{{"fromMs": 0, "mode": "real", "scale": 1}},
		Channels: map[string]any{
			"measurements": BoardMeasurements,
			"alarms":       true,
			"locations":    true,
			"commands":     false,
		},
		Board: map[string]string{"path": o.BoardPath, "sourceCommit": o.BoardCommit, "sha256": sha(o.BoardBytes)},
		SourceHashes: map[string]string{
			o.PublishLogName: sha(o.PublishLog),
			o.AlarmsName:     sha(o.Alarms),
		},
		Converter: map[string]any{"version": ProducerVersion, "name": ProducerName},
		Chapters: []map[string]any{{
			"tMs":  ms(o.LiveFrom),
			"name": "showcase",
			"note": "an hour and more of the site's history is behind this point",
		}},
	}

	// The devices, in the order the take2 recording lists them: haulers, loaders, dozers, plant.
	devices := append([]sim.DeviceInstance(nil), o.Devices...)
	order := map[string]int{"Hauler": 0, "Loader": 1, "Dozer": 2, "Plant": 3}
	sort.SliceStable(devices, func(i, j int) bool {
		ki, kj := order[kindOf(devices[i].ExternalId)], order[kindOf(devices[j].ExternalId)]
		if ki != kj {
			return ki < kj
		}
		return devices[i].ExternalId < devices[j].ExternalId
	})
	index := map[string]int{}
	for i, d := range devices {
		k := kindOf(d.ExternalId)
		if k == "" {
			return nil, rep, refuse("%s is not a sitepulse device", d.ExternalId)
		}
		rec.Devices = append(rec.Devices, recDevice{ID: d.ExternalId, Token: d.Token, Kind: k})
		index[d.Token] = i
	}
	if err := declare(rec, devices, o.Declared); err != nil {
		return nil, rep, err
	}
	nameIndex := map[string]int{}
	for i, n := range BoardMeasurements {
		nameIndex[n] = i
	}
	fullRate := map[int]bool{}
	for _, name := range o.FullRate {
		n, ok := nameIndex[name]
		if !ok {
			return nil, rep, refuse("--full-rate %q is not a measurement the board shows", name)
		}
		fullRate[n] = true
	}

	// ---- measurements ----
	type mrow struct {
		d, n int
		t    int64
		v    float64
	}
	var rows []mrow
	seenInWindow := map[string]bool{}
	for _, d := range devices {
		got, err := p.Measurements(ctx, d.Token, o.From, o.To)
		if err != nil {
			return nil, rep, err
		}
		for _, r := range got {
			if r.At.Before(o.From) || r.At.After(o.To) {
				return nil, rep, refuse("the platform answered a %s row at %s, outside the window", r.Device, r.At)
			}
			k := key(r.Device, "Measurement", r.At)
			seenInWindow[k] = true
			n, onBoard := nameIndex[r.Name]
			if r.Name != sim.SitepulseEngineHoursKey && !onBoard {
				return nil, rep, refuse("%s reported %q, which the sitepulse profile does not declare", r.Device, r.Name)
			}
			pub, ok := sent[k]
			if !ok {
				return nil, rep, refuse("the platform holds a %s reading at %s that the devices never sent: a fabricated row",
					r.Device, r.At.Format(time.RFC3339Nano))
			}
			if s, ok := pub.values[r.Name]; !ok || !sameNumber(s, r.Value, 0) {
				return nil, rep, refuse("the platform holds %s %s = %v at %s; the device sent %q", r.Device, r.Name, r.Value,
					r.At.Format(time.RFC3339Nano), pub.values[r.Name])
			}
			rep.PlatformRows++
			if !onBoard {
				continue
			}
			rows = append(rows, mrow{d: index[r.Device], n: n, t: ms(r.At), v: r.Value})
		}
	}
	for k := range sent {
		parts := strings.Split(k, "|")
		at, _ := strconv.ParseInt(parts[2], 10, 64)
		if parts[1] != "Measurement" || at < micro(o.From) || at > micro(o.To) {
			continue
		}
		rep.PublishedInWindow++
		if !seenInWindow[k] {
			rep.MissingOnServer++
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].t != rows[j].t {
			return rows[i].t < rows[j].t
		}
		if rows[i].d != rows[j].d {
			return rows[i].d < rows[j].d
		}
		return rows[i].n < rows[j].n
	})
	live := ms(o.LiveFrom)
	every := o.HistoryEvery.Milliseconds()
	kept := map[[3]int64]bool{}
	for _, r := range rows {
		if every > 0 && r.t < live && !fullRate[r.n] {
			step := [3]int64{int64(r.d), int64(r.n), r.t / every}
			if kept[step] {
				continue
			}
			kept[step] = true
		}
		rec.Measurements.D = append(rec.Measurements.D, r.d)
		rec.Measurements.N = append(rec.Measurements.N, r.n)
		rec.Measurements.T = append(rec.Measurements.T, r.t)
		rec.Measurements.V = append(rec.Measurements.V, r.v)
		rec.Measurements.S = append(rec.Measurements.S, 0)
	}
	rep.KeptRows = len(rec.Measurements.T)

	// ---- positions: the history before the showcase needs only each machine's last fix ----
	type frow struct {
		d   int
		t   int64
		fix Fix
	}
	var fixes []frow
	for _, d := range devices {
		got, err := p.Locations(ctx, d.Token, o.From, o.To)
		if err != nil {
			return nil, rep, err
		}
		for _, f := range got {
			pub, ok := sent[key(f.Device, "Location", f.At)]
			if !ok {
				return nil, rep, refuse("the platform holds a %s position at %s that the device never sent", f.Device, f.At.Format(time.RFC3339Nano))
			}
			if !sameNumber(pub.values["latitude"], f.Latitude, coordinateTolerance) ||
				!sameNumber(pub.values["longitude"], f.Longitude, coordinateTolerance) ||
				(f.Heading != nil && !sameNumber(pub.values["heading"], *f.Heading, columnTolerance)) ||
				(f.Speed != nil && !sameNumber(pub.values["speed"], *f.Speed, columnTolerance)) {
				return nil, rep, refuse("the platform's %s position at %s is not the one the device sent", f.Device, f.At.Format(time.RFC3339Nano))
			}
			rep.PlatformFixes++
			fixes = append(fixes, frow{d: index[f.Device], t: ms(f.At), fix: f})
		}
	}
	sort.SliceStable(fixes, func(i, j int) bool {
		if fixes[i].t != fixes[j].t {
			return fixes[i].t < fixes[j].t
		}
		return fixes[i].d < fixes[j].d
	})
	lastBefore := map[int]int{}
	for i, f := range fixes {
		if f.t < live {
			lastBefore[f.d] = i
		}
	}
	for i, f := range fixes {
		if f.t < live && lastBefore[f.d] != i {
			continue
		}
		rec.Locations.D = append(rec.Locations.D, f.d)
		rec.Locations.T = append(rec.Locations.T, f.t)
		rec.Locations.Lat = append(rec.Locations.Lat, f.fix.Latitude)
		rec.Locations.Lon = append(rec.Locations.Lon, f.fix.Longitude)
		rec.Locations.Hdg = append(rec.Locations.Hdg, f.fix.Heading)
		rec.Locations.Spd = append(rec.Locations.Spd, f.fix.Speed)
	}
	rep.KeptFixes = len(rec.Locations.T)

	// ---- alarms ----
	if err := foldAlarms(rec, o, index, sent, &rep); err != nil {
		return nil, rep, err
	}
	return rec, rep, nil
}

// foldAlarms turns the watched answers into the format's history: the last answer at or
// before the window's start as a snapshot at 0, then one event for every alarm whose state,
// raise or clear time or acknowledgement differs from the previous answer, at the time
// that answer came back. An alarm that VANISHES from an answer cannot be an event, so that
// answer is recorded whole, as a snapshot.
//
// Each alarm carries the reading that raised it, from the publish log (see recAlarm.Val). The
// platform's own lastValue, when the watch has it, must be that reading: a different one is a
// value this recording cannot explain, and is refused.
func foldAlarms(rec *Recording, o Options, index map[string]int, sent map[string]published, rep *Report) error {
	ms := func(t time.Time) int64 { return t.Sub(o.From).Milliseconds() }
	// The first instant the devices published at: an alarm raised before it was raised before
	// this run, by a reading the publish log cannot hold.
	firstSent := time.Time{}
	for k := range sent {
		us, _ := strconv.ParseInt(k[strings.LastIndexByte(k, '|')+1:], 10, 64)
		if at := time.UnixMicro(us); firstSent.IsZero() || at.Before(firstSent) {
			firstSent = at
		}
	}
	var answers []AlarmWatch
	sc := bufio.NewScanner(strings.NewReader(string(o.Alarms)))
	sc.Buffer(make([]byte, 4<<20), 4<<20)
	for n := 1; sc.Scan(); n++ {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var a AlarmWatch
		if err := json.Unmarshal(sc.Bytes(), &a); err != nil {
			return refuse("alarm watch line %d is not JSON: %v", n, err)
		}
		if a.Total != len(a.Alarms) {
			return refuse("alarm watch line %d is a truncated answer", n)
		}
		answers = append(answers, a)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	at := func(s string) time.Time { t, _ := time.Parse(time.RFC3339Nano, s); return t }
	first := -1
	for i, a := range answers {
		if !at(a.AnsweredAt).After(o.From) {
			first = i
		}
	}
	if first < 0 {
		return refuse("no alarm answer was watched at or before the window's start %s: the alarm state at the start is unknown", o.From)
	}
	// The watch must cover the window without a gap a person would notice.
	prev := at(answers[first].AnsweredAt)
	for _, a := range answers[first+1:] {
		t := at(a.AnsweredAt)
		if t.After(o.To) {
			break
		}
		if t.Sub(prev) > time.Minute {
			return refuse("the alarm watch has a gap of %s ending %s", t.Sub(prev), a.AnsweredAt)
		}
		prev = t
	}
	if o.To.Sub(prev) > time.Minute {
		return refuse("the alarm watch ended %s before the window did", o.To.Sub(prev))
	}

	ids := map[string]string{}
	mint := func(token string) string {
		if id, ok := ids[token]; ok {
			return id
		}
		ids[token] = "alarm-" + strconv.Itoa(len(ids)+1)
		return ids[token]
	}
	toRec := func(a WatchedAlarm) (recAlarm, error) {
		if _, ok := index[a.Originator]; !ok {
			return recAlarm{}, refuse("alarm %s is on %q, which is not a device of this run", a.AlarmKey, a.Originator)
		}
		occ := a.RaisedTime
		if a.State == "CLEARED" {
			occ = a.ClearedTime
		}
		if occ == nil {
			return recAlarm{}, refuse("a %s alarm on %s carries no time for its state", a.State, a.Originator)
		}
		t, err := time.Parse(time.RFC3339Nano, *occ)
		if err != nil {
			return recAlarm{}, refuse("alarm time %q: %v", *occ, err)
		}
		r := recAlarm{ID: mint(a.Token), Dev: a.Originator, Key: a.AlarmKey, Metric: a.MetricKey, State: a.State, Sev: a.Severity,
			Occ: t.UTC().Format("2006-01-02T15:04:05.000000Z")}
		if a.Acknowledged {
			yes := true
			r.Ack = &yes
		}
		if a.RaisedTime != nil {
			raised, err := time.Parse(time.RFC3339Nano, *a.RaisedTime)
			if err != nil {
				return recAlarm{}, refuse("alarm raise time %q: %v", *a.RaisedTime, err)
			}
			if pub, ok := sent[key(a.Originator, "Measurement", raised)]; ok {
				if s, ok := pub.values[a.MetricKey]; ok {
					v, err := strconv.ParseFloat(s, 64)
					if err != nil {
						return recAlarm{}, refuse("the publish log holds %s %s = %q at %s, not a number", a.Originator, a.MetricKey, s, *a.RaisedTime)
					}
					r.Val = &v
				}
			}
			switch {
			case a.LastValue == nil:
			case r.Val != nil && *r.Val != *a.LastValue:
				return recAlarm{}, refuse("the platform says %s on %s was raised by %v; the device sent %v at its raise instant %s",
					a.AlarmKey, a.Originator, *a.LastValue, *r.Val, *a.RaisedTime)
			case r.Val == nil && !raised.Before(firstSent):
				// Raised while the devices were publishing, by a reading they never sent.
				return recAlarm{}, refuse("the platform says %s on %s was raised by %v at %s; the device sent no %s at that instant",
					a.AlarmKey, a.Originator, *a.LastValue, *a.RaisedTime, a.MetricKey)
			}
		}
		return r, nil
	}
	snapshot := func(a AlarmWatch, tMs, requested int64) error {
		s := recSnapshot{TMs: tMs, RequestedAt: requested, Total: len(a.Alarms), Alarms: []recAlarm{}}
		sorted := append([]WatchedAlarm(nil), a.Alarms...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Token < sorted[j].Token })
		for _, w := range sorted {
			r, err := toRec(w)
			if err != nil {
				return err
			}
			s.Alarms = append(s.Alarms, r)
		}
		rec.Alarms.Snapshots = append(rec.Alarms.Snapshots, s)
		return nil
	}
	if err := snapshot(answers[first], 0, 0); err != nil {
		return err
	}
	rep.AlarmAnswers = 1
	state := map[string]WatchedAlarm{}
	for _, w := range answers[first].Alarms {
		state[w.Token] = w
	}
	same := func(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	for _, a := range answers[first+1:] {
		answered, requested := at(a.AnsweredAt), at(a.RequestedAt)
		if answered.After(o.To) {
			break
		}
		rep.AlarmAnswers++
		now := map[string]WatchedAlarm{}
		for _, w := range a.Alarms {
			now[w.Token] = w
		}
		vanished := false
		for token := range state {
			if _, ok := now[token]; !ok {
				vanished = true
			}
		}
		if vanished {
			req := ms(requested)
			if req < 0 {
				req = 0
			}
			if err := snapshot(a, ms(answered), req); err != nil {
				return err
			}
			state = now
			continue
		}
		var changed []WatchedAlarm
		for _, w := range a.Alarms {
			old, ok := state[w.Token]
			if !ok || old.State != w.State || !same(old.RaisedTime, w.RaisedTime) || !same(old.ClearedTime, w.ClearedTime) ||
				old.Acknowledged != w.Acknowledged || old.Severity != w.Severity {
				changed = append(changed, w)
			}
		}
		sort.SliceStable(changed, func(i, j int) bool { return changed[i].Token < changed[j].Token })
		for _, w := range changed {
			r, err := toRec(w)
			if err != nil {
				return err
			}
			rec.Alarms.Events = append(rec.Alarms.Events, recAlarmEvent{recAlarm: r, TMs: ms(answered)})
		}
		state = now
	}
	rep.AlarmEvents = len(rec.Alarms.Events)
	return nil
}

// Write writes the recording as compact JSON, newline-terminated.
func Write(w io.Writer, rec *Recording) error {
	if rec == nil {
		return errors.New("no recording")
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

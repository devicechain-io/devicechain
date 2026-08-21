// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-event-sources/processor"
)

// ---- The manifest -----------------------------------------------------------------

func TestFleetpulseManifestShape(t *testing.T) {
	m := NewFleetpulse(1, Load{}).Manifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("fleetpulse manifest does not validate: %v", err)
	}
	if m.Name != "fleetpulse" {
		t.Errorf("manifest name = %q, want %q", m.Name, "fleetpulse")
	}
	if len(m.Profiles) != 1 || m.Profiles[0].Token != FleetpulseProfileToken {
		t.Fatalf("want one profile %q, got %+v", FleetpulseProfileToken, m.Profiles)
	}
	// The speed metric specifically: it is the location fix's companion on a chart, and
	// a scenario emitting speed_mps against a profile that does not declare it would
	// provision cleanly and produce a metric no widget can bind.
	var speed *MetricSpec
	for i, metric := range m.Profiles[0].Metrics {
		if metric.Key == FleetpulseSpeedKey {
			speed = &m.Profiles[0].Metrics[i]
		}
	}
	if speed == nil {
		t.Fatalf("profile declares no %q metric: %+v", FleetpulseSpeedKey, m.Profiles[0].Metrics)
	}
	if speed.Unit != "m/s" {
		t.Errorf("speed metric unit = %q, want %q — the platform's speed unit is fixed, and a "+
			"chart labelled kph over metres per second is a wrong number that looks right",
			speed.Unit, "m/s")
	}
	if len(m.DeviceTypes) != 1 || m.DeviceTypes[0].ProfileToken != FleetpulseProfileToken {
		t.Errorf("want one device type on profile %q, got %+v", FleetpulseProfileToken, m.DeviceTypes)
	}
	if len(m.Populations) != 1 || m.Populations[0].Count != fleetpulseVehicleCount {
		t.Fatalf("want one population of %d, got %+v", fleetpulseVehicleCount, m.Populations)
	}
	if m.FixedTopology || m.DevicesPublishTheirOwnTelemetry {
		t.Error("fleetpulse drives its own telemetry from Tick and has no bound dashboards, " +
			"so it must be both resizable and load-drivable")
	}
	devices := m.Expand(m.Seed)
	if len(devices) != fleetpulseVehicleCount {
		t.Fatalf("expanded %d devices, want %d", len(devices), fleetpulseVehicleCount)
	}
	if devices[0].Token != "fleetpulse-001" || devices[0].ExternalId != "VIN-FLEETPULSE-00001" {
		t.Errorf("first device = (%q, %q), want (fleetpulse-001, VIN-FLEETPULSE-00001)",
			devices[0].Token, devices[0].ExternalId)
	}
}

// ---- The track --------------------------------------------------------------------

// fleetpulseTestCounts are the fleet sizes every track property is checked at: one
// vehicle, the demo sizing, and an awkward resize. Spacing is 2*pi/count, so a bug in
// the angle derivation can easily hold at 6 and not at 17.
var fleetpulseTestCounts = []int{1, fleetpulseVehicleCount, 17}

// 🔴 Every generated position must be accepted by the REAL decoder.
//
// This is the in-range check, and it is deliberately NOT a restatement of the bounds:
// a test that re-declared "latitude within +/-90" would pass against a producer that
// drifted along with it, and would say nothing about the ranges the platform actually
// enforces — heading's exclusive 360 checked half a rounding-quantum early, accuracy
// and speed non-negative, the metre magnitude ceiling. The decoder owns those. The
// track is emitted and run through it.
func TestFleetpulseTrackStaysInsideTheCoordinateContract(t *testing.T) {
	var mu sync.Mutex
	rejected := 0
	rt := fakeIngress(t, 1, func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if _, _, err := processor.NewJsonDecoder(nil, 0).Decode(raw); err != nil {
			mu.Lock()
			rejected++
			if rejected <= 3 {
				t.Errorf("the real decoder refused a generated fix: %v\n%s", err, raw)
			}
			mu.Unlock()
		}
		w.WriteHeader(http.StatusAccepted)
	})

	emitted := 0
	// One full circuit plus a tick, so the wrap point itself is covered rather than
	// only the interior of the loop.
	for _, count := range fleetpulseTestCounts {
		for tick := int64(1); tick <= fleetpulseLoopTicks+1; tick++ {
			for i := 0; i < count; i++ {
				fix := fleetpulseFix(7, tick, i, count)
				if err := EmitLocation(context.Background(), rt, rt.Devices[0], fix); err != nil {
					t.Fatalf("EmitLocation: %v", err)
				}
				emitted++
			}
		}
	}
	// Negative control: a loop that emitted nothing would report a perfect track.
	if emitted == 0 {
		t.Fatal("no fixes were emitted, so this test asserted nothing")
	}
	if rejected > 0 {
		t.Errorf("%d of %d generated fixes were refused at decode", rejected, emitted)
	}
}

// The same bounds over a range no HTTP-backed test can afford, so "a long run stays
// bounded" is measured rather than asserted about the first loop.
//
// The bounds here ARE restated, and that is the trade: the decoder-backed test above is
// the authority on what the platform accepts, and this one covers a hundred thousand
// ticks to show the track does not creep. A pure-function sweep is the only instrument
// that reaches that far.
func TestFleetpulseTrackDoesNotCreepOverALongRun(t *testing.T) {
	for _, count := range fleetpulseTestCounts {
		for tick := int64(1); tick <= 100_000; tick++ {
			for i := 0; i < count; i++ {
				fix := fleetpulseFix(7, tick, i, count)
				if math.Abs(fix.Latitude-FleetpulseCentreLatitude) > fleetpulseLoopRadius+1e-9 {
					t.Fatalf("tick %d vehicle %d/%d: latitude %v left the loop", tick, i, count, fix.Latitude)
				}
				lonRadius := fleetpulseLoopRadius * fleetpulseLongitudeScale
				if math.Abs(fix.Longitude-FleetpulseCentreLongitude) > lonRadius+1e-9 {
					t.Fatalf("tick %d vehicle %d/%d: longitude %v left the loop", tick, i, count, fix.Longitude)
				}
				if *fix.Heading < 0 || *fix.Heading >= 360 {
					t.Fatalf("tick %d vehicle %d/%d: heading %v is outside [0, 360)", tick, i, count, *fix.Heading)
				}
				if *fix.Speed < 0 || *fix.Accuracy < 0 {
					t.Fatalf("tick %d vehicle %d/%d: speed %v / accuracy %v went negative — both "+
						"are refused at decode", tick, i, count, *fix.Speed, *fix.Accuracy)
				}
			}
		}
	}
}

// fleetpulseTrack renders one fleet's positions over a window, as comparable strings.
func fleetpulseTrack(seed int64, ticks int64, count int) []string {
	var out []string
	for tick := int64(1); tick <= ticks; tick++ {
		for i := 0; i < count; i++ {
			fix := fleetpulseFix(seed, tick, i, count)
			out = append(out, formatFixValue(fix.Latitude)+","+formatFixValue(fix.Longitude)+
				","+formatFixValue(*fix.Heading)+","+formatFixValue(*fix.Speed))
		}
	}
	return out
}

// A given (sim, seed) must render the same track every run — that is what makes a demo
// reproducible and a bug report reproducible with it.
//
// Both directions are asserted. "Same seed, same track" alone is satisfied by a
// generator that ignores the seed entirely, which would make every sim of this scenario
// draw one identical track and make the seed a lie.
func TestFleetpulseTrackIsDeterministicForAFixedSeed(t *testing.T) {
	first := fleetpulseTrack(42, 40, fleetpulseVehicleCount)
	second := fleetpulseTrack(42, 40, fleetpulseVehicleCount)
	if len(first) == 0 {
		t.Fatal("the track is empty, so every comparison here is vacuous")
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("seed 42 rendered two different tracks: position %d was %q then %q",
				i, first[i], second[i])
		}
	}

	other := fleetpulseTrack(43, 40, fleetpulseVehicleCount)
	same := true
	for i := range first {
		if first[i] != other[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("seeds 42 and 43 render the identical track, so the seed does not reach " +
			"the generator and two sims of this scenario are indistinguishable")
	}
}

// The loop must CLOSE: tick t and tick t + one circuit are the same position.
//
// This is the property that keeps a long demo bounded, and it is the one an
// integrate-the-heading generator silently lacks — such a generator looks correct for
// ten minutes and then the fleet is in the Atlantic, with nothing having gone wrong
// that anyone can point at.
func TestFleetpulseLoopIsClosed(t *testing.T) {
	for _, count := range fleetpulseTestCounts {
		for tick := int64(1); tick <= fleetpulseLoopTicks; tick++ {
			for i := 0; i < count; i++ {
				here := fleetpulseFix(7, tick, i, count)
				lap := fleetpulseFix(7, tick+fleetpulseLoopTicks, i, count)
				if here.Latitude != lap.Latitude || here.Longitude != lap.Longitude {
					t.Fatalf("vehicle %d/%d at tick %d is (%v, %v) but one circuit later is "+
						"(%v, %v): the loop does not close", i, count, tick,
						here.Latitude, here.Longitude, lap.Latitude, lap.Longitude)
				}
			}
		}
	}
	// Negative control: a "closed loop" would also be satisfied by a fleet that never
	// moves, and a stationary demo is the failure this whole scenario exists to avoid.
	start := fleetpulseFix(7, 1, 0, fleetpulseVehicleCount)
	quarter := fleetpulseFix(7, 1+fleetpulseLoopTicks/4, 0, fleetpulseVehicleCount)
	if start.Latitude == quarter.Latitude && start.Longitude == quarter.Longitude {
		t.Error("the vehicle has not moved a quarter of the way round the loop, so " +
			"'the loop closes' is true only because nothing ever moves")
	}
}

// The longitude scale must match the centre latitude it was computed for, or the
// circuit is an ellipse and every heading derived from it is wrong.
func TestFleetpulseLongitudeScaleMatchesTheCentreLatitude(t *testing.T) {
	want := 1 / math.Cos(FleetpulseCentreLatitude*math.Pi/180)
	if math.Abs(fleetpulseLongitudeScale-want) > 1e-4 {
		t.Errorf("fleetpulseLongitudeScale = %v but 1/cos(%v deg) = %v", fleetpulseLongitudeScale,
			FleetpulseCentreLatitude, want)
	}
}

// A vehicle's reported heading must point where it is actually going.
//
// A heading that is merely in range is worthless: it renders as an arrow on a map, and
// an arrow pointing the wrong way is a wrong answer that looks like a right one. This
// compares the reported bearing against the bearing to the NEXT generated position,
// converting the longitude difference back to ground distance so the comparison is on
// the ground rather than in the projection.
func TestFleetpulseHeadingPointsTheWayTheVehicleIsMoving(t *testing.T) {
	const count = fleetpulseVehicleCount
	for tick := int64(1); tick <= fleetpulseLoopTicks; tick++ {
		for i := 0; i < count; i++ {
			here := fleetpulseFix(7, tick, i, count)
			next := fleetpulseFix(7, tick+1, i, count)

			north := next.Latitude - here.Latitude
			east := (next.Longitude - here.Longitude) * math.Cos(FleetpulseCentreLatitude*math.Pi/180)
			travel := canonicalHeading(math.Atan2(east, north) * 180 / math.Pi)

			// The chord between two samples is a secant of the circuit, so it lags the
			// tangent by half the angular step — 360/(2*72) = 2.5 degrees. The tolerance
			// is that, with room to spare, and is far tighter than any wrong-field or
			// wrong-sign defect could hide in.
			diff := math.Abs(travel - *here.Heading)
			if diff > 180 {
				diff = 360 - diff
			}
			if diff > 5 {
				t.Fatalf("vehicle %d at tick %d reports heading %v but is travelling %v "+
					"(difference %v deg)", i, tick, *here.Heading, travel, diff)
			}
		}
	}
}

// canonicalHeading's own contract, including the case the column's rounding creates.
func TestCanonicalHeadingStaysInsideTheStoredRange(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{
		{0, 0},
		{271.125, 271.125},
		{360, 0},
		{720, 0},
		{-90, 270},
		// A hair below zero wraps to a hair below 360, which then folds to north.
		{-0.0001, 0},
		// 🔴 The one that matters. 359.99999 is a legal float below 360 and stores as
		// exactly 360.0000 after the column's 4-dp rounding — the second spelling of
		// north the exclusive bound exists to keep out.
		{359.99999, 0},
		{359.9999, 0},
	} {
		if got := canonicalHeading(tc.in); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("canonicalHeading(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
	// And nothing it returns may be a heading the platform refuses.
	//
	// 🔑 This ASKS THE REAL DECODER rather than comparing against the bound written
	// down here. The bound is an unexported constant in another module, so a literal
	// copy of it in this file would be a mirrored value whose hazard is not that a
	// change breaks it — it is that a change leaves it syntactically fine and
	// semantically stale, still passing against a number the platform no longer
	// uses. That failure mode is exactly what wirevocabulary.go exists to warn about.
	// Driving the decoder cannot go stale: if the bound moves, this moves with it.
	for deg := -1000.0; deg <= 1000.0; deg += 0.37 {
		got := canonicalHeading(deg)
		body := fmt.Sprintf(
			`{"device":"d1","eventType":"Location","payload":{"entries":[`+
				`{"latitude":"33.749","longitude":"-84.388","heading":%q}]}}`,
			formatFixValue(got))
		if _, _, err := processor.NewJsonDecoder(nil, 0).Decode([]byte(body)); err != nil {
			t.Fatalf("canonicalHeading(%v) = %v, which the real decoder refuses: %v", deg, got, err)
		}
	}
}

// ---- The tick ---------------------------------------------------------------------

// emittedEvent is one body a tick posted, decoded enough to say what it was.
type emittedEvent struct {
	device    string
	eventType string
	entry     map[string]string
}

// runFleetpulseTick drives one real Tick against a capturing ingress and returns what
// reached the wire, having run EVERY body through the real decoder on the way.
func runFleetpulseTick(t *testing.T, count int) []emittedEvent {
	t.Helper()
	var (
		mu   sync.Mutex
		seen []emittedEvent
	)
	rt := fakeIngress(t, count, func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if _, _, derr := processor.NewJsonDecoder(nil, 0).Decode(raw); derr != nil {
			t.Errorf("the real decoder refused a tick's event: %v\n%s", derr, raw)
		}
		var envelope struct {
			Device    string `json:"device"`
			EventType string `json:"eventType"`
			Payload   struct {
				Entries []struct {
					Measurements map[string]string `json:"measurements"`
				} `json:"entries"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Errorf("unmarshal body: %v", err)
		}
		// The location fields sit at the top of the entry and the measurements sit one
		// level in, so both are read as a flat string map here.
		var entries []map[string]json.RawMessage
		var flat struct {
			Payload struct {
				Entries []map[string]json.RawMessage `json:"entries"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(raw, &flat); err == nil {
			entries = flat.Payload.Entries
		}
		values := map[string]string{}
		if len(entries) == 1 {
			for key, rawValue := range entries[0] {
				var s string
				if err := json.Unmarshal(rawValue, &s); err == nil {
					values[key] = s
				}
			}
		}
		if len(envelope.Payload.Entries) == 1 {
			for key, value := range envelope.Payload.Entries[0].Measurements {
				values[key] = value
			}
		}
		mu.Lock()
		seen = append(seen, emittedEvent{envelope.Device, envelope.EventType, values})
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	})

	driver := NewFleetpulse(7, Load{})
	if err := driver.Tick(context.Background(), rt); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	return seen
}

// Each tick must emit BOTH events, one of each, for every device — and both must
// decode.
func TestFleetpulseTickEmitsALocationAndAMeasurementPerDevice(t *testing.T) {
	const count = fleetpulseVehicleCount
	seen := runFleetpulseTick(t, count)

	byDevice := map[string]map[string]int{}
	for _, event := range seen {
		if byDevice[event.device] == nil {
			byDevice[event.device] = map[string]int{}
		}
		byDevice[event.device][event.eventType]++
	}
	if len(byDevice) != count {
		t.Fatalf("%d distinct devices emitted, want %d", len(byDevice), count)
	}
	for device, counts := range byDevice {
		if counts["Location"] != 1 || counts["Measurement"] != 1 {
			t.Errorf("device %s emitted %d Location and %d Measurement events, want 1 of each",
				device, counts["Location"], counts["Measurement"])
		}
		if len(counts) != 2 {
			t.Errorf("device %s emitted event types %v, want exactly Location and Measurement",
				device, counts)
		}
	}
}

// The speed in the fix and the speed_mps metric must be ONE value seen twice.
//
// They are generated from a single fleetpulseFix call per pass, and this holds the two
// passes to that. Two independently-plausible curves would look identical on a
// dashboard and be a lie about the same instant — which is exactly the sort of
// disagreement a location demo is supposed to expose, not create.
func TestFleetpulseSpeedIsOneValueOnBothPaths(t *testing.T) {
	const count = fleetpulseVehicleCount
	seen := runFleetpulseTick(t, count)

	location := map[string]string{}
	measurement := map[string]string{}
	for _, event := range seen {
		switch event.eventType {
		case "Location":
			location[event.device] = event.entry["speed"]
		case "Measurement":
			measurement[event.device] = event.entry[FleetpulseSpeedKey]
		}
	}
	if len(location) != count || len(measurement) != count {
		t.Fatalf("captured %d location and %d measurement speeds, want %d of each",
			len(location), len(measurement), count)
	}
	for device, raw := range location {
		// Compared as NUMBERS: the two paths format independently (the fix uses 'f'
		// notation, the measurement uses %v), so a string comparison would fail on
		// formatting rather than on the value, and fixing that failure would mean
		// weakening the assertion.
		fromFix, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			t.Errorf("device %s location speed %q is not a number: %v", device, raw, err)
			continue
		}
		fromMetric, err := strconv.ParseFloat(measurement[device], 64)
		if err != nil {
			t.Errorf("device %s speed_mps %q is not a number: %v", device, measurement[device], err)
			continue
		}
		if math.Abs(fromFix-fromMetric) > 1e-9 {
			t.Errorf("device %s reported speed %v on its location and %v as its metric",
				device, fromFix, fromMetric)
		}
	}
	// Negative control: every device reporting the same speed would satisfy the pairing
	// above while telling us the per-device index never reached the generator.
	distinct := map[string]bool{}
	for _, raw := range location {
		distinct[raw] = true
	}
	if len(distinct) < 2 {
		t.Errorf("all %d vehicles report the same speed (%v), so the pairing check would "+
			"pass with the device index ignored entirely", count, distinct)
	}
}

// A tick against an empty device set must do nothing rather than divide by it.
func TestFleetpulseTickWithNoDevicesIsANoOp(t *testing.T) {
	rt := fakeIngress(t, 0, func(w http.ResponseWriter, r *http.Request) {
		t.Error("an empty device set still POSTed to the ingress")
	})
	if err := NewFleetpulse(7, Load{}).Tick(context.Background(), rt); err != nil {
		t.Fatalf("Tick on an empty device set: %v", err)
	}
}

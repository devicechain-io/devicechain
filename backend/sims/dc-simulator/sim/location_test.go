// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-event-sources/processor"
)

// ---- The location wire contract ---------------------------------------------------
//
// These tests exist because the location spine had no producer for the life of the
// project, and a wire shape nobody emits is a wire shape nobody has had to be right
// about. Both in-repo location fixtures used a form the decoder accepted and stored
// nothing for; deleting latitude, longitude and elevation from them left the suite
// green. So the assertions here are deliberately over the RAW BYTES and over the REAL
// DECODER, not over a struct this package built and then read back to itself.

// captureIngress is fakeIngress with the raw request bodies kept. The bytes matter
// rather than a decoded view of them: half of what these tests assert — a value being
// a JSON string, a key being ABSENT — is invisible once the body has been unmarshalled
// into a typed struct, which is precisely how the wrong shape survived in fixtures.
type captureIngress struct {
	rt     *Runtime
	bodies chan []byte
}

func newCaptureIngress(t *testing.T, deviceCount int) *captureIngress {
	t.Helper()
	// Buffered well past any test's emit count so a handler never blocks on a reader.
	ing := &captureIngress{bodies: make(chan []byte, 4096)}
	ing.rt = fakeIngress(t, deviceCount, func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		ing.bodies <- raw
		w.WriteHeader(http.StatusAccepted)
	})
	return ing
}

// sole returns the single body captured, failing if there was not exactly one. The
// count is part of the contract: one entry per event means one POST per fix, and a
// producer that batched would still satisfy every field-level assertion below.
func (c *captureIngress) sole(t *testing.T) []byte {
	t.Helper()
	close(c.bodies)
	var all [][]byte
	for body := range c.bodies {
		all = append(all, body)
	}
	if len(all) != 1 {
		t.Fatalf("%d requests reached the ingress, want exactly 1", len(all))
	}
	return all[0]
}

// A fix whose every field is distinct and NON-ROUND. Round numbers hide two whole
// classes of defect at once: a swapped pair of fields still "looks right", and a
// formatter that dropped precision or reached for exponent notation prints the same
// thing either way.
func testFix() Fix {
	elevation, accuracy, speed, heading := 320.55, 4.25, 13.75, 271.125
	return Fix{
		Latitude:  33.74912345,
		Longitude: -84.38854321,
		Elevation: &elevation,
		Accuracy:  &accuracy,
		Speed:     &speed,
		Heading:   &heading,
	}
}

// rawEntry is entries[0] with every value left as raw JSON, so a test can ask what
// TYPE it arrived as and whether a key is there at all.
func rawEntry(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var envelope struct {
		Payload struct {
			Entries []map[string]json.RawMessage `json:"entries"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("unmarshal emitted body: %v\n%s", err, body)
	}
	if len(envelope.Payload.Entries) != 1 {
		t.Fatalf("payload.entries has %d entries, want exactly 1 — content belongs under "+
			"payload.entries[] and one entry per event.\n%s", len(envelope.Payload.Entries), body)
	}
	return envelope.Payload.Entries[0]
}

// The emitted body must be the canonical shape, field by field, as STRINGS.
func TestEmittedLocationBodyIsTheCanonicalWireShape(t *testing.T) {
	ing := newCaptureIngress(t, 1)
	if err := EmitLocation(context.Background(), ing.rt, ing.rt.Devices[0], testFix()); err != nil {
		t.Fatalf("EmitLocation: %v", err)
	}
	body := ing.sole(t)

	var envelope struct {
		Device         string `json:"device"`
		EventType      string `json:"eventType"`
		OccurredTime   string `json:"occurredTime"`
		CredentialType string `json:"credentialType"`
		CredentialId   string `json:"credentialId"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("unmarshal emitted body: %v\n%s", err, body)
	}
	if envelope.Device != ing.rt.Devices[0].Token {
		t.Errorf("device = %q, want %q", envelope.Device, ing.rt.Devices[0].Token)
	}
	if envelope.EventType != "Location" {
		t.Errorf("eventType = %q, want %q — the decoder switches on this and a location "+
			"posted as anything else is decoded by the wrong builder", envelope.EventType, "Location")
	}
	if envelope.CredentialType != credentialTypeAccessToken || envelope.CredentialId != "cred" {
		t.Errorf("credential = (%q, %q), want (%q, %q): a location event authenticates the "+
			"same way a measurement does",
			envelope.CredentialType, envelope.CredentialId, credentialTypeAccessToken, "cred")
	}
	if _, err := time.Parse(time.RFC3339Nano, envelope.OccurredTime); err != nil {
		t.Errorf("occurredTime %q is not RFC3339Nano: %v", envelope.OccurredTime, err)
	}

	entry := rawEntry(t, body)
	// The expected strings are written out rather than computed with the emitter's own
	// formatter: a test that formats its expectation the way the code does would agree
	// with any formatting the code chose, including exponent notation.
	for _, want := range []struct{ key, value string }{
		{"latitude", "33.74912345"},
		{"longitude", "-84.38854321"},
		{"elevation", "320.55"},
		{"accuracy", "4.25"},
		{"speed", "13.75"},
		{"heading", "271.125"},
	} {
		raw, ok := entry[want.key]
		if !ok {
			t.Errorf("entry has no %q", want.key)
			continue
		}
		var got string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("%s = %s, which is not a JSON string: %v — every value on this wire "+
				"is a string, and a bare number fails the WHOLE decode", want.key, raw, err)
			continue
		}
		if got != want.value {
			t.Errorf("%s = %q, want %q", want.key, got, want.value)
		}
	}
	// The per-entry stamp is carried even though persistence discards it, mirroring the
	// measurement entry — but it must not be mistaken for one of the six fix fields.
	if _, ok := entry["occurredTime"]; !ok {
		t.Error("entry carries no occurredTime")
	}
}

// 🔴 THE ORACLE. The exact bytes the emitter produced, run through event-sources' own
// JsonDecoder — the thing that actually decides what the platform accepts.
//
// Everything else in this file is this package agreeing with itself. This is the test
// that makes the simulator a real oracle: if the wire contract moves, or if a value
// stops being a string, or if the entries wrapper is dropped, it fails HERE — in a
// unit test, in the module that produces the bytes — rather than in a Unity scene
// against a live cluster, which is where the previous contract change would have been
// found.
func TestEmittedLocationDecodesThroughTheRealDecoder(t *testing.T) {
	ing := newCaptureIngress(t, 1)
	if err := EmitLocation(context.Background(), ing.rt, ing.rt.Devices[0], testFix()); err != nil {
		t.Fatalf("EmitLocation: %v", err)
	}
	body := ing.sole(t)

	event, payload, err := processor.NewJsonDecoder(nil).Decode(body)
	if err != nil {
		t.Fatalf("the REAL decoder rejected the emitter's own bytes: %v\n%s", err, body)
	}
	if event.EventType != model.Location {
		t.Fatalf("decoded event type = %v, want %v", event.EventType, model.Location)
	}
	locations, ok := payload.(*model.UnresolvedLocationsPayload)
	if !ok {
		t.Fatalf("decoded payload is %T, want *model.UnresolvedLocationsPayload", payload)
	}
	if len(locations.Entries) != 1 {
		t.Fatalf("decoded %d location entries, want exactly 1 — a zero-entry decode is what "+
			"a flat payload produces, and it used to be acked as success", len(locations.Entries))
	}

	entry := locations.Entries[0]
	for _, check := range []struct {
		name string
		got  *string
		want string
	}{
		{"latitude", entry.Latitude, "33.74912345"},
		{"longitude", entry.Longitude, "-84.38854321"},
		{"elevation", entry.Elevation, "320.55"},
		{"accuracy", entry.Accuracy, "4.25"},
		{"speed", entry.Speed, "13.75"},
		{"heading", entry.Heading, "271.125"},
	} {
		if check.got == nil {
			t.Errorf("decoded %s is nil — the field did not survive the wire", check.name)
			continue
		}
		if *check.got != check.want {
			t.Errorf("decoded %s = %q, want %q", check.name, *check.got, check.want)
		}
	}
}

// An unreported optional must be ABSENT from the JSON, not sent as "0".
//
// Asserted on the RAW KEYS, which is the only place the distinction exists: both
// spellings unmarshal into the decoder's *string entry without complaint, and one of
// them then persists a heading of due north for a vehicle that has no compass — a
// stored zero being indistinguishable from a measured one forever after.
func TestAnUnreportedOptionalIsAbsentFromTheJson(t *testing.T) {
	ing := newCaptureIngress(t, 1)
	// Latitude/longitude only: the four optionals are all unreported.
	fix := Fix{Latitude: 33.74912345, Longitude: -84.38854321}
	if err := EmitLocation(context.Background(), ing.rt, ing.rt.Devices[0], fix); err != nil {
		t.Fatalf("EmitLocation: %v", err)
	}
	body := ing.sole(t)
	entry := rawEntry(t, body)

	for _, key := range []string{"elevation", "accuracy", "speed", "heading"} {
		if raw, present := entry[key]; present {
			t.Errorf("unreported %s is present as %s; it must be ABSENT — %q is a device "+
				"REPORTING that value, and for heading that is a claim of due north",
				key, raw, string(raw))
		}
	}
	// The negative control: the required pair must still be there, or "absent" would be
	// satisfied by an emitter that sent nothing at all.
	for _, key := range []string{"latitude", "longitude"} {
		if _, present := entry[key]; !present {
			t.Errorf("required field %q is missing, so this test's absence checks say nothing", key)
		}
	}
	// And a fix with no optionals must still be a LEGAL event, not merely a small one:
	// the decoder REQUIRES latitude and longitude, so an emitter that had gone one field
	// too far in omitting things would satisfy every absence check above and produce a
	// body the platform refuses.
	if _, _, err := processor.NewJsonDecoder(nil).Decode(body); err != nil {
		t.Fatalf("a fix reporting only latitude/longitude was refused by the real decoder: "+
			"%v — the optionals are optional, so omitting them must still decode\n%s", err, body)
	}
}

// No fix value may reach the wire in exponent notation.
//
// "1e-07" is a legal float to the decoder and stores correctly, so this is not a
// correctness bug in the platform — it is a value that reads as a producer bug to
// everyone who meets it in a log, a dead-letter or a debugger, and costs an afternoon.
func TestAFixValueNeverReachesTheWireInExponentNotation(t *testing.T) {
	ing := newCaptureIngress(t, 1)
	tiny := 0.0000001
	fix := Fix{Latitude: tiny, Longitude: -tiny, Accuracy: &tiny}
	if err := EmitLocation(context.Background(), ing.rt, ing.rt.Devices[0], fix); err != nil {
		t.Fatalf("EmitLocation: %v", err)
	}
	entry := rawEntry(t, ing.sole(t))
	for key, raw := range entry {
		if key == "occurredTime" {
			continue
		}
		if strings.ContainsAny(string(raw), "eE") {
			t.Errorf("%s reached the wire as %s — exponent notation decodes fine and reads "+
				"as a bug to every human who sees it", key, raw)
		}
	}
	if got := string(entry["latitude"]); got != `"0.0000001"` {
		t.Errorf("latitude = %s, want \"0.0000001\"", got)
	}
}

// EmitLocationAll must share EmitAll's accounting exactly: a 429 is a governed SHED,
// a device that reports no fix is SILENT and posts nothing.
//
// The two emitters run through one pool for this reason. A location path with its own
// copy of the switch would eventually file a shed as a failure, and a run under a
// contention floor would report a break that never happened.
func TestEmitLocationAllRoutesShedAndSilenceLikeEmitAll(t *testing.T) {
	t.Run("shed", func(t *testing.T) {
		rt := fakeIngress(t, 8, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
		err := EmitLocationAll(context.Background(), rt, 4, func(int, DeviceInstance) (Fix, bool) {
			return testFix(), true
		})
		if err != nil {
			t.Errorf("a tick that only SHED reported failure: %v", err)
		}
		if got := rt.Stats.Shed.Load(); got != 8 {
			t.Errorf("shed = %d, want 8", got)
		}
		if got := rt.Stats.Failed.Load(); got != 0 {
			t.Errorf("failed = %d, want 0 — a 429 is a clean shed", got)
		}
	})

	t.Run("silence", func(t *testing.T) {
		var posts atomic.Int64
		rt := fakeIngress(t, 4, func(w http.ResponseWriter, r *http.Request) {
			posts.Add(1)
			w.WriteHeader(http.StatusAccepted)
		})
		err := EmitLocationAll(context.Background(), rt, 2, func(i int, _ DeviceInstance) (Fix, bool) {
			// Every other device reports nothing. The zero Fix is a REAL position
			// (Null Island), so a device going quiet has to say so with the bool.
			return Fix{}, i%2 == 1
		})
		if err != nil {
			t.Fatalf("EmitLocationAll: %v", err)
		}
		if got := posts.Load(); got != 2 {
			t.Errorf("%d devices reached the ingress, want 2 — a device reporting no fix "+
				"must not post Null Island", got)
		}
		if got := rt.Stats.Emitted.Load(); got != 2 {
			t.Errorf("counted %d emits, want 2", got)
		}
	})
}

// The Measurement envelope must have survived the extraction of the shared post helper.
//
// The existing emit tests read entries[0].measurements and would not notice a changed
// eventType, a dropped credential, or an envelope stamp that no longer matches the
// entry's — all three of which the refactor could have introduced silently, and the
// last of which would put two different times on one reading.
func TestMeasurementEnvelopeSurvivedTheSharedPostHelper(t *testing.T) {
	ing := newCaptureIngress(t, 1)
	err := EmitMeasurements(context.Background(), ing.rt, ing.rt.Devices[0],
		map[string]float64{"speed_mps": 13.75})
	if err != nil {
		t.Fatalf("EmitMeasurements: %v", err)
	}
	body := ing.sole(t)

	var envelope struct {
		Device         string `json:"device"`
		EventType      string `json:"eventType"`
		OccurredTime   string `json:"occurredTime"`
		CredentialType string `json:"credentialType"`
		CredentialId   string `json:"credentialId"`
		Payload        struct {
			Entries []struct {
				Measurements map[string]string `json:"measurements"`
				OccurredTime string            `json:"occurredTime"`
			} `json:"entries"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("unmarshal emitted body: %v\n%s", err, body)
	}
	if envelope.EventType != "Measurement" {
		t.Errorf("eventType = %q, want %q", envelope.EventType, "Measurement")
	}
	if envelope.Device != ing.rt.Devices[0].Token ||
		envelope.CredentialType != credentialTypeAccessToken || envelope.CredentialId != "cred" {
		t.Errorf("envelope identity = (%q, %q, %q), want (%q, %q, %q)",
			envelope.Device, envelope.CredentialType, envelope.CredentialId,
			ing.rt.Devices[0].Token, credentialTypeAccessToken, "cred")
	}
	if len(envelope.Payload.Entries) != 1 {
		t.Fatalf("payload.entries has %d entries, want 1", len(envelope.Payload.Entries))
	}
	if got := envelope.Payload.Entries[0].Measurements["speed_mps"]; got != "13.75" {
		t.Errorf("measurements[speed_mps] = %q, want %q", got, "13.75")
	}
	if envelope.OccurredTime != envelope.Payload.Entries[0].OccurredTime {
		t.Errorf("envelope stamp %q and entry stamp %q differ; they are one reading and the "+
			"helper takes the time as an argument precisely so they cannot drift",
			envelope.OccurredTime, envelope.Payload.Entries[0].OccurredTime)
	}
	// And the measurement path still decodes, for the same reason the location one must.
	if _, _, err := processor.NewJsonDecoder(nil).Decode(body); err != nil {
		t.Fatalf("the real decoder rejected an emitted Measurement: %v\n%s", err, body)
	}
}

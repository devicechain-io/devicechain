// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventidentity"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
)

// The ORACLE: the json.Marshal derivations exactly as they were before the identity bytes
// were built directly (event_identity.go DeriveEventId over json.Marshal, and
// canonicalPayloadEntry + DerivePayloadId). They are kept here, verbatim, as the definition
// of what every digest must stay.

func oracleEventId(tenant string, ev *Event, payload any) ([]byte, error) {
	encoded, err := json.Marshal(eventidentity.Payload(payload))
	if err != nil {
		return nil, err
	}
	return DeriveEventId(tenant, ev, encoded), nil
}

func oracleMeasurementId(r *MeasurementEventCreateRequest) ([]byte, error) {
	if r.Derived {
		entry, err := json.Marshal(struct {
			Kind     string
			Name     string
			Occurred time.Time
		}{"derived", r.Name, CanonicalEntryInstant(r.EntryOccurredTime)})
		if err != nil {
			return nil, err
		}
		return DerivePayloadId(r.EventId, entry), nil
	}
	entry, err := json.Marshal(struct {
		Name           string
		Value          *float64
		Classifier     *uint
		Unit, DataType *string
		Occurred       time.Time
	}{r.Name, r.Value, r.Classifier, r.Unit, r.DataType, r.EntryOccurredTime})
	if err != nil {
		return nil, err
	}
	return DerivePayloadId(r.EventId, entry), nil
}

func oracleLocationId(r *LocationEventCreateRequest) ([]byte, error) {
	entry, err := json.Marshal(struct {
		Lat, Lon, Elev           *float64
		Accuracy, Speed, Heading *float64
		Occurred                 time.Time
	}{r.Latitude, r.Longitude, r.Elevation, r.Accuracy, r.Speed, r.Heading, r.EntryOccurredTime})
	if err != nil {
		return nil, err
	}
	return DerivePayloadId(r.EventId, entry), nil
}

func oracleAlertId(r *AlertEventCreateRequest) ([]byte, error) {
	entry, err := json.Marshal(struct {
		Type     string
		Level    uint32
		Message  string
		Source   string
		Occurred time.Time
	}{r.Type, r.Level, r.Message, r.Source, r.EntryOccurredTime})
	if err != nil {
		return nil, err
	}
	return DerivePayloadId(r.EventId, entry), nil
}

// ---- generators ---------------------------------------------------------------

var stringPool = []string{
	"", "a", "speed_kph", "temperature", "device-00001", "sim-r26-4mqtt", "Hello, World",
	`quote"inside`, `back\slash`, "tab\there", "new\nline", "cr\rlf", "bell\a", "nul\x00byte",
	"\x01\x02\x1f", "del\x7f", "<script>alert(1)</script>", "a&b", "ampersand &amp; <tag>",
	"é", "日本語", "emoji 😀🚀", "line sep", "para sep", "�", "zero​width",
	"\U0010ffff", "mixed é\n\"<&>\\ 日本 😀",
	"\xff\xfe", "trunc\xe6\x97", "ok\xc0\xaf", // invalid UTF-8: takes the json fallback
}

func randString(r *rand.Rand) string {
	switch r.Intn(4) {
	case 0:
		return stringPool[r.Intn(len(stringPool))]
	case 1:
		n := r.Intn(40)
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(r.Intn(128))
		}
		return string(b)
	case 2:
		n := r.Intn(12)
		rs := make([]rune, 0, n)
		for i := 0; i < n; i++ {
			for {
				c := rune(r.Intn(0x110000))
				if c >= 0xD800 && c <= 0xDFFF {
					continue
				}
				rs = append(rs, c)
				break
			}
		}
		return string(rs)
	default:
		return fmt.Sprintf("%d", r.Int63())
	}
}

func randStrPtr(r *rand.Rand) *string {
	if r.Intn(3) == 0 {
		return nil
	}
	s := randString(r)
	return &s
}

var floatPool = []float64{
	0, math.Copysign(0, -1), 1, -1, 0.1, 21.5, 1e-6, 9.99e-7, 1e-7, 1.5e-9, 123456789.125,
	1e20, 9.99e20, 1e21, 1.5e21, 1e100, math.MaxFloat64, math.SmallestNonzeroFloat64,
	-math.MaxFloat64, 3.141592653589793, 100, 1234567, 0.000001, 2.5e-5,
	math.NaN(), math.Inf(1), math.Inf(-1), // json refuses these: error parity
}

func randFloat(r *rand.Rand) float64 {
	switch r.Intn(4) {
	case 0:
		return floatPool[r.Intn(len(floatPool))]
	case 1:
		return math.Float64frombits(r.Uint64())
	case 2:
		return float64(r.Intn(2000000)-1000000) / float64(1+r.Intn(1000))
	default:
		return r.NormFloat64() * math.Pow(10, float64(r.Intn(60)-30))
	}
}

func randFloatPtr(r *rand.Rand) *float64 {
	if r.Intn(3) == 0 {
		return nil
	}
	f := randFloat(r)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		// Keep the error-parity cases rare so most iterations exercise the fast path.
		if r.Intn(8) != 0 {
			f = r.Float64()
		}
	}
	return &f
}

func randTime(r *rand.Rand) time.Time {
	base := time.Date(2020+r.Intn(10), time.Month(1+r.Intn(12)), 1+r.Intn(28),
		r.Intn(24), r.Intn(60), r.Intn(60), 0, time.UTC)
	switch r.Intn(4) {
	case 0:
		base = base.Add(time.Duration(r.Intn(1000000000))) // nanoseconds
	case 1:
		base = base.Add(time.Duration(r.Intn(1000)) * time.Millisecond)
	case 2:
		base = base.Add(time.Duration(r.Intn(1000000)) * time.Microsecond)
	}
	switch r.Intn(8) {
	case 0:
		return base.In(time.FixedZone("p", (r.Intn(47)-23)*3600))
	case 1:
		return base.In(time.FixedZone("q", (r.Intn(24*60)-12*60)*60))
	case 2:
		return base.In(time.FixedZone("s", r.Intn(7200)-3600)) // offsets with seconds
	case 3:
		return [...]time.Time{{}, time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
			time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), // json refuses
			time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC),    // json refuses
			time.Unix(0, 0).UTC(), time.Now()}[r.Intn(7)]
	}
	return base
}

func randEvent(r *rand.Rand) *Event {
	ev := &Event{
		DeviceToken:  randString(r),
		EventType:    eventtype.EventType(r.Intn(8)),
		OccurredTime: randTime(r),
	}
	switch r.Intn(3) {
	case 0:
		ev.AltId = sql.NullString{String: randString(r), Valid: true}
	case 1:
		ev.AltId = sql.NullString{String: "", Valid: true}
	}
	return ev
}

func randMeasurementsPayload(r *rand.Rand) *resolvedevent.ResolvedMeasurementsPayload {
	p := &resolvedevent.ResolvedMeasurementsPayload{}
	switch r.Intn(10) {
	case 0:
		return p // nil Entries
	case 1:
		p.Entries = []resolvedevent.ResolvedMeasurementsEntry{}
		return p
	}
	for i, n := 0, 1+r.Intn(4); i < n; i++ {
		s := resolvedevent.ResolvedMeasurementsEntry{OccurredTime: randTime(r)}
		switch r.Intn(8) {
		case 0: // nil Entries
		case 1:
			s.Entries = []resolvedevent.ResolvedMeasurementEntry{}
		default:
			for j, m := 0, 1+r.Intn(6); j < m; j++ {
				e := resolvedevent.ResolvedMeasurementEntry{
					Name: randString(r), Value: randString(r), Unit: randStrPtr(r),
					DataType: randStrPtr(r), Derived: r.Intn(6) == 0,
				}
				if r.Intn(2) == 0 {
					c := r.Uint64()
					if r.Intn(2) == 0 {
						c = uint64(r.Intn(100))
					}
					e.Classifier = &c
				}
				s.Entries = append(s.Entries, e)
			}
		}
		if r.Intn(8) == 0 {
			s.Dropped = []resolvedevent.DroppedMeasurementEntry{{Name: randString(r), Reason: "r", Value: randString(r)}}
		}
		p.Entries = append(p.Entries, s)
	}
	return p
}

func randLocationsPayload(r *rand.Rand) *resolvedevent.ResolvedLocationsPayload {
	p := &resolvedevent.ResolvedLocationsPayload{}
	switch r.Intn(8) {
	case 0:
		return p
	case 1:
		p.Entries = []resolvedevent.ResolvedLocationEntry{}
		return p
	}
	for i, n := 0, 1+r.Intn(4); i < n; i++ {
		p.Entries = append(p.Entries, resolvedevent.ResolvedLocationEntry{
			Latitude: randStrPtr(r), Longitude: randStrPtr(r), Elevation: randStrPtr(r),
			Accuracy: randStrPtr(r), Speed: randStrPtr(r), Heading: randStrPtr(r),
			OccurredTime: randTime(r),
		})
	}
	return p
}

func randAlertsPayload(r *rand.Rand) *resolvedevent.ResolvedAlertsPayload {
	p := &resolvedevent.ResolvedAlertsPayload{}
	switch r.Intn(8) {
	case 0:
		return p
	case 1:
		p.Entries = []resolvedevent.ResolvedAlertEntry{}
		return p
	}
	for i, n := 0, 1+r.Intn(4); i < n; i++ {
		lvl := uint32(r.Intn(6))
		if r.Intn(4) == 0 {
			lvl = r.Uint32()
		}
		p.Entries = append(p.Entries, resolvedevent.ResolvedAlertEntry{
			Type: randString(r), Level: lvl, Message: randString(r), Source: randString(r),
			OccurredTime: randTime(r),
		})
	}
	return p
}

func randStateChangePayload(r *rand.Rand) *resolvedevent.ResolvedStateChangePayload {
	return &resolvedevent.ResolvedStateChangePayload{
		State:  [...]string{"CONNECTED", "DISCONNECTED", "DEMOTED", randString(r)}[r.Intn(4)],
		Reason: randString(r), ExpectedSessionId: r.Uint64() >> uint(r.Intn(64)),
		SessionId: r.Uint64() >> uint(r.Intn(64)), OccurredTime: randStrPtr(r),
	}
}

func randNewRelationshipPayload(r *rand.Rand) *resolvedevent.ResolvedNewRelationshipPayload {
	p := &resolvedevent.ResolvedNewRelationshipPayload{RelationshipTypeId: r.Uint64() >> uint(r.Intn(64)), TargetType: randStrPtr(r)}
	if r.Intn(2) == 0 {
		id := r.Uint64() >> uint(r.Intn(64))
		p.TargetId = &id
	}
	return p
}

// randPayload returns a payload of every kind, as a pointer (what the resolver hands over)
// and, for the sake of the type switch, sometimes as a value.
func randPayload(r *rand.Rand) any {
	var p any
	switch r.Intn(5) {
	case 0:
		p = randMeasurementsPayload(r)
	case 1:
		p = randLocationsPayload(r)
	case 2:
		p = randAlertsPayload(r)
	case 3:
		p = randStateChangePayload(r)
	default:
		p = randNewRelationshipPayload(r)
	}
	if r.Intn(6) == 0 {
		switch v := p.(type) {
		case *resolvedevent.ResolvedMeasurementsPayload:
			return *v
		case *resolvedevent.ResolvedLocationsPayload:
			return *v
		case *resolvedevent.ResolvedAlertsPayload:
			return *v
		case *resolvedevent.ResolvedStateChangePayload:
			return *v
		case *resolvedevent.ResolvedNewRelationshipPayload:
			return *v
		}
	}
	if r.Intn(40) == 0 {
		return nil
	}
	return p
}

// ---- the property -------------------------------------------------------------

const identityIterations = 60000

// sameDigestOrSameError asserts the new derivation agrees with the oracle: the identical
// digest, or, where json refuses the input, the same refusal.
func sameDigestOrSameError(t *testing.T, what string, want []byte, wantErr error, got []byte, gotErr error) {
	t.Helper()
	if wantErr != nil {
		require.Error(t, gotErr, "%s: json refused the input, so must the new path", what)
		return
	}
	require.NoError(t, gotErr, what)
	require.Equal(t, want, got, "%s: digest moved", what)
}

func TestEventIdIsByteIdenticalToTheJSONDerivation(t *testing.T) {
	r := rand.New(rand.NewSource(20261103))
	fast, fallback := 0, 0
	for i := 0; i < identityIterations; i++ {
		ev, payload, tenant := randEvent(r), randPayload(r), randString(r)

		// The document itself, where the fast path claims it.
		if doc, ok := appendCanonicalPayload(nil, payload); ok {
			fast++
			want, err := json.Marshal(eventidentity.Payload(payload))
			require.NoError(t, err)
			require.Equal(t, string(want), string(doc), "iteration %d: canonical payload bytes differ", i)
		} else {
			fallback++
		}

		want, werr := oracleEventId(tenant, ev, payload)
		got, gerr := DeriveEventIdForPayload(tenant, ev, payload)
		sameDigestOrSameError(t, fmt.Sprintf("iteration %d event id", i), want, werr, got, gerr)
	}
	// The fast path must carry the bulk, or this proves little about it.
	require.Greater(t, fast, identityIterations*6/10, "fast path coverage: %d fast, %d fallback", fast, fallback)
	t.Logf("event id: %d fast, %d via json fallback", fast, fallback)
}

func TestPayloadIdsAreByteIdenticalToTheJSONDerivation(t *testing.T) {
	r := rand.New(rand.NewSource(1103))
	fast, fallback := 0, 0
	for i := 0; i < identityIterations; i++ {
		eventId := make([]byte, r.Intn(40))
		r.Read(eventId)

		m := &MeasurementEventCreateRequest{
			Event: Event{EventId: eventId}, EntryOccurredTime: randTime(r), Name: randString(r),
			Value: randFloatPtr(r), Unit: randStrPtr(r), DataType: randStrPtr(r), Derived: r.Intn(4) == 0,
		}
		if r.Intn(2) == 0 {
			c := uint(r.Uint32())
			m.Classifier = &c
		}
		want, werr := oracleMeasurementId(m)
		got, gerr := DeriveMeasurementPayloadId(m)
		sameDigestOrSameError(t, fmt.Sprintf("iteration %d measurement", i), want, werr, got, gerr)
		if _, ok := appendMeasurementEntry(nil, m); ok {
			fast++
		} else {
			fallback++
		}

		l := &LocationEventCreateRequest{
			Event: Event{EventId: eventId}, EntryOccurredTime: randTime(r),
			Latitude: randFloatPtr(r), Longitude: randFloatPtr(r), Elevation: randFloatPtr(r),
			Accuracy: randFloatPtr(r), Speed: randFloatPtr(r), Heading: randFloatPtr(r),
		}
		want, werr = oracleLocationId(l)
		got, gerr = DeriveLocationPayloadId(l)
		sameDigestOrSameError(t, fmt.Sprintf("iteration %d location", i), want, werr, got, gerr)

		a := &AlertEventCreateRequest{
			Event: Event{EventId: eventId}, EntryOccurredTime: randTime(r),
			Type: randString(r), Level: uint32(r.Intn(1 << uint(1+r.Intn(32)))),
			Message: randString(r), Source: randString(r),
		}
		want, werr = oracleAlertId(a)
		got, gerr = DeriveAlertPayloadId(a)
		sameDigestOrSameError(t, fmt.Sprintf("iteration %d alert", i), want, werr, got, gerr)
	}
	require.Greater(t, fast, identityIterations*5/10, "fast path coverage: %d fast, %d fallback", fast, fallback)
}

// TestDigestInputIsTheSameByteSequence pins the digest input itself, not only its hash: the
// pooled single-buffer build must equal what DeriveEventId streams into sha256.
func TestDigestInputIsTheSameByteSequence(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 5000; i++ {
		ev, payload, tenant := randEvent(r), randPayload(r), randString(r)
		doc, ok := appendCanonicalPayload(nil, payload)
		if !ok {
			continue
		}
		var want bytes.Buffer
		want.Write(appendEventIdHead(nil, tenant, ev))
		want.Write(appendField(nil, doc))
		sum := sha256.Sum256(want.Bytes())
		require.Equal(t, DeriveEventId(tenant, ev, doc), sum[:], "iteration %d", i)
	}
}

// TestNegativeControlOneByteOffIsDetected shows the comparison can fail: hash the oracle's
// document with ONE byte changed and the digest must differ from the fast path's. Without
// this, the equality above would pass for a test that compares nothing.
func TestNegativeControlOneByteOffIsDetected(t *testing.T) {
	r := rand.New(rand.NewSource(99))
	checked := 0
	for i := 0; i < 2000; i++ {
		ev, payload, tenant := randEvent(r), randPayload(r), randString(r)
		doc, ok := appendCanonicalPayload(nil, payload)
		if !ok {
			continue
		}
		fast, err := DeriveEventIdForPayload(tenant, ev, payload)
		require.NoError(t, err)

		oracleDoc, err := json.Marshal(eventidentity.Payload(payload))
		require.NoError(t, err)
		require.Equal(t, doc, oracleDoc)
		require.Equal(t, fast, DeriveEventId(tenant, ev, oracleDoc), "control sanity: equal inputs agree")

		mutated := append([]byte(nil), oracleDoc...)
		mutated[r.Intn(len(mutated))] ^= 1 // one bit, one byte
		require.NotEqual(t, fast, DeriveEventId(tenant, ev, mutated),
			"a one-byte difference in the document must change the digest (and so fail the property)")
		checked++
	}
	require.Greater(t, checked, 500)
}

// TestKnownDigestsAreFrozen hashes LITERAL documents, so the oracle itself cannot drift
// unnoticed: these ids are what a stored event derived before this change.
func TestKnownDigestsAreFrozen(t *testing.T) {
	ev := &Event{DeviceToken: "dev-1", EventType: eventtype.Measurement,
		OccurredTime: time.Date(2026, 11, 3, 10, 0, 0, 123456789, time.UTC)}
	payload := &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
		OccurredTime: ev.OccurredTime,
		Entries:      []resolvedevent.ResolvedMeasurementEntry{{Name: "speed_kph", Value: "42.5"}},
	}}}
	const literal = `{"Entries":[{"Entries":[{"Name":"speed_kph","Value":"42.5","Classifier":null,` +
		`"Unit":null,"DataType":null}],"OccurredTime":"2026-11-03T10:00:00.123456789Z"}]}`
	want := DeriveEventId("acme", ev, []byte(literal))
	got, err := DeriveEventIdForPayload("acme", ev, payload)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

// ---- benchmarks ---------------------------------------------------------------

func benchPayload() (*Event, *resolvedevent.ResolvedMeasurementsPayload) {
	t := time.Date(2026, 11, 3, 10, 0, 0, 123456789, time.UTC)
	unit, dt := "kph", "float"
	cls := uint64(3)
	return &Event{DeviceToken: "devicepulse-00001", EventType: eventtype.Measurement, OccurredTime: t},
		&resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
			OccurredTime: t,
			Entries: []resolvedevent.ResolvedMeasurementEntry{
				{Name: "speed_kph", Value: "42.5", Classifier: &cls, Unit: &unit, DataType: &dt},
				{Name: "battery_pct", Value: "88", Unit: &unit, DataType: &dt},
			},
		}}}
}

func BenchmarkEventIdOld(b *testing.B) {
	ev, p := benchPayload()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := oracleEventId("sim-r26-4mqtt", ev, p); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEventIdNew(b *testing.B) {
	ev, p := benchPayload()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := DeriveEventIdForPayload("sim-r26-4mqtt", ev, p); err != nil {
			b.Fatal(err)
		}
	}
}

func benchMeasurement() *MeasurementEventCreateRequest {
	v, unit, dt := 42.5, "kph", "float"
	return &MeasurementEventCreateRequest{Event: Event{EventId: make([]byte, 32)},
		EntryOccurredTime: time.Date(2026, 11, 3, 10, 0, 0, 123456789, time.UTC),
		Name:              "speed_kph", Value: &v, Unit: &unit, DataType: &dt}
}

func BenchmarkPayloadIdOld(b *testing.B) {
	m := benchMeasurement()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := oracleMeasurementId(m); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPayloadIdNew(b *testing.B) {
	m := benchMeasurement()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := DeriveMeasurementPayloadId(m); err != nil {
			b.Fatal(err)
		}
	}
}

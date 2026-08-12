// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-sources/processor"
)

// maxIngressResponseBytes bounds how much of an unexpected error body is read.
const maxIngressResponseBytes = 4096

// ErrShed marks an emit the ingress REJECTED at the per-tenant rate limit (HTTP 429,
// ADR-023/063). It is a definitive, clean non-accept — the ingress returns 429 before
// reading the body, so the event provably never entered the pipeline — which is what
// lets a governed run distinguish an EXPECTED shed from a real failure and reconcile
// persisted == emitted with the shed events correctly absent. EmitMeasurements wraps
// it so callers test with errors.Is(err, ErrShed); EmitAll routes it to Stats.Shed.
var ErrShed = errors.New("emit shed at ingress rate limit (HTTP 429)")

// MetricsFunc returns the metrics device d (the i-th in rt.Devices) emits on
// the current tick. It is pure per-device value generation — EmitAll owns the
// wire path, the concurrency, and the accounting.
//
// Returning an EMPTY map means the device emits NOTHING this tick — a device gone
// silent, which is a first-class state a scenario needs to be able to produce: it is
// what an offline device looks like to every widget, and it is what an absence rule
// fires on. It is distinct from emitting a measurement with no values, which is a
// device reporting that it has nothing to say.
type MetricsFunc func(i int, d DeviceInstance) map[string]float64

// EmitAll emits one Measurement per device concurrently, bounded to workers,
// and records the result in rt.Stats.
//
// It does NOT stop at the first failure. A load generator that halts a tick
// because one device got a 503 under-applies exactly the load the run is trying
// to measure, and does it most at the moment the platform is most stressed —
// which would bias the measurement toward looking cheaper than it is. Every
// device is attempted; failures are counted and summarized.
func EmitAll(ctx context.Context, rt *Runtime, workers int, metrics MetricsFunc) error {
	return emitEach(ctx, rt, workers, func(i int, d DeviceInstance) (bool, error) {
		values := metrics(i, d)
		if len(values) == 0 {
			// Silent this tick. Not counted as emitted, failed or shed: nothing
			// was offered to the ingress, so there is nothing for a count
			// reconciliation to account for.
			return false, nil
		}
		return true, EmitMeasurements(ctx, rt, d, values)
	})
}

// emitEach is the tick's worker pool and its accounting, shared by every
// per-device emitter (EmitAll, EmitLocationAll).
//
// emit reports whether the device OFFERED anything this tick, separately from
// whether the offer succeeded. The two are genuinely different: a device that
// offered nothing is silent — counted nowhere, because nothing reached the ingress
// for a reconciliation to account for — while a device that offered and was refused
// is a shed or a failure. Collapsing them into "err == nil" would file every silent
// device as an accepted emit and inflate the achieved rate a measurement rests on.
//
// 🔴 Shared rather than copied, and the accounting is the reason. The 429/ErrShed
// split is what keeps a governed run reconcilable, and a second pool with its own
// copy of the switch is one edit away from filing a shed as a failure in one
// scenario and not the other — a divergence that shows up as a load report nobody
// can reconcile rather than as anything that looks like a bug here.
func emitEach(ctx context.Context, rt *Runtime, workers int,
	emit func(i int, d DeviceInstance) (offered bool, err error)) error {
	devices := rt.Devices
	if len(devices) == 0 {
		return nil
	}
	if workers < 1 {
		workers = 1
	}

	var (
		mu       sync.Mutex
		failed   int
		firstErr error
	)

	idx := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				offered, err := emit(i, devices[i])
				if !offered {
					continue
				}
				switch {
				case err == nil:
					rt.Stats.Emitted.Add(1)
				case errors.Is(err, ErrShed):
					// A governed shed (429) is a clean non-accept, NOT a failure: it is
					// counted separately so a run under a contention floor stays
					// reconcilable (persisted == emitted, shed events absent). It does not
					// set firstErr — EmitAll only "fails" on real errors.
					rt.Stats.Shed.Add(1)
				default:
					rt.Stats.Failed.Add(1)
					mu.Lock()
					failed++
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
				}
			}
		}()
	}

	for i := range devices {
		select {
		case <-ctx.Done():
			// Stop feeding work; the workers drain and exit on the closed channel.
			close(idx)
			wg.Wait()
			return ctx.Err()
		case idx <- i:
		}
	}
	close(idx)
	wg.Wait()

	if failed > 0 {
		return fmt.Errorf("%d of %d emits failed (first: %w)", failed, len(devices), firstErr)
	}
	return nil
}

// EmitMeasurement posts one Measurement event carrying a single metric. It
// delegates to EmitMeasurements so devicepulse (one metric per device) and
// buildingpulse (four metrics in one event) share the exact same wire path.
func EmitMeasurement(ctx context.Context, rt *Runtime, d DeviceInstance, metricKey string, value float64) error {
	return EmitMeasurements(ctx, rt, d, map[string]float64{metricKey: value})
}

// EmitMeasurements posts one Measurement event for device d carrying every
// entry in metrics as a single measurements map — the "rich emit" shape
// (multiple metric keys in one entries[0].measurements object) rather than one
// event per metric. It uses the same real device-plane HTTP ingress route and
// JsonEvent shape any physical device uses (no sim-only backdoor),
// authenticated by the credential bootstrap.go provisioned for d. It expects
// HTTP 202 (accepted into the pipeline; persistence/resolution happen
// asynchronously downstream).
func EmitMeasurements(ctx context.Context, rt *Runtime, d DeviceInstance, metrics map[string]float64) error {
	now := eventTimestamp()

	values := make(map[string]string, len(metrics))
	for key, value := range metrics {
		values[key] = fmt.Sprintf("%v", value)
	}
	entry := map[string]any{
		"measurements": values,
		"occurredTime": now,
	}
	payload, err := jsonRoundTrip(map[string]any{"entries": []any{entry}})
	if err != nil {
		return fmt.Errorf("build measurement payload: %w", err)
	}
	return postEvent(ctx, rt, d, "Measurement", now, payload)
}

// ---- Location -------------------------------------------------------------------
//
// The location spine — decoder, protobuf, resolver, hypertable, GraphQL — has existed
// since inception, and until this emitter NOTHING in the repository ever produced a
// location event. Every defect the spine turned out to carry follows from that: a wire
// shape with no producer is a shape nobody has ever had to be right about, so both
// in-repo fixtures used a form the decoder silently accepted and stored nothing for.
//
// This is therefore not merely "a second event type the sim can send". It is the
// oracle: the emitter and TestEmittedLocationDecodesThroughTheRealDecoder run the exact
// bytes that leave here through event-sources' own JsonDecoder, so a change to the wire
// contract fails in this package rather than in a Unity scene against a live cluster.

// Fix is one position report, in the units the platform fixes for every device:
// WGS84/EPSG:4326 decimal degrees, elevation above the ELLIPSOID in metres, accuracy in
// metres, speed in metres per second, heading in degrees clockwise from true north.
//
// Latitude and Longitude are values because the decoder REQUIRES them — an entry
// without them is refused, not stored blank. The other four are pointers because "not
// reported" and "reported as zero" are different facts about a device and only a
// pointer can carry the difference: a vehicle with no compass has not reported due
// north, and a stationary one reporting speed 0 has said something a missing field has
// not. EmitLocation omits a nil field from the JSON entirely rather than sending "0".
type Fix struct {
	Latitude  float64
	Longitude float64

	Elevation *float64
	Accuracy  *float64
	Speed     *float64
	Heading   *float64
}

// FixFunc returns the position device d (the i-th in rt.Devices) reports on the
// current tick, and whether it reports one at all.
//
// The bool is MetricsFunc's empty map in the shape a struct needs: a Fix has no
// "empty" value — the zero Fix is the valid position (0, 0) in the Gulf of Guinea —
// so a scenario that wants a device silent this tick has no way to say so through the
// value alone. Returning false is that way. Without it, a device meant to go quiet
// would emit Null Island, which is a real coordinate that looks like data.
type FixFunc func(i int, d DeviceInstance) (Fix, bool)

// EmitLocation posts ONE Location event for device d carrying exactly one entry.
//
// One entry per event is simply what a simulated tick IS — one device, one position, one
// instant — not a workaround. It used to be a workaround: a batch's per-entry occurredTime
// was discarded at persistence, so every entry landed at the envelope's time and two
// identical positions in one batch collided on their payload id, one being dropped. That
// is fixed — a batch now keeps each fix's own instant, and distinct instants give distinct
// payload ids — so a scenario that wants to exercise store-and-forward batching may send
// one, and should say why.
func EmitLocation(ctx context.Context, rt *Runtime, d DeviceInstance, fix Fix) error {
	now := eventTimestamp()

	// EVERY value is a string, including the numbers: the decoder's entry type holds
	// *string per field, so a bare JSON number does not unmarshal into it and the
	// whole decode fails. Written out field by field rather than through a reflective
	// loop so the wire contract is readable as itself.
	entry := map[string]any{
		"latitude":     formatFixValue(fix.Latitude),
		"longitude":    formatFixValue(fix.Longitude),
		"occurredTime": now,
	}
	for key, value := range map[string]*float64{
		"elevation": fix.Elevation,
		"accuracy":  fix.Accuracy,
		"speed":     fix.Speed,
		"heading":   fix.Heading,
	} {
		// 🔴 ABSENT, not "0". Setting the key to a zero string would turn "this device
		// has no compass" into "this device is pointing due north" — a fact the platform
		// would store, a widget would draw, and nothing downstream could ever unpick,
		// because a stored zero is indistinguishable from a measured one.
		if value != nil {
			entry[key] = formatFixValue(*value)
		}
	}

	payload, err := jsonRoundTrip(map[string]any{"entries": []any{entry}})
	if err != nil {
		return fmt.Errorf("build location payload: %w", err)
	}
	return postEvent(ctx, rt, d, "Location", now, payload)
}

// EmitLocationAll emits one Location per device concurrently, bounded to workers,
// with the same accounting EmitAll uses — and through the same pool, so a shed here
// and a shed there are counted the same way.
func EmitLocationAll(ctx context.Context, rt *Runtime, workers int, fixes FixFunc) error {
	return emitEach(ctx, rt, workers, func(i int, d DeviceInstance) (bool, error) {
		fix, reporting := fixes(i, d)
		if !reporting {
			return false, nil
		}
		return true, EmitLocation(ctx, rt, d, fix)
	})
}

// canonicalHeading folds a bearing in degrees into the [0, 360) the platform stores,
// and is where a location producer's heading must pass before it reaches Fix.
//
// The wrap-to-zero at the top of the range is the part that is easy to get wrong, and
// it is not a clamp: 360 and 0 are the SAME bearing, so folding one onto the other
// loses nothing. What forces it is the column, which stores 4 decimal places and
// ROUNDS to get there — so 359.99999 is a legal float below 360, and stores as exactly
// 360.0000, the second spelling of north the exclusive bound exists to keep out. The
// decoder refuses it half a quantum early for that reason; a producer that hands over
// a raw atan2 result eventually trips it and 400s an otherwise perfect fix.
func canonicalHeading(degrees float64) float64 {
	degrees = math.Mod(degrees, 360)
	if degrees < 0 {
		degrees += 360
	}
	// Comfortably inside the decoder's 360 - quantum/2, so rounding at the column
	// cannot carry a value the decoder admitted up to 360.0000.
	if degrees >= 359.9999 {
		degrees = 0
	}
	return degrees
}

// formatFixValue renders a coordinate component as the decimal string the wire
// carries.
//
// 'f' with precision -1 rather than %v, and the difference is not cosmetic:
// fmt.Sprintf("%v", 1e-7) produces "1e-07", which the decoder's ParseFloat accepts
// happily — so the value is CORRECT and unreadable. A latitude that reaches a log, a
// dead-letter or a debugger as "1e-07" reads as a bug in the producer, and someone
// spends an afternoon on it. -1 keeps the shortest round-trippable form, so no
// precision is invented either.
func formatFixValue(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// eventTimestamp is the occurred-time stamp every emit on this wire carries.
//
// Sub-second precision is load-bearing, not cosmetic. A base event is keyed by
// (tenant, device, event_type, occurred_time), so two emits from one device
// within the same wall-clock SECOND collide on that key and the second one's
// envelope is silently discarded. At the demo's 5s cadence that never bites,
// but a load run emitting faster than 1/device/sec loses events — and loses
// them indistinguishably from a drop, to a count-reconciling oracle.
// RFC3339Nano stamps every emit distinctly, so a device may emit at any rate
// and each reading is its own event (which is also just true — a device
// sampling sub-second carries sub-second times).
//
// 🔴 This once described the collapse as the platform "correctly dedup"ing its
// second-identical events. It is not correct behaviour, it is data loss: two
// DISTINCT readings sharing a key, with the later one dropped. Stamping the
// simulator distinctly stopped the oracle from seeing it, which is why the
// platform-side defect survived — a fix to the measuring instrument that reads
// like a fix to the thing measured. The platform fix is the per-message event
// identity that gives a base event a key of its own.
func eventTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// postEvent is the wire tail every emitter shares: it wraps payload in the
// JsonEvent envelope for device d, POSTs it to the real device-plane HTTP ingress
// route a physical device uses (no sim-only backdoor), authenticated by the
// credential bootstrap.go provisioned, and classifies the response — 202 accepted,
// 429 wrapped as ErrShed, anything else a failure.
//
// 🔴 It is a shared helper rather than a shape each emitter repeats, and the reason
// is the 429 branch specifically. ErrShed is what keeps a governed run reconcilable
// (EmitAll routes it to Stats.Shed rather than Stats.Failed); a second copy of this
// classification is a second thing to remember when the accounting changes, and the
// copy that was forgotten reports a shed as a failure — turning an EXPECTED outcome
// under a contention floor into a run that says it broke.
//
// occurredTime is passed IN rather than stamped here because the entry inside the
// payload carries the same stamp: computing it twice would put two different times
// on one reading, which is exactly the kind of difference nobody notices.
func postEvent(ctx context.Context, rt *Runtime, d DeviceInstance,
	eventType, occurredTime string, payload map[string]interface{}) error {
	credType := credentialTypeAccessToken
	credId := d.CredentialId

	jevent := processor.JsonEvent{
		Device:         d.Token,
		EventType:      eventType,
		OccurredTime:   &occurredTime,
		Payload:        payload,
		CredentialType: &credType,
		CredentialId:   &credId,
	}
	body, err := json.Marshal(jevent)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	url := fmt.Sprintf("%s/%s/%s/events", strings.TrimRight(rt.Endpoints.Ingress, "/"), rt.InstanceId, rt.Tenant)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build ingress request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := rt.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("post to ingress %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		// A governed shed. Drain a bounded prefix so the connection can be reused, but
		// wrap ErrShed so EmitAll routes it to Stats.Shed rather than Stats.Failed.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxIngressResponseBytes))
		return fmt.Errorf("ingress %s returned 429 (%s): %w", url, strings.TrimSpace(string(raw)), ErrShed)
	}
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxIngressResponseBytes))
		return fmt.Errorf("ingress %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

// jsonRoundTrip marshals v and unmarshals it back into a map[string]interface{}
// — JsonEvent.Payload's declared type — so callers can build the payload with
// typed Go literals instead of hand-assembling a map.
func jsonRoundTrip(v any) (map[string]interface{}, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

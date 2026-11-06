// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/integrationcodec"
	"github.com/devicechain-io/dc-microservice/payloaddecode"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// A PAYLOAD DECODER'S PLACE IN RESOLUTION.
//
// An integration whose device type has a decoder does not flatten at the door: event-sources
// carries the sender's raw input (frame, fPort, fCnt, the generic item or the network server's
// decoded object, the link metrics) as an IntegrationUplink on a stream of its own, and this file
// turns it into an ordinary Measurement:
//
//	auth → scope → alias → resolveProfile → [decode] → validateMeasurements → ResolveMeasurementsEventPayload
//
// Everything after the decode is the code an ordinary measurement runs, so calculated metrics
// derive from decoded readings, entries are sorted for identity, and DETECT sees them.
//
// 🔴 A DECODE IS A PURE FUNCTION OF THE UPLINK AND THE PUBLISHED DECODER, so a failure is terminal
// on the first delivery (ErrDecodeFailed, see Process): the redelivery would answer the same, and
// retrying a broken decoder five times only multiplies its load and delays the tenant's feedback.
// It is never a silent drop: the failure is a failed-events record with the sub-reason leading its
// text, written before the source is acked.
//
// 🔴 THE OUTPUT IS VALIDATED INSIDE THE DECODE (payloaddecode, against the version's own metrics),
// not left to validateMeasurements below it: an out-of-range or unstorable value is a
// DECODE_FAILED/invalid_output on delivery 1, where the same value failing the later check would be
// an Invalid that is retried five times.

// ErrDecodeFailed is the failure of an integration uplink the payload decoder refused: every
// payloaddecode failure, a decoder this build cannot run, and an uplink that cannot be flattened
// when no decoder is left to run. It is terminal on the first delivery.
var ErrDecodeFailed = errors.New("decode failed")

// ReasonTooManyReadings is the sub-reason of a decoderless uplink that flattens to more readings than
// one event may hold (the Cut A rule, applied by device-management when the route did not).
const ReasonTooManyReadings payloaddecode.Reason = "too_many_readings"

// DecodeFailure is one refused decode. Its text leads with the sub-reason, the way the failed-events
// record carries it (`decode_failed: no_section: ...`), and is built only from the reason, the
// expression path, the function name and the offset, never from platform internals, so it is safe
// to show the tenant.
type DecodeFailure struct {
	Reason payloaddecode.Reason
	Text   string
	// Cost is what the decode had spent when it failed.
	Cost uint64
	// Suppressed is set when the observer refused the failure its failed-events record (the record
	// budget is spent). The uplink is then settled without one, and counted as suppressed.
	Suppressed bool
}

func (e *DecodeFailure) Error() string { return "decode_failed: " + e.Text }

// Is makes every DecodeFailure an ErrDecodeFailed.
func (e *DecodeFailure) Is(target error) bool { return target == ErrDecodeFailed }

// DecodeOutcome is how one decode attempt ended.
type DecodeOutcome string

const (
	DecodeDecoded DecodeOutcome = "decoded"
	DecodeIgnored DecodeOutcome = "ignored"
	DecodeFailed  DecodeOutcome = "failed"
)

// DecodeObservation is one decode attempt, as told to a DecodeObserver.
type DecodeObservation struct {
	Tenant              string
	DeviceTypeId        uint
	Device              string
	ProfileVersionToken string
	Outcome             DecodeOutcome
	// Reason is the sub-reason of a failed outcome, empty otherwise.
	Reason payloaddecode.Reason
	// Section is the section that matched, for a decoded or ignored outcome.
	Section string
	// Cost is the cost the decode spent.
	Cost uint64
	// Error is the tenant-facing text of a failed outcome (`decode_failed: <reason>: ...`).
	Error string
	// ChargeSkew is how many readings a decoded outcome stored beyond what the route charged for.
	ChargeSkew int
	// Uplink is the input the attempt ran over.
	Uplink *esmodel.UnresolvedIntegrationUplinkPayload
}

// DecodeObserver is told the outcome of every decode attempt, once, after the outcome is decided and
// before the event is settled. It is the seam the decoder's accounting hangs on (health counts, the
// per-(tenant, device type) record budget and failed-input samples); the resolver does none of that
// itself. It is called from the resolver pool's goroutines concurrently and must not block.
//
// The result matters only for a failed outcome: false refuses the failure its failed-events record
// (the budget is spent), and the resolver settles the uplink without one. It is true for every other
// outcome and whenever there is no observer, so an unbudgeted build records every failure.
type DecodeObserver interface {
	ObserveDecode(DecodeObservation) (record bool)
}

// WithDecodeObserver sets the observer every resolver of the pool tells its decode outcomes to.
func WithDecodeObserver(o DecodeObserver) InboundOption {
	return func(p *InboundEventsProcessor) { p.decodeObserver = o }
}

// integrationAuth is the integration an event authenticated as, and the bearer token that proved
// it. The token is held for the length of one resolve, to act on the integration's behalf (record
// an undecoded uplink), and is never logged or stored.
type integrationAuth struct {
	integration *model.Integration
	token       string
}

type integrationAuthKey struct{}

func withIntegration(ctx context.Context, a integrationAuth) context.Context {
	return context.WithValue(ctx, integrationAuthKey{}, a)
}

func integrationFrom(ctx context.Context) (integrationAuth, bool) {
	a, ok := ctx.Value(integrationAuthKey{}).(integrationAuth)
	return a, ok && a.integration != nil
}

// The directions of kind skew: the route and the resolver disagreed about whether the device type has
// a decoder, because a publish or a clear fell inside the route's 60 s window on the plan.
const (
	// kindSkewDecoderGone: an uplink was carried raw for a decoder that is no longer there.
	kindSkewDecoderGone = "decoder_gone"
	// kindSkewDecoderNew: a reading was flattened at the route for a type that has a decoder now.
	kindSkewDecoderNew = "decoder_new"
)

// DecodeMetrics counts what decoding did. Label-free of tenant, integration and device type on
// purpose: a tenant could grow those without bound. A nil value counts nothing.
type DecodeMetrics struct {
	results    *prometheus.CounterVec
	skipped    *prometheus.CounterVec
	chargeSkew prometheus.Counter
	kind       *prometheus.CounterVec
}

// decodeResults are every value of the results counter's label, so each series is exported at zero.
var decodeResults = []string{
	string(DecodeDecoded), string(DecodeIgnored),
	string(payloaddecode.ReasonEvalError), string(payloaddecode.ReasonCostExceeded),
	string(payloaddecode.ReasonInvalidOutput), string(payloaddecode.ReasonNoSection),
	string(payloaddecode.ReasonNoOutputs), string(payloaddecode.ReasonMissingInput),
	string(payloaddecode.ReasonMapOverflow), string(payloaddecode.ReasonInputTooLarge),
	string(payloaddecode.ReasonInputMalformed), string(payloaddecode.ReasonEnvUnsupported),
	string(payloaddecode.ReasonCompileFailed), string(ReasonTooManyReadings),
}

func newDecodeMetrics(ms *core.Microservice) *DecodeMetrics {
	m := &DecodeMetrics{
		results: ms.NewCounterVec("resolve_decode_total",
			"Integration uplinks run through a payload decoder, by result: decoded, ignored (an ignore section matched), "+
				"or the sub-reason of a DecodeFailed failure. A failed uplink is dead-lettered on its first delivery.",
			[]string{"result"}),
		skipped: ms.NewCounterVec("resolve_decode_skipped_total",
			"Map-output keys a decoder produced that were not stored, by reason (undeclared, collision, reserved).",
			[]string{"reason"}),
		chargeSkew: ms.NewCounter("resolve_decode_charge_skew_total",
			"Readings stored beyond what the route charged for the uplink: the decoder that ran produces more than the version "+
				"the route selected on (a publish inside the route's 60 s window). The excess is stored, never refused."),
		kind: ms.NewCounterVec("resolve_decode_kind_skew_total",
			"Integration uplinks whose route and resolver disagreed about the device type having a decoder, by direction "+
				"(decoder_gone: carried raw, flattened here; decoder_new: flattened at the route, accepted as it came).",
			[]string{"direction"}),
	}
	for _, r := range decodeResults {
		m.results.WithLabelValues(r)
	}
	for _, r := range []string{"undeclared", "collision", "reserved"} {
		m.skipped.WithLabelValues(r)
	}
	for _, d := range []string{kindSkewDecoderGone, kindSkewDecoderNew} {
		m.kind.WithLabelValues(d)
	}
	return m
}

func (m *DecodeMetrics) result(r string) {
	if m != nil {
		m.results.WithLabelValues(r).Inc()
	}
}

func (m *DecodeMetrics) skip(reason string) {
	if m != nil {
		m.skipped.WithLabelValues(reason).Inc()
	}
}

func (m *DecodeMetrics) skew(excess int) {
	if m != nil && excess > 0 {
		m.chargeSkew.Add(float64(excess))
	}
}

func (m *DecodeMetrics) kindSkew(direction string) {
	if m != nil {
		m.kind.WithLabelValues(direction).Inc()
	}
}

// HandleIntegrationUplink resolves an integration's raw uplink into the Measurement it stands for.
//
//   - a decoder is declared: decode it, validate every output in the decode step, append the
//     integration's link metrics, store what comes out (even beyond the charge: charge skew);
//   - the decoder cannot be run by this build (a snapshot from a newer release, or one that does not
//     compile): DECODE_FAILED, never flattened;
//   - no decoder is declared (cleared inside the route's window): flatten here, with the code the
//     route runs, so nothing the route accepted is lost.
func (rez *EventResolver) HandleIntegrationUplink(ctx context.Context, device *model.Device,
	event *esmodel.UnresolvedEvent) ([]EventResolutionResults, uint, error) {
	auth, ok := integrationFrom(ctx)
	if !ok {
		return nil, uint(resolvedevent.FailureReason_Unauthenticated),
			errors.New("an integration uplink must come from an authenticated integration")
	}
	up, ok := event.Payload.(*esmodel.UnresolvedIntegrationUplinkPayload)
	if !ok {
		return nil, uint(resolvedevent.FailureReason_Invalid),
			fmt.Errorf("an integration uplink carries a %T payload", event.Payload)
	}
	// The event's one profile read, as for any standard event (see HandleStandardEvent).
	api := model.ReadAheadForEvent(ctx, rez.Api, device)
	res, reason, err := rez.resolveProfile(ctx, api, device)
	if err != nil {
		return nil, reason, err
	}

	var readings map[string]string
	dec := res.Decoder()
	switch {
	case !dec.Declared():
		rez.decode.kindSkew(kindSkewDecoderGone)
		var done bool
		readings, done, reason, err = rez.flattenUplink(ctx, device, auth, res.Scope().ProfileVersionToken, up)
		if err != nil {
			return nil, reason, err
		}
		if done {
			return nil, 0, nil
		}
	default:
		var ignored bool
		readings, ignored, err = rez.decodeUplink(ctx, device, auth, res, up)
		if err != nil {
			return nil, uint(resolvedevent.FailureReason_DecodeFailed), err
		}
		if ignored {
			return nil, 0, nil
		}
	}

	// The decoded readings continue as an ordinary measurement of the device: same envelope, same
	// source, same identity, the type and payload swapped. validateMeasurements re-runs on them
	// and cannot fail what the decode step already validated.
	measured := *event
	measured.EventType = eventtype.Measurement
	measured.Payload = &esmodel.UnresolvedMeasurementsPayload{
		Entries: []esmodel.UnresolvedMeasurementsEntry{{Measurements: readings}},
	}
	return rez.finishStandardEvent(ctx, api, res, device, &measured)
}

// decodeUplink runs the version's decoder over the uplink. A nil error with ignored set is a matched
// ignore section: accepted, nothing stored.
func (rez *EventResolver) decodeUplink(ctx context.Context, device *model.Device, auth integrationAuth,
	res model.ProfileResolution, up *esmodel.UnresolvedIntegrationUplinkPayload) (map[string]string, bool, error) {
	dec := res.Decoder()
	obs := DecodeObservation{DeviceTypeId: device.DeviceTypeId, Device: device.Token,
		ProfileVersionToken: res.Scope().ProfileVersionToken, Uplink: up}
	obs.Tenant, _ = core.TenantFromContext(ctx)

	// 🔴 A DECODER THIS BUILD CANNOT RUN FAILS THE UPLINK AND NOTHING ELSE, and it never falls back
	// to flattening: a snapshot from a newer release (env_unsupported, a downgrade) or a document
	// that no longer compiles would otherwise store readings the decoder was meant to produce, or
	// none, with nothing to say so. The failure is terminal and visible; the version's other event
	// types resolved above this point as they always did.
	if unusable := dec.Unusable(); unusable != nil {
		return nil, false, rez.decodeFailed(&obs, &DecodeFailure{Reason: unusable.Reason, Text: planFailureText(unusable)})
	}
	plan := dec.Plan()
	out, derr := plan.Decode(uplinkInput(up))
	if derr != nil {
		return nil, false, rez.decodeFailed(&obs, &DecodeFailure{Reason: derr.Reason, Text: derr.Error(), Cost: derr.Cost})
	}
	obs.Section, obs.Cost = out.Section, out.Cost
	for _, s := range out.Skipped {
		rez.decode.skip(s.Reason)
	}
	if out.Ignored {
		obs.Outcome = DecodeIgnored
		rez.decode.result(string(DecodeIgnored))
		rez.observeDecode(obs)
		return nil, true, nil
	}

	readings := make(map[string]string, len(out.Readings)+3)
	for _, r := range out.Readings {
		readings[r.Key] = r.Value
	}
	// The link metrics are the platform's: appended after the decode by the AUTHENTICATED
	// integration's own setting, so a decoder cannot spoof them (it cannot emit their prefix) and a
	// route that sent them for an integration that has since turned them off does not store them.
	if auth.integration.LinkMetrics {
		for k, v := range linkReadings(up.Link) {
			readings[k] = v
		}
	}
	// CHARGE SKEW. The route charged the tenant's reading ceiling for the version it selected on; the
	// decoder that ran here may be a newer one that produces more. The readings are stored (they were
	// accepted at the door and the device was told so) and the excess is counted, so a publish inside
	// the route's window is visible rather than silent.
	if excess := len(readings) - int(up.MaxReadings); excess > 0 && up.MaxReadings > 0 {
		rez.decode.skew(excess)
		obs.ChargeSkew = excess
	}
	obs.Outcome = DecodeDecoded
	rez.decode.result(string(DecodeDecoded))
	rez.observeDecode(obs)
	return readings, false, nil
}

// decodeFailed counts, reports and builds the error of one refused decode.
func (rez *EventResolver) decodeFailed(obs *DecodeObservation, f *DecodeFailure) error {
	obs.Outcome, obs.Reason, obs.Cost, obs.Error = DecodeFailed, f.Reason, f.Cost, f.Error()
	rez.decode.result(string(f.Reason))
	f.Suppressed = !rez.observeDecode(*obs)
	return f
}

// observeDecode tells the observer and returns whether the outcome may be recorded.
func (rez *EventResolver) observeDecode(o DecodeObservation) bool {
	if rez.decodeObserver == nil {
		return true
	}
	return rez.decodeObserver.ObserveDecode(o)
}

// planFailureText is the tenant-facing text of a decoder this build cannot run. The compile error
// can span lines (errors.Join), which a failed record's single-line text cannot.
func planFailureText(e *model.DecodePlanError) string {
	msg := strings.ReplaceAll(strings.TrimSpace(e.Err.Error()), "\n", "; ")
	return string(e.Reason) + ": " + msg
}

// uplinkInput is the decoder's view of a carried uplink. An absent input stays nil (the decoder
// fails missing_input), a present empty one is empty.
func uplinkInput(up *esmodel.UnresolvedIntegrationUplinkPayload) payloaddecode.Input {
	in := payloaddecode.Input{Frame: up.Frame, JSON: up.Json, Decoded: up.Decoded, Link: up.Link}
	if up.FPort != nil || up.FCnt != nil {
		in.LoRa = &payloaddecode.LoRa{FPort: -1, FCnt: -1}
		if up.FPort != nil {
			in.LoRa.FPort = int64(*up.FPort)
		}
		if up.FCnt != nil {
			in.LoRa.FCnt = int64(*up.FCnt)
		}
	}
	return in
}

// linkReadings are the receive-link readings of a carried link map, under the platform's own keys.
// rssi and snr travel together or not at all.
func linkReadings(link map[string]float64) map[string]string {
	if len(link) == 0 {
		return nil
	}
	out := make(map[string]string, 3)
	rssi, hasRSSI := link[integrationLinkRSSI]
	snr, hasSNR := link[integrationLinkSNR]
	if hasRSSI && hasSNR {
		out[integrationcodec.KeyRSSI] = calcfield.FormatDouble(rssi)
		out[integrationcodec.KeySNR] = calcfield.FormatDouble(snr)
	}
	if g, ok := link[integrationLinkGateways]; ok {
		out[integrationcodec.KeyGateways] = strconv.Itoa(int(g))
	}
	return out
}

// The keys of the carrier's link map, which event-sources writes (processor.LinkRSSI and its peers).
const (
	integrationLinkRSSI     = "rssi"
	integrationLinkSNR      = "snr"
	integrationLinkGateways = "gateways"
)

// flattenUplink is the decoder-less path: the route carried the uplink raw for a decoder that has
// since been cleared, so the same Flatten the route runs reads the carried object. done is set when
// the uplink held nothing to read: it is recorded on the integration (the Cut A undecoded marker)
// and accepted, as the route would have done.
func (rez *EventResolver) flattenUplink(ctx context.Context, device *model.Device, auth integrationAuth,
	profileVersion string, up *esmodel.UnresolvedIntegrationUplinkPayload) (readings map[string]string, done bool, reason uint, err error) {
	obs := DecodeObservation{DeviceTypeId: device.DeviceTypeId, Device: device.Token, Uplink: up,
		ProfileVersionToken: profileVersion}
	obs.Tenant, _ = core.TenantFromContext(ctx)

	// A generic sender's item is carried as `json`, a network server's decoded object as `decoded`;
	// at most one is present, and neither is when the decoder read only the frame (which cannot be
	// flattened: that uplink is recorded as undecoded below, with its frame as the sample).
	raw := up.Json
	if raw == nil {
		raw = up.Decoded
	}
	var flat integrationcodec.Flattened
	if raw != nil {
		var object map[string]any
		dc := json.NewDecoder(bytes.NewReader(raw))
		dc.UseNumber()
		if err := dc.Decode(&object); err != nil {
			return nil, false, uint(resolvedevent.FailureReason_Invalid),
				fmt.Errorf("the carried object does not parse: %w", err)
		}
		var link *integrationcodec.Link
		if auth.integration.LinkMetrics {
			link = carriedLink(up.Link)
		}
		var ferr error
		flat, ferr = integrationcodec.Flatten(object, link)
		var tooMany integrationcodec.ErrTooManyReadings
		if errors.As(ferr, &tooMany) {
			return nil, false, uint(resolvedevent.FailureReason_DecodeFailed),
				rez.decodeFailed(&obs, &DecodeFailure{Reason: ReasonTooManyReadings, Text: string(ReasonTooManyReadings) + ": " + ferr.Error()})
		}
		if ferr != nil {
			return nil, false, uint(resolvedevent.FailureReason_Invalid), ferr
		}
	}
	if flat.Leaves == 0 {
		sample := ""
		if len(up.Frame) > 0 {
			sample = hex.EncodeToString(up.Frame[:min(len(up.Frame), model.MaxUndecodedSampleHex/2)])
		}
		if err := rez.Api.RecordIntegrationUndecoded(ctx, auth.integration.Token, auth.token, 1, sample); err != nil {
			// The marker is how an operator learns the integration is connected and storing nothing; a
			// failure to write it is the store's, so the uplink is retried rather than counted lost.
			return nil, false, uint(resolvedevent.FailureReason_ApiCallFailed), err
		}
		log.Debug().Str("integration", auth.integration.Token).
			Msg("An uplink carried for a decoder that is gone held nothing to flatten; recorded as undecoded")
		return nil, true, 0, nil
	}
	return flat.Readings, false, 0, nil
}

// carriedLink rebuilds the codec's Link from the carrier's link map.
func carriedLink(m map[string]float64) *integrationcodec.Link {
	if len(m) == 0 {
		return nil
	}
	l := &integrationcodec.Link{}
	if g, ok := m[integrationLinkGateways]; ok {
		l.Gateways = int(g)
	}
	rssi, hasRSSI := m[integrationLinkRSSI]
	snr, hasSNR := m[integrationLinkSNR]
	if hasRSSI && hasSNR {
		l.RSSI, l.SNR, l.HasSignal = rssi, snr, true
	}
	return l
}

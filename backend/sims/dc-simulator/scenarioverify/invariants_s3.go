// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/webhooksig"
	"github.com/devicechain-io/dc-simulator/scenariosink"
	"github.com/devicechain-io/dc-simulator/sim"
)

// The S3 invariants: deliveries out of the tenant (a signed webhook, an MQTT bulletin), the duty
// officer's notifications and their escalation, the truck's geofence, the pumps' calculated well
// fill, and the LoRaWAN gauges' decoding.
const (
	InvWebhookSigned         = "webhook-signed"
	InvWebhookControl        = "webhook-signature-control-is-red"
	InvMQTTBulletin          = "mqtt-bulletin"
	InvNotifyEscalates       = "notify-escalates"
	InvNotifyAckControl      = "notify-ack-control-is-red"
	InvTruckFence            = "truck-fence"
	InvWellFill              = "well-fill-calculated"
	InvLoraDecoded           = "lora-decoded"
	InvDecoderFailureVisible = "decoder-failure-visible"
)

// SinkInvariants need the in-cluster sink: without one (--sink) they are not judged, and the
// report names them as omitted rather than passing them.
var SinkInvariants = []string{InvWebhookSigned, InvWebhookControl, InvMQTTBulletin, InvNotifyEscalates, InvNotifyAckControl}

// AckGauge is the gauge whose flood alarm the verifier acknowledges once it has been paged at MAJOR,
// before its escalation is due: the in-run control of notify-escalates.
const AckGauge = "hp-gauge-north-2"

// ackAfter is how long the verifier lets AckGauge's alarm sit at MAJOR before acknowledging it:
// long enough for the page to go out, well inside the escalation window.
const ackAfter = 10 * time.Second

// webhookTolerance is how far a delivery's signed timestamp may sit from when the sink received it.
const webhookTolerance = 5 * time.Minute

// Ack is the verifier's acknowledgement of AckGauge's flood alarm.
type Ack struct {
	Device string    `json:"device"`
	Token  string    `json:"token"`
	At     time.Time `json:"at"`
}

// S3 is what the verifier read for the S3 beats.
type S3 struct {
	Tenant string
	Seed   int64
	// Sink is the in-cluster sink's record; nil when the run had no sink.
	Sink *scenariosink.Received
	// SigningSecret is the flood webhook's secret, which the verifier rotated in at the start of the
	// day; WrongSecret is a valid secret the platform never had (the control).
	SigningSecret, WrongSecret string
	Attempts                   map[string][]Attempt
	Ack                        Ack
	Health                     map[string]DecoderHealth
	DecodeFailures             map[string][]DecodeFailure
}

func init() {
	Controls[InvWebhookSigned] = sim.HarborpulseDefectUnsignedWebhook
	Controls[InvWebhookControl] = "none: it IS the control of " + InvWebhookSigned + " (a secret the platform never had)"
	Controls[InvMQTTBulletin] = sim.HarborpulseDefectMQTTUnreachable
	Controls[InvNotifyEscalates] = "the verifier acknowledges " + AckGauge + " before its escalation (" + InvNotifyAckControl + ")"
	Controls[InvNotifyAckControl] = "none: it IS the control of " + InvNotifyEscalates
	Controls[InvTruckFence] = sim.HarborpulseDefectTruckStaysHome
	Controls[InvWellFill] = sim.HarborpulseDefectWrongWellDepth
	Controls[InvLoraDecoded] = sim.HarborpulseDefectLoraWrongScale
	Controls[InvDecoderFailureVisible] = sim.HarborpulseDefectLegacyDecoderFixed
}

// s3Checks are the S3 invariants in report order.
func s3Checks() []struct {
	name string
	fn   func(Observations) error
} {
	return []struct {
		name string
		fn   func(Observations) error
	}{
		{InvWebhookSigned, func(o Observations) error { return webhookSigned(o, o.S3.SigningSecret) }},
		{InvWebhookControl, webhookControlIsRed},
		{InvMQTTBulletin, mqttBulletin},
		{InvNotifyEscalates, notifyEscalates},
		{InvNotifyAckControl, notifyAckControlIsRed},
		{InvTruckFence, truckFence},
		{InvWellFill, wellFill},
		{InvLoraDecoded, loraDecoded},
		{InvDecoderFailureVisible, decoderFailureVisible},
	}
}

func inWindow(t, from, to time.Time) bool { return !t.Before(from) && !t.After(to) }

// deliveredDevice is the device a delivery body names.
func deliveredDevice(body string) string {
	var b struct {
		Device string `json:"device"`
	}
	_ = json.Unmarshal([]byte(body), &b)
	return b.Device
}

// floodWebhooks are the deliveries the sink received on the flood webhook's path through the storm.
func floodWebhooks(o Observations) []scenariosink.Webhook {
	var out []scenariosink.Webhook
	for _, w := range o.S3.Sink.Webhooks {
		if w.Path == sim.HarborpulseWebhookPath && inWindow(w.At, o.at(stormFrom), o.end()) {
			out = append(out, w)
		}
	}
	return out
}

func verifyWebhook(w scenariosink.Webhook, secret string) error {
	h := http.Header{}
	for k, v := range w.Headers {
		for _, x := range v {
			h.Add(k, x)
		}
	}
	return webhooksig.Verify(h, []byte(w.Body), w.At, webhookTolerance, secret)
}

// webhookSigned: the flood webhook reached the sink for every East Quay gauge, and only for them,
// with a body whose signature verifies under this tenant's secret. Deliveries other sims' tenants
// sent to the same sink verify under none of this one's keys and are not counted, so the secret is
// both the authentication and the attribution.
func webhookSigned(o Observations, secret string) error {
	if secret == "" {
		return fmt.Errorf("no signing secret to verify with")
	}
	arrived := floodWebhooks(o)
	verified := map[string]time.Time{}
	unsigned := 0
	for _, w := range arrived {
		if len(w.Headers["Webhook-Signature"]) == 0 {
			unsigned++
			continue
		}
		if verifyWebhook(w, secret) == nil {
			if d := deliveredDevice(w.Body); d != "" {
				if _, ok := verified[d]; !ok {
					verified[d] = w.At
				}
			}
		}
	}
	if diff := sameSet(verified, fleetOf(sim.HarborpulseGaugeType, sim.HarborpulseOutageQuay)); diff != "" {
		return fmt.Errorf("signed flood webhook %s (%d deliveries arrived through the storm, %d unsigned)",
			strings.ReplaceAll(diff, "raised", "verified"), len(arrived), unsigned)
	}
	return nil
}

// webhookControlIsRed: the same deliveries checked against a secret the platform never had must not
// verify, or webhook-signed could not tell a signature from a header.
func webhookControlIsRed(o Observations) error {
	if len(floodWebhooks(o)) == 0 {
		return fmt.Errorf("no flood webhook reached the sink, so the control shows nothing")
	}
	if o.S3.WrongSecret == "" || o.S3.WrongSecret == o.S3.SigningSecret {
		return fmt.Errorf("the control has no secret of its own")
	}
	for _, w := range floodWebhooks(o) {
		if verifyWebhook(w, o.S3.WrongSecret) == nil {
			return fmt.Errorf("a delivery verified under a secret the platform never had")
		}
	}
	if webhookSigned(o, o.S3.WrongSecret) == nil {
		return fmt.Errorf("%s was judged green under a secret the platform never had: it cannot fail", InvWebhookSigned)
	}
	return nil
}

// mqttBulletin: a flood bulletin reached the sink's broker, on this tenant's topic, for every gauge
// that passed the major level (all nine), and for nothing else.
func mqttBulletin(o Observations) error {
	topic := sim.HarborpulseBulletinTopic(o.S3.Tenant)
	seen := map[string]time.Time{}
	n := 0
	for _, m := range o.S3.Sink.Messages {
		if m.Topic != topic || !inWindow(m.At, o.at(stormFrom), o.end()) {
			continue
		}
		n++
		if d := deliveredDevice(m.Payload); d != "" {
			if _, ok := seen[d]; !ok {
				seen[d] = m.At
			}
		}
	}
	if diff := sameSet(seen, fleetOf(sim.HarborpulseGaugeType, "")); diff != "" {
		return fmt.Errorf("flood bulletin on %s %s (%d bulletins received)", topic, strings.ReplaceAll(diff, "raised", "published"), n)
	}
	return nil
}

// floodTokens are the flood alarm tokens of gauge g seen through the storm.
func floodTokens(o Observations, g string) []string {
	set := map[string]time.Time{}
	for _, a := range samples(o, o.at(stormFrom), o.end(), func(a AlarmSample) bool {
		return a.Device == g && strings.HasPrefix(a.Key, sim.HarborpulseFloodAlarmKey)
	}) {
		set[a.Token] = a.At
	}
	return sortedKeys(set)
}

// gaugePagedAndEscalated reports why gauge g's flood alarm was not paged and then re-paged.
func gaugePagedAndEscalated(o Observations, g string) error {
	tokens := floodTokens(o, g)
	if len(tokens) == 0 {
		return fmt.Errorf("%s: no flood alarm", g)
	}
	sent := map[string]int{}
	var statuses []string
	for _, t := range tokens {
		for _, a := range o.S3.Attempts[t] {
			statuses = append(statuses, a.Trigger+"/"+a.Status+a.ErrorCode)
			if a.Status == "SENT" {
				sent[a.Trigger]++
			}
		}
	}
	switch {
	case sent["ALARM"] == 0:
		return fmt.Errorf("%s: never paged (attempts: %s)", g, strings.Join(statuses, ", "))
	case sent["ESCALATION"] == 0:
		return fmt.Errorf("%s: paged but never escalated (attempts: %s)", g, strings.Join(statuses, ", "))
	}
	return nil
}

// sentFor counts the SENT attempts on the gauges' flood alarms.
func sentFor(o Observations, gauges []string) int {
	n := 0
	for _, g := range gauges {
		for _, t := range floodTokens(o, g) {
			for _, a := range o.S3.Attempts[t] {
				if a.Status == "SENT" {
					n++
				}
			}
		}
	}
	return n
}

// notifyEscalates: every gauge's flood alarm paged the duty officer when it reached MAJOR and paged
// again when nobody acknowledged it within the policy's escalation window, and the mail reached the
// sink. The one gauge the verifier acknowledged is the control and is judged there instead.
func notifyEscalates(o Observations) error {
	var problems []string
	var judged []string
	for _, g := range fleetOf(sim.HarborpulseGaugeType, "") {
		if g == AckGauge {
			continue
		}
		judged = append(judged, g)
		if err := gaugePagedAndEscalated(o, g); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	recipient := sim.HarborpulseDutyRecipient(o.S3.Tenant)
	mail := 0
	for _, m := range o.S3.Sink.Mail {
		for _, to := range m.To {
			if to == recipient && inWindow(m.At, o.at(stormFrom), o.end()) {
				mail++
			}
		}
	}
	if sent := sentFor(o, judged); mail < sent {
		return fmt.Errorf("the platform recorded %d pages SENT for the flood alarms but the sink received %d mails for %s", sent, mail, recipient)
	}
	return nil
}

// notifyAckControlIsRed: the gauge the verifier acknowledged was paged and then NOT escalated, so the
// check notify-escalates applies to every other gauge is red for it.
func notifyAckControlIsRed(o Observations) error {
	if o.S3.Ack.Token == "" {
		return fmt.Errorf("the verifier never acknowledged %s's flood alarm, so the control shows nothing", AckGauge)
	}
	sent := false
	for _, a := range o.S3.Attempts[o.S3.Ack.Token] {
		if a.Trigger == "ESCALATION" {
			return fmt.Errorf("%s was escalated at %s although it was acknowledged at %s", AckGauge, a.At.Format(time.RFC3339), o.S3.Ack.At.Format(time.RFC3339))
		}
		sent = sent || (a.Trigger == "ALARM" && a.Status == "SENT")
	}
	if !sent {
		return fmt.Errorf("%s was never paged, so its missing escalation shows nothing", AckGauge)
	}
	if gaugePagedAndEscalated(o, AckGauge) == nil {
		return fmt.Errorf("the acknowledged gauge was judged green: %s cannot fail", InvNotifyEscalates)
	}
	return nil
}

// truckFence: the truck-in-flood-plain alarm is raised for the truck alone, once it enters the
// flood plain, and clears after it leaves.
func truckFence(o Observations) error {
	truck := fleetOf(sim.HarborpulseTruckType, "")
	seen := activeDevices(o, sim.HarborpulseTruckFenceAlarmKey, o.at(WatchFrom), o.end())
	if diff := sameSet(seen, truck); diff != "" {
		return fmt.Errorf("truck-in-flood-plain %s", diff)
	}
	enters, leaves := o.at(sim.HarborpulseTruckInPlainFrom), o.at(sim.HarborpulseTruckInPlainTo)
	first := seen[truck[0]]
	if first.Before(enters.Add(-maxPollGap)) || first.After(o.at(sim.HarborpulseTruckInPlainFrom+0.5)) {
		return fmt.Errorf("truck-in-flood-plain first active at %s; the truck enters the plain at %s", first.Format(time.RFC3339), enters.Format(time.RFC3339))
	}
	var tokens []string
	for _, a := range samples(o, o.at(WatchFrom), o.end(), func(a AlarmSample) bool { return a.Key == sim.HarborpulseTruckFenceAlarmKey }) {
		if a.State == sim.AlarmStateActiveWire && a.At.After(o.at(sim.HarborpulseTruckInPlainTo+0.75)) {
			return fmt.Errorf("truck-in-flood-plain still active at %s; the truck left the plain at %s", a.At.Format(time.RFC3339), leaves.Format(time.RFC3339))
		}
		if !sliceHas(tokens, a.Token) {
			tokens = append(tokens, a.Token)
		}
	}
	for _, t := range tokens {
		if st := lastState(o, t); st != sim.AlarmStateClearedWire {
			return fmt.Errorf("truck-in-flood-plain last seen %s, not cleared after the truck left", st)
		}
	}
	return nil
}

func sliceHas(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// minPaired is the fewest calculated readings a pump must have stored through the storm.
const minPaired = 10

// wellFill: every pump's calculated well fill is stored beside its wet well reading, and equals the
// level over the well's depth, as a percentage.
func wellFill(o Observations) error {
	var problems []string
	for _, p := range fleetOf(sim.HarborpulsePumpType, "") {
		from, to := o.at(stormFrom), o.at(pumpTo)
		wet := map[int64]float64{}
		for _, r := range readingsOf(o, p, sim.HarborpulseWetWellKey, from, to) {
			wet[r.At.UnixMicro()] = r.Value
		}
		paired, wrong := 0, 0
		var example string
		for _, r := range readingsOf(o, p, sim.HarborpulseWellFillKey, from, to) {
			w, ok := wet[r.At.UnixMicro()]
			if !ok {
				continue
			}
			paired++
			if want := w / sim.HarborpulseWellDepthM * 100; math.Abs(r.Value-want) > 0.01 {
				wrong++
				if example == "" {
					example = fmt.Sprintf("%.3f m stored %.3f %%, want %.3f %%", w, r.Value, want)
				}
			}
		}
		switch {
		case paired < minPaired:
			problems = append(problems, fmt.Sprintf("%s: %d calculated well fill readings beside a wet well reading through the storm, want at least %d", p, paired, minPaired))
		case wrong > 0:
			problems = append(problems, fmt.Sprintf("%s: %d of %d well fill readings are not level over a %.1f m well (%s)", p, wrong, paired, sim.HarborpulseWellDepthM, example))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// minUplinks is the fewest decoded levels a LoRaWAN gauge must have stored through the watch.
const minUplinks = 20

// loraTolerance is how far a decoded level may sit from the model's: the frame carries centimetres,
// and the stored time may be coarser than the instant the level was sampled at.
const loraTolerance = 0.011

// loraDecoded: each LoRaWAN gauge on the current decoder had its uplinks decoded and stored, every
// stored level is the one the gauge encoded, and its profile's decoder failed nothing.
func loraDecoded(o Observations) error {
	var problems []string
	for _, g := range sim.HarborpulseLoraGauges() {
		rs := readingsOf(o, g, sim.HarborpulseWaterLevelKey, o.at(WatchFrom), o.end())
		wrong := 0
		var example string
		for _, r := range rs {
			// The gauge sampled at its tick's instant, which the stored time carries to the
			// microsecond: a second boundary a microsecond away picks the neighbouring sample.
			elapsed := r.At.Sub(o.Clock.StartedAt)
			want, _ := sim.HarborpulseLoraLevelAt(o.S3.Seed, g, elapsed, o.Clock.DayLength)
			prev, _ := sim.HarborpulseLoraLevelAt(o.S3.Seed, g, elapsed-time.Millisecond, o.Clock.DayLength)
			if math.Abs(r.Value-want) > loraTolerance && math.Abs(r.Value-prev) > loraTolerance {
				wrong++
				if example == "" {
					example = fmt.Sprintf("%s stored %.3f m, the gauge sent %.2f m", r.At.Format(time.RFC3339), r.Value, want)
				}
			}
		}
		switch {
		case len(rs) < minUplinks:
			problems = append(problems, fmt.Sprintf("%s: %d decoded levels stored through the watch, want at least %d", g, len(rs), minUplinks))
		case wrong > 0:
			problems = append(problems, fmt.Sprintf("%s: %d of %d decoded levels are not what the gauge sent (%s)", g, wrong, len(rs), example))
		}
		if n := len(o.S3.DecodeFailures[g]); n > 0 {
			problems = append(problems, fmt.Sprintf("%s: %d uplinks failed to decode (%s)", g, n, o.S3.DecodeFailures[g][0].Error))
		}
	}
	h := o.S3.Health[sim.HarborpulseLoraGaugeProfile]
	if h.Decoded < 1 || h.Failed > 0 {
		problems = append(problems, fmt.Sprintf("the current decoder's health: %.0f decoded, %.0f failed (%s)", h.Decoded, h.Failed, h.LastError))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// decoderFailureVisible: the legacy gauge's layout-2 uplinks are recorded as DECODE_FAILED, with
// the evaluation error that caused them, and the decoder health shows them, while its layout-1
// uplinks still decode and are stored.
func decoderFailureVisible(o Observations) error {
	g := sim.HarborpulseLegacyGauge
	var problems []string
	h := o.S3.Health[sim.HarborpulseLegacyGaugeProfile]
	if h.Failed < 1 || h.Decoded < 1 {
		problems = append(problems, fmt.Sprintf("the legacy decoder's health: %.0f decoded, %.0f failed; want both", h.Decoded, h.Failed))
	}
	var fails []DecodeFailure
	for _, f := range o.S3.DecodeFailures[g] {
		if inWindow(f.At, o.at(WatchFrom), o.end()) {
			fails = append(fails, f)
		}
	}
	sort.Slice(fails, func(i, j int) bool { return fails[i].At.Before(fails[j].At) })
	if len(fails) == 0 {
		problems = append(problems, g+": no DECODE_FAILED record through the watch")
	}
	for _, f := range fails {
		if !strings.Contains(f.Error, "eval_error") {
			problems = append(problems, fmt.Sprintf("%s: a DECODE_FAILED record names %q, not the evaluation error", g, f.Error))
			break
		}
	}
	if len(readingsOf(o, g, sim.HarborpulseWaterLevelKey, o.at(WatchFrom), o.end())) == 0 {
		problems = append(problems, g+": not one layout-1 uplink was decoded and stored")
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

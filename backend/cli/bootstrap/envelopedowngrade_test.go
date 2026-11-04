// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
)

func marks(inbound, resolved uint64) string {
	b, _ := json.Marshal(map[string]uint64{streams.InboundEvents: inbound, streams.ResolvedEvents: resolved})
	return string(b)
}

func bounds(inFirst, inLast, resFirst, resLast uint64) map[string]streamBounds {
	return map[string]streamBounds{
		streams.InboundEvents:  {First: inFirst, Last: inLast},
		streams.ResolvedEvents: {First: resFirst, Last: resLast},
	}
}

func probeOf(res envelopeProbeResult, err error, called *int) func(context.Context) (envelopeProbeResult, error) {
	return func(context.Context) (envelopeProbeResult, error) {
		if called != nil {
			*called++
		}
		return res, err
	}
}

func TestEnvelopeDowngradeIsExact(t *testing.T) {
	ctx := context.Background()
	clean := envelopeProbeResult{Bounds: bounds(500, 900, 500, 900)}
	cases := []struct {
		name       string
		f          envelopeFacts
		res        envelopeProbeResult
		probeErr   error
		wantRefuse bool
		wantProbe  bool
	}{
		{"flag on, to an old release", envelopeFacts{From: "v1.2.0", To: "v1.0.0", FlagOn: true}, clean, nil, true, false},
		{"both switches on", envelopeFacts{From: "v1.2.0", To: "v1.0.0", FlagOn: true, Switches: []string{"resolved", "inbound"}}, clean, nil, true, false},
		{"never enabled, streams clean", envelopeFacts{From: "v1.2.0", To: "v1.0.0"}, clean, nil, false, true},
		{"never enabled, but the sample finds one (set outside dcctl)", envelopeFacts{From: "v1.2.0", To: "v1.0.0"},
			envelopeProbeResult{Bounds: bounds(1, 9, 1, 9), Sample: "resolved-events (message 4)"}, nil, true, true},
		{"enabled, off-mark passed on both streams", envelopeFacts{From: "v1.2.0", To: "v1.0.0", EnabledAt: "t", OffMarks: marks(400, 400)}, clean, nil, false, true},
		{"enabled, one stream still holds messages from before the mark", envelopeFacts{From: "v1.2.0", To: "v1.0.0", EnabledAt: "t", OffMarks: marks(400, 600)}, clean, nil, true, true},
		{"enabled, the oldest message IS the mark", envelopeFacts{From: "v1.2.0", To: "v1.0.0", EnabledAt: "t", OffMarks: marks(500, 400)}, clean, nil, true, true},
		{"enabled, no off-mark recorded", envelopeFacts{From: "v1.2.0", To: "v1.0.0", EnabledAt: "t"}, clean, nil, true, true},
		{"enabled, unreadable off-mark", envelopeFacts{From: "v1.2.0", To: "v1.0.0", EnabledAt: "t", OffMarks: "{"}, clean, nil, true, true},
		{"enabled, a stream missing from the mark", envelopeFacts{From: "v1.2.0", To: "v1.0.0", EnabledAt: "t", OffMarks: `{"inbound-events":1}`}, clean, nil, true, true},
		{"probe cannot tell", envelopeFacts{From: "v1.2.0", To: "v1.0.0"}, clean, errors.New("no route"), true, true},
		{"to the cutoff", envelopeFacts{From: "v1.2.0", To: "v1.1.0", FlagOn: true}, clean, nil, false, false},
		{"to its release candidate", envelopeFacts{From: "v1.2.0", To: "v1.1.0-rc.1", FlagOn: true}, clean, nil, false, false},
		{"above the cutoff", envelopeFacts{From: "v1.2.0", To: "v1.3.4", FlagOn: true}, clean, nil, false, false},
		{"from below the cutoff: no producer ever ran", envelopeFacts{From: "v1.0.0", To: "v0.27.0", FlagOn: true}, clean, nil, false, false},
		{"to a local build tag with envelopes on", envelopeFacts{From: "v1.2.0", To: "dev", FlagOn: true}, clean, nil, true, false},
		{"to a local build tag, probe cannot tell", envelopeFacts{From: "v1.2.0", To: "dev"}, clean, errors.New("no route"), true, true},
		{"from a local build tag to an old release, envelopes on", envelopeFacts{From: "dev", To: "v1.0.0", FlagOn: true}, clean, nil, true, false},
		{"the same tag is no move", envelopeFacts{From: "v1.0.0", To: "v1.0.0", FlagOn: true}, clean, nil, false, false},
		{"the same local build tag is no move", envelopeFacts{From: "dev", To: "dev", FlagOn: true}, clean, nil, false, false},
		{"--allow-envelope-downgrade skips the refusal", envelopeFacts{From: "v1.2.0", To: "v1.0.0", FlagOn: true, Allow: true}, clean, nil, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			probes := 0
			err := checkEnvelopeDowngrade(ctx, c.f, probeOf(c.res, c.probeErr, &probes))
			if (err != nil) != c.wantRefuse {
				t.Fatalf("refusal = %v, want %v (err %v)", err != nil, c.wantRefuse, err)
			}
			if (probes > 0) != c.wantProbe {
				t.Fatalf("probed = %v, want %v", probes > 0, c.wantProbe)
			}
			if err != nil && !strings.Contains(err.Error(), "Nothing has been changed") {
				t.Fatalf("a refusal must say nothing moved and what to do: %v", err)
			}
		})
	}
}

// The records the exact rule rests on: turning envelopes on is recorded (clearing an earlier off
// record), a switch found on without a record is recorded as set outside dcctl, and once the
// switch is off the marks are taken from the streams exactly once.
func TestTheEnvelopeSwitchLeavesTheRecordsTheRuleReads(t *testing.T) {
	ctx := context.Background()
	dyn := declarationClient(declaredInstance(t, "prod", nil))

	if err := recordEnvelopesEnabled(ctx, dyn, "prod", true); err != nil {
		t.Fatal(err)
	}
	a := readBack(t, dyn, "prod").Annotations
	if a[AnnotationEnvelopesEnabledAt] == "" || a[AnnotationEnvelopesOffMarks] != "" {
		t.Fatalf("after turning on: %v", a)
	}

	// Off again, and the rollout done: the marks are the streams' newest sequences.
	prev := envelopeStreamProbe
	t.Cleanup(func() { envelopeStreamProbe = prev })
	probes := 0
	envelopeStreamProbe = func(context.Context, string, string) (envelopeProbeResult, error) {
		probes++
		return envelopeProbeResult{Bounds: bounds(10, 700, 10, 800)}, nil
	}
	st := &State{Instance: "prod"}
	if err := settleEnvelopeMarks(ctx, dyn, st); err != nil {
		t.Fatal(err)
	}
	var got map[string]uint64
	if err := json.Unmarshal([]byte(readBack(t, dyn, "prod").Annotations[AnnotationEnvelopesOffMarks]), &got); err != nil {
		t.Fatal(err)
	}
	if got[streams.InboundEvents] != 700 || got[streams.ResolvedEvents] != 800 {
		t.Fatalf("marks = %v, want the newest sequences 700 and 800", got)
	}
	// A later run never moves a mark that is recorded (it would only make the rule weaker).
	envelopeStreamProbe = func(context.Context, string, string) (envelopeProbeResult, error) {
		probes++
		return envelopeProbeResult{Bounds: bounds(10, 9999, 10, 9999)}, nil
	}
	if err := settleEnvelopeMarks(ctx, dyn, st); err != nil {
		t.Fatal(err)
	}
	if probes != 1 {
		t.Fatalf("the streams were read %d times; a recorded mark is final", probes)
	}

	// An instance that never had envelopes records nothing and reads nothing.
	other := declarationClient(declaredInstance(t, "fresh", nil))
	if err := settleEnvelopeMarks(ctx, other, &State{Instance: "fresh"}); err != nil || probes != 1 {
		t.Fatalf("a never-enabled instance was probed or failed: %v (probes %d)", err, probes)
	}

	// Found on with no record (set outside dcctl): recorded as such; a record that exists is kept.
	if err := recordEnvelopesEnabled(ctx, other, "fresh", false); err != nil {
		t.Fatal(err)
	}
	if v := readBack(t, other, "fresh").Annotations[AnnotationEnvelopesEnabledAt]; !strings.Contains(v, "set outside dcctl") {
		t.Fatalf("enabled-at = %q", v)
	}
	before := readBack(t, other, "fresh").Annotations[AnnotationEnvelopesEnabledAt]
	if err := recordEnvelopesEnabled(ctx, other, "fresh", false); err != nil {
		t.Fatal(err)
	}
	if readBack(t, other, "fresh").Annotations[AnnotationEnvelopesEnabledAt] != before {
		t.Fatal("an existing record was rewritten")
	}
	// Turning it on AGAIN clears the marks of the earlier off.
	if err := recordEnvelopesEnabled(ctx, dyn, "prod", true); err != nil {
		t.Fatal(err)
	}
	if readBack(t, dyn, "prod").Annotations[AnnotationEnvelopesOffMarks] != "" {
		t.Fatal("turning envelopes on again left the earlier off-marks")
	}
}

func TestTheOverlaySwitchIsReadFromItsNestedKey(t *testing.T) {
	on := map[string]interface{}{"instance": map[string]interface{}{"config": map[string]interface{}{"infrastructure": map[string]interface{}{
		"messaging": map[string]interface{}{"envelopes": map[string]interface{}{"resolved": true}}}}}}
	if !overlayEnvelopesOn(on) {
		t.Fatal("a true switch was not read")
	}
	inbound := map[string]interface{}{"instance": map[string]interface{}{"config": map[string]interface{}{"infrastructure": map[string]interface{}{
		"messaging": map[string]interface{}{"envelopes": map[string]interface{}{"inbound": true}}}}}}
	if !overlayEnvelopesOn(inbound) {
		t.Fatal("the inbound switch alone was not read as envelopes on")
	}
	if !envelopesOn(config.EnvelopesConfiguration{Inbound: true}) || !envelopesOn(config.EnvelopesConfiguration{Resolved: true}) ||
		envelopesOn(config.EnvelopesConfiguration{}) {
		t.Fatal("either deployed switch must read as envelopes on, and neither as off")
	}
	if overlayEnvelopesOn(nil) || overlayEnvelopesOn(map[string]interface{}{"instance": "x"}) {
		t.Fatal("an absent or malformed switch must read as off")
	}
}

func TestTheRefusalNamesEverySwitchThatIsOn(t *testing.T) {
	err := checkEnvelopeDowngrade(context.Background(),
		envelopeFacts{From: "v1.2.0", To: "v1.0.0", FlagOn: true, Switches: envelopeSwitchNames(config.EnvelopesConfiguration{Resolved: true, Inbound: true})},
		probeOf(envelopeProbeResult{}, nil, nil))
	if err == nil || !strings.Contains(err.Error(), "resolved, inbound") {
		t.Fatalf("both switches are on and the refusal must name both: %v", err)
	}
	err = checkEnvelopeDowngrade(context.Background(),
		envelopeFacts{From: "v1.2.0", To: "v1.0.0", FlagOn: true, Switches: envelopeSwitchNames(config.EnvelopesConfiguration{Inbound: true})},
		probeOf(envelopeProbeResult{}, nil, nil))
	if err == nil || !strings.Contains(err.Error(), "ON (inbound)") {
		t.Fatalf("only inbound is on and the refusal must say so: %v", err)
	}
}

type fakeStreams struct {
	infos map[string]*nats.StreamInfo
	msgs  map[string]map[uint64]*nats.RawStreamMsg
}

func (f *fakeStreams) StreamInfo(s string, _ ...nats.JSOpt) (*nats.StreamInfo, error) {
	if i, ok := f.infos[s]; ok {
		return i, nil
	}
	return nil, fmt.Errorf("wrapped: %w", nats.ErrStreamNotFound)
}

func (f *fakeStreams) GetMsg(name string, seq uint64, _ ...nats.JSOpt) (*nats.RawStreamMsg, error) {
	if m, ok := f.msgs[name][seq]; ok {
		return m, nil
	}
	return nil, fmt.Errorf("wrapped: %w", nats.ErrMsgNotFound)
}

func TestSampleReportsBoundsFindsAnEnvelopeAndRefusesAMissingStream(t *testing.T) {
	res := messaging.StreamName("i1", streams.ResolvedEvents)
	inb := messaging.StreamName("i1", streams.InboundEvents)
	plain := &nats.RawStreamMsg{Header: nats.Header{}}
	env := &nats.RawStreamMsg{Header: nats.Header{eventenvelope.HeaderName: []string{eventenvelope.HeaderValue}}}
	f := &fakeStreams{
		infos: map[string]*nats.StreamInfo{
			inb: {State: nats.StreamState{Msgs: 2, FirstSeq: 7, LastSeq: 8}},
			res: {State: nats.StreamState{Msgs: 5, FirstSeq: 1, LastSeq: 5}},
		},
		msgs: map[string]map[uint64]*nats.RawStreamMsg{inb: {7: plain, 8: plain}, res: {1: plain, 2: plain, 3: plain, 4: env, 5: plain}},
	}
	got, err := sampleStreams(context.Background(), f, "i1")
	if err != nil || !strings.Contains(got.Sample, "resolved-events (message 4)") {
		t.Fatalf("sample = %+v, %v; want the envelope at message 4", got, err)
	}
	if got.Bounds[streams.InboundEvents] != (streamBounds{7, 8}) || got.Bounds[streams.ResolvedEvents] != (streamBounds{1, 5}) {
		t.Fatalf("bounds = %+v", got.Bounds)
	}
	f.msgs[res][4] = plain
	if got, err := sampleStreams(context.Background(), f, "i1"); err != nil || got.Sample != "" {
		t.Fatalf("clean streams: %+v, %v", got, err)
	}
	// A stream that does not exist on a running instance is an error, never "clean".
	delete(f.infos, res)
	if _, err := sampleStreams(context.Background(), f, "i1"); err == nil {
		t.Fatal("a missing hot stream was read as clean")
	}
}

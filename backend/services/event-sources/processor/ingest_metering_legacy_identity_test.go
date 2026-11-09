// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// THE LEGACY GATE IS UNCHANGED BY THE LAG-CREDITED ONE (#2163).
//
// ingestMetering defaults to legacy, and the lag-credited gate added plumbing to the gateway source's
// handle() (a second branch, OriginPreMetered, rawMessage.metering) and to the decode worker's reading
// stage. None of it may change a legacy decision. This test drives a fixed sequence of captured messages
// through the real legacy composition main.go builds (RefuseDeletedTenants over NewRateGate, NewMeterAt,
// NewReadingGate, all on one virtual clock) and writes down everything observable: every call the
// source made to the message gate and to the reading stage, with its arguments and verdict, every shed
// and unresolved-admission count the limiters reported (the hooks main.go turns into metrics), and what
// was received, published and acked.
//
// The golden file was RECORDED ON main before the lag-credited gate was wired in (this file compiles
// against main unchanged; regenerate with -update-legacy-transcript only from a main checkout), so a
// pass here is the claim the default path is identical, decision for decision and count for count.
//
// It must be able to fail: TestTheLegacyTranscriptSeesAChangedDecision is its negative control.

var updateLegacyTranscript = flag.Bool("update-legacy-transcript", false,
	"rewrite testdata/legacy_gate_transcript.golden (only from a checkout of main)")

const legacyTranscriptGolden = "testdata/legacy_gate_transcript.golden"

// legacyClock is the one virtual clock every limiter and the routing decision read.
type legacyClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *legacyClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *legacyClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// legacyStep is one delivery of the sequence. stamp is the broker append time relative to the clock at
// that moment (nil: no append time at all); tenant picks the subject.
type legacyStep struct {
	tenant     string
	seq        uint64
	deliveries int
	stamp      *time.Duration
	advance    time.Duration
	// altId, when set, sends the message on a gateway's namespace carrying that key, so the source owes
	// the gateway an answer (accepted, or refused when it is shed).
	altId string
}

func ago(d time.Duration) *time.Duration { v := -d; return &v }

// legacySequence mixes every case the legacy gate distinguishes: a fresh burst past the tenant's burst,
// a backlog older than the cover (routed to the backlog limiter at its send time), a stamp the live burst
// can absorb, an unstamped delivery, redeliveries (exempt from metering, never from the lifecycle), a
// deleted tenant, a tenant whose ceiling has not resolved (counted as an unresolved admission), a stamp
// in the pod's future, and enough admitted messages to run the reading stage dry.
func legacySequence() []legacyStep {
	var s []legacyStep
	seq := uint64(0)
	add := func(tenant string, deliveries int, stamp *time.Duration, advance time.Duration) {
		seq++
		s = append(s, legacyStep{tenant: tenant, seq: seq, deliveries: deliveries, stamp: stamp, advance: advance})
	}
	for i := 0; i < 8; i++ {
		add("acme", 1, ago(0), 10*time.Millisecond) // a fresh burst: 5 fit, then the ceiling
	}
	add("acme", 2, ago(time.Second), 0) // a redelivery: never metered
	for i := 0; i < 4; i++ {
		add("beta", 1, ago(40*time.Second-time.Duration(i)*100*time.Millisecond), 5*time.Millisecond) // backlog
	}
	add("acme", 1, ago(200*time.Millisecond), 0) // within the cover: live
	add("acme", 1, nil, 0)                       // no append time: live, at now
	add("gone", 1, ago(0), 0)                    // a deleted tenant
	add("gone", 3, ago(0), 0)                    // ...refused on a redelivery as well
	add("pending", 1, ago(0), 0)                 // ceiling not resolved yet: admitted at the default, counted
	add("pending", 1, ago(0), 0)
	add("beta", 1, &[]time.Duration{3 * time.Second}[0], 0) // a stamp in the pod's future
	add("acme", 1, ago(0), 1500*time.Millisecond)           // after a refill
	for i := 0; i < 6; i++ {
		add("acme", 1, ago(0), 20*time.Millisecond)
	}
	for i := 0; i < 3; i++ {
		add("beta", 1, ago(20*time.Second), 0) // a second backlog, older stamps than the bucket has seen
	}
	add("acme", 4, ago(time.Hour), 0) // an old redelivery
	// On a gateway's namespace: every outcome is answered. After a refill, 8 at once: 5 pass the message
	// ceiling, of which the reading stage takes 3 (accepted) and sheds 2 (refused), and 3 are shed at the
	// message gate (refused); then a deleted tenant's gateway event (refused).
	for i := 0; i < 8; i++ {
		adv := time.Duration(0)
		if i == 0 {
			adv = 2 * time.Second
		}
		add("acme", 1, ago(0), adv)
		s[len(s)-1].altId = fmt.Sprintf("edge:i:e:%d", i)
	}
	add("gone", 1, ago(0), 0)
	s[len(s)-1].altId = "edge:i:e:gone"
	return s
}

// legacyTranscript runs the sequence and returns the transcript, one line per observable event. configure,
// when set, acts on the source after the legacy wiring (the negative controls use it to break that wiring).
func legacyTranscript(t *testing.T, configure func(es *GatewayJetStreamSource)) []string {
	t.Helper()
	clk := &legacyClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	base := clk.now()
	rel := func(at time.Time) string {
		if at.IsZero() {
			return "zero"
		}
		return at.Sub(base).String()
	}

	var mu sync.Mutex
	var lines []string
	log := func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		mu.Unlock()
	}

	ceiling := func(rate float64, burst int) core.TenantCeilingResolver {
		return func(tenant string) core.TenantCeiling {
			src := core.CeilingResolved
			if tenant == "pending" {
				src = core.CeilingPending
			}
			return core.TenantCeiling{RatePerSecond: rate, Burst: burst, Source: src}
		}
	}
	unresolved := func(stage string) core.TenantRateLimiterOption {
		return core.WithUnresolvedAdmissions(func(src core.CeilingSource) { log("  unresolved stage=%s cause=%s", stage, src) })
	}
	msgCeiling, readCeiling := ceiling(10, 5), ceiling(10, 3)
	opt := core.WithClock(clk.now)
	live := core.NewTenantRateLimiter(msgCeiling, opt, unresolved("live"))
	backlog := core.NewTenantRateLimiter(msgCeiling, opt, unresolved("backlog"))
	untrusted := core.NewTenantRateLimiter(msgCeiling, opt, unresolved("untrusted"))
	integration := core.NewTenantRateLimiter(msgCeiling, opt, unresolved("integration"))
	rLive := core.NewTenantRateLimiter(readCeiling, opt)
	rBacklog := core.NewTenantRateLimiter(readCeiling, opt)
	rUntrusted := core.NewTenantRateLimiter(readCeiling, opt)
	rIntegration := core.NewTenantRateLimiter(readCeiling, opt)

	gate := RefuseDeletedTenants(func(tenant string) bool { return tenant == "gone" },
		NewRateGate(live, backlog, untrusted, integration, func(source, tenant string) {
			log("  shed source=%s tenant=%s", source, tenant)
		}),
		func(source, tenant string) { log("  tenant-gone source=%s tenant=%s", source, tenant) })
	readings := NewReadingGate(rLive, rBacklog, rUntrusted, rIntegration, func(source, tenant string, n int) {
		log("  reading-shed source=%s tenant=%s readings=%d", source, tenant, n)
	})

	feedback := &transcriptFeedback{log: log}
	h := newCaptureHarnessWith(t, func(es *GatewayJetStreamSource) {
		es.UseGatewayFeedback(feedback)
		es.meter = newMeterAtClock(live, clk.now)
		if configure != nil {
			configure(es)
		}
	})
	h.gate = func(source, tenant string, sentAt time.Time, redelivery bool, origin Origin) bool {
		ok := gate(source, tenant, sentAt, redelivery, origin)
		log("  gate tenant=%s sentAt=%s redelivery=%t origin=%d -> %t", tenant, rel(sentAt), redelivery, origin, ok)
		return ok
	}
	h.readings = func(source, tenant string, sentAt time.Time, redelivery bool, origin Origin, n int) bool {
		ok := readings(source, tenant, sentAt, redelivery, origin, n)
		log("  readings tenant=%s sentAt=%s redelivery=%t origin=%d n=%d -> %t", tenant, rel(sentAt), redelivery, origin, n, ok)
		return ok
	}

	for i, st := range legacySequence() {
		clk.advance(st.advance)
		stamp := "none"
		ack := &recordingAck{}
		subject, body := "inst-1."+st.tenant+".devices.sensor-001.events", validEvent
		if st.altId != "" {
			subject, body = "inst-1."+st.tenant+".gateways.gw-1.devices.sensor-001.events", gatewayEvent(st.altId)
		}
		m := capturedMsg(subject, body, st.deliveries, st.seq, ack)
		if st.stamp != nil {
			m.AppendTime = clk.now().Add(*st.stamp)
			stamp = rel(m.AppendTime)
		}
		log("msg %d tenant=%s seq=%d deliveries=%d stamp=%s now=%s", i, st.tenant, st.seq, st.deliveries, stamp, rel(clk.now()))
		h.source.handle(m)
		require.Truef(t, ack.settled(t), "message %d was never settled", i)
		published, failed, received, _ := h.counts()
		log("  acks=%d published=%d failed=%d received=%d", ack.counts(), published, failed, received)
	}
	return lines
}

// transcriptFeedback writes the answers the source gives a gateway into the transcript.
type transcriptFeedback struct {
	log func(format string, args ...any)
}

func (f *transcriptFeedback) Accepted(tenant, gateway, altId string) {
	f.log("  feedback accepted tenant=%s gateway=%s altId=%s", tenant, gateway, altId)
}

func (f *transcriptFeedback) Refused(tenant, gateway string, n messaging.GatewayNotice) {
	f.log("  feedback refused tenant=%s gateway=%s altId=%s device=%s reason=%s", tenant, gateway, n.AltId, n.Device, n.Reason)
}

func TestTheLegacyGateIsUnchangedByTheLagCreditedGate(t *testing.T) {
	got := strings.Join(legacyTranscript(t, nil), "\n") + "\n"
	path := filepath.FromSlash(legacyTranscriptGolden)
	if *updateLegacyTranscript {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		t.Logf("wrote %s", path)
		return
	}
	require.Equal(t, legacyGolden(t), got,
		"the legacy ingest gate (the default) no longer makes the decisions, or emits the counts, it made on main")
	// The transcript has to have exercised what it claims to: every kind of line, at least once.
	for _, kind := range []string{"  shed ", "  reading-shed ", "  tenant-gone ", "  unresolved stage=live",
		"redelivery=true", "sentAt=zero", "origin=1", "acks=1 published=", "feedback accepted", "feedback refused"} {
		require.Containsf(t, got, kind, "the sequence no longer exercises %q", kind)
	}
}

// legacyGolden is the recorded transcript.
func legacyGolden(t *testing.T) string {
	t.Helper()
	want, err := os.ReadFile(filepath.FromSlash(legacyTranscriptGolden))
	require.NoError(t, err)
	return strings.ReplaceAll(string(want), "\r\n", "\n")
}

// The negative control: the comparison fails when the legacy routing decision moves. Here every message
// is metered at now (the age cutoff that sends an old message to the backlog limiter is skipped), which is
// the kind of change a mis-wired branch in handle() would make.
func TestTheLegacyTranscriptSeesAChangedDecision(t *testing.T) {
	got := strings.Join(legacyTranscript(t, func(es *GatewayJetStreamSource) {
		es.meter = func(string, time.Time) time.Time { return time.Time{} }
	}), "\n") + "\n"
	require.NotEqual(t, legacyGolden(t), got, "the transcript must see a changed routing decision")
}

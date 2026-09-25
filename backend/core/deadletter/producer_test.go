// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletter

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// testProducer builds a Producer the way a service does, on a Microservice with a registry,
// so its counter can be read back by its EXPORTED name rather than through a handle.
func testProducer(t *testing.T, area string) (*Producer, *prometheus.Registry) {
	t.Helper()
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: area}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	return NewProducer(ms), reg
}

// lostCount reads devicechain_<area>_dead_letter_lost_total off reg by its full exported
// name. It fails the test when the family is absent: absent and zero are different claims,
// and the alert over this counter can only tell them apart if the series exists.
func lostCount(t *testing.T, reg *prometheus.Registry, area string) float64 {
	t.Helper()
	name := "devicechain_" + strings.ReplaceAll(area, "-", "") + "_dead_letter_lost_total"
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gathering the registry: %v", err)
	}
	for _, f := range families {
		if f.GetName() == name {
			if len(f.GetMetric()) != 1 {
				t.Fatalf("%s has %d series, want exactly 1", name, len(f.GetMetric()))
			}
			return f.GetMetric()[0].GetCounter().GetValue()
		}
	}
	t.Fatalf("the registry exports no %s", name)
	return 0
}

// 🔑 THE SINK STAMPS THE SOURCE; THE CALLER CANNOT FORGET IT. A letter built with no
// Source is written naming the producing service — which is the defect this exists to
// end: a service whose constructor lost its copy of the area wrote every letter with an
// empty source, Validate refused them all, and each one was lost.
func TestTheSinkStampsTheSource(t *testing.T) {
	p, reg := testProducer(t, "command-delivery")
	w := &fakeWriter{}
	e := good()
	e.Source = ""
	if err := p.NewSink(w).Write(context.Background(), e); err != nil {
		t.Fatalf("a letter with no caller-set source was refused: %v", err)
	}
	if len(w.got) != 1 {
		t.Fatalf("the sink wrote %d messages, want 1", len(w.got))
	}
	back, err := Unmarshal(w.got[0].Value)
	if err != nil {
		t.Fatalf("the written letter does not read back: %v", err)
	}
	if back.Source != "command-delivery" {
		t.Fatalf("written source = %q, want %q", back.Source, "command-delivery")
	}
	if got := lostCount(t, reg, "command-delivery"); got != 0 {
		t.Fatalf("a written letter counted %v on dead_letter_lost_total, want 0", got)
	}
}

// And the Sink's value wins over a caller that set a DIFFERENT one: the source is the
// producing service's identity, not something a call site gets a say in.
func TestTheSinkSourceWinsOverTheCallers(t *testing.T) {
	p, _ := testProducer(t, "event-processing")
	w := &fakeWriter{}
	e := good()
	e.Source = "somebody-else"
	if err := p.NewSink(w).Write(context.Background(), e); err != nil {
		t.Fatalf("write refused: %v", err)
	}
	back, err := Unmarshal(w.got[0].Value)
	if err != nil {
		t.Fatalf("the written letter does not read back: %v", err)
	}
	if back.Source != "event-processing" {
		t.Fatalf("written source = %q, want the producer's %q", back.Source, "event-processing")
	}
}

// The counter exists at zero before anything is lost. A series that appears only on the
// first loss reads as ABSENT until then, and absent is not the same answer as "nothing
// lost" to anyone querying it.
func TestTheLostCounterIsExportedAtZero(t *testing.T) {
	_, reg := testProducer(t, "notification-management")
	if got := lostCount(t, reg, "notification-management"); got != 0 {
		t.Fatalf("a fresh producer's dead_letter_lost_total = %v, want 0", got)
	}
}

// 🔴 A REFUSAL IS A LOSS TOO. A letter the service refuses as malformed is never written,
// its source message has already been given up on, and so the work is gone exactly as if
// the broker had refused it. Counting only broker failures would make a producing
// service's own defect silent.
func TestARefusedLetterIsCountedAsLost(t *testing.T) {
	p, reg := testProducer(t, "device-management")
	w := &fakeWriter{}
	e := good()
	e.Kind = Kind("conector-dispatch")
	err := p.NewSink(w).Write(context.Background(), e)
	if err == nil {
		t.Fatal("an off-vocabulary letter was accepted")
	}
	if !strings.Contains(err.Error(), "LOST") {
		t.Fatalf("the refusal does not say the work is gone: %v", err)
	}
	if w.calls != 0 {
		t.Fatalf("the refused letter reached the writer %d times", w.calls)
	}
	if got := lostCount(t, reg, "device-management"); got != 1 {
		t.Fatalf("dead_letter_lost_total = %v after one refused letter, want 1", got)
	}
}

// Two sinks from one producer count on ONE counter. A service with two dead-letter arms
// builds both from the same producer; a second registration of the name would panic, and
// two differently-named counters are the per-service list this replaced.
func TestTwoSinksShareTheProducersCounter(t *testing.T) {
	p, reg := testProducer(t, "device-management")
	broken := &fakeWriter{failures: 99, err: errors.New("broker is away")}
	_ = p.NewSink(broken).Write(context.Background(), good())
	_ = p.NewSink(broken).Write(context.Background(), good())
	if got := lostCount(t, reg, "device-management"); got != 2 {
		t.Fatalf("dead_letter_lost_total = %v after one loss on each of two sinks, want 2", got)
	}
}

// Lost counts on the same series a Sink's loss does, for the producer whose give-up write
// cannot go through a Sink.
func TestLostCountsOnTheProducersCounter(t *testing.T) {
	p, reg := testProducer(t, "outbound-connectors")
	p.Lost()
	if got := lostCount(t, reg, "outbound-connectors"); got != 1 {
		t.Fatalf("dead_letter_lost_total = %v after Lost(), want 1", got)
	}
}

// 🔴 AN INDEX SINK'S LOSS IS ITS CALLER'S, NEVER THE PRODUCER'S. It carries a copy of
// something already durable elsewhere, so losing it loses a listing and not the work —
// counted on dead_letter_lost_total it would page at the severity of a real loss.
func TestAnIndexSinkLossCallsItsHookAndNotTheCounter(t *testing.T) {
	p, reg := testProducer(t, "outbound-connectors")
	hooked := 0
	sink := p.NewIndexSink(&fakeWriter{failures: 99, err: errors.New("broker is away")},
		func(error) { hooked++ })
	if err := sink.Write(context.Background(), good()); err == nil {
		t.Fatal("a write that never succeeded was reported as written")
	}
	if hooked != 1 {
		t.Fatalf("the index sink's hook fired %d times, want 1", hooked)
	}
	if got := lostCount(t, reg, "outbound-connectors"); got != 0 {
		t.Fatalf("an index loss moved dead_letter_lost_total to %v, want 0", got)
	}
}

// Each of these is a wiring mistake with no runtime condition to handle, so each is
// refused at the moment it is made rather than surfacing as letters that vanish.
func TestAProducerCannotBeMisbuilt(t *testing.T) {
	p, _ := testProducer(t, "outbound-connectors")
	var nilProducer *Producer
	for name, build := range map[string]func(){
		"nil microservice": func() { NewProducer(nil) },
		"blank area":       func() { NewProducer(&core.Microservice{FunctionalArea: "  "}) },
		"nil producer":     func() { nilProducer.NewSink(&fakeWriter{}) },
		"nil producer idx": func() { nilProducer.NewIndexSink(&fakeWriter{}, func(error) {}) },
		"nil producer lost": func() {
			nilProducer.Lost()
		},
		"nil writer":       func() { p.NewSink(nil) },
		"nil index writer": func() { p.NewIndexSink(nil, func(error) {}) },
		"nil index hook":   func() { p.NewIndexSink(&fakeWriter{}, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s was accepted", name)
				}
			}()
			build()
		})
	}
}

// 🔑 THE EXPORTED NAME MUST MATCH THE ALERT'S SELECTOR, FOR EVERY SHIPPED AREA.
//
// The DeadLetterWriteLost alert in deploy/helm/devicechain/templates/prometheusrule-dead-letter.yaml
// selects every adopter of this package by __name__ regex and lists none of them — so the
// composed name is now the ONLY thing joining a loss to the alert. Every other test reads
// the counter by the name this package is expected to produce; this one checks that name
// against a copy of the selector, for every area, so an area whose name composes badly
// (or a change to how the subsystem is built) fails here instead of silently falling out
// of a critical alert.
//
// 🔴 THE SELECTOR IS `[a-z0-9]+`, NOT `.+`, and this test pins the stricter form. `.+`
// would also match the per-path names device-management used to export, and during an
// upgrade old pods still export those beside the new one: after rate() drops the name,
// two series from one pod share a labelset and the rule's evaluation ERRORS, silencing the
// alert for every service. The regex is restated rather than parsed out of the chart,
// because this module cannot see deploy/.
func TestTheLostCounterNameMatchesTheAlertSelector(t *testing.T) {
	selector := regexp.MustCompile(`^devicechain_[a-z0-9]+_dead_letter_lost_total$`)
	for _, area := range []string{
		"device-management", "user-management", "event-processing", "event-sources",
		"event-management", "device-state", "command-delivery", "dashboard-management",
		"notification-management", "outbound-connectors", "ai-inference", "mcp",
		"lwm2m-ingest", "sparkplug-ingest",
	} {
		ms := &core.Microservice{InstanceId: "test", FunctionalArea: area}
		reg := prometheus.NewRegistry()
		ms.UseMetricsRegistry(reg)
		NewProducer(ms)

		families, err := reg.Gather()
		if err != nil {
			t.Fatalf("gathering metrics for %q: %v", area, err)
		}
		// The prefix filter is what makes a family evidence about THIS area: matching on
		// the suffix alone would accept a name built from the wrong one.
		prefix := "devicechain_" + strings.ReplaceAll(area, "-", "") + "_"
		var name string
		for _, f := range families {
			if strings.HasPrefix(f.GetName(), prefix) && strings.HasSuffix(f.GetName(), "_dead_letter_lost_total") {
				name = f.GetName()
			}
		}
		if name == "" {
			t.Errorf("area %q exported no dead_letter_lost_total", area)
			continue
		}
		if !selector.MatchString(name) {
			t.Errorf("area %q exports %q, which the DeadLetterWriteLost selector %q does NOT "+
				"match: this service's lost letters would alert nobody", area, name, selector)
		}
	}
}

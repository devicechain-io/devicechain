// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package ruletest is the scenario and test-file model behind `dcctl test`: a declarative
// description of the events a device fleet would send, and of what a tenant's rules are expected to
// do about them.
//
// A scenario (scenario.yaml) says WHAT HAPPENS: devices, the readings each reports (a constant, a
// bounded random walk, a step schedule, or a recorded series), and the faults the delivery suffers
// (a gap, a duplicate, a reorder, an offline window). A test file (*.test.yaml) says WHAT SHOULD
// FOLLOW: which alarms are raised or cleared within what time, which never are, and which REACT
// intent (a command, a webhook, a published message) is emitted.
//
// This package is pure. It generates events and judges results; it neither calls a server nor reads
// a clock. Given the same scenario files it generates the same events on any machine, because
// every random choice comes from a generator seeded by the scenario's seed and the identity of the
// thing being generated. That is what lets a scenario be a test: a failure is reproducible, and a
// fix is proven by the same run passing.
//
// 🔴 THE FORMAT STARTS IN THE PREVIEW TIER. Every file carries an apiVersion, and this one is
// v1alpha1: it may change incompatibly in a minor release, with the change in the release notes,
// until it is promoted. A file whose apiVersion this build does not know is refused, not guessed at,
// and so is a key it does not know: a misspelled expectation that was ignored would pass.
package ruletest

import (
	"bytes"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"math/rand/v2"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// APIVersion is the version of the scenario and test-file formats in this build.
const APIVersion = "devicechain.io/ruletest/v1alpha1"

// Kinds of file.
const (
	KindScenario = "Scenario"
	KindTest     = "RuleTest"
)

// Rule sets a scenario can run against.
const (
	RuleSetPublished = "PUBLISHED"
	RuleSetDraft     = "DRAFT"
)

// MaxEvents bounds the events one scenario may generate. It matches the most the server accepts in
// one evaluation, so a scenario that is too large is refused here, before it is generated and sent,
// with a message that names the knobs that made it large.
const MaxEvents = 250_000

// Duration is a time.Duration written the way a person writes one: Go's units plus d (24h) and w
// (7d), so "7d" and "90m" and "1d12h" all read. It is signed so a window can be measured
// backwards where that is meaningful, but a scenario rejects a negative interval.
type Duration time.Duration

// D returns the duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

var dayWeek = regexp.MustCompile(`^(-?)(?:(\d+)w)?(?:(\d+)d)?(.*)$`)

// ParseDuration reads a duration with the d and w units added.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty duration")
	}
	m := dayWeek.FindStringSubmatch(s)
	var total time.Duration
	if m[2] != "" {
		w, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil || w > 5200 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		total += time.Duration(w) * 7 * 24 * time.Hour
	}
	if m[3] != "" {
		d, err := strconv.ParseInt(m[3], 10, 64)
		if err != nil || d > 36500 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		total += time.Duration(d) * 24 * time.Hour
	}
	if rest := m[4]; rest != "" {
		r, err := time.ParseDuration(rest)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q (units are ns, us, ms, s, m, h, d, w)", s)
		}
		total += r
	} else if m[2] == "" && m[3] == "" {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	if m[1] == "-" {
		total = -total
	}
	return total, nil
}

// UnmarshalYAML reads "90m", "7d" and the like. A bare number is refused: seconds or
// milliseconds would be a guess.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return errors.New("a duration is a string like \"90m\" or \"7d\"")
	}
	v, err := ParseDuration(n.Value)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// Scenario is scenario.yaml.
type Scenario struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	// Profile is the token of the device profile whose rules are evaluated.
	Profile string `yaml:"profile"`
	// RuleSet is PUBLISHED (the rules live now; the default) or DRAFT (the rules named by Draft).
	RuleSet string `yaml:"ruleSet"`
	// Draft names the draft rules when RuleSet is DRAFT: exactly one of a rule definition (the
	// JSON a rule is stored as) or a canvas graph, as a path relative to the scenario file.
	Draft *Draft `yaml:"draft"`
	// Start is the instant the scenario begins, in event time. It is required: a default of "now"
	// would make every run different.
	Start time.Time `yaml:"start"`
	// Duration is how long the scenario runs. The engine's clock is moved to Start+Duration after
	// the last event, which is what lets a rule that waits for silence decide.
	Duration Duration `yaml:"duration"`
	// Seed seeds every random choice. The same seed always generates the same events.
	Seed    int64        `yaml:"seed"`
	Devices []DeviceSpec `yaml:"devices"`
	Faults  []Fault      `yaml:"faults"`
}

// Draft names draft rules.
type Draft struct {
	RuleDefinition string `yaml:"ruleDefinition"`
	Graph          string `yaml:"graph"`
}

// DeviceSpec is one simulated device and what it reports.
type DeviceSpec struct {
	Token   string   `yaml:"token"`
	Streams []Stream `yaml:"streams"`
}

// Stream is one metric's series on a device. Exactly one behaviour is named.
type Stream struct {
	// Metric is the metric key reported. A playback stream may leave it empty when its file
	// names the metric on each row.
	Metric string `yaml:"metric"`
	// Every is the interval between samples; required for every behaviour but playback.
	Every Duration `yaml:"every"`
	// From and Until bound the stream as offsets from the scenario start. From defaults to 0 and
	// Until to the scenario's duration. A stream that stops before the end of the scenario is how
	// a device "goes quiet".
	From  Duration  `yaml:"from"`
	Until *Duration `yaml:"until"`

	Constant   *float64    `yaml:"constant"`
	RandomWalk *RandomWalk `yaml:"randomWalk"`
	Steps      []Step      `yaml:"steps"`
	Playback   *Playback   `yaml:"playback"`
}

// RandomWalk is a bounded random walk: each sample moves by a uniform step in [-Step, +Step] from
// the last and is clamped to [Min, Max].
type RandomWalk struct {
	Start float64 `yaml:"start"`
	Step  float64 `yaml:"step"`
	Min   float64 `yaml:"min"`
	Max   float64 `yaml:"max"`
}

// Step is one point of a step schedule: from At (an offset from the scenario start) the value
// holds, until the next step.
type Step struct {
	At    Duration `yaml:"at"`
	Value float64  `yaml:"value"`
}

// Playback replays recorded readings from a CSV or NDJSON file, named relative to the scenario file.
// A row has an at (an RFC 3339 instant, or an offset from the scenario start like "90m") and a
// value, and may name the device and metric; a row that does not takes the stream's. CSV needs a
// header row.
type Playback struct {
	File string `yaml:"file"`
}

// Fault is one thing that goes wrong on the way from device to engine. Exactly one is named. A fault
// that names no device applies to every device.
type Fault struct {
	Gap       *GapFault       `yaml:"gap"`
	Offline   *OfflineFault   `yaml:"offline"`
	Duplicate *DuplicateFault `yaml:"duplicate"`
	Reorder   *ReorderFault   `yaml:"reorder"`
}

// GapFault drops the samples that fall in [From, From+For): they are lost for good.
type GapFault struct {
	Device string   `yaml:"device"`
	Metric string   `yaml:"metric"`
	From   Duration `yaml:"from"`
	For    Duration `yaml:"for"`
}

// OfflineFault models a device that keeps sampling while disconnected and uploads what it stored when
// it reconnects: the samples that fall in [From, From+For) keep their own times but are delivered
// together at the end of the window, behind readings that occurred after them. It is the
// store-and-forward case, and what separates it from a gap is that the data arrives, late.
type OfflineFault struct {
	Device string   `yaml:"device"`
	From   Duration `yaml:"from"`
	For    Duration `yaml:"for"`
}

// DuplicateFault delivers an event a second time, with the given probability.
type DuplicateFault struct {
	Device      string  `yaml:"device"`
	Metric      string  `yaml:"metric"`
	Probability float64 `yaml:"probability"`
}

// ReorderFault delays the delivery of an event by a random time up to MaxDelay, with the given
// probability, so it arrives behind events that occurred after it.
type ReorderFault struct {
	Device      string   `yaml:"device"`
	Probability float64  `yaml:"probability"`
	MaxDelay    Duration `yaml:"maxDelay"`
}

// Metric is one reading of a generated event.
type Metric struct {
	Name  string
	Value float64
}

// Event is one generated event, in the order it is DELIVERED.
type Event struct {
	Device  string
	At      time.Time
	Metrics []Metric
}

// Generated is what a scenario produces.
type Generated struct {
	Events []Event
	// AdvanceTo is the instant the engine's clock is moved to after the last event.
	AdvanceTo time.Time
}

// FileReader reads a file named relative to the scenario file. It returns the bytes of a file that
// does not escape the scenario's directory, and refuses any other path.
type FileReader func(rel string) ([]byte, error)

// DecodeStrict decodes YAML into out, refusing a key out does not declare and a document with more
// than one YAML document in it.
func DecodeStrict(data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("the file is empty")
		}
		return err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return errors.New("the file holds more than one YAML document")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// ParseScenario reads and validates scenario.yaml.
func ParseScenario(data []byte) (*Scenario, error) {
	var s Scenario
	if err := DecodeStrict(data, &s); err != nil {
		return nil, err
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

func checkHeader(apiVersion, kind, want string) error {
	if apiVersion != APIVersion {
		if apiVersion == "" {
			return fmt.Errorf("apiVersion is required (this build reads %s)", APIVersion)
		}
		return fmt.Errorf("apiVersion %q is not one this build reads (it reads %s)", apiVersion, APIVersion)
	}
	if kind != want {
		return fmt.Errorf("kind must be %s, not %q", want, kind)
	}
	return nil
}

// Validate checks everything that can be checked without reading a playback file.
func (s *Scenario) Validate() error {
	if err := checkHeader(s.APIVersion, s.Kind, KindScenario); err != nil {
		return err
	}
	if strings.TrimSpace(s.Profile) == "" {
		return errors.New("profile is required: the token of the device profile whose rules are evaluated")
	}
	switch s.RuleSet {
	case "":
		s.RuleSet = RuleSetPublished
	case RuleSetPublished, RuleSetDraft:
	default:
		return fmt.Errorf("ruleSet must be %s or %s, not %q", RuleSetPublished, RuleSetDraft, s.RuleSet)
	}
	switch {
	case s.RuleSet == RuleSetDraft && s.Draft == nil:
		return errors.New("ruleSet DRAFT needs a draft: naming a ruleDefinition or a graph file")
	case s.RuleSet == RuleSetPublished && s.Draft != nil:
		return errors.New("draft is set but ruleSet is PUBLISHED; set ruleSet: DRAFT to evaluate the draft")
	case s.Draft != nil && (s.Draft.RuleDefinition == "") == (s.Draft.Graph == ""):
		return errors.New("draft must name exactly one of ruleDefinition and graph")
	}
	if s.Start.IsZero() {
		return errors.New("start is required, as an RFC 3339 instant: a scenario has no wall clock to default to")
	}
	if s.Duration <= 0 {
		return errors.New("duration is required and must be positive, like 7d")
	}
	if len(s.Devices) == 0 {
		return errors.New("devices is required: a scenario with no device reports nothing")
	}
	seen := map[string]bool{}
	for i, d := range s.Devices {
		if strings.TrimSpace(d.Token) == "" {
			return fmt.Errorf("devices[%d]: token is required", i)
		}
		if seen[d.Token] {
			return fmt.Errorf("devices[%d]: device %q is listed twice", i, d.Token)
		}
		seen[d.Token] = true
		if len(d.Streams) == 0 {
			return fmt.Errorf("devices[%d]: device %q has no streams, so it reports nothing; give it a stream or remove it", i, d.Token)
		}
		for j, st := range d.Streams {
			if err := st.validate(s.Duration.D()); err != nil {
				return fmt.Errorf("devices[%d] (%s) streams[%d]: %w", i, d.Token, j, err)
			}
		}
	}
	for i, f := range s.Faults {
		if err := f.validate(seen); err != nil {
			return fmt.Errorf("faults[%d]: %w", i, err)
		}
	}
	return nil
}

func (st Stream) behaviours() []string {
	var b []string
	if st.Constant != nil {
		b = append(b, "constant")
	}
	if st.RandomWalk != nil {
		b = append(b, "randomWalk")
	}
	if len(st.Steps) > 0 {
		b = append(b, "steps")
	}
	if st.Playback != nil {
		b = append(b, "playback")
	}
	return b
}

func (st Stream) validate(total time.Duration) error {
	b := st.behaviours()
	if len(b) != 1 {
		return fmt.Errorf("name exactly one behaviour of constant, randomWalk, steps and playback (found %d)", len(b))
	}
	if st.Playback == nil {
		if st.Metric == "" {
			return errors.New("metric is required")
		}
		if st.Every <= 0 {
			return errors.New("every is required and must be positive, like 1m")
		}
	} else if st.Playback.File == "" {
		return errors.New("playback needs a file")
	}
	if st.From < 0 || (st.Until != nil && *st.Until <= st.From) {
		return errors.New("from must not be negative and until must be after from")
	}
	if st.From.D() >= total {
		return fmt.Errorf("from must be before the scenario's duration %s: a stream that starts at or after the end reports nothing", total)
	}
	if st.Until != nil && st.Until.D() > total {
		return fmt.Errorf("until %s is past the scenario's duration %s", st.Until.D(), total)
	}
	if rw := st.RandomWalk; rw != nil {
		if rw.Min > rw.Max {
			return errors.New("randomWalk: min is above max")
		}
		if rw.Start < rw.Min || rw.Start > rw.Max {
			return errors.New("randomWalk: start is outside [min, max]")
		}
		if rw.Step < 0 {
			return errors.New("randomWalk: step must not be negative")
		}
	}
	if len(st.Steps) > 0 {
		for i, s := range st.Steps {
			if s.At < 0 || (i > 0 && s.At <= st.Steps[i-1].At) {
				return errors.New("steps: at must not be negative and must increase")
			}
		}
		if st.Steps[0].At != 0 {
			return errors.New("steps: the first step must be at 0s, so the series has a value from its start")
		}
	}
	if c := st.Constant; c != nil && (math.IsNaN(*c) || math.IsInf(*c, 0)) {
		return errors.New("constant is not a finite number")
	}
	return nil
}

func (f Fault) validate(devices map[string]bool) error {
	n := 0
	var device string
	if f.Gap != nil {
		n++
		device = f.Gap.Device
		if f.Gap.For <= 0 || f.Gap.From < 0 {
			return errors.New("gap: from must not be negative and for must be positive")
		}
	}
	if f.Offline != nil {
		n++
		device = f.Offline.Device
		if f.Offline.For <= 0 || f.Offline.From < 0 {
			return errors.New("offline: from must not be negative and for must be positive")
		}
	}
	if f.Duplicate != nil {
		n++
		device = f.Duplicate.Device
		if p := f.Duplicate.Probability; !(p > 0 && p <= 1) {
			return errors.New("duplicate: probability must be in (0, 1]")
		}
	}
	if f.Reorder != nil {
		n++
		device = f.Reorder.Device
		if p := f.Reorder.Probability; !(p > 0 && p <= 1) {
			return errors.New("reorder: probability must be in (0, 1]")
		}
		if f.Reorder.MaxDelay <= 0 {
			return errors.New("reorder: maxDelay is required and must be positive")
		}
	}
	if n != 1 {
		return fmt.Errorf("name exactly one of gap, offline, duplicate and reorder (found %d)", n)
	}
	if device != "" && !devices[device] {
		return fmt.Errorf("device %q is not in the scenario", device)
	}
	return nil
}

// sample is one generated reading before delivery.
type sample struct {
	device  string
	metric  string
	at      time.Time
	value   float64
	order   int // generation order, the final tie-break
	deliver time.Time
}

// Generate produces the scenario's events in delivery order. read resolves playback files.
func (s *Scenario) Generate(read FileReader) (*Generated, error) {
	end := s.Start.Add(s.Duration.D())
	var samples []sample
	for _, d := range s.Devices {
		for si, st := range d.Streams {
			got, err := st.generate(s, d.Token, si, read)
			if err != nil {
				return nil, fmt.Errorf("device %s stream %d: %w", d.Token, si, err)
			}
			for _, g := range got {
				if len(samples) >= MaxEvents*4 {
					return nil, fmt.Errorf("the scenario generates more than %d readings; lengthen `every` or shorten `duration`", MaxEvents*4)
				}
				g.order = len(samples)
				g.deliver = g.at
				samples = append(samples, g)
			}
		}
	}
	samples = s.applySampleFaults(samples)

	sort.SliceStable(samples, func(i, j int) bool {
		a, b := samples[i], samples[j]
		if !a.deliver.Equal(b.deliver) {
			return a.deliver.Before(b.deliver)
		}
		if !a.at.Equal(b.at) {
			return a.at.Before(b.at)
		}
		return a.order < b.order
	})

	// Readings of one device at one instant, delivered together, are one event: that is what a
	// device that reports several metrics in a sample sends.
	type key struct {
		device  string
		at      int64
		deliver int64
	}
	var events []Event
	delivered := []time.Time{}
	index := map[key]int{}
	for _, sm := range samples {
		k := key{sm.device, sm.at.UnixNano(), sm.deliver.UnixNano()}
		if i, ok := index[k]; ok {
			events[i].Metrics = append(events[i].Metrics, Metric{sm.metric, sm.value})
			continue
		}
		index[k] = len(events)
		events = append(events, Event{Device: sm.device, At: sm.at, Metrics: []Metric{{sm.metric, sm.value}}})
		delivered = append(delivered, sm.deliver)
	}
	events, delivered = s.applyEventFaults(events, delivered)
	idx := make([]int, len(events))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool { return delivered[idx[i]].Before(delivered[idx[j]]) })
	out := make([]Event, len(events))
	for i, k := range idx {
		out[i] = events[k]
	}
	if len(out) == 0 {
		return nil, errors.New("the scenario generates no events (its faults remove every reading): a run over no events can never fail")
	}
	if len(out) > MaxEvents {
		return nil, fmt.Errorf("the scenario generates %d events, above the maximum of %d; lengthen `every` or shorten `duration`", len(out), MaxEvents)
	}
	return &Generated{Events: out, AdvanceTo: end}, nil
}

// rngFor returns a generator whose stream depends only on the seed and a label, so adding a device
// or a fault never changes what another one generates.
func rngFor(seed int64, parts ...any) *rand.Rand {
	h := fnv.New64a()
	for _, p := range parts {
		fmt.Fprintf(h, "%v\x00", p)
	}
	return rand.New(rand.NewPCG(uint64(seed), h.Sum64()))
}

func (st Stream) generate(s *Scenario, device string, idx int, read FileReader) ([]sample, error) {
	total := s.Duration.D()
	from := st.From.D()
	until := total
	if st.Until != nil {
		until = st.Until.D()
	}
	// Refuse an oversized series before generating it, not after: a stream of `every: 1ns` over
	// hours would otherwise allocate every sample before the scenario's limit could refuse it.
	if st.Playback == nil && st.Every > 0 {
		if n := int64((until-from)/st.Every.D()) + 1; n > MaxEvents*4 {
			return nil, fmt.Errorf("the stream generates %d readings, more than %d; lengthen `every` or shorten `duration`", n, MaxEvents*4)
		}
	}
	var out []sample
	add := func(off time.Duration, v float64) {
		out = append(out, sample{device: device, metric: st.Metric, at: s.Start.Add(off), value: v})
	}
	switch {
	case st.Playback != nil:
		// A recorded series honours the stream's bounds like a generated one, so `until` makes a
		// played-back device go quiet: the rows outside [from, until] are not sent.
		rows, err := st.playback(s, device, read)
		if err != nil {
			return nil, err
		}
		lo, hi := s.Start.Add(from), s.Start.Add(until)
		for _, r := range rows {
			if !r.at.Before(lo) && !r.at.After(hi) {
				out = append(out, r)
			}
		}
		return out, nil
	case st.Constant != nil:
		for off := from; off <= until; off += st.Every.D() {
			add(off, *st.Constant)
		}
	case st.RandomWalk != nil:
		rw := st.RandomWalk
		rng := rngFor(s.Seed, "walk", device, st.Metric, idx)
		v := rw.Start
		for off := from; off <= until; off += st.Every.D() {
			add(off, v)
			v += (rng.Float64()*2 - 1) * rw.Step
			v = math.Min(rw.Max, math.Max(rw.Min, v))
		}
	case len(st.Steps) > 0:
		for off := from; off <= until; off += st.Every.D() {
			v := st.Steps[0].Value
			for _, step := range st.Steps {
				if step.At.D() <= off {
					v = step.Value
				}
			}
			add(off, v)
		}
	}
	return out, nil
}

func (s *Scenario) applySampleFaults(samples []sample) []sample {
	for _, f := range s.Faults {
		switch {
		case f.Gap != nil:
			g := f.Gap
			lo, hi := s.Start.Add(g.From.D()), s.Start.Add(g.From.D()+g.For.D())
			kept := samples[:0]
			for _, sm := range samples {
				if (g.Device == "" || g.Device == sm.device) && (g.Metric == "" || g.Metric == sm.metric) &&
					!sm.at.Before(lo) && sm.at.Before(hi) {
					continue
				}
				kept = append(kept, sm)
			}
			samples = kept
		case f.Offline != nil:
			o := f.Offline
			lo, hi := s.Start.Add(o.From.D()), s.Start.Add(o.From.D()+o.For.D())
			for i := range samples {
				sm := &samples[i]
				if (o.Device == "" || o.Device == sm.device) && !sm.at.Before(lo) && sm.at.Before(hi) {
					sm.deliver = hi
				}
			}
		}
	}
	return samples
}

func (s *Scenario) applyEventFaults(events []Event, delivered []time.Time) ([]Event, []time.Time) {
	for fi, f := range s.Faults {
		switch {
		case f.Reorder != nil:
			r := f.Reorder
			rng := rngFor(s.Seed, "reorder", fi)
			for i := range events {
				if r.Device != "" && r.Device != events[i].Device {
					continue
				}
				// Always draw both numbers so the stream of draws does not depend on which events
				// happened to be chosen.
				pick, frac := rng.Float64(), rng.Float64()
				if pick < r.Probability {
					delivered[i] = delivered[i].Add(time.Duration(frac * float64(r.MaxDelay.D())))
				}
			}
		case f.Duplicate != nil:
			d := f.Duplicate
			rng := rngFor(s.Seed, "duplicate", fi)
			n := len(events)
			for i := 0; i < n; i++ {
				if d.Device != "" && d.Device != events[i].Device {
					continue
				}
				if d.Metric != "" && !hasMetric(events[i], d.Metric) {
					continue
				}
				if rng.Float64() < d.Probability {
					events = append(events, events[i])
					delivered = append(delivered, delivered[i])
				}
			}
		}
	}
	return events, delivered
}

func hasMetric(e Event, name string) bool {
	for _, m := range e.Metrics {
		if m.Name == name {
			return true
		}
	}
	return false
}

// SafeJoin resolves rel against dir and refuses a path that leaves it.
func SafeJoin(dir, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("empty path")
	}
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "\\") {
		return "", fmt.Errorf("%q is absolute; name files relative to the file that refers to them", rel)
	}
	joined := filepath.Join(dir, filepath.FromSlash(rel))
	r, err := filepath.Rel(dir, joined)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q leaves the directory of the file that refers to it", rel)
	}
	return joined, nil
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// This is the multi-harness driver: what it takes to offer more load than one loadgen pod can,
// by running one harness per tenant and treating the set as ONE measurement.
//
// One harness offers one tenant's fleet from one process, and a single pod tops out far below
// the 100k a demo wants (TLS sessions, source ports, client CPU). The fleet is therefore N
// harnesses, each with its own tenant and its own identity ledger, on as many pods as it takes.
// Three things make N harnesses one measurement, and they are all here:
//
//   - ONE START. --start-at holds every harness's drive until the same instant, after it has
//     provisioned (and, for MQTT, connected its fleet), so the steady windows coincide. Without
//     it the harnesses ramp whenever each happens to finish provisioning and the "steady" part
//     of the run is a different second for each of them.
//   - ONE WINDOW CHECK. The merge does not trust that the start was shared; it measures it. The
//     report states the start spread and the window every harness was driving at once, and the
//     merged run FAILS the windows-aligned invariant when they do not overlap enough to call a
//     summed rate a rate. A misaligned run is not wrong data, it is data that cannot be added.
//   - ONE VERDICT. The merged run passes only when every harness passed, every tenant is
//     distinct (two harnesses on one tenant would each see the other's events as unexpected, and
//     a pass would then mean nothing), and, when told how many to expect, none is missing: a
//     harness that died before writing a report must not read as five good ones.

const (
	// MaxStartDelay bounds --start-at, so a typo that puts it a day ahead is refused rather
	// than waited out.
	MaxStartDelay = 6 * time.Hour
	// DefaultAlignTolerance is the largest start spread (first harness to last) a merged run
	// accepts as one window.
	DefaultAlignTolerance = 5 * time.Second
	// DefaultMinOverlap is the share of the mean hold that every harness must have been driving
	// at once.
	DefaultMinOverlap = 0.9

	// InvHarnessesPassed, InvTenantsDistinct, InvWindowsAligned and InvHarnessCount are the
	// merged run's invariants.
	InvHarnessesPassed = "harnesses-passed"
	InvTenantsDistinct = "tenants-distinct"
	InvWindowsAligned  = "windows-aligned"
	InvHarnessCount    = "harness-count"
)

// awaitStart blocks until t. A zero t, or one already past, returns at once: a harness that
// missed the barrier starts late and the merge reports the spread, instead of refusing to run.
func awaitStart(ctx context.Context, t time.Time) error {
	if t.IsZero() {
		return nil
	}
	d := time.Until(t)
	if d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// HarnessSummary is one harness's report, reduced to what merging needs.
type HarnessSummary struct {
	Source string   `json:"source"`
	Kind   string   `json:"kind"` // "http" or "mqtt"
	Tenant string   `json:"tenant"`
	Passed bool     `json:"passed"`
	Loss   bool     `json:"loss"`
	Failed []string `json:"failedInvariants,omitempty"`

	StartedAt   time.Time `json:"startedAt"`
	HoldSeconds float64   `json:"holdSeconds"`

	Devices        int     `json:"devices"`
	TargetRatePS   float64 `json:"targetRatePerSec"`
	AchievedRatePS float64 `json:"achievedRatePerSec"`
	// Accepted is what the platform promised to store (HTTP 2xx, or MQTT PUBACKs); Persisted
	// what the harness found stored.
	Accepted  int64 `json:"accepted"`
	Persisted int64 `json:"persisted"`
	Lost      int64 `json:"lost"`
}

// End is when the harness stopped driving.
func (h HarnessSummary) End() time.Time {
	return h.StartedAt.Add(time.Duration(h.HoldSeconds * float64(time.Second)))
}

func failedNames(invs []Invariant) []string {
	var out []string
	for _, i := range invs {
		if !i.Passed {
			out = append(out, i.Name)
		}
	}
	return out
}

// SummarizeReport reads one harness report (the JSON dc-loadtest or dc-loadtest-mqtt wrote).
func SummarizeReport(source string, data []byte) (HarnessSummary, error) {
	var probe struct {
		Transport *json.RawMessage `json:"transport"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return HarnessSummary{}, fmt.Errorf("%s: not a harness report: %w", source, err)
	}
	if probe.Transport != nil {
		var r MQTTReport
		if err := json.Unmarshal(data, &r); err != nil {
			return HarnessSummary{}, fmt.Errorf("%s: %w", source, err)
		}
		return summarizeMQTT(source, &r)
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return HarnessSummary{}, fmt.Errorf("%s: %w", source, err)
	}
	return summarizeHTTP(source, &r)
}

func summarizeHTTP(source string, r *Report) (HarnessSummary, error) {
	if r.Tenant == "" || r.StartedAt.IsZero() {
		return HarnessSummary{}, fmt.Errorf("%s: a load-test report with no tenant or start time", source)
	}
	h := HarnessSummary{
		Source: source, Kind: "http", Tenant: r.Tenant, Passed: r.Passed(), Failed: failedNames(r.Invariants),
		StartedAt: r.StartedAt, HoldSeconds: r.Drive.HoldSeconds,
		Devices: r.Drive.Devices, TargetRatePS: r.Drive.TargetRatePS, AchievedRatePS: r.Drive.AchievedRatePS,
		Accepted: r.Drive.Accepted, Persisted: r.PersistedSeen,
	}
	if r.Identity != nil {
		h.Lost = r.Identity.Missing
		h.Loss = r.Identity.Missing > 0
	}
	return h, nil
}

func summarizeMQTT(source string, r *MQTTReport) (HarnessSummary, error) {
	if r.Tenant == "" || r.StartedAt.IsZero() {
		return HarnessSummary{}, fmt.Errorf("%s: an mqtt load-test report with no tenant or start time", source)
	}
	return HarnessSummary{
		Source: source, Kind: "mqtt", Tenant: r.Tenant, Passed: r.Passed(), Loss: r.Loss, Failed: failedNames(r.Invariants),
		StartedAt: r.StartedAt, HoldSeconds: r.Drive.HoldSeconds,
		Devices: r.Drive.Devices, TargetRatePS: r.Drive.TargetRatePS, AchievedRatePS: r.Drive.AckedRatePS,
		Accepted: r.Storage.Promised, Persisted: r.Storage.Persisted, Lost: r.Storage.Lost,
	}, nil
}

// MergeOptions tune MergeReports.
type MergeOptions struct {
	// Expect, when positive, is how many harnesses the run launched; fewer reports fail.
	Expect int
	// AlignTolerance (0 = DefaultAlignTolerance) is the largest start spread accepted as one
	// window; MinOverlap (0 = DefaultMinOverlap) the share of the mean hold all harnesses must
	// have been driving at once.
	AlignTolerance time.Duration
	MinOverlap     float64
}

// MergedReport is N harness reports read as one run.
type MergedReport struct {
	Harnesses []HarnessSummary `json:"harnesses"`

	Tenants int `json:"tenants"`
	Devices int `json:"devices"`
	// TargetRatePS and AchievedRatePS are the SUMS of the harnesses' own rates; they are a rate
	// of the whole run only while windows-aligned holds.
	TargetRatePS   float64 `json:"targetRatePerSec"`
	AchievedRatePS float64 `json:"achievedRatePerSec"`
	Accepted       int64   `json:"accepted"`
	Persisted      int64   `json:"persisted"`
	Lost           int64   `json:"lost"`
	Loss           bool    `json:"loss"`

	// StartSpreadSeconds is the first harness's start to the last one's. CommonWindowSeconds is
	// how long every harness was driving at once (the intersection of the windows; 0 when they
	// never all overlapped), MeanHoldSeconds the harnesses' mean hold, and OverlapRatio the first
	// over the second.
	StartSpreadSeconds  float64   `json:"startSpreadSeconds"`
	CommonWindowSeconds float64   `json:"commonWindowSeconds"`
	MeanHoldSeconds     float64   `json:"meanHoldSeconds"`
	OverlapRatio        float64   `json:"overlapRatio"`
	CommonFrom          time.Time `json:"commonFrom"`
	CommonTo            time.Time `json:"commonTo"`

	Invariants []Invariant `json:"invariants"`
}

// MergeReports merges harness summaries into one verdict. An empty set is a failure, never a
// vacuous pass.
func MergeReports(hs []HarnessSummary, opt MergeOptions) *MergedReport {
	tol := opt.AlignTolerance
	if tol <= 0 {
		tol = DefaultAlignTolerance
	}
	minOverlap := opt.MinOverlap
	if minOverlap <= 0 {
		minOverlap = DefaultMinOverlap
	}
	m := &MergedReport{Harnesses: append([]HarnessSummary(nil), hs...)}
	sort.Slice(m.Harnesses, func(i, j int) bool {
		if m.Harnesses[i].Tenant != m.Harnesses[j].Tenant {
			return m.Harnesses[i].Tenant < m.Harnesses[j].Tenant
		}
		return m.Harnesses[i].Source < m.Harnesses[j].Source
	})
	add := func(name string, ok bool, format string, args ...any) {
		m.Invariants = append(m.Invariants, Invariant{Name: name, Passed: ok, Detail: fmt.Sprintf(format, args...)})
	}
	if len(hs) == 0 {
		add(InvHarnessesPassed, false, "no harness reports: nothing was measured")
		return m
	}

	seen := map[string][]string{}
	var firstStart, lastStart, firstEnd, lastEnd time.Time
	var holdSum float64
	var failing []string
	for i, h := range m.Harnesses {
		m.Devices += h.Devices
		m.TargetRatePS += h.TargetRatePS
		m.AchievedRatePS += h.AchievedRatePS
		m.Accepted += h.Accepted
		m.Persisted += h.Persisted
		m.Lost += h.Lost
		m.Loss = m.Loss || h.Loss
		holdSum += h.HoldSeconds
		seen[h.Tenant] = append(seen[h.Tenant], h.Source)
		if !h.Passed {
			failing = append(failing, fmt.Sprintf("%s (%s: %s)", h.Tenant, h.Source, strings.Join(h.Failed, ", ")))
		}
		s, e := h.StartedAt, h.End()
		if i == 0 || s.Before(firstStart) {
			firstStart = s
		}
		if i == 0 || s.After(lastStart) {
			lastStart = s
		}
		if i == 0 || e.Before(firstEnd) {
			firstEnd = e
		}
		if i == 0 || e.After(lastEnd) {
			lastEnd = e
		}
	}
	m.Tenants = len(seen)
	m.MeanHoldSeconds = holdSum / float64(len(hs))
	m.StartSpreadSeconds = lastStart.Sub(firstStart).Seconds()
	// The window everyone was driving in: the latest start to the earliest end.
	if firstEnd.After(lastStart) {
		m.CommonFrom, m.CommonTo = lastStart, firstEnd
		m.CommonWindowSeconds = firstEnd.Sub(lastStart).Seconds()
	}
	if m.MeanHoldSeconds > 0 {
		m.OverlapRatio = m.CommonWindowSeconds / m.MeanHoldSeconds
	}

	if len(failing) == 0 {
		add(InvHarnessesPassed, true, "all %d harnesses passed", len(hs))
	} else {
		add(InvHarnessesPassed, false, "%d of %d harnesses failed: %s", len(failing), len(hs), strings.Join(failing, "; "))
	}

	var dup []string
	for t, srcs := range seen {
		if len(srcs) > 1 {
			dup = append(dup, fmt.Sprintf("%s (%s)", t, strings.Join(srcs, ", ")))
		}
	}
	sort.Strings(dup)
	if len(dup) == 0 {
		add(InvTenantsDistinct, true, "%d harnesses on %d distinct tenants", len(hs), m.Tenants)
	} else {
		add(InvTenantsDistinct, false, "tenants driven by more than one harness: %s; each would count the other's events as unexpected", strings.Join(dup, "; "))
	}

	spread := time.Duration(m.StartSpreadSeconds * float64(time.Second))
	switch {
	case spread > tol:
		add(InvWindowsAligned, false, "the harnesses started %.1fs apart (tolerance %s): the summed rate is not a rate of one window", m.StartSpreadSeconds, tol)
	case m.OverlapRatio < minOverlap:
		add(InvWindowsAligned, false, "all harnesses were driving at once for %.0fs of a mean %.0fs hold (%.0f%%, need %.0f%%)",
			m.CommonWindowSeconds, m.MeanHoldSeconds, 100*m.OverlapRatio, 100*minOverlap)
	default:
		add(InvWindowsAligned, true, "start spread %.1fs, all harnesses driving together for %.0fs of a mean %.0fs hold (%.0f%%)",
			m.StartSpreadSeconds, m.CommonWindowSeconds, m.MeanHoldSeconds, 100*m.OverlapRatio)
	}

	if opt.Expect > 0 {
		if len(hs) == opt.Expect {
			add(InvHarnessCount, true, "%d of %d expected harness reports", len(hs), opt.Expect)
		} else {
			add(InvHarnessCount, false, "%d harness report(s) for %d launched: a harness that wrote no report is a failed one", len(hs), opt.Expect)
		}
	}
	return m
}

// Passed is true only when every merged invariant held and something was merged.
func (m *MergedReport) Passed() bool {
	if len(m.Harnesses) == 0 || len(m.Invariants) == 0 {
		return false
	}
	for _, i := range m.Invariants {
		if !i.Passed {
			return false
		}
	}
	return true
}

// ExitCode follows the MQTT command's: ExitLoss when any harness lost promised events, ExitFail
// for any other failed invariant, ExitPass otherwise.
func (m *MergedReport) ExitCode() int {
	switch {
	case m.Loss:
		return ExitLoss
	case !m.Passed():
		return ExitFail
	default:
		return ExitPass
	}
}

// JSON renders the merged report.
func (m *MergedReport) JSON() ([]byte, error) { return json.MarshalIndent(m, "", "  ") }

// Human renders the operator-readable summary.
func (m *MergedReport) Human() string {
	var b strings.Builder
	verdict := "FAIL"
	switch {
	case m.Loss:
		verdict = "LOSS"
	case m.Passed():
		verdict = "PASS"
	}
	fmt.Fprintf(&b, "load-test merged %s — %d harnesses, %d tenants, %d devices\n", verdict, len(m.Harnesses), m.Tenants, m.Devices)
	fmt.Fprintf(&b, "  rate: target %.0f ev/s, achieved %.0f ev/s (summed); accepted %d, persisted %d, lost %d\n",
		m.TargetRatePS, m.AchievedRatePS, m.Accepted, m.Persisted, m.Lost)
	fmt.Fprintf(&b, "  window: start spread %.1fs, common %.0fs of a mean %.0fs hold (%.0f%%)\n",
		m.StartSpreadSeconds, m.CommonWindowSeconds, m.MeanHoldSeconds, 100*m.OverlapRatio)
	for _, h := range m.Harnesses {
		mark := "ok"
		if !h.Passed {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "  [%-4s] %-24s %-4s %6d devices %9.0f ev/s accepted %d persisted %d lost %d\n",
			mark, h.Tenant, h.Kind, h.Devices, h.AchievedRatePS, h.Accepted, h.Persisted, h.Lost)
	}
	for _, i := range m.Invariants {
		mark := "FAIL"
		if i.Passed {
			mark = "ok"
		}
		fmt.Fprintf(&b, "  [%-4s] %s — %s\n", mark, i.Name, i.Detail)
	}
	return b.String()
}

// MergeFiles reads harness report files and merges them.
func MergeFiles(paths []string, opt MergeOptions) (*MergedReport, error) {
	var hs []HarnessSummary
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		h, err := SummarizeReport(p, data)
		if err != nil {
			return nil, err
		}
		hs = append(hs, h)
	}
	return MergeReports(hs, opt), nil
}

// Runner runs one harness and returns its JSON report, which is also what a pod-per-harness
// deployment would have written to a file; RunMany merges exactly those.
type Runner func(ctx context.Context, hs *sim.Handshake, startAt time.Time) ([]byte, error)

// HTTPRunner returns the Runner for dc-loadtest's HTTP mode: base is the profile every harness
// shares, and each gets its own steady marker and aggregate-ledger path (see SteadyMarker.For).
func HTTPRunner(base Profile) Runner {
	return func(ctx context.Context, hs *sim.Handshake, startAt time.Time) ([]byte, error) {
		p := base
		p.Seed = hs.Seed
		p.StartAt = startAt
		p.Steady = base.Steady.For(hs.Tenant)
		if base.AggregateLedger != "" {
			p.AggregateLedger = suffixPath(base.AggregateLedger, hs.Tenant)
		}
		rep, err := Run(ctx, hs, p)
		if err != nil {
			return nil, err
		}
		return rep.JSON()
	}
}

// RunMany runs one harness per handshake concurrently with one shared start instant (now +
// lead, which must exceed the slowest harness's provisioning, or the merge reports the late
// starters' spread), then merges their reports. A harness that fails to produce a report is
// returned as an error and counted against Expect, so it cannot read as a smaller, good run.
// reportDir, when non-empty, receives each harness's JSON report.
func RunMany(ctx context.Context, handshakes []*sim.Handshake, run Runner, lead time.Duration, reportDir string, opt MergeOptions) (*MergedReport, []error) {
	startAt := time.Now().Add(lead)
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		sums []HarnessSummary
		errs []error
	)
	for i, hs := range handshakes {
		wg.Add(1)
		go func(i int, hs *sim.Handshake) {
			defer wg.Done()
			data, err := run(ctx, hs, startAt)
			if err == nil && reportDir != "" {
				err = os.WriteFile(filepath.Join(reportDir, fmt.Sprintf("harness-%02d-%s.json", i, hs.Tenant)), append(data, '\n'), 0o644)
			}
			var sum HarnessSummary
			if err == nil {
				sum, err = SummarizeReport(fmt.Sprintf("harness-%02d(%s)", i, hs.Tenant), data)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("harness %d (tenant %s): %w", i, hs.Tenant, err))
				return
			}
			sums = append(sums, sum)
		}(i, hs)
	}
	wg.Wait()
	// Whatever the caller asked to expect, the number LAUNCHED is the number that must report: an
	// Expect below it would let a failed harness drop out of the count and the verdict.
	opt.Expect = len(handshakes)
	return MergeReports(sums, opt), errs
}

// For returns a copy of the marker aimed at its own file for tenant, so N harnesses in one
// process do not overwrite each other's; nil stays nil.
func (m *SteadyMarker) For(tenant string) *SteadyMarker {
	if m == nil || m.Path == "" {
		return m
	}
	return &SteadyMarker{Path: suffixPath(m.Path, tenant), Phase: m.Phase, Warmup: m.Warmup}
}

// suffixPath inserts "-<tag>" before the extension.
func suffixPath(path, tag string) string {
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + "-" + tag + ext
}

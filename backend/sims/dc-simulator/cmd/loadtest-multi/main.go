// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// dc-loadtest-multi is the multi-harness driver: it runs one dc-loadtest harness per tenant with
// a shared start instant, or merges the reports of harnesses that ran elsewhere, into ONE
// verdict with the windows checked for alignment.
//
//	dc-loadtest-multi run   --handshake a.json --handshake b.json ... [dc-loadtest flags]
//	dc-loadtest-multi merge [--expect N] report-a.json report-b.json ...
//
// run drives N tenants from this process (HTTP mode): every harness provisions, then waits for
// the same instant (now + --lead) and drives together. merge is for a fleet of loadgen pods,
// each running dc-loadtest or dc-loadtest-mqtt with the same --start-at; it reads their report
// files and fails the run if the start spread, the overlap, the tenant set or the harness count
// is wrong, as well as if any harness failed.
//
// Exit codes: 0 pass; 1 an invariant failed; 2 the run could not be carried out (a harness
// produced no report, bad flags); 3 a harness lost events it had promised to store.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-simulator/internal/stopsig"
	"github.com/devicechain-io/dc-simulator/loadtest"
	"github.com/devicechain-io/dc-simulator/sim"
)

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: dc-loadtest-multi run|merge ...")
		os.Exit(loadtest.ExitError)
	}
	switch os.Args[1] {
	case "merge":
		os.Exit(mergeCmd(os.Args[2:]))
	case "run":
		os.Exit(runCmd(os.Args[2:]))
	default:
		fmt.Fprintln(os.Stderr, "usage: dc-loadtest-multi run|merge ...")
		os.Exit(loadtest.ExitError)
	}
}

func mergeFlags(fs *flag.FlagSet) (expect *int, tol *time.Duration, overlap *float64, report *string) {
	expect = fs.Int("expect", 0, "how many harnesses were launched; fewer reports fail the run (0 = do not check)")
	tol = fs.Duration("align-tolerance", loadtest.DefaultAlignTolerance, "largest start spread (first harness to last) accepted as one window")
	overlap = fs.Float64("min-overlap", loadtest.DefaultMinOverlap, "share of the mean hold that every harness must have been driving at once")
	report = fs.String("report", "", "write the merged JSON report to this path")
	return
}

func emit(m *loadtest.MergedReport, path string) int {
	if path != "" {
		b, err := m.JSON()
		if err == nil {
			err = os.WriteFile(path, append(b, '\n'), 0o644)
		}
		if err != nil {
			log.Error().Err(err).Msg("write the merged report")
			return loadtest.ExitError
		}
	}
	os.Stderr.WriteString("\n" + m.Human() + "\n")
	return m.ExitCode()
}

func mergeCmd(args []string) int {
	fs := flag.NewFlagSet("merge", flag.ExitOnError)
	expect, tol, overlap, report := mergeFlags(fs)
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "merge: at least one report file is required")
		return loadtest.ExitError
	}
	m, err := loadtest.MergeFiles(fs.Args(), loadtest.MergeOptions{Expect: *expect, AlignTolerance: *tol, MinOverlap: *overlap})
	if err != nil {
		log.Error().Err(err).Msg("merge")
		return loadtest.ExitError
	}
	return emit(m, *report)
}

func runCmd(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var handshakes listFlag
	fs.Var(&handshakes, "handshake", "a handshake file written by 'dcctl sim create' (repeatable: one per tenant; positional arguments are taken too)")
	manifest := fs.String("manifest", "", "scenario id (default: the first handshake's manifestId, else devicepulse)")
	devices := fs.Int("devices", 0, "devices PER TENANT (0 keeps the scenario's own sizing)")
	emitInterval := fs.Duration("emit-interval", 0, "emit cadence per device (0 keeps the 5s demo cadence)")
	concurrency := fs.Int("concurrency", 0, "max emits in flight per tick, per harness (0 derives it from the device count)")
	hold := fs.Duration("hold", loadtest.DefaultHold, "how long every harness holds at load")
	minAccepted := fs.Int64("min-accepted", loadtest.DefaultMinAccepted, "floor of accepted events each harness must apply")
	quiesceTimeout := fs.Duration("quiesce-timeout", loadtest.DefaultQuiesceTimeout, "max time each harness waits for its persisted count to settle")
	quiesceSettle := fs.Duration("quiesce-settle", loadtest.DefaultQuiesceSettle, "how long each harness keeps watching its count after it reaches the target")
	stateTimeout := fs.Duration("state-timeout", loadtest.DefaultStateTimeout, "how long after the drive the live state may take to catch up (0 = do not check)")
	steadyMarker := fs.String("steady-marker", "", "steady-marker path; each harness writes <name>-<tenant>.<ext>")
	steadyWarmup := fs.Duration("steady-warmup", 5*time.Minute, "with --steady-marker, how far into the drive the steady state begins")
	steadyPhase := fs.String("steady-phase", "", "with --steady-marker, the phase label")
	aggregateLedger := fs.String("aggregate-ledger", "", "aggregate-ledger path; each harness writes <name>-<tenant>.<ext>")
	lead := fs.Duration("lead", 2*time.Minute, "how far ahead the shared start instant is set; it must exceed the slowest harness's provisioning, or the merge reports the late starters' spread")
	reportDir := fs.String("report-dir", "", "write each harness's JSON report here")
	expect, tol, overlap, report := mergeFlags(fs)
	_ = fs.Parse(args)
	handshakes = append(handshakes, fs.Args()...)
	if len(handshakes) == 0 {
		fmt.Fprintln(os.Stderr, "run: at least one --handshake is required")
		return loadtest.ExitError
	}
	var hs []*sim.Handshake
	for _, p := range handshakes {
		h, err := sim.LoadHandshake(p)
		if err != nil {
			log.Error().Err(err).Str("handshake", p).Msg("load handshake")
			return loadtest.ExitError
		}
		hs = append(hs, h)
	}
	manifestId := *manifest
	if manifestId == "" {
		manifestId = hs[0].ManifestId
	}
	if manifestId == "" {
		manifestId = "devicepulse"
	}
	var steady *loadtest.SteadyMarker
	if *steadyMarker != "" {
		steady = &loadtest.SteadyMarker{Path: *steadyMarker, Phase: *steadyPhase, Warmup: *steadyWarmup}
	}
	base := loadtest.Profile{
		Steady: steady, Manifest: manifestId, Devices: *devices, EmitInterval: *emitInterval, Concurrency: *concurrency,
		Hold: *hold, MinAccepted: *minAccepted, QuiesceTimeout: *quiesceTimeout, QuiesceSettle: *quiesceSettle,
		StateTimeout: *stateTimeout, AggregateLedger: *aggregateLedger,
	}
	if *reportDir != "" {
		if err := os.MkdirAll(*reportDir, 0o755); err != nil {
			log.Error().Err(err).Msg("report dir")
			return loadtest.ExitError
		}
	}
	ctx, cancel, stop := stopsig.Notify(context.Background())
	defer cancel()

	log.Info().Int("harnesses", len(hs)).Str("manifest", manifestId).Dur("hold", *hold).Dur("lead", *lead).Msg("multi load-test starting")
	m, errs := loadtest.RunMany(ctx, hs, loadtest.HTTPRunner(base), *lead, *reportDir,
		loadtest.MergeOptions{Expect: *expect, AlignTolerance: *tol, MinOverlap: *overlap})
	if code, ok := stop.ExitCode(); ok {
		log.Warn().Int("exit", code).Msg("multi load-test stopped by a signal: no verdict")
		return code
	}
	for _, err := range errs {
		log.Error().Err(err).Msg("harness produced no report")
	}
	code := emit(m, *report)
	if len(errs) > 0 && code != loadtest.ExitLoss {
		code = loadtest.ExitError
	}
	return code
}

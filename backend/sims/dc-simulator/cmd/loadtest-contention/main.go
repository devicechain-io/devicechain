// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// dc-loadtest-contention runs the ADR-064 L3 tiered contention profile — the
// acceptance test for ADR-063 preferential shedding. It drives TWO probe tenants (a
// GOLD tenant and a best-effort SHED tenant, created at different shed priorities with
// `dcctl sim create --shed-priority`) at the same load over the shared platform, then
// asserts the tenant-priority contract: under an operator-set contention floor, GOLD
// rides through with zero sheds and zero loss while the SHED probe sheds (429s) yet
// loses ONLY what it was told it lost — shedding drops, it must never corrupt.
//
// The floor is event-sources instance config the operator sets before the run (helm
// value contention.manualFloor + a rollout); the harness cannot read it, so state it
// with --expect-floor. Run it BOTH ways:
//   - --expect-floor N (N>=1) with the floor set: the positive test (gold rides, shed sheds);
//   - --expect-floor 0 with the floor at 0: the NEGATIVE CONTROL — neither tenant may
//     shed, proving the positive run's sheds were caused by the floor, not the base ceiling.
//
// It is an untrusted external client (device-plane HTTP ingress + tenant GraphQL only,
// no service token, no DB) exactly like dc-loadtest. Provision the two tenants first:
//
//	dcctl sim create l3-gold --shed-priority 90
//	dcctl sim create l3-shed --shed-priority 10
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-simulator/loadtest"
	"github.com/devicechain-io/dc-simulator/sim"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	goldPath := flag.String("gold-handshake", envOr("DC_LOADTEST_GOLD_HANDSHAKE", ""),
		"handshake JSON for the GOLD probe tenant (created with dcctl sim create --shed-priority 90)")
	shedPath := flag.String("shed-handshake", envOr("DC_LOADTEST_SHED_HANDSHAKE", ""),
		"handshake JSON for the best-effort SHED probe tenant (created with dcctl sim create --shed-priority 10)")
	expectFloor := flag.Int("expect-floor", envIntOr("DC_LOADTEST_EXPECT_FLOOR", 0),
		"the ADR-063 contention manual_floor the operator set on event-sources (0..3). 0 = negative control (no tenant may shed)")
	manifest := flag.String("manifest", envOr("DC_LOADTEST_MANIFEST", "devicepulse"),
		"scenario each probe tenant drives (devicepulse, buildingpulse)")
	devices := flag.Int("devices", envIntOr("DC_LOADTEST_DEVICES", 0),
		"devices per probe tenant (0 = default; sizes the drive rate)")
	emitInterval := flag.Duration("emit-interval", envDurationOr("DC_LOADTEST_EMIT_INTERVAL", 0),
		"per-device emit cadence (0 = default; with devices, sets the drive rate into the contention sweet spot)")
	hold := flag.Duration("hold", envDurationOr("DC_LOADTEST_HOLD", 0),
		"steady-state emit window (0 = default)")
	minAccepted := flag.Int64("min-accepted", int64(envIntOr("DC_LOADTEST_MIN_ACCEPTED", 0)),
		"per-tenant load floor for the verdict to count (0 = default)")
	quiescePoll := flag.Duration("quiesce-poll", envDurationOr("DC_LOADTEST_QUIESCE_POLL", 0),
		"oracle read-back cadence (0 = default)")
	quiesceTimeout := flag.Duration("quiesce-timeout", envDurationOr("DC_LOADTEST_QUIESCE_TIMEOUT", 0),
		"backstop for a persisted count that never reaches its target (0 = default)")
	reportPath := flag.String("report", envOr("DC_LOADTEST_REPORT", ""),
		"write the JSON contention report to this path (also printed to stderr)")
	flag.Parse()

	if *goldPath == "" || *shedPath == "" {
		log.Fatal().Msg("--gold-handshake and --shed-handshake are both required (two distinct tenants at different shed priorities)")
	}

	gold, err := sim.LoadHandshake(*goldPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", *goldPath).Msg("load gold handshake")
	}
	shed, err := sim.LoadHandshake(*shedPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", *shedPath).Msg("load shed handshake")
	}

	cfg := loadtest.ContentionConfig{
		Profile: loadtest.Profile{
			Manifest:       *manifest,
			Seed:           gold.Seed,
			Devices:        *devices,
			EmitInterval:   *emitInterval,
			Hold:           *hold,
			MinAccepted:    *minAccepted,
			QuiescePoll:    *quiescePoll,
			QuiesceTimeout: *quiesceTimeout,
		},
		ExpectFloor: *expectFloor,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Info().Str("gold", gold.Tenant).Str("shed", shed.Tenant).Int("expectFloor", *expectFloor).
		Msg("contention profile run starting")

	report, err := loadtest.RunContentionProbes(ctx, gold, shed, cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("contention run failed")
	}

	if *reportPath != "" {
		data, jerr := report.JSON()
		if jerr != nil {
			log.Fatal().Err(jerr).Msg("render report")
		}
		if werr := os.WriteFile(*reportPath, append(data, '\n'), 0o644); werr != nil {
			log.Fatal().Err(werr).Str("path", *reportPath).Msg("write report")
		}
		log.Info().Str("path", *reportPath).Msg("wrote JSON report")
	}

	os.Stderr.WriteString("\n" + report.Human() + "\n")

	if !report.Passed() {
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Fatal().Str(key, v).Msg("not an integer")
	}
	return n
}

func envDurationOr(key string, fallback time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Fatal().Str(key, v).Msg("not a duration (want e.g. 200ms, 5s)")
	}
	return d
}

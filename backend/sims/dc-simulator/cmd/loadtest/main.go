// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// dc-loadtest is the ADR-064 correctness-under-load harness runner. It reads the
// same handshake file `dcctl sim create` writes (the scoped identity + resolved
// endpoints), drives a scenario at a target load over the real device wire,
// waits for the pipeline to quiesce, reconciles persisted vs. accepted events
// against tenant-scoped platform truth, and exits non-zero if any correctness
// invariant failed — the hard pre-release-tag gate.
//
// Like dc-simulator, it is an untrusted external client: no service framework,
// no database, no service token, no admin access. It uses only the RealClient
// auth layer (dc-microservice/userclient) and the sim subsystem's own driver.
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

	handshakePath := flag.String("handshake", envOr("DC_SIM_HANDSHAKE", ""),
		"path to the handshake JSON file written by `dcctl sim create` (or set DC_SIM_HANDSHAKE)")
	manifest := flag.String("manifest", envOr("DC_LOADTEST_MANIFEST", ""),
		"scenario id to drive (default: the handshake's manifestId, else devicepulse)")
	devices := flag.Int("devices", envIntOr("DC_LOADTEST_DEVICES", 0),
		"override the scenario's device count (0 keeps its own sizing)")
	emitInterval := flag.Duration("emit-interval", envDurationOr("DC_LOADTEST_EMIT_INTERVAL", 0),
		"emit cadence per device, e.g. 200ms (0 keeps the 5s demo cadence)")
	concurrency := flag.Int("concurrency", envIntOr("DC_LOADTEST_CONCURRENCY", 0),
		"max emits in flight per tick (0 derives it from the device count)")
	hold := flag.Duration("hold", envDurationOr("DC_LOADTEST_HOLD", loadtest.DefaultHold),
		"how long to hold at load before reconciling")
	quiesceTimeout := flag.Duration("quiesce-timeout", envDurationOr("DC_LOADTEST_QUIESCE_TIMEOUT", loadtest.DefaultQuiesceTimeout),
		"max time to wait for the persisted count to settle after the drive stops")
	reportPath := flag.String("report", envOr("DC_LOADTEST_REPORT", ""),
		"write the JSON correctness report to this path (also printed to stderr)")
	flag.Parse()

	if *handshakePath == "" {
		log.Fatal().Msg("--handshake (or DC_SIM_HANDSHAKE) is required")
	}

	hs, err := sim.LoadHandshake(*handshakePath)
	if err != nil {
		log.Fatal().Err(err).Msg("load handshake")
	}

	// Manifest precedence: explicit flag, else the handshake's own manifestId,
	// else devicepulse (the pre-slice-2 default the persistent sim also assumes).
	manifestId := *manifest
	if manifestId == "" {
		manifestId = hs.ManifestId
	}
	if manifestId == "" {
		manifestId = "devicepulse"
	}

	profile := loadtest.Profile{
		Manifest:       manifestId,
		Seed:           hs.Seed,
		Devices:        *devices,
		EmitInterval:   *emitInterval,
		Concurrency:    *concurrency,
		Hold:           *hold,
		QuiesceTimeout: *quiesceTimeout,
	}

	// SIGINT/SIGTERM abort the run (drive returns an error, no verdict) rather
	// than producing a report against a torn stop boundary.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Info().Str("tenant", hs.Tenant).Str("manifest", manifestId).
		Int("devices", *devices).Dur("hold", *hold).Msg("load-test starting")

	report, err := loadtest.Run(ctx, hs, profile)
	if err != nil {
		log.Fatal().Err(err).Msg("load-test run failed")
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

	// The human summary goes to stderr so stdout stays clean for piping the JSON.
	os.Stderr.WriteString("\n" + report.Human() + "\n")

	if !report.Passed() {
		// The hard gate: a single failed invariant fails the run.
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// envIntOr / envDurationOr FAIL on a malformed value rather than silently
// falling back — a load test must run the size it was told, not a default it
// mistook for one (the same rule dc-simulator's main applies).
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

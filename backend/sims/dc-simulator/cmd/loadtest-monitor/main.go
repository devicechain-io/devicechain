// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// dc-loadtest-monitor drives a scenario while the ADR-064 live safety monitor
// watches a bounded probe cohort over the real measurementStream subscription. It
// asserts the safety-continuous invariants that need no planted fixtures —
// per-device occurred_time monotonicity and bounded-cohort membership/isolation —
// throughout the run, and exits non-zero if any is violated or the monitor was
// left blind. Like dc-loadtest it is an untrusted external client: no service
// framework, no database, no service token.
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
		"path to the handshake JSON written by `dcctl sim create` (or set DC_SIM_HANDSHAKE)")
	manifest := flag.String("manifest", envOr("DC_LOADTEST_MANIFEST", ""),
		"scenario id to drive (default: the handshake's manifestId, else devicepulse)")
	devices := flag.Int("devices", envIntOr("DC_LOADTEST_DEVICES", 0),
		"override the scenario's device count (0 keeps its own sizing)")
	emitInterval := flag.Duration("emit-interval", envDurationOr("DC_LOADTEST_EMIT_INTERVAL", 0),
		"emit cadence per device, e.g. 200ms (0 keeps the 5s demo cadence)")
	concurrency := flag.Int("concurrency", envIntOr("DC_LOADTEST_CONCURRENCY", 0),
		"max emits in flight per tick (0 derives it from the device count)")
	hold := flag.Duration("hold", envDurationOr("DC_LOADTEST_HOLD", loadtest.DefaultHold),
		"how long to hold at load while the monitor watches")
	cohort := flag.Int("cohort", envIntOr("DC_LOADTEST_COHORT", loadtest.DefaultMonitorCohort),
		"number of probe devices the safety monitor subscribes to (bounded cohort)")
	reportPath := flag.String("report", envOr("DC_LOADTEST_REPORT", ""),
		"write the JSON safety report to this path (also printed to stderr)")
	flag.Parse()

	if *handshakePath == "" {
		log.Fatal().Msg("--handshake (or DC_SIM_HANDSHAKE) is required")
	}

	hs, err := sim.LoadHandshake(*handshakePath)
	if err != nil {
		log.Fatal().Err(err).Msg("load handshake")
	}

	manifestId := *manifest
	if manifestId == "" {
		manifestId = hs.ManifestId
	}
	if manifestId == "" {
		manifestId = "devicepulse"
	}

	profile := loadtest.Profile{
		Manifest:     manifestId,
		Seed:         hs.Seed,
		Devices:      *devices,
		EmitInterval: *emitInterval,
		Concurrency:  *concurrency,
		Hold:         *hold,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Info().Str("tenant", hs.Tenant).Str("manifest", manifestId).
		Int("devices", *devices).Int("cohort", *cohort).Dur("hold", *hold).Msg("safety-monitor run starting")

	report, err := loadtest.RunMonitored(ctx, hs, profile, *cohort)
	if err != nil {
		log.Fatal().Err(err).Msg("safety-monitor run failed")
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

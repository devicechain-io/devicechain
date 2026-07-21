// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// dc-loadtest-selftest validates the ADR-064 load-test ORACLE itself — it closes
// the "check that cannot fail" on the harness. It drives a small known volume,
// confirms the oracle counts it exactly over the real tenant GraphQL, then deletes
// exactly one persisted row DIRECTLY in Postgres (out-of-band, beneath the API) and
// confirms the oracle's completeness verdict flips to a detected drop. It exits 0
// only when the oracle is SOUND (passed intact, failed perturbed).
//
// Unlike dc-loadtest, this binary is privileged: it holds a DB connection so it can
// perturb ground truth. That privilege is exactly why it is a separate tool — the
// load test proper never touches the database.
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
	"github.com/devicechain-io/dc-simulator/loadtest/pgperturb"
	"github.com/devicechain-io/dc-simulator/sim"
)

// defaultSelfTestMinAccepted is well below dc-loadtest's gate floor: the self-test
// validates the oracle, it does not certify load, so it only needs enough events
// for the reconcile invariants to be conclusive.
const defaultSelfTestMinAccepted = 50

// defaultDSN is the standard local kind coordinates (port-forward
// dc-timescaledb-single-0 in dc-system to 127.0.0.1:5432). Override for any other
// cluster.
const defaultDSN = "postgres://postgres:devicechain@127.0.0.1:5432/devicechain"

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
		"how long to hold at load before reconciling")
	minAccepted := flag.Int64("min-accepted", int64(envIntOr("DC_LOADTEST_MIN_ACCEPTED", defaultSelfTestMinAccepted)),
		"floor of accepted events for the reconcile to be conclusive (a self-test needs far less than the gate)")
	quiesceTimeout := flag.Duration("quiesce-timeout", envDurationOr("DC_LOADTEST_QUIESCE_TIMEOUT", loadtest.DefaultQuiesceTimeout),
		"max time to wait for the persisted count to reach the target")
	dsn := flag.String("db-dsn", envOr("DC_LOADTEST_DB_DSN", defaultDSN),
		"Postgres DSN for the event store (the out-of-band deletion path)")
	reportPath := flag.String("report", envOr("DC_LOADTEST_REPORT", ""),
		"write the JSON self-test report to this path (also printed to stderr)")
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
		Manifest:       manifestId,
		Seed:           hs.Seed,
		Devices:        *devices,
		EmitInterval:   *emitInterval,
		Concurrency:    *concurrency,
		Hold:           *hold,
		MinAccepted:    *minAccepted,
		QuiesceTimeout: *quiesceTimeout,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	perturber, err := pgperturb.New(ctx, *dsn, hs.Tenant)
	if err != nil {
		log.Fatal().Err(err).Msg("open event-store perturber")
	}
	defer func() {
		// Best-effort close on a fresh context: ctx may already be cancelled.
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		_ = perturber.Close(closeCtx)
	}()

	log.Info().Str("tenant", hs.Tenant).Str("manifest", manifestId).
		Int("devices", *devices).Dur("hold", *hold).Msg("oracle self-test starting")

	report, err := loadtest.SelfTest(ctx, hs, profile, perturber)
	if err != nil {
		log.Fatal().Err(err).Msg("self-test run failed")
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

	// Exit non-zero unless the oracle proved itself sound. An inconclusive run is
	// NOT a pass — nothing was proven — so it also exits non-zero.
	if !report.Sound() {
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

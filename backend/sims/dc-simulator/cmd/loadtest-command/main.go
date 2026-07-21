// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// dc-loadtest-command runs the ADR-064 L2d-3 planted command round-trip probes: it
// provisions a dedicated harness profile with a published command definition and a
// pinned threshold rule whose sendCommand action enqueues that command, a set of
// safety probes (emit strictly below the threshold — must never enqueue a command)
// and command probes (cross it exactly K times — must enqueue exactly K commands,
// each completing the full loop to durable SUCCESSFUL), plus a saturating background
// fleet on the same rule. It connects a real device-plane MQTT receiver for each
// probe (which answers the commands, closing the round-trip), drives the load, waits
// for the planted commands to settle, and exits non-zero if any invariant is
// violated. Like dc-loadtest it is an untrusted external client: no service
// framework, no database, no service token.
//
// It needs BOTH a device-plane ingress reach and a device-plane MQTT reach:
//   - the HTTP ingress (event-sources :8081) for telemetry, and the /api ingress for
//     GraphQL, exactly like dc-loadtest / dc-loadtest-detection;
//   - the NATS MQTT gateway (dc-nats:1883) for command receive/respond — port-forward
//     it and pass --mqtt-broker (default tcp://127.0.0.1:1883).
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
	mqttBroker := flag.String("mqtt-broker", envOr("DC_LOADTEST_MQTT_BROKER", ""),
		"NATS MQTT gateway address for the command receiver, e.g. tcp://127.0.0.1:1883 (port-forward dc-nats:1883)")
	bgDevices := flag.Int("bg-devices", envIntOr("DC_LOADTEST_BG_DEVICES", 0),
		"background fleet size that saturates the enqueue/dispatch path (0 = default)")
	bgInterval := flag.Duration("bg-interval", envDurationOr("DC_LOADTEST_BG_INTERVAL", 0),
		"background emit cadence per device, e.g. 200ms (0 = default)")
	concurrency := flag.Int("concurrency", envIntOr("DC_LOADTEST_CONCURRENCY", 0),
		"max background emits in flight per tick (0 derives it from the device count)")
	safetyProbes := flag.Int("safety-probes", envIntOr("DC_LOADTEST_SAFETY_PROBES", 0),
		"number of below-threshold safety probes — must never enqueue a command (0 = default)")
	commandProbes := flag.Int("command-probes", envIntOr("DC_LOADTEST_COMMAND_PROBES", 0),
		"number of threshold-crossing command probes (0 = default)")
	cycles := flag.Int("cycles", envIntOr("DC_LOADTEST_CYCLES", 0),
		"K: threshold crossings per command probe → expect exactly K commands, all SUCCESSFUL (0 = default)")
	probeInterval := flag.Duration("probe-interval", envDurationOr("DC_LOADTEST_PROBE_INTERVAL", 0),
		"probe emit cadence — low + fixed, so probe crossings are never shed traffic (0 = default)")
	minAccepted := flag.Int64("min-accepted", int64(envIntOr("DC_LOADTEST_MIN_ACCEPTED", 0)),
		"background load floor for the verdict to count (0 = default)")
	reportPath := flag.String("report", envOr("DC_LOADTEST_REPORT", ""),
		"write the JSON command report to this path (also printed to stderr)")
	flag.Parse()

	if *handshakePath == "" {
		log.Fatal().Msg("--handshake (or DC_SIM_HANDSHAKE) is required")
	}

	hs, err := sim.LoadHandshake(*handshakePath)
	if err != nil {
		log.Fatal().Err(err).Msg("load handshake")
	}

	cfg := loadtest.CommandConfig{
		Seed:               hs.Seed,
		BackgroundDevices:  *bgDevices,
		BackgroundInterval: *bgInterval,
		Concurrency:        *concurrency,
		SafetyProbes:       *safetyProbes,
		CommandProbes:      *commandProbes,
		Cycles:             *cycles,
		ProbeInterval:      *probeInterval,
		MinAccepted:        *minAccepted,
		MqttBroker:         *mqttBroker,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Info().Str("tenant", hs.Tenant).Int("bgDevices", *bgDevices).
		Int("safetyProbes", *safetyProbes).Int("commandProbes", *commandProbes).Int("cycles", *cycles).
		Msg("command round-trip probe run starting")

	report, err := loadtest.RunCommandProbes(ctx, hs, cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("command round-trip run failed")
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

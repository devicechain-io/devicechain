// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// dc-loadtest-detection runs the ADR-064 L2d-1 planted detection probes: it
// provisions a dedicated harness profile with a pinned threshold DetectionRule, a
// set of safety probes (emit strictly below the threshold — must never raise) and
// edge probes (cross it exactly K times — must produce exactly K raised + K resolved
// edges), plus a saturating background fleet on the same rule. It subscribes to
// detectionStream, drives the load, waits for the planted edges to drain, and exits
// non-zero if any invariant is violated. Like dc-loadtest it is an untrusted external
// client: no service framework, no database, no service token.
package main

import (
	"context"
	"flag"
	"os"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-simulator/internal/stopsig"
	"github.com/devicechain-io/dc-simulator/loadtest"
	"github.com/devicechain-io/dc-simulator/mqttload"
	"github.com/devicechain-io/dc-simulator/sim"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	handshakePath := flag.String("handshake", envOr("DC_SIM_HANDSHAKE", ""),
		"path to the handshake JSON written by 'dcctl sim create' (or set DC_SIM_HANDSHAKE)")
	bgDevices := flag.Int("bg-devices", envIntOr("DC_LOADTEST_BG_DEVICES", 0),
		"background fleet size that saturates the pipeline (0 = default)")
	bgInterval := flag.Duration("bg-interval", envDurationOr("DC_LOADTEST_BG_INTERVAL", 0),
		"background emit cadence per device, e.g. 200ms (0 = default)")
	concurrency := flag.Int("concurrency", envIntOr("DC_LOADTEST_CONCURRENCY", 0),
		"max background emits in flight per tick (0 derives it from the device count)")
	safetyProbes := flag.Int("safety-probes", envIntOr("DC_LOADTEST_SAFETY_PROBES", 0),
		"number of below-threshold safety probes (0 = default)")
	edgeProbes := flag.Int("edge-probes", envIntOr("DC_LOADTEST_EDGE_PROBES", 0),
		"number of threshold-crossing edge probes (0 = default)")
	cycles := flag.Int("cycles", envIntOr("DC_LOADTEST_CYCLES", 0),
		"K: threshold crossings per edge probe → expect exactly K raised + K resolved (0 = default)")
	probeInterval := flag.Duration("probe-interval", envDurationOr("DC_LOADTEST_PROBE_INTERVAL", 0),
		"probe emit cadence — low + fixed, so probe edges are never shed traffic (0 = default)")
	minAccepted := flag.Int64("min-accepted", int64(envIntOr("DC_LOADTEST_MIN_ACCEPTED", 0)),
		"background load floor for the verdict to count (0 = default)")
	lagBound := flag.Duration("lag-bound", envDurationOr("DC_LOADTEST_LAG_BOUND", 0),
		"detection-lag bound: each probe cycle's alarm transition must show in the durable alarm within this long of its emit, or the run fails (0 = default 30s)")
	reportPath := flag.String("report", envOr("DC_LOADTEST_REPORT", ""),
		"write the JSON detection report to this path (also printed to stderr)")
	transport := flag.String("transport", envOr("DC_LOADTEST_TRANSPORT", "http"),
		"how the background fleet AND the probes reach the platform: http (the ingress, default) or mqtt (one session per device on the MQTT gateway, an emit accepted only on its PUBACK)")
	mqttBroker := flag.String("mqtt-broker", envOr("DC_LOADTEST_MQTT_BROKER", ""),
		"--transport mqtt: broker address, overriding the handshake's endpoints.mqttBroker")
	useTLS := flag.Bool("tls", true, "--transport mqtt: dial the broker over TLS (--tls=false dials plaintext)")
	noTLS := flag.Bool("no-tls", false, "--transport mqtt: shorthand for --tls=false")
	tlsInsecure := flag.Bool("tls-insecure", false, "--transport mqtt: skip verification of the broker certificate (the handshake mqttTLSInsecure also applies)")
	mqttQoS := flag.Int("qos", 1, "--transport mqtt: MQTT QoS, 1 or 0")
	flag.Parse()

	if *handshakePath == "" {
		log.Fatal().Msg("--handshake (or DC_SIM_HANDSHAKE) is required")
	}

	hs, err := sim.LoadHandshake(*handshakePath)
	if err != nil {
		log.Fatal().Err(err).Msg("load handshake")
	}

	cfg := loadtest.DetectionConfig{
		Seed:               hs.Seed,
		BackgroundDevices:  *bgDevices,
		BackgroundInterval: *bgInterval,
		Concurrency:        *concurrency,
		SafetyProbes:       *safetyProbes,
		EdgeProbes:         *edgeProbes,
		Cycles:             *cycles,
		ProbeInterval:      *probeInterval,
		MinAccepted:        *minAccepted,
		LagBound:           *lagBound,
	}

	switch *transport {
	case "http":
	case "mqtt":
		tlsOn := *useTLS && !*noTLS
		cfg.MQTT = &mqttload.Config{
			Broker: *mqttBroker, TLS: tlsOn, TLSInsecure: *tlsInsecure,
			QoS: byte(*mqttQoS), CleanSession: true,
		}
	default:
		log.Fatal().Str("transport", *transport).Msg("--transport must be http or mqtt")
	}

	ctx, cancel, stop := stopsig.Notify(context.Background())
	defer cancel()

	log.Info().Str("tenant", hs.Tenant).Int("bgDevices", *bgDevices).
		Int("safetyProbes", *safetyProbes).Int("edgeProbes", *edgeProbes).Int("cycles", *cycles).
		Msg("detection-probe run starting")

	report, err := loadtest.RunDetectionProbes(ctx, hs, cfg)
	if err != nil {
		if code, ok := stop.ExitCode(); ok {
			log.Warn().Err(err).Int("exit", code).Msg("detection-probe run stopped by a signal: no verdict")
			os.Exit(code)
		}
		log.Fatal().Err(err).Msg("detection-probe run failed")
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

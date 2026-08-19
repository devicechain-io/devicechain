// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// dc-loadtest-batch runs the L2d-4 fleet-write harness: it provisions two profiles —
// one publishing the batch command, one publishing a DIFFERENT command so it is
// constrained and genuinely refuses the batch one — plus four cohorts (targets that
// receive the fan-out over real MQTT, bystanders that must be untouched, a poison
// device that causes the whole-batch refusal, and a background fleet that supplies
// contention). It then fans a command out, waits for every command row to complete
// its round trip to the device and back, replays the batch token concurrently, and
// fires a batch that must be refused whole and leave nothing behind.
//
// Like the other load-test binaries it is an untrusted external client: no service
// framework, no database, no service token. It needs the HTTP ingress and /api for
// GraphQL, and the NATS MQTT gateway (port-forward dc-nats:1883, pass --mqtt-broker)
// for the devices that answer their commands.
//
// Exit 0 when every invariant held — or, with --control, when the control flipped
// exactly the invariants it must. Exit 1 otherwise.
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
		"path to the handshake JSON written by 'dcctl sim create' (or set DC_SIM_HANDSHAKE)")
	mqttBroker := flag.String("mqtt-broker", envOr("DC_LOADTEST_MQTT_BROKER", ""),
		"NATS MQTT gateway address the target devices connect to, e.g. ssl://127.0.0.1:1883 (port-forward dc-nats:1883; the listener is TLS)")
	mqttInsecure := flag.Bool("mqtt-insecure", true,
		"skip the gateway server-cert verification for an ssl:// broker (default true — a kind/dev cert's SAN is the in-cluster DNS name, unmatchable by a 127.0.0.1 port-forward)")
	targets := flag.Int("target-devices", envIntOr("DC_LOADTEST_TARGET_DEVICES", 0),
		"devices in the batch, each of which must end with exactly one SUCCESSFUL command (0 = default)")
	bystanders := flag.Int("bystander-devices", envIntOr("DC_LOADTEST_BYSTANDER_DEVICES", 0),
		"devices of the same type that no batch names — they must hold zero commands (0 = default)")
	poison := flag.Int("poison-devices", envIntOr("DC_LOADTEST_POISON_DEVICES", 0),
		"devices on the vocabulary-mismatched profile that cause the whole-batch refusal (0 = default)")
	replays := flag.Int("replays", envIntOr("DC_LOADTEST_REPLAYS", 0),
		"K: how many re-issues of the same batch token fire AT ONCE (0 = default)")
	bgDevices := flag.Int("bg-devices", envIntOr("DC_LOADTEST_BG_DEVICES", 0),
		"background fleet size that supplies contention (0 = default)")
	bgInterval := flag.Duration("bg-interval", envDurationOr("DC_LOADTEST_BG_INTERVAL", 0),
		"background emit cadence per device, e.g. 200ms (0 = default)")
	concurrency := flag.Int("concurrency", envIntOr("DC_LOADTEST_CONCURRENCY", 0),
		"max background emits in flight per tick (0 derives it from the device count)")
	minAccepted := flag.Int64("min-accepted", int64(envIntOr("DC_LOADTEST_MIN_ACCEPTED", 0)),
		"background load floor for the verdict to count (0 = default)")
	control := flag.String("control", "",
		"run a negative control instead of a plain pass: \""+loadtest.ControlDeafDevice+"\" leaves one target without a receiver and requires the run to fail on EXACTLY the round-trip invariant")
	reportPath := flag.String("report", envOr("DC_LOADTEST_REPORT", ""),
		"write the JSON batch report to this path (also printed to stderr)")
	flag.Parse()

	if *handshakePath == "" {
		log.Fatal().Msg("--handshake (or DC_SIM_HANDSHAKE) is required")
	}

	hs, err := sim.LoadHandshake(*handshakePath)
	if err != nil {
		log.Fatal().Err(err).Msg("load handshake")
	}

	cfg := loadtest.BatchConfig{
		Seed:               hs.Seed,
		TargetDevices:      *targets,
		BystanderDevices:   *bystanders,
		PoisonDevices:      *poison,
		Replays:            *replays,
		BackgroundDevices:  *bgDevices,
		BackgroundInterval: *bgInterval,
		Concurrency:        *concurrency,
		MinAccepted:        *minAccepted,
		MqttBroker:         *mqttBroker,
		MqttTLSInsecure:    *mqttInsecure,
		Control:            *control,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Info().Str("tenant", hs.Tenant).Int("targets", *targets).Int("bystanders", *bystanders).
		Int("replays", *replays).Str("control", *control).Msg("fleet-write batch run starting")

	report, err := loadtest.RunBatch(ctx, hs, cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("batch run failed")
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

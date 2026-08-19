// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// dc-loadtest-presence runs the L4 broker-asserted MQTT presence harness: it
// provisions four cohorts on a dedicated profile — steady devices that connect and
// never leave, churn devices that reconnect R times, departed devices that leave
// once, and a background fleet that supplies contention over HTTP telemetry — then
// asserts what the platform believes about who is connected, read over the same
// tenant-scoped device-state API a console reads.
//
// Like the other load-test binaries it is an untrusted external client: no service
// framework, no database, no service token. It needs BOTH reaches the command
// harness needs — the HTTP ingress for telemetry and /api for GraphQL, and the NATS
// MQTT gateway (port-forward dc-nats:1883, pass --mqtt-broker) for the presence
// cohorts' connections, which are the thing under test.
//
// 🔴 ITS EXIT CODE HAS THREE VALUES, NOT TWO:
//
//	0  every presence invariant held (or, with --control, the control flipped
//	   exactly the invariants it must)
//	1  a presence invariant was violated — a real finding
//	2  the run COULD NOT MEASURE presence, and has no opinion. Either the tap is not
//	   running on this instance, or the background load was shed at the per-tenant
//	   ingest ceiling that presence transitions also pass through — in which case a
//	   transition this harness did not see may have been refused rather than lost.
//	   Reporting that as either a pass or a defect would be a guess.
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

// exitCouldNotMeasure is what a run that never produced a report exits with. A
// harness that could not drive its own cohort — an unreachable broker, a refused
// bootstrap — has not found a presence defect, and saying so as though it had is the
// same mistake as reporting a shed transition as a lost one.
const exitCouldNotMeasure = 2

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	handshakePath := flag.String("handshake", envOr("DC_SIM_HANDSHAKE", ""),
		"path to the handshake JSON written by 'dcctl sim create' (or set DC_SIM_HANDSHAKE)")
	mqttBroker := flag.String("mqtt-broker", envOr("DC_LOADTEST_MQTT_BROKER", ""),
		"NATS MQTT gateway address the presence cohorts connect to, e.g. ssl://127.0.0.1:1883 (port-forward dc-nats:1883; the listener is TLS)")
	mqttInsecure := flag.Bool("mqtt-insecure", true,
		"skip the gateway server-cert verification for an ssl:// broker (default true — a kind/dev cert's SAN is the in-cluster DNS name, unmatchable by a 127.0.0.1 port-forward)")
	steady := flag.Int("steady-devices", envIntOr("DC_LOADTEST_STEADY_DEVICES", 0),
		"devices that connect once and must never read offline (0 = default)")
	churn := flag.Int("churn-devices", envIntOr("DC_LOADTEST_CHURN_DEVICES", 0),
		"devices that reconnect R times and must climb in session ordering (0 = default)")
	departed := flag.Int("departed-devices", envIntOr("DC_LOADTEST_DEPARTED_DEVICES", 0),
		"devices that leave once and must read offline — the disconnect-edge oracle (0 = default)")
	rounds := flag.Int("churn-rounds", envIntOr("DC_LOADTEST_CHURN_ROUNDS", 0),
		"R: disconnect/reconnect cycles per churn device (0 = default)")
	bgDevices := flag.Int("bg-devices", envIntOr("DC_LOADTEST_BG_DEVICES", 0),
		"background fleet size that supplies contention (0 = default)")
	bgInterval := flag.Duration("bg-interval", envDurationOr("DC_LOADTEST_BG_INTERVAL", 0),
		"background emit cadence per device, e.g. 200ms (0 = default)")
	concurrency := flag.Int("concurrency", envIntOr("DC_LOADTEST_CONCURRENCY", 0),
		"max background emits in flight per tick (0 derives it from the device count)")
	minAccepted := flag.Int64("min-accepted", int64(envIntOr("DC_LOADTEST_MIN_ACCEPTED", 0)),
		"background load floor for the verdict to count (0 = default)")
	pageSize := flag.Int("page-size", envIntOr("DC_LOADTEST_PAGE_SIZE", 0),
		"assertedDeviceStates page size — kept far below the cohort on purpose, so the cursor is genuinely walked (0 = default)")
	tapTimeout := flag.Duration("tap-timeout", envDurationOr("DC_LOADTEST_TAP_TIMEOUT", 0),
		"how long to wait, PRE-LOAD and on an idle tenant, for the first device's ASSERTED row before declaring the tap not running (0 = default)")
	flipTimeout := flag.Duration("flip-timeout", envDurationOr("DC_LOADTEST_FLIP_TIMEOUT", 0),
		"how long to wait for one churn transition to appear in the projection (0 = default). Applied to 2 waits per round, so it dominates the run's worst case")
	tailHold := flag.Duration("tail-hold", envDurationOr("DC_LOADTEST_TAIL_HOLD", 0),
		"how long to keep watching the steady cohort after the last churn round (0 = default)")
	settle := flag.Duration("settle", envDurationOr("DC_LOADTEST_SETTLE", 0),
		"how long to hold with the load off before the authoritative reads (0 = default)")
	control := flag.String("control", "",
		"run a negative control instead of a plain pass: \""+loadtest.ControlDropSteadyDevice+"\" disconnects one steady device and requires the run to fail on EXACTLY the invariants that departure must flip")
	reportPath := flag.String("report", envOr("DC_LOADTEST_REPORT", ""),
		"write the JSON presence report to this path (also printed to stderr)")
	flag.Parse()

	if *handshakePath == "" {
		log.Fatal().Msg("--handshake (or DC_SIM_HANDSHAKE) is required")
	}

	hs, err := sim.LoadHandshake(*handshakePath)
	if err != nil {
		log.Fatal().Err(err).Msg("load handshake")
	}

	cfg := loadtest.PresenceConfig{
		Seed:               hs.Seed,
		SteadyDevices:      *steady,
		ChurnDevices:       *churn,
		DepartedDevices:    *departed,
		ChurnRounds:        *rounds,
		BackgroundDevices:  *bgDevices,
		BackgroundInterval: *bgInterval,
		Concurrency:        *concurrency,
		MinAccepted:        *minAccepted,
		PageSize:           *pageSize,
		TapTimeout:         *tapTimeout,
		FlipTimeout:        *flipTimeout,
		TailHold:           *tailHold,
		Settle:             *settle,
		MqttBroker:         *mqttBroker,
		MqttTLSInsecure:    *mqttInsecure,
		Control:            *control,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Info().Str("tenant", hs.Tenant).Int("steady", *steady).Int("churn", *churn).
		Int("departed", *departed).Int("rounds", *rounds).Str("control", *control).
		Msg("broker-asserted presence run starting")

	report, err := loadtest.RunPresence(ctx, hs, cfg)
	if err != nil {
		log.Error().Err(err).Msgf("the presence harness could not complete a run, so it has no verdict (exit %d)", exitCouldNotMeasure)
		os.Exit(exitCouldNotMeasure)
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
	os.Exit(report.ExitCode())
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

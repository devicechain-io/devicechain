// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// dc-loadtest-mqtt is the MQTT telemetry mode of the correctness-under-load harness. It reads the
// same handshake file 'dcctl sim create' writes, provisions the same fleet of devices with the
// same credentials dc-loadtest does, then connects ONE MQTT session per device to the platform's
// MQTT gateway and publishes their telemetry at a target rate for a set duration, QoS 1 by
// default, over TLS by default.
//
// It counts what the broker acknowledged (a PUBACK seen by the session store, never a completed
// publish token), waits for the platform to persist, and compares: a QoS 1 publish the broker
// PUBACKed is a promise that the event is stored, so a run where PUBACKed exceeds persisted is a
// LOSS. It is reported as one, with the number, and the process exits 3.
//
// Exit codes: 0 every invariant held; 1 an invariant failed; 2 the run could not be carried out
// (bad flags, an unreachable platform or broker, an aborted drive: no verdict); 3 PUBACKed events
// were lost.
//
// Like dc-loadtest it is an untrusted external client: no service framework, no database, no
// service token. It uses the sim's own provisioning, the devices' own credentials, and the
// tenant-scoped GraphQL API a real client reads with.
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
	"github.com/devicechain-io/dc-simulator/mqttload"
	"github.com/devicechain-io/dc-simulator/sim"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
	os.Exit(run())
}

func run() int {
	handshakePath := flag.String("handshake", envOr("DC_SIM_HANDSHAKE", ""),
		"path to the handshake JSON file written by 'dcctl sim create' (or set DC_SIM_HANDSHAKE)")
	manifest := flag.String("manifest", envOr("DC_LOADTEST_MANIFEST", ""),
		"scenario id to drive (default: the handshake's manifestId, else devicepulse)")
	devices := flag.Int("devices", envIntOr("DC_LOADTEST_DEVICES", 0),
		"number of devices, each holding ONE MQTT session (the broker confines a credential to its own client id, so connections == devices); 0 keeps the scenario's own count")
	rate := flag.Float64("rate", envFloatOr("DC_LOADTEST_RATE", 1000),
		"total events per second across the fleet (each device publishes at rate/devices)")
	duration := flag.Duration("duration", envDurationOr("DC_LOADTEST_DURATION", time.Minute),
		"how long to publish, e.g. 60m")
	qos := flag.Int("qos", envIntOr("DC_LOADTEST_MQTT_QOS", 1), "MQTT QoS: 1 (PUBACKed delivery) or 0")
	cleanSession := flag.Bool("clean-session", envBoolOr("DC_LOADTEST_MQTT_CLEAN_SESSION", true),
		"connect with a clean session; false keeps the session and its unacknowledged publishes across reconnects")
	useTLS := flag.Bool("tls", envBoolOr("DC_LOADTEST_MQTT_TLS", true),
		"dial the broker over TLS (the default). --tls=false dials plaintext; the report states what was observed on the sockets")
	noTLS := flag.Bool("no-tls", false, "shorthand for --tls=false")
	tlsInsecure := flag.Bool("tls-insecure", false,
		"skip verification of the broker's certificate; unset, the handshake's mqttTLSInsecure decides (a local bring-up's gateway is self-signed), and --tls-insecure=false forces verification")
	broker := flag.String("mqtt-broker", envOr("DC_LOADTEST_MQTT_BROKER", ""),
		"MQTT broker address, overriding the handshake's endpoints.mqttBroker; the scheme is set by --tls")
	metric := flag.String("metric", envOr("DC_LOADTEST_METRIC", ""),
		"measurement key each event carries (default for devicepulse: its speed metric)")
	padBytes := flag.Int("pad-bytes", envIntOr("DC_LOADTEST_PAD_BYTES", 0),
		"add an inert padding member of this many bytes to every event (a larger payload; the decoder ignores it)")
	connectConcurrency := flag.Int("connect-concurrency", envIntOr("DC_LOADTEST_MQTT_CONNECT_CONCURRENCY", 0),
		"simultaneous CONNECTs while ramping and reconnecting (0 = 64)")
	connectTimeout := flag.Duration("connect-timeout", envDurationOr("DC_LOADTEST_MQTT_CONNECT_TIMEOUT", 0),
		"one CONNECT attempt's timeout (0 = 15s)")
	connectWait := flag.Duration("connect-wait", envDurationOr("DC_LOADTEST_MQTT_CONNECT_WAIT", 0),
		"how long to wait for every device to connect before the drive starts (0 = 5m)")
	ackTimeout := flag.Duration("ack-timeout", envDurationOr("DC_LOADTEST_MQTT_ACK_TIMEOUT", 0),
		"how long a publish may wait for its PUBACK before it counts as unconfirmed (0 = 30s)")
	keepAlive := flag.Duration("keepalive", envDurationOr("DC_LOADTEST_MQTT_KEEPALIVE", 0), "MQTT keep-alive (0 = 30s)")
	backoffBase := flag.Duration("backoff-base", envDurationOr("DC_LOADTEST_MQTT_BACKOFF_BASE", 0),
		"reconnect backoff base; the delay doubles with jitter up to --backoff-max (0 = 500ms)")
	backoffMax := flag.Duration("backoff-max", envDurationOr("DC_LOADTEST_MQTT_BACKOFF_MAX", 0), "reconnect backoff cap (0 = 30s)")
	maxInflight := flag.Int("max-inflight", envIntOr("DC_LOADTEST_MQTT_MAX_INFLIGHT", 0),
		"per-device cap on unacknowledged publishes (0 = 64, at most 100)")
	identity := flag.String("identity", envOr("DC_LOADTEST_IDENTITY", loadtest.IdentityAuto),
		"how to read the platform back: full (every event, exact loss), count (one count; no durability verdict if anything was unconfirmed or re-sent), auto (full up to --identity-max-events)")
	identityMax := flag.Int64("identity-max-events", int64(envIntOr("DC_LOADTEST_IDENTITY_MAX_EVENTS", loadtest.DefaultIdentityMaxEvents)),
		"PUBACKed volume above which --identity auto compares counts instead of reading every event back")
	minAccepted := flag.Int64("min-accepted", int64(envIntOr("DC_LOADTEST_MIN_ACCEPTED", loadtest.DefaultMinAccepted)),
		"floor of acknowledged events the run must apply for its verdict to count")
	minOffered := flag.Float64("min-offered-ratio", envFloatOr("DC_LOADTEST_MIN_OFFERED_RATIO", loadtest.DefaultMinOfferedRatio),
		"share of rate*duration the run must actually offer, and have acknowledged, for its verdict to count")
	qos0Tolerance := flag.Float64("qos0-tolerance", envFloatOr("DC_LOADTEST_QOS0_TOLERANCE", 0),
		"share of written QoS 0 events allowed to be missing (QoS 0 promises no delivery; the default 0 reports any shortfall as a failure)")
	maxAckP99 := flag.Duration("max-ack-p99", envDurationOr("DC_LOADTEST_MAX_ACK_P99", 0),
		"fail the run when the PUBACK p99 exceeds this (0 = report only)")
	quiesceTimeout := flag.Duration("quiesce-timeout", envDurationOr("DC_LOADTEST_QUIESCE_TIMEOUT", loadtest.DefaultQuiesceTimeout),
		"max time to wait for the persisted count to reach the PUBACKed count after the drive stops (raise it for a run that builds a backlog)")
	quiesceSettle := flag.Duration("quiesce-settle", envDurationOr("DC_LOADTEST_QUIESCE_SETTLE", loadtest.DefaultQuiesceSettle),
		"how long to keep watching the count after it reaches the target, to catch a late over-persist")
	reportInterval := flag.Duration("report-interval", envDurationOr("DC_LOADTEST_REPORT_INTERVAL", loadtest.DefaultMQTTReportInterval),
		"progress cadence while publishing; each slice is logged and recorded in the report (negative disables)")
	reportPath := flag.String("report", envOr("DC_LOADTEST_REPORT", ""),
		"write the JSON report to this path (also printed to stderr)")
	flag.Parse()

	if *handshakePath == "" {
		log.Error().Msg("--handshake (or DC_SIM_HANDSHAKE) is required")
		return loadtest.ExitError
	}
	if *noTLS && flagSet("tls") && *useTLS {
		log.Error().Msg("--no-tls and --tls=true contradict each other")
		return loadtest.ExitError
	}
	tlsOn := *useTLS && !*noTLS

	hs, err := sim.LoadHandshake(*handshakePath)
	if err != nil {
		log.Error().Err(err).Msg("load handshake")
		return loadtest.ExitError
	}
	manifestId := *manifest
	if manifestId == "" {
		manifestId = hs.ManifestId
	}
	if manifestId == "" {
		manifestId = "devicepulse"
	}

	profile := loadtest.MQTTProfile{
		Manifest: manifestId, Seed: hs.Seed, Devices: *devices,
		Rate: *rate, Duration: *duration, MetricKey: *metric, PadBytes: *padBytes,
		MQTT: mqttload.Config{
			Broker: *broker, TLS: tlsOn, TLSInsecure: *tlsInsecure,
			QoS: byte(*qos), CleanSession: *cleanSession,
			ConnectTimeout: *connectTimeout, AckTimeout: *ackTimeout, KeepAlive: *keepAlive,
			ConnectConcurrency: *connectConcurrency, BackoffBase: *backoffBase, BackoffMax: *backoffMax,
			MaxInflight: *maxInflight,
		},
		TLSInsecureExplicit: flagSet("tls-insecure"),
		ConnectTimeout:      *connectWait, ReportInterval: *reportInterval,
		Identity: *identity, IdentityMaxEvents: *identityMax,
		MinAccepted: *minAccepted, MinOfferedRatio: *minOffered, QoS0Tolerance: *qos0Tolerance, MaxAckP99: *maxAckP99,
		QuiesceTimeout: *quiesceTimeout, QuiesceSettle: *quiesceSettle,
	}

	// SIGINT/SIGTERM abort the run (no verdict) rather than reconciling a torn stop boundary.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Info().Str("tenant", hs.Tenant).Str("manifest", manifestId).Int("devices", *devices).
		Float64("rate", *rate).Dur("duration", *duration).Int("qos", *qos).Bool("tls", tlsOn).
		Msg("mqtt load-test starting")

	report, err := loadtest.RunMQTT(ctx, hs, profile)
	if err != nil {
		log.Error().Err(err).Msg("mqtt load-test run failed: no verdict")
		return loadtest.ExitError
	}

	if *reportPath != "" {
		data, jerr := report.JSON()
		if jerr != nil {
			log.Error().Err(jerr).Msg("render report")
			return loadtest.ExitError
		}
		if werr := os.WriteFile(*reportPath, append(data, '\n'), 0o644); werr != nil {
			log.Error().Err(werr).Str("path", *reportPath).Msg("write report")
			return loadtest.ExitError
		}
		log.Info().Str("path", *reportPath).Msg("wrote JSON report")
	}
	os.Stderr.WriteString("\n" + report.Human() + "\n")
	return report.ExitCode()
}

// flagSet reports whether the named flag was given on the command line.
func flagSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// The env helpers FAIL on a malformed value rather than silently falling back: a load test must
// run the size it was told, not a default it mistook for one.
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

func envFloatOr(key string, fallback float64) float64 {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		log.Fatal().Str(key, v).Msg("not a number")
	}
	return n
}

func envBoolOr(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Fatal().Str(key, v).Msg("not a boolean")
	}
	return b
}

func envDurationOr(key string, fallback time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Fatal().Str(key, v).Msg("not a duration (want e.g. 200ms, 5s, 60m)")
	}
	return d
}

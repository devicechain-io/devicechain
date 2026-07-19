// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// dc-simulator is the Go reference runner for the DeviceChain sim subsystem
// (sim-subsystem-contract.md). It reads a handshake file written by `dcctl sim
// create` (the scoped identity + resolved endpoints), authenticates as that
// identity, provisions its manifest's topology through the tenant
// device-management API, emits telemetry over the real device-plane HTTP
// ingress, and serves a small control API + a live presentation page.
//
// It deliberately does NOT use the dc-microservice service framework
// (core.Microservice) — it is not a platform microservice: no database, no
// NATS, no service token, no tenant-scoped storage of its own. It is an
// untrusted external client of the platform, exactly like a real device
// integration would be, using only the "RealClient" auth layer
// (dc-microservice/userclient) and the token-grammar helper (dc-microservice/core).
package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-simulator/sim"
)

//go:embed web/index.html
var webFiles embed.FS

// defaultPort is the sim's own control/presentation HTTP port. Distinct from
// any platform service port since it belongs to an external process.
const defaultPort = "8090"

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	handshakePath := flag.String("handshake", envOr("DC_SIM_HANDSHAKE", ""),
		"path to the handshake JSON file written by `dcctl sim create` (or set DC_SIM_HANDSHAKE)")
	port := flag.String("port", envOr("DC_SIM_PORT", defaultPort),
		"port the control API and presentation page listen on (or set DC_SIM_PORT)")
	bind := flag.String("bind", envOr("DC_SIM_BIND", "127.0.0.1"),
		"address to bind the control API + presentation page. Defaults to loopback because "+
			"/config.json serves a live tenant access token; only widen this on a trusted host.")
	devices := flag.Int("devices", envIntOr("DC_SIM_DEVICES", 0),
		"override the scenario's device count (0 keeps its own demo sizing)")
	emitInterval := flag.Duration("emit-interval", envDurationOr("DC_SIM_EMIT_INTERVAL", 0),
		"override the telemetry cadence, e.g. 200ms (0 keeps the 5s demo cadence)")
	concurrency := flag.Int("concurrency", envIntOr("DC_SIM_CONCURRENCY", 0),
		"max emits in flight per tick (0 derives it from the device count)")
	flag.Parse()

	if *handshakePath == "" {
		log.Fatal().Msg("--handshake (or DC_SIM_HANDSHAKE) is required")
	}

	load := sim.Load{
		DeviceCount:  *devices,
		EmitInterval: *emitInterval,
		Concurrency:  *concurrency,
	}
	if err := run(*handshakePath, *bind, *port, load); err != nil {
		log.Fatal().Err(err).Msg("dc-simulator exited")
	}
}

func run(handshakePath, bind, port string, load sim.Load) error {
	hs, err := sim.LoadHandshake(handshakePath)
	if err != nil {
		return err
	}
	// Pre-slice-2 handshake files never set manifestId; default to devicepulse
	// (the original MVP scenario) so an existing sim record keeps working
	// unchanged rather than suddenly failing to start.
	manifestId := hs.ManifestId
	if manifestId == "" {
		manifestId = "devicepulse"
	}
	// NewSim resolves the scenario AND checks the load profile is legal against
	// its manifest, so an impossible run is refused here rather than discovered
	// as a wrong number at the end of a measurement.
	driver, err := sim.NewSim(manifestId, hs.Seed, load)
	if err != nil {
		return err
	}

	// The device count the client's connection pool is sized against is the
	// RESOLVED one — after any override — not the flag, which is 0 whenever the
	// scenario's own sizing is in use.
	//
	// Summed from the populations rather than measured with Expand: Expand
	// materializes every DeviceInstance (deriving a SHA-256 credential per
	// device), and Provision expands again a moment later. Paying for a whole
	// extra population just to learn its size is a poor look in a tool whose
	// entire purpose is measuring memory.
	deviceCount := sim.DeviceCount(driver.Manifest())
	rt, err := sim.NewRuntime(hs, load, deviceCount)
	if err != nil {
		return err
	}

	lc := sim.NewLifecycle(driver, rt)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Info().Str("tenant", hs.Tenant).Str("instance", hs.InstanceId).Str("manifestId", manifestId).
		Msg("bootstrapping sim")
	if err := lc.Bootstrap(ctx); err != nil {
		return err
	}
	if err := lc.Start(ctx); err != nil {
		return err
	}
	log.Info().Int("deviceCount", len(rt.Devices)).
		Dur("emitInterval", load.Interval()).
		Float64("targetRatePerSec", load.TargetRate(len(rt.Devices))).
		Msg("sim running")

	webRoot, err := fs.Sub(webFiles, "web")
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	sim.NewControlServer(lc, rt).Register(mux)
	sim.RegisterPresentation(mux, webRoot, rt, manifestId)

	srv := &http.Server{Addr: bind + ":" + port, Handler: mux}
	go func() {
		<-ctx.Done()
		// Halt the emit loop before draining the server so a graceful shutdown
		// stops POSTing telemetry rather than emitting until the process dies.
		_ = lc.Stop()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info().Str("bind", bind).Str("port", port).Msg("dc-simulator control API + presentation page listening")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// envIntOr and envDurationOr FAIL on a malformed value rather than falling back
// to it. Silently treating DC_SIM_DEVICES=1OO (letter O) as "use the scenario
// default" would run a measurement at 12 devices while its operator believed it
// was running at 100 — a wrong published number, from a typo, with no symptom.
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

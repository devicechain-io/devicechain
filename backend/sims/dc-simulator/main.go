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
	flag.Parse()

	if *handshakePath == "" {
		log.Fatal().Msg("--handshake (or DC_SIM_HANDSHAKE) is required")
	}

	if err := run(*handshakePath, *bind, *port); err != nil {
		log.Fatal().Err(err).Msg("dc-simulator exited")
	}
}

func run(handshakePath, bind, port string) error {
	hs, err := sim.LoadHandshake(handshakePath)
	if err != nil {
		return err
	}
	if hs.ManifestId != "devicepulse" {
		// Slice 1 ships exactly one manifest; a mismatch is almost certainly an
		// operator/dcctl config error, so fail loud rather than silently running
		// the wrong scenario.
		log.Warn().Str("manifestId", hs.ManifestId).
			Msg("only the devicepulse manifest is implemented; running it regardless")
	}

	rt, err := sim.NewRuntime(hs)
	if err != nil {
		return err
	}

	driver := sim.NewDevicepulse(hs.Seed)
	lc := sim.NewLifecycle(driver, rt)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Info().Str("tenant", hs.Tenant).Str("instance", hs.InstanceId).Msg("bootstrapping devicepulse")
	if err := lc.Bootstrap(ctx); err != nil {
		return err
	}
	if err := lc.Start(ctx); err != nil {
		return err
	}
	log.Info().Int("deviceCount", len(rt.Devices)).Msg("devicepulse running")

	webRoot, err := fs.Sub(webFiles, "web")
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	sim.NewControlServer(lc, rt).Register(mux)
	sim.RegisterPresentation(mux, webRoot, rt, hs.ManifestId)

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

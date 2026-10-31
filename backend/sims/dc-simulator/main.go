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
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-simulator/lorawan"
	"github.com/devicechain-io/dc-simulator/sim"
)

//go:embed web/index.html
var webFiles embed.FS

// Build metadata, stamped by the release build (-X main.version=...). Plain package vars,
// like the edge agent's: an unstamped `go build` reports "dev".
var (
	version   = "dev"
	gitCommit = "unknown"
	buildDate = "unknown"
)

// defaultPort is the sim's own control/presentation HTTP port. Distinct from
// any platform service port since it belongs to an external process.
const defaultPort = "8090"

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	handshakePath := flag.String("handshake", envOr("DC_SIM_HANDSHAKE", ""),
		"path to the handshake JSON file written by 'dcctl sim create' (or set DC_SIM_HANDSHAKE)")
	port := flag.String("port", envOr("DC_SIM_PORT", defaultPort),
		"port the control API and presentation page listen on (or set DC_SIM_PORT)")
	bind := flag.String("bind", envOr("DC_SIM_BIND", "127.0.0.1"),
		"address to bind the control API + presentation page. Defaults to loopback because "+
			"/config.json is unauthenticated and serves a live TENANT-ADMIN access token — full "+
			"create/read/delete over the tenant's devices, credentials and dashboards — alongside "+
			"the MQTT broker address and instance id; only widen this on a trusted host.")
	devices := flag.Int("devices", envIntOr("DC_SIM_DEVICES", 0),
		"override the scenario's device count (0 keeps its own demo sizing)")
	emitInterval := flag.Duration("emit-interval", envDurationOr("DC_SIM_EMIT_INTERVAL", 0),
		"override the telemetry cadence, e.g. 200ms (0 keeps the 5s demo cadence)")
	concurrency := flag.Int("concurrency", envIntOr("DC_SIM_CONCURRENCY", 0),
		"max emits in flight per tick (0 derives it from the device count)")
	noFarEnd := flag.Bool("no-command-far-end", envBoolOr("DC_SIM_NO_COMMAND_FAR_END", false),
		"run a scenario that needs a command far end WITHOUT one. Its command widgets then "+
			"enqueue commands nothing answers, which reach SENT and expire — an escape hatch for "+
			"a host that cannot reach the MQTT gateway, not a default")
	externalPlane := flag.Bool("external-device-plane", envBoolOr("DC_SIM_EXTERNAL_DEVICE_PLANE", false),
		"an EXTERNAL client (e.g. a Unity player) is the device plane: provision and converge the "+
			"tenant, then idle with the control API and presentation page only -- no telemetry is "+
			"published and no device subscribes to commands from this process. Unlike "+
			"--no-command-far-end, which leaves telemetry running")
	playDevices := flag.Bool("play-devices", envBoolOr("DC_SIM_PLAY_DEVICES", false),
		"sitepulse only: play the device plane in this process (the scene's machine model and fleet "+
			"tracks) instead of a Unity player. Never run a player against the same sim while this is on")
	publishLog := flag.String("publish-log", envOr("DC_SIM_PUBLISH_LOG", ""),
		"with --play-devices: append every event the ingress accepted to this NDJSON file, so a "+
			"recording made from the platform can be checked against what the devices sent")
	lorawanNS := flag.String("lorawan-ns", envOr("DC_SIM_LORAWAN_NS", ""),
		"integration mode: play a LoRaWAN network server ("+lorawan.ChirpStack+" or "+lorawan.TTS+") posting uplinks to a "+
			"tenant's inbound integration, then exit. Needs no handshake; see --integration-url")
	integrationURL := flag.String("integration-url", envOr("DC_SIM_INTEGRATION_URL", ""),
		"integration mode: the integration's URL, https://<host>/api/integrations/<tenant>/<integration>")
	integrationToken := flag.String("integration-token", "",
		"integration mode: the integration's bearer token (prefer DC_SIM_INTEGRATION_TOKEN, which stays out of the process list)")
	integrationHost := flag.String("integration-host", envOr("DC_SIM_INTEGRATION_HOST", ""),
		"integration mode: send this as the request's Host, for an ingress host that does not resolve from here")
	devEUIs := flag.String("deveui", envOr("DC_SIM_DEVEUIS", ""),
		"integration mode: comma-separated DevEUIs (16 hex digits) of the devices that uplink")
	uplinks := flag.Int("uplinks", envIntOr("DC_SIM_UPLINKS", 5), "integration mode: uplinks per device")
	resend := flag.Bool("resend", envBoolOr("DC_SIM_RESEND", false),
		"integration mode: post every uplink twice, byte for byte, as a network server retrying does")
	x509Mode := flag.Bool("x509", envBoolOr("DC_SIM_X509", false),
		"X.509 (mTLS) device mode: mint a throwaway test CA and device certificates into --x509-dir, "+
			"optionally register them (--x509-register), connect to the handshake's MQTT gateway "+
			"presenting each certificate, then hold the sessions until interrupted. Needs --handshake")
	x509 := x509Flags{
		dir:        flag.String("x509-dir", envOr("DC_SIM_X509_DIR", "x509-state"), "x509 mode: state dir (CA, its key at mode 0600, device certificates, manifest)"),
		pinned:     flag.Int("x509-pinned", envIntOr("DC_SIM_X509_PINNED", 1), "x509 mode: devices with a pinned credential"),
		jit:        flag.Int("x509-jit", envIntOr("DC_SIM_X509_JIT", 0), "x509 mode: certificates for devices that are NOT registered, for the registration path"),
		prefix:     flag.String("x509-prefix", envOr("DC_SIM_X509_PREFIX", "sim-"), "x509 mode: externalId (CN) prefix"),
		anchor:     flag.String("x509-anchor", envOr("DC_SIM_X509_ANCHOR", ""), "x509 mode: trust anchor token (default dc-sim-x509-ca)"),
		deviceType: flag.String("x509-device-type", envOr("DC_SIM_X509_DEVICE_TYPE", ""), "x509 mode: device type token pinned devices are created under (required with --x509-register)"),
		register:   flag.Bool("x509-register", envBoolOr("DC_SIM_X509_REGISTER", false), "x509 mode: register the CA as a trust anchor and create the pinned devices + credentials"),
		mintOnly:   flag.Bool("x509-mint-only", false, "x509 mode: mint (and register if asked) and exit without connecting"),
		serverCA:   flag.String("x509-server-ca", envOr("DC_SIM_X509_SERVER_CA", ""), "x509 mode: PEM bundle verifying the broker's certificate (default: system roots, or the handshake's mqttTLSInsecure)"),
		validity:   flag.Duration("x509-validity", envDurationOr("DC_SIM_X509_VALIDITY", 0), "x509 mode: device certificate lifetime (default 1 year)"),
		holdFor:    flag.Duration("x509-hold", envDurationOr("DC_SIM_X509_HOLD", 0), "x509 mode: disconnect after this long (0 holds until interrupted)"),
	}
	showVersion := flag.Bool("version", false, "print the build version, commit and date, then exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("dc-simulator %s (commit %s, built %s)\n", version, gitCommit, buildDate)
		return
	}

	if *x509Mode {
		if *handshakePath == "" {
			log.Fatal().Msg("--x509 needs --handshake (tenant, instance and endpoints)")
		}
		if err := runX509(*handshakePath, x509); err != nil {
			log.Fatal().Err(err).Msg("dc-simulator x509 mode failed")
		}
		return
	}

	if *lorawanNS != "" {
		token := *integrationToken
		if token == "" {
			token = os.Getenv("DC_SIM_INTEGRATION_TOKEN")
		}
		if err := runLoRaWAN(lorawan.Config{
			NS: *lorawanNS, URL: *integrationURL, Token: token, HostHeader: *integrationHost, DevEUIs: lorawan.ParseDevEUIs(*devEUIs),
			Uplinks: *uplinks, Interval: *emitInterval, Resend: *resend,
		}); err != nil {
			log.Fatal().Err(err).Msg("dc-simulator integration mode failed")
		}
		return
	}

	if *handshakePath == "" {
		log.Fatal().Msg("--handshake (or DC_SIM_HANDSHAKE) is required")
	}

	load := sim.Load{
		DeviceCount:  *devices,
		EmitInterval: *emitInterval,
		Concurrency:  *concurrency,
	}
	if *externalPlane && *playDevices {
		log.Fatal().Msg("--external-device-plane and --play-devices are opposites: one says an outside client is the " +
			"devices, the other that this process is")
	}
	if *publishLog != "" && !*playDevices {
		log.Fatal().Msg("--publish-log logs what --play-devices publishes; without it this process publishes nothing")
	}
	if err := run(*handshakePath, *bind, *port, load, *noFarEnd, devicePlane{play: *playDevices, publishLog: *publishLog, external: *externalPlane}); err != nil {
		log.Fatal().Err(err).Msg("dc-simulator exited")
	}
}

// runLoRaWAN is the integration mode: one run of uplinks, a summary line on stdout, and an exit
// status that says only whether the run could be made. Whether the platform ANSWERED as expected is
// for the caller to judge from the summary, because a refusal (a bad token) is a result here, not
// a failure.
func runLoRaWAN(c lorawan.Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	sum, err := lorawan.Run(ctx, c)
	if err != nil {
		return err
	}
	out, err := json.Marshal(sum)
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	if sum.Errors > 0 {
		return fmt.Errorf("%d request(s) did not get an answer: %s", sum.Errors, sum.FirstError)
	}
	return nil
}

// devicePlane is the --play-devices choice and its publish log.
type devicePlane struct {
	play       bool
	publishLog string
	// external: an outside client is the device plane (--external-device-plane).
	external bool
}

func run(handshakePath, bind, port string, load sim.Load, noFarEnd bool, plane devicePlane) error {
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

	if plane.play {
		var publishLog io.Writer
		if plane.publishLog != "" {
			f, err := os.OpenFile(plane.publishLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				return fmt.Errorf("open publish log: %w", err)
			}
			defer f.Close()
			publishLog = f
		}
		if err := sim.PlayDevices(driver, publishLog); err != nil {
			return err
		}
		log.Warn().Str("manifestId", manifestId).
			Msg("this process is playing the device plane: do not run a Unity player against this sim")
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
	// The accounts this sim mints for the scenario's people are filed in the handshake file, which
	// is dcctl's sim record: `dcctl sim destroy` deletes exactly those.
	rt.Minted = sim.NewFileLedger(hs, handshakePath)
	rt.FarEndDisabled = noFarEnd
	rt.ExternalDevicePlane = plane.external
	if noFarEnd {
		log.Warn().Str("manifestId", manifestId).
			Msg(farEndOptOutWarning(driver.Manifest().FarEndMode()))
	}

	lc := sim.NewLifecycle(driver, rt)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Info().Str("tenant", hs.Tenant).Str("instance", hs.InstanceId).Str("manifestId", manifestId).
		Msg("bootstrapping sim")
	if err := lc.Bootstrap(ctx); err != nil {
		return err
	}
	if plane.external {
		// Provisioned and converged; nothing else runs. The control API and the presentation page
		// below stay up so the sim can be inspected and re-converged (POST /reset), but Start is refused.
		log.Warn().Int("deviceCount", len(rt.Devices)).
			Msg("external device plane: this process publishes no telemetry and subscribes as no device")
	} else {
		if err := lc.Start(ctx); err != nil {
			return err
		}
		log.Info().Int("deviceCount", len(rt.Devices)).
			Dur("emitInterval", load.Interval()).
			Float64("targetRatePerSec", load.TargetRate(len(rt.Devices))).
			Msg("sim running")
	}

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
		// Then release the command far end's broker connections. After Stop, not
		// instead of it: the far end outlives a Stop on purpose (a real device keeps
		// listening while it is not reporting) and only a process exit ends it.
		lc.Close()
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

// farEndOptOutWarning is what --no-command-far-end actually DID, per far-end mode.
//
// It is a pure function, and split out for exactly that reason: it is the only report
// of the flag for a scenario that declares no far end, /status having nothing to say
// there — so the branch that matters most is the one an operator sees least, and it
// has to be reachable by a test rather than buried in a log call inside run().
//
// EVERY mode warns, including the one where the flag changes nothing. A flag that
// silently has no effect is the same class of defect as the rest of this seam: the
// operator believes they changed the run, and reads everything that happens next
// through that belief. And all three state the CONSEQUENCE rather than the flag that
// was passed, because the person who set it is not necessarily the person later
// reading a board whose Send button appears to do nothing.
func farEndOptOutWarning(mode sim.CommandFarEndMode) string {
	switch mode {
	case sim.FarEndInternal:
		return "--no-command-far-end: this scenario's devices will NOT receive commands; " +
			"a command issued from its dashboard reaches SENT and expires unanswered"
	case sim.FarEndExternal:
		return "--no-command-far-end: this scenario's far end is an external presentation " +
			"client, and the broker address it would be handed is no longer required, so a " +
			"command issued from its dashboard reaches SENT and expires unanswered"
	default:
		return "--no-command-far-end has nothing to disable: this scenario declares no command " +
			"far end at all, so the flag changes nothing about how it runs"
	}
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

func envBoolOr(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Fatal().Str(key, v).Msg("not a boolean (want e.g. 1, true, 0, false)")
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
		log.Fatal().Str(key, v).Msg("not a duration (want e.g. 200ms, 5s)")
	}
	return d
}

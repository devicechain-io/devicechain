// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/fatih/color"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	METRICS_NAMESPACE = "devicechain"
)

// Primary microservice implementation
type Microservice struct {
	StartTime time.Time

	// Passed from environment
	InstanceId       string
	TenantId         string
	TenantName       string
	MicroserviceId   string
	MicroserviceName string
	FunctionalArea   string

	// Configuration content
	InstanceConfiguration        config.InstanceConfiguration
	MicroserviceConfigurationRaw []byte

	// Readiness gates the data plane on auth being live (ADR-022 decision 3).
	Readiness *ReadinessGate

	// Observability metrics (E17). nil when the microservice was built without
	// NewMicroservice (e.g. in unit tests), so every use is nil-guarded.
	readyGauge   prometheus.Gauge
	authAttempts prometheus.Counter
	authFailures prometheus.Counter

	// Internal lifeycle processing
	lifecycle LifecycleManager
	shutdown  chan os.Signal

	// outcome carries HOW the process ended: nil for an orderly shutdown, non-nil
	// when startup was refused or teardown failed. Run turns it into an exit status.
	//
	// 🔴 It is deliberately not the `chan bool` it used to be. That channel's value was
	// always true, so it recorded that the process had stopped while discarding the only
	// bit anyone can act on — which is how a service that refused its own configuration
	// came to exit 0 and report Completed.
	outcome chan error

	// finish makes the FIRST outcome the reported one and every later one a no-op.
	//
	// ⚠️ Be exact about what it does NOT do: it does not prevent a deadlock. With a
	// cap-1 buffer and Run already sitting on the receive, a late sender could never
	// have blocked for longer than that receive takes. An earlier version of this
	// comment claimed a stranded-goroutine hazard, in the emphatic register, and the
	// hazard did not exist.
	//
	// What it buys is that "exactly one outcome" is stated rather than inferred from a
	// buffer size, and that it keeps holding if a third sender is ever added. 🔴 The
	// property that actually matters — a later nil never painting over an earlier
	// failure — is pinned by TestARefusedStartupSurvivesTheStopThatFollowsIt, not by
	// this field.
	finish sync.Once

	// phase is the distinction whose absence made shutdown incoherent: whether this
	// service ever actually started. Two goroutines drive the lifecycle — startup, and
	// the signal handler, which is armed BEFORE startup is launched — and this is what
	// says which of them owns the process.
	//
	// 🔴 Without it, a SIGTERM during initialization ran the full teardown against a
	// service that did not exist yet. Stop's state guards do not refuse Initializing or
	// Initialized (they refuse Uninitialized and Starting), and initialization is where
	// a service spends most of its startup. So Stop and Terminate both SUCCEEDED,
	// reported an orderly shutdown, and took the outcome slot — and a startup failure
	// arriving a moment later was dropped, exiting 0. That is the exact defect the
	// outcome channel exists to fix, reachable through the channel meant to fix it.
	//
	// 🔴 IT IS A CAS AND NOT A FLAG, because a flag leaves a window instead of closing
	// one. Set after InitializeAndStart returns, a plain flag is still false for the
	// instant after startup has genuinely finished, so a SIGTERM landing there would
	// treat a fully-started service as never-started: no teardown, and no readiness
	// drain, severing in-flight requests on a pod that was serving. Exactly one of the
	// two goroutines can move phase out of phaseStarting, so there is no such instant.
	//
	// 🔴 It also supplies a happens-before edge that was missing outright:
	// LifecycleManager has no synchronization of its own, so reading its State from the
	// signal goroutine while startup writes it is a data race — one that now decides an
	// exit status. Startup's CAS publishes every state write it made, and the shutdown
	// path's CAS observes them. ⚠️ CI runs no -race, so this was proven by hand.
	phase atomic.Int32

	// rootCtx is the cancelable context handed to Initialize/Start; cancel is
	// invoked at the start of shutdown so long-running loops (NATS consumers, the
	// background auth gate) observe cancellation and unwind instead of running on
	// a context that is never cancelled (ADR-022 review E10).
	rootCtx context.Context
	cancel  context.CancelFunc
}

// Create a new microservice instance
func NewMicroservice(callbacks LifecycleCallbacks) *Microservice {
	ms := &Microservice{}
	ms.StartTime = time.Now()
	ms.InstanceId = os.Getenv(ENV_INSTANCE_ID)
	ms.TenantId = os.Getenv(ENV_TENANT_ID)
	ms.TenantName = os.Getenv(ENV_TENANT_NAME)
	ms.MicroserviceId = os.Getenv(ENV_MICROSERVICE_ID)
	ms.MicroserviceName = os.Getenv(ENV_MICROSERVICE_NAME)
	ms.FunctionalArea = os.Getenv(ENV_MS_FUNCTIONAL_AREA)

	// Structured logging (E16): JSON by default for log aggregation, the colorized
	// ConsoleWriter only when DC_LOG_CONSOLE is set (local dev). Every line is
	// stamped with the instance/area (and tenant, when the pod is tenant-scoped) so
	// logs are filterable without threading those fields through every call site.
	if os.Getenv(ENV_LOG_CONSOLE) != "" {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
	} else {
		log.Logger = zerolog.New(os.Stderr).With().Timestamp().Logger()
	}
	baseCtx := log.Logger.With().Str("instance", ms.InstanceId).Str("area", ms.FunctionalArea)
	if ms.TenantId != "" {
		baseCtx = baseCtx.Str("tenant", ms.TenantId)
	}
	log.Logger = baseCtx.Logger()

	// Create common tooling.
	ms.Readiness = NewReadinessGate()

	// Readiness/auth-degrade observability (E17): a gauge that is 1 once the data
	// plane is ready and counters for the background auth-gate attempts/failures,
	// so degraded-for-N is a first-class, alertable app signal.
	ms.readyGauge = ms.NewGauge("ready", "1 when the data plane is ready (auth live), else 0", nil)
	ms.authAttempts = ms.NewCounter("auth_gate_attempts_total", "Background auth-gate JWKS fetch attempts", nil)
	ms.authFailures = ms.NewCounter("auth_gate_failures_total", "Background auth-gate JWKS fetch failures", nil)

	// Create lifecycle manager and channels for tracking shutdown.
	ms.lifecycle = NewLifecycleManager(ms.FunctionalArea, ms, callbacks)
	ms.rootCtx, ms.cancel = context.WithCancel(context.Background())
	ms.outcome = make(chan error, 1)
	ms.shutdown = make(chan os.Signal, 1)

	// Hook interrupt and terminate signals for graceful shutdown
	signal.Notify(ms.shutdown, syscall.SIGINT, syscall.SIGTERM)

	// Async handle shutdown on signals
	go func() {
		sig := <-ms.shutdown
		fmt.Println()
		log.Warn().Msgf("Received signal '%v'. Shutting down gracefully...", sig)
		ms.ShutDownNow()
	}()

	return ms
}

// Prints a banner to the console
func (ms *Microservice) Banner() {
	fmt.Println(color.HiGreenString(`
    ____            _           ________          _     
   / __ \___ _   __(_)_______  / ____/ /_  ____ _(_)___ 
  / / / / _ \ | / / / ___/ _ \/ /   / __ \/ __  / / __ \
 / /_/ /  __/ |/ / / /__/  __/ /___/ / / / /_/ / / / / /
/_____/\___/|___/_/\___/\___/\____/_/ /_/\__,_/_/_/ /_/ 

`))
	// A borderless two-column key/value banner. text/tabwriter (stdlib) aligns the
	// value column across rows, which is all this startup banner needs.
	table := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintf(table, "  Tenant\t%s (%s)\n", ms.TenantName, ms.TenantId)
	fmt.Fprintf(table, "  Microservice\t%s (%s)\n", ms.MicroserviceName, ms.MicroserviceId)
	table.Flush()
	fmt.Println()
}

// The phases a process moves through, held in Microservice.phase. Only one goroutine
// may leave phaseStarting, and whichever does owns what happens next.
const (
	phaseStarting int32 = iota // startup has not finished; nothing exists to tear down
	phaseRunning               // startup succeeded; a shutdown must tear down in full
	phaseStopping              // a shutdown claimed the process
)

// exitProcess is os.Exit behind a variable so the exit decision can be tested. Only a
// test should reassign it, and should restore it — nothing enforces either half; it is
// a package-level var in a package that also holds production code.
var exitProcess = os.Exit

// Run creates the microservice, starts it, and blocks until it is over.
//
// ⚠️ In a real binary its return value is ALWAYS nil, and a caller must not branch on
// it: the only non-nil outcome exits the process before Run can return. The signature
// is what it is because the exit is indirected for testing, and a test does observe
// the error. Writing `if err := ms.Run(); err != nil { … }` produces dead code.
//
// 🔴 THE EXIT LIVES HERE ON PURPOSE, RATHER THAN IN EACH main(). Run has always
// returned an error and every caller discards it, so "return the error and let main
// exit" would change nothing observable, would have to be repeated in every service,
// and would be silently absent from the next service somebody adds. This repo's own
// rule, from command-delivery/presence: one rule that cannot be forgotten beats two
// where the weaker one looks like the protection. This costs nothing that would
// otherwise run — no main() holds a defer, and all teardown happens inside
// ShutDownNow, well before this point.
//
// What a non-zero status actually buys is REPORTING, and it is worth being exact
// about that: the services are Deployments, whose default restartPolicy of Always
// restarts a pod whatever its exit code, so a service refusing its own config was
// already looping. It simply looped reporting Completed — indistinguishable from an
// orderly stop to `kubectl get pods`, to a container-exit alert, and to anyone
// reading the event stream.
func (ms *Microservice) Run() error {
	log.Info().Msg("Creating new microservice and running intialization/startup...")

	go func() {
		ms.Banner()
		if err := ms.InitializeAndStart(); err != nil {
			ms.finished(err)
			return
		}
		// Losing this means a shutdown claimed the process while startup was on its
		// last instructions. It has already reported the outcome and is not waiting
		// for us; the process is going away, and every resource this goroutine
		// acquired goes with it.
		ms.phase.CompareAndSwap(phaseStarting, phaseRunning)
	}()

	return ms.reportOutcome(ms.waitForShutdown())
}

// finished records how the process ended. The first caller wins; later ones are
// dropped rather than blocking (see the finish field).
//
// 🔴 First-wins is only sound because of the phase gate in ShutDownNow, which is what
// makes a nil outcome reachable ONLY after startup succeeded. Without it, tidying up
// after an interrupted startup reported success and the real failure was discarded —
// first-wins was the bug, not the rule. Name the gate before changing either.
func (ms *Microservice) finished(err error) {
	ms.finish.Do(func() { ms.outcome <- err })
}

// reportOutcome logs a failed lifecycle and sets a non-zero exit status for it,
// leaving an orderly shutdown to exit 0.
func (ms *Microservice) reportOutcome(err error) error {
	if err != nil {
		log.Error().Err(err).Msg("Microservice did not complete its lifecycle; exiting with a non-zero status.")
		exitProcess(1)
		// ⚠️ Nothing may be added below this line. In production exitProcess is os.Exit
		// and never returns; under test it does. A statement here would run in every
		// test and never in the field — the two would silently disagree.
	}
	return err
}

// Issue initialize and start commands to microservice
func (ms *Microservice) InitializeAndStart() error {
	startedat := time.Now()
	// Fail closed on a malformed instance id: it is spliced verbatim into every
	// messaging subject ({instanceId}.{tenant}.suffix), the device-plane MQTT topic
	// and HTTP ingest route (ADR-048), and (sanitized) into JetStream stream names,
	// so a metacharacter ('.', '*', '>', '/', '+', '#', '{', '}', whitespace) would
	// shift subject segments, inject a NATS/MQTT wildcard reaching across instances,
	// or malform a route. ValidateToken is exactly this safety alphabet — the same
	// guard tenants get where they are spliced into a subject — enforced once at
	// startup rather than resting on the RFC-1123 namespace-name backstop upstream.
	if err := ValidateToken(ms.InstanceId); err != nil {
		log.Error().Err(err).Str("instanceId", ms.InstanceId).Msg("Invalid instance id (DC_INSTANCE_ID); refusing to start")
		return fmt.Errorf("invalid instance id: %w", err)
	}
	err := ms.Initialize(ms.rootCtx)
	if err != nil {
		log.Error().Err(err).Msg("Unable to initialize microservice")
		return err
	}
	err = ms.Start(ms.rootCtx)
	if err != nil {
		log.Error().Err(err).Msg("Unable to start microservice")
		return err
	}
	elapsed := time.Since(startedat)
	log.Info().Msg(fmt.Sprintf("Microservice started in %s", elapsed.String()))
	return nil
}

// defaultShutdownDrain is the drain window used when DC_SHUTDOWN_DRAIN_SECONDS is
// unset or invalid. ~5s comfortably covers Service-endpoint removal propagation
// while staying well under the chart's default terminationGracePeriodSeconds.
const defaultShutdownDrain = 5 * time.Second

// shutdownDrainDelay resolves the readiness-drain window from the environment,
// falling back to defaultShutdownDrain. A value of 0 disables the drain (useful
// for local single-instance runs where there is no Service to drain from).
func shutdownDrainDelay() time.Duration {
	v := os.Getenv(ENV_SHUTDOWN_DRAIN_SECONDS)
	if v == "" {
		return defaultShutdownDrain
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		log.Warn().Str("value", v).Msgf("Invalid %s; using default drain.", ENV_SHUTDOWN_DRAIN_SECONDS)
		return defaultShutdownDrain
	}
	return time.Duration(n) * time.Second
}

// Issue stop and terminate commands to microservice
func (ms *Microservice) ShutDownNow() {
	// 🔴 A service that never finished starting has nothing to tear down and MUST NOT
	// try. The lifecycle's own state guards do not stop it: they refuse a stop from
	// Uninitialized and Starting but permit one from Initializing and Initialized,
	// which is where a service spends most of its startup — dialing Postgres, running
	// migrations, connecting to the broker. Teardown then ran against components that
	// were never built, and each service's stop callback reaches package-level values
	// that initialization had not yet assigned.
	//
	// It also decided the exit status wrongly, which is why the gate lives here rather
	// than in the lifecycle guards: that teardown SUCCEEDED, reported an orderly
	// shutdown, and took the outcome slot — so a startup failure arriving a moment
	// later was dropped and the process exited 0.
	//
	// One atomic swap answers all three cases and claims the process in the same step,
	// so no ordering against the startup goroutine is left to chance.
	switch ms.phase.Swap(phaseStopping) {
	case phaseStarting:
		// 🔴 THIS REPORTS AN ORDERLY STOP, NOT A FAILURE, and that is a deliberate call.
		// Reaching here means something ASKED this process to stop; not having finished
		// starting was not its own verdict on itself. Kubernetes terminates a
		// still-starting pod for entirely routine reasons — a rollout undone, a node
		// drained, a replica scaled away — and startup here runs for seconds, since it
		// dials Postgres and applies migrations. Exiting non-zero would report a fault
		// on every one of those.
		//
		// It does NOT weaken the case this change was built for. A startup that REFUSES
		// itself — bad config, invalid instance id — reports its own error and needs no
		// signal to do it, so it still exits non-zero. And because the first outcome
		// wins, a refusal that has ALREADY happened can never be overwritten by a stop
		// arriving afterwards. Masking would need the failure to occur strictly after
		// the stop claimed the process — i.e. the service was still trying when it was
		// killed — and reporting the signal is the honest answer there. Nor does a
		// genuinely stuck startup go unreported: the chart's startupProbe fails the pod
		// after failureThreshold × periodSeconds, and Kubernetes says so itself, in
		// events and in the restart count.
		//
		// The readiness drain is skipped for the same reason it exists: it lets endpoint
		// removal propagate before the pod stops serving, and a service that never
		// became ready was never in a Service's endpoints. There is nothing to drain,
		// and no reason to sleep the window before exiting.
		log.Warn().Msg("Asked to shut down before startup completed; nothing to tear down.")
		ms.cancel()
		ms.finished(nil)
		return
	case phaseStopping:
		// A shutdown is already running. Teardown is not idempotent, and the outcome
		// has an owner already.
		log.Warn().Msg("Shutdown already in progress; ignoring.")
		return
	}

	// Zero-downtime drain (methodology §10.2): flip readiness to 503 FIRST so the
	// endpoint controllers pull this pod from Service endpoints, then keep serving
	// for a short window while that removal propagates (kube-proxy is eventually
	// consistent). The scratch service images have no shell for a preStop hook, so
	// this drain is app-side. Only after the window do we cancel and tear down, so
	// in-flight requests are not severed.
	if ms.Readiness != nil {
		ms.Readiness.BeginDrain()
		if d := shutdownDrainDelay(); d > 0 {
			log.Info().Dur("drain", d).Msg("Draining: readiness now reports 503; waiting for endpoint removal to propagate.")
			time.Sleep(d)
		}
	}

	// Cancel the root context first so long-running loops (NATS consumers, the
	// auth gate) observe cancellation and unwind (E10). Stop/Terminate run on
	// fresh contexts so teardown still completes after the cancellation.
	ms.cancel()

	err := ms.Stop(context.Background())
	if err != nil {
		log.Error().Err(err).Msg("Unable to stop microservice")
		// Terminate is deliberately NOT attempted after a failed Stop. Stop restores
		// the previous lifecycle state on any error, and Terminate refuses every state
		// but Stopped, so the only thing a second call could produce here is a second
		// state-guard error burying the first.
		//
		// ⚠️ What that costs, since the reason above explains only why it is pointless:
		// when Stop fails in its Postprocess — after ExecuteStop already succeeded —
		// whatever Terminate would have closed is left to the process exit instead.
		ms.finished(err)
		return
	}
	err = ms.Terminate(context.Background())
	if err != nil {
		log.Error().Err(err).Msg("Unable to terminate microservice")
		ms.finished(err)
		return
	}

	ms.finished(nil)
}

// Wait for microservice to shut down, returning how it ended.
func (ms *Microservice) waitForShutdown() error {
	return <-ms.outcome
}

// LoadInstanceConfiguration reads the instance configuration from the mounted
// config volume. It runs once at startup — a config change is rolled out by the
// chart's checksum annotation restarting the pod, not by an in-place reload (E9),
// hence "Load" rather than "Reload". After decoding it applies defaults and
// validates, failing closed on an invalid instance configuration (E3).
func (ms *Microservice) LoadInstanceConfiguration() error {
	raw, err := os.ReadFile("/etc/dci-config/instance")
	if err != nil {
		return err
	}
	cfg := &config.InstanceConfiguration{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return err
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("instance configuration invalid: %w", err)
	}
	ms.InstanceConfiguration = *cfg
	return nil
}

// LoadMicroserviceConfiguration reads this service's configuration from the
// mounted config volume. Startup-only, like LoadInstanceConfiguration (E9).
func (ms *Microservice) LoadMicroserviceConfiguration() error {
	fa, found := os.LookupEnv(ENV_MS_FUNCTIONAL_AREA)
	if !found {
		return fmt.Errorf("environment variable for functional area (%s) not set", ENV_MS_FUNCTIONAL_AREA)
	}

	// Read config from filesystem.
	cfgbytes, err := os.ReadFile(fmt.Sprintf("%s/%s", MicroserviceConfigDir, fa))
	if err != nil {
		return err
	}
	ms.MicroserviceConfigurationRaw = cfgbytes

	// Log a short hash of the configuration, not its contents (E20): the raw
	// config is a latent home for sensitive values, and a hash is enough to
	// correlate a running pod with its config version. The full document is
	// available only at debug level.
	sum := sha256.Sum256(cfgbytes)
	log.Info().Str("config_sha256", hex.EncodeToString(sum[:])[:16]).Msg("Loaded microservice configuration")
	if log.Debug().Enabled() {
		var fmted bytes.Buffer
		json.Indent(&fmted, cfgbytes, "", "  ")
		log.Debug().Msg(fmt.Sprintf("Microservice configuration:\n\n%s\n", fmted.String()))
	}
	return nil
}

// Create a new counter with the namespace and subsystem auto-filled based on microservice
func (ms *Microservice) NewCounter(name string, help string, labels []string) prometheus.Counter {
	return promauto.NewCounter(prometheus.CounterOpts{
		Namespace: METRICS_NAMESPACE,
		Subsystem: strings.ReplaceAll(ms.FunctionalArea, "-", ""),
		Name:      name,
		Help:      help,
	})
}

// Create a new counter vector with the namespace and subsystem auto-filled based on microservice
func (ms *Microservice) NewCounterVec(name string, help string, labels []string) *prometheus.CounterVec {
	return promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: METRICS_NAMESPACE,
		Subsystem: strings.ReplaceAll(ms.FunctionalArea, "-", ""),
		Name:      name,
		Help:      help,
	}, labels)
}

// Create a new gauge with the namespace and subsystem auto-filled based on microservice
func (ms *Microservice) NewGauge(name string, help string, labels []string) prometheus.Gauge {
	return promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: METRICS_NAMESPACE,
		Subsystem: strings.ReplaceAll(ms.FunctionalArea, "-", ""),
		Name:      name,
		Help:      help,
	})
}

// Create a new gauge vector with the namespace and subsystem auto-filled based on microservice
func (ms *Microservice) NewGaugeVec(name string, help string, labels []string) *prometheus.GaugeVec {
	return promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: METRICS_NAMESPACE,
		Subsystem: strings.ReplaceAll(ms.FunctionalArea, "-", ""),
		Name:      name,
		Help:      help,
	}, labels)
}

// Initialize microservice
func (ms *Microservice) Initialize(ctx context.Context) error {
	return ms.lifecycle.Initialize(ctx)
}

// Initialize microservice (as called by lifecycle manager)
func (ms *Microservice) ExecuteInitialize(ctx context.Context) error {
	// Load instance configuration.
	err := ms.LoadInstanceConfiguration()
	if err != nil {
		return err
	}
	log.Info().Msg("Successfully loaded instance configuration.")

	// Load microservice configuration.
	err = ms.LoadMicroserviceConfiguration()
	if err != nil {
		return err
	}
	log.Info().Msg("Successfully loaded microservice configuration.")
	return nil
}

// Start microservice
func (ms *Microservice) Start(ctx context.Context) error {
	return ms.lifecycle.Start(ctx)
}

// Start microservice (as called by lifecycle manager)
func (ms *Microservice) ExecuteStart(ctx context.Context) error {
	return nil
}

// Stop microservice
func (ms *Microservice) Stop(ctx context.Context) error {
	return ms.lifecycle.Stop(ctx)
}

// Stop microservice (as called by lifecycle manager)
func (ms *Microservice) ExecuteStop(ctx context.Context) error {
	return nil
}

// Terminate microservice
func (ms *Microservice) Terminate(ctx context.Context) error {
	return ms.lifecycle.Terminate(ctx)
}

// Terminate microservice (as called by lifecycle manager)
func (ms *Microservice) ExecuteTerminate(ctx context.Context) error {
	return nil
}

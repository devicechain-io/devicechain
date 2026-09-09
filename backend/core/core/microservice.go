// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
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

// Microservice is the shared body of a DeviceChain service: its identity, its
// configuration, its own metrics registry, its own HTTP mux, and its lifecycle.
//
// 🔴 A STRUCT LITERAL IS A SUPPORTED WAY TO BUILD ONE, AND WHAT SURVIVES IT IS A
// PER-METHOD FACT RATHER THAN A PER-FIELD ONE. Around thirty fixtures in this tree write
// &core.Microservice{…}, two of them exported package-level vars in non-test files, so
// the mode is real and is staying. The list below is what actually holds, and it is
// pinned by TestStructLiteralMicroserviceMethods rather than asserted here:
//
//	Safe — they behave as they do on a constructed Microservice:
//	  Banner, Mux, RegisterProbes, NewHttpServer, MetricsSubsystem, MetricsRegisterer,
//	  MetricsHandler, UseMetricsRegistry, NewCounter, NewCounterVec, NewGauge,
//	  NewGaugeVec, NewProcessorMetrics, LoadInstanceConfiguration,
//	  LoadInstanceConfigurationFrom, LoadMicroserviceConfiguration, ExecuteInitialize,
//	  ExecuteStart, ExecuteStop, ExecuteTerminate, InitializeAndStart, Run, ShutDownNow,
//	  FailNow.
//
//	  The metric constructors are the one place where "safe" is not "identical": with no
//	  registry the collectors are built UNREGISTERED. They still count, so the code under
//	  test behaves the same; they are simply not gatherable. MetricsRegisterer says why
//	  that is the safe direction.
//
//	  The two config loads are safe for a plainer reason: nothing on their path is a
//	  constructor-only field. InstanceConfiguration and MicroserviceConfigurationRaw are
//	  plain value fields, so a struct literal has them, and the load fills them in exactly
//	  as it does on a constructed Microservice. LoadInstanceConfigurationFrom is therefore
//	  not merely non-panicking here but fully FUNCTIONAL, which is why the tests for the
//	  instance document drive it: it reads the path it is handed rather than the chart's
//	  mount point, which is the only part of that pair a test can supply. Its no-argument
//	  sibling is equally safe and, off a pod, simply reports that /etc/dci-config/instance
//	  is not there.
//
//	Refuse, loudly, because any answer they could invent would be indistinguishable from
//	a real one:
//	  Initialize, Start, Stop, Terminate — the zero LifecycleManager has no Component and
//	  no Callbacks. A manufactured one would return nil, i.e. success, for a service that
//	  ran no initializer and started nothing.
//	  MarkReady, MarkReadyWithoutAuthSurface, StartAuthGate, StartInstanceAuthGate — see
//	  readinessGate for why a gate is not conjured up on demand.
//
// A fixture that needs one of those sets the single field it needs, or calls
// NewMicroservice.
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
	//
	// nil on a struct literal, and deliberately NOT created on demand the way mux below
	// is — the methods that need it refuse instead. readinessGate holds the reason: this
	// field is read DIRECTLY by the things that serve traffic, so a gate created behind
	// their backs would not be the one they are consulting.
	Readiness *ReadinessGate

	// metricsReg is the registry every metric this microservice constructs is
	// registered in, and it is a registry this microservice OWNS rather than the
	// process-global default one.
	//
	// The registration key those constructors compose is
	// devicechain_<MetricsSubsystem()>_<name>, and MetricsSubsystem() derives from an
	// environment variable — so on a shared registry the key is ambient environment
	// plus a caller-supplied string, and a duplicate is a MustRegister panic. That
	// made "one Microservice per process, forever" an unstated invariant of a
	// constructor whose name says otherwise, and it had already escaped the library:
	// three places outside core had independently written a unique-area workaround to
	// get around it.
	//
	// nil for a Microservice built as a struct literal instead of by NewMicroservice.
	// See MetricsRegisterer for what that means and why it is the safe direction.
	metricsReg *prometheus.Registry

	// metricsHandedOut records that something has asked where to register, i.e. that a
	// collector may already be sitting on whatever metricsReg was at the time. It is
	// what lets UseMetricsRegistry REFUSE a late call instead of merely documenting
	// that it must not happen.
	//
	// A late call is not a no-op, it is a SPLIT: a collector is registered where it was
	// built and cannot be moved, so the metrics built before the swap stay on the old
	// registry while the gatherer reads the new one. Called after NewMicroservice that
	// strands the three readiness collectors — /metrics answers 200 with a missing
	// `ready` gauge, which is the exact failure this registry ownership exists to close.
	//
	// Atomic because MetricsRegisterer is reachable from any goroutine that builds a
	// metric, and an unsynchronized bool written from two of them is a data race.
	metricsHandedOut atomic.Bool

	// mux is the HTTP multiplexer this microservice owns, created on first use by
	// Mux(). Lazily rather than in NewMicroservice so a Microservice built as a struct
	// literal has one too — unlike the metrics registry above, a private mux carries no
	// collision hazard, since each Microservice gets its own.
	muxOnce sync.Once
	mux     *http.ServeMux

	// Observability metrics (E17). nil when the microservice was built without
	// NewMicroservice (e.g. in unit tests), so every use of THESE THREE is nil-guarded.
	//
	// ⚠️ That is a statement about these three fields and nothing else. Read as a
	// statement about the type it is false, and it was: four other constructor-only
	// fields carried no such guarantee. The per-method boundary is on Microservice above.
	readyGauge   prometheus.Gauge
	authAttempts prometheus.Counter
	authFailures prometheus.Counter

	// Internal lifeycle processing.
	//
	// lifecycle is a VALUE, so a struct literal holds a zero LifecycleManager rather than
	// a nil one — no Component and no Callbacks, which is what makes Initialize, Start,
	// Stop and Terminate panic on one. See Microservice above for why they are left that way.
	lifecycle LifecycleManager
	shutdown  chan os.Signal

	// outcome carries HOW the process ended: nil for an orderly shutdown, non-nil
	// when startup was refused or teardown failed. Run turns it into an exit status.
	//
	// 🔴 It is deliberately not the `chan bool` it used to be. That channel's value was
	// always true, so it recorded that the process had stopped while discarding the only
	// bit anyone can act on — which is how a service that refused its own configuration
	// came to exit 0 and report Completed.
	//
	// Created by outcomeCh on first use, and by nothing else — see that method for why
	// building it in NewMicroservice put the defect back through the field that fixes it.
	outcomeOnce sync.Once
	outcome     chan error

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
	// service that did not exist yet. Stop's state guards permitted it — they were deny
	// lists, so every state nobody named was legal by omission — and initialization is
	// where a service spends most of its startup. So Stop and Terminate both SUCCEEDED,
	// reported an orderly shutdown, and took the outcome slot — and a startup failure
	// arriving a moment later was dropped, exiting 0. That is the exact defect the
	// outcome channel exists to fix, reachable through the channel meant to fix it.
	//
	// Those guards are allow lists now and refuse a stop from Initializing, but that is
	// a second line and NOT a replacement for this field. A stop from Initialized is
	// still permitted — it has to be, for reasons lifecycle.go sets out — and the guards
	// can only ever refuse a teardown, never decide whether the process ends orderly or
	// failed, nor whether a readiness drain is owed. Those are this field's job.
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

	// This microservice's own metrics registry. It is created BEFORE the metrics
	// below, because a metric constructed while this is nil is not registered
	// anywhere and would be missing from /metrics with nothing to say so.
	ms.metricsReg = prometheus.NewRegistry()

	// Readiness/auth-degrade observability (E17): a gauge that is 1 once the data
	// plane is ready and counters for the background auth-gate attempts/failures,
	// so degraded-for-N is a first-class, alertable app signal.
	ms.readyGauge = ms.NewGauge("ready", "1 when the data plane is ready (auth live), else 0", nil)
	ms.authAttempts = ms.NewCounter("auth_gate_attempts_total", "Background auth-gate JWKS fetch attempts", nil)
	ms.authFailures = ms.NewCounter("auth_gate_failures_total", "Background auth-gate JWKS fetch failures", nil)

	// Create lifecycle manager and channels for tracking shutdown.
	ms.lifecycle = NewLifecycleManager(ms.FunctionalArea, ms, callbacks)
	ms.rootCtx, ms.cancel = context.WithCancel(context.Background())
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
	ms.finish.Do(func() { ms.outcomeCh() <- err })
}

// outcomeCh is the channel finished sends on and waitForShutdown receives from, created
// on first use so that BOTH ways of building a Microservice have one. It is the only
// place it is created, for the same reason Mux is the only place the mux is.
//
// 🔴 BUILT IN NewMicroservice INSTEAD, IT IS NIL ON A STRUCT LITERAL — AND A NIL CHANNEL
// DOES NOT FAIL, IT BLOCKS. finished sends inside a sync.Once, so the send parks forever
// and every later caller then parks on that Once's mutex; waitForShutdown parks on the
// receive. Run therefore logged its startup error and hung with no further output, which
// is the exact defect this channel was introduced to fix, reachable through the field
// that fixes it.
//
// A test could work around that by hand-rolling the channel, and several did — a
// workaround that only the tests which already knew about it were carrying, in a
// constructor no service calls.
func (ms *Microservice) outcomeCh() chan error {
	ms.outcomeOnce.Do(func() { ms.outcome = make(chan error, 1) })
	return ms.outcome
}

// cancelRoot cancels the root context if this Microservice has one.
//
// A struct literal has none, and it has none because nothing was ever launched on it —
// so there is nothing to unwind and the guard skips work that does not exist rather than
// substituting a value for it. Before it, every path into shutDown panicked there.
func (ms *Microservice) cancelRoot() {
	if ms.cancel != nil {
		ms.cancel()
	}
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
	// A removed environment variable that is still being SET is refused rather than
	// ignored. Ignoring it is the silent half of the defect this key was moved into
	// typed config to close: the deployment that wrote a drain window would get the
	// default instead of the number it asked for, with nothing to say so, and the
	// symptom would be a shutdown behaving differently from the one configured.
	if v, ok := os.LookupEnv(ENV_REMOVED_SHUTDOWN_DRAIN_SECONDS); ok {
		err := fmt.Errorf("%s is set (%q) but is no longer read: the drain window is instance "+
			"configuration now. Set shutdownDrainSeconds in the chart values — or "+
			"infrastructure.shutdown.drainSeconds directly, in a hand-written instance document "+
			"— and remove this variable", ENV_REMOVED_SHUTDOWN_DRAIN_SECONDS, v)
		log.Error().Err(err).Msg("Refusing to start on configuration that would be ignored")
		return err
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

// drainSleep is time.Sleep behind a variable, for the same reason exitProcess is
// os.Exit behind one: a test has to be able to observe the window a shutdown asked
// for without spending it. Only a test should reassign it, and should restore it.
//
// A test that measured the drain by actually sleeping it would be measuring a
// number it chose to keep small, which is the one number an operator never
// configures.
var drainSleep = time.Sleep

// shutdownTeardownMargin is what the teardown budget deliberately leaves unspent:
// enough for the process to log its outcome and set an exit status before the kubelet's
// clock runs out. Reaching the kubelet's bound instead means SIGKILL, which is a bare
// 137 with nothing in the log saying which component was still working — so the whole
// value of the budget is in ending a few seconds EARLY.
const shutdownTeardownMargin = 2 * time.Second

// minShutdownTeardownBudget keeps a pathologically small grace period from producing a
// budget of zero, which would abandon teardown before it had begun. A budget this small
// will not finish, but it will try, and it will say so.
const minShutdownTeardownBudget = time.Second

// teardownBudget is the total time Stop plus Terminate are given, measured from the
// moment the readiness drain ends.
//
// It is DERIVED from the drain window and the grace period rather than being a third
// number to keep in step with them. Those two are already one budget — the config
// refuses a drain longer than half the grace period, on the argument that the drain
// only waits while the teardown after it is what does the work — and this is the
// remainder of that same budget. A fixed constant would have been wrong in both
// directions: too long for a raised drain, and no longer for a raised grace period.
//
// A zero ShutdownConfiguration reads as the defaults, which is what a Microservice
// built as a struct literal has; DrainWindow does the same, and for the same reason.
func (ms *Microservice) teardownBudget() time.Duration {
	shutdown := ms.InstanceConfiguration.Infrastructure.Shutdown
	grace := shutdown.TerminationGracePeriodSeconds
	if grace <= 0 {
		grace = config.DefaultTerminationGracePeriodSeconds
	}
	budget := time.Duration(grace)*time.Second - shutdown.DrainWindow() - shutdownTeardownMargin
	if budget < minShutdownTeardownBudget {
		return minShutdownTeardownBudget
	}
	return budget
}

// teardown runs Stop and then Terminate under ctx, and returns when they finish OR
// when ctx's budget runs out, whichever comes first.
//
// 🔴 THE BUDGET IS ENFORCED HERE AS WELL AS BEING PASSED DOWN, and both halves are
// load-bearing. Passing it down is what lets a component unwind cleanly and early —
// the NATS manager's stop bounds its sampler join on it, RetryInfraConnect returns
// the context error rather than spending its budget. But a component that does not
// consult its context at all would make the deadline decorative: the call would still
// block, and a deadline nothing observes reads as a bound while being none. The select
// below is what makes the bound hold regardless of who honours what.
//
// Abandoning teardown mid-flight is a real cost and it is the smaller one. The
// alternative at this point is not "teardown completes" — it is SIGKILL a few seconds
// later, which abandons exactly the same work while also destroying the process's
// ability to say so. The goroutine is left running deliberately; the process is on its
// way out and its remaining life is measured in milliseconds.
func (ms *Microservice) teardown(ctx context.Context) error {
	done := make(chan error, 1)
	go func() {
		if err := ms.Stop(ctx); err != nil {
			log.Error().Err(err).Msg("Unable to stop microservice")
			// Terminate is deliberately NOT attempted after a failed Stop. Stop restores
			// the previous lifecycle state on any error, and Terminate refuses every state
			// but Stopped, so the only thing a second call could produce here is a second
			// state-guard error burying the first.
			//
			// ⚠️ What that costs, since the reason above explains only why it is pointless:
			// when Stop fails in its Postprocess — after ExecuteStop already succeeded —
			// whatever Terminate would have closed is left to the process exit instead.
			done <- err
			return
		}
		if err := ms.Terminate(ctx); err != nil {
			log.Error().Err(err).Msg("Unable to terminate microservice")
			done <- err
			return
		}
		done <- nil
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		err := fmt.Errorf("core: teardown did not finish within %s: %w", ms.teardownBudget(), ctx.Err())
		log.Error().Err(err).Msg("Teardown exceeded its budget; exiting without waiting for it to finish. " +
			"A component is blocked on a dependency that is not answering.")
		return err
	}
}

// Issue stop and terminate commands to microservice
//
// It reports an ORDERLY stop (exit 0). A component that has decided the process is
// no longer fit to run must call FailNow instead.
func (ms *Microservice) ShutDownNow() { ms.shutDown(nil) }

// FailNow tears the process down exactly as ShutDownNow does and then exits NON-ZERO.
//
// 🔴 IT EXISTS BECAUSE A COMPONENT THAT FAILS AFTER STARTUP HAD NO WAY TO SAY SO. A
// failure during InitializeAndStart is returned, reported and exits 1; a failure a
// minute later had only ShutDownNow, which reports an orderly stop. So a component
// that had irrecoverably stopped doing its job could either keep a Ready pod alive
// doing nothing, or exit 0 — indistinguishable from a rollout. DETECT losing its
// leadership supervisor is the first real instance: with replicas:1 nothing else
// takes the partition, so the pod must go away and be replaced rather than sit there.
//
// The distinction is worth being exact about, because the restartPolicy makes the
// pod come back either way: what a non-zero status buys is REPORTING. Exit 0 looks
// like an orderly stop to `kubectl get pods`, to a container-exit alert and to anyone
// reading the event stream; exit 1 does not.
//
// err must be non-nil. A nil here would silently become an orderly stop, which is the
// one thing a caller reaching for this method does not want.
func (ms *Microservice) FailNow(err error) {
	if err == nil {
		err = errors.New("core: a component ended the process without saying why")
	}
	log.Error().Err(err).Msg("A component has declared this process unfit to continue; shutting down with a non-zero status.")
	ms.shutDown(err)
}

func (ms *Microservice) shutDown(fatal error) {
	// 🔴 A service that never finished starting has nothing to tear down and MUST NOT
	// try. The lifecycle's own state guards do not stop it: a stop from Initialized is
	// permitted, deliberately and for reasons lifecycle.go sets out, and Initialized is
	// where a service lands the moment it finishes dialing Postgres and running
	// migrations. Teardown then ran against components that were never built, and each
	// service's stop callback reaches package-level values that initialization had not
	// yet assigned.
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
		ms.cancelRoot()
		// nil for a signal-driven stop, which is not this process's verdict on itself;
		// non-nil when a component called FailNow, which is.
		ms.finished(fatal)
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
		shutdown := ms.InstanceConfiguration.Infrastructure.Shutdown
		if d := shutdown.DrainWindow(); d > 0 {
			log.Info().
				Dur("drain", d).
				Int("terminationGracePeriodSeconds", shutdown.TerminationGracePeriodSeconds).
				Msg("Draining: readiness now reports 503; waiting for endpoint removal to propagate.")
			startedDraining := time.Now()
			drainSleep(d)
			// The window ENDING is logged as well as its beginning, because those are
			// the two lines whose absence tells an operator what happened. A pod
			// SIGKILLed mid-drain — the failure the config validation now refuses up
			// front — prints the first line and never the second, and that is the only
			// evidence it leaves. Nothing is abandoned at the boundary: the window is a
			// fixed wait, not a deadline on in-flight work, and the teardown below is
			// what finishes the requests still running.
			log.Info().
				Dur("elapsed", time.Since(startedDraining)).
				Msg("Drain window elapsed; tearing down. In-flight work is finished by the shutdown that follows, not severed here.")
		}
	}

	// Cancel the root context first so long-running loops (NATS consumers, the
	// auth gate) observe cancellation and unwind (E10). Teardown runs on a context
	// DETACHED from the root — it must still complete after that cancellation — but
	// detached is not the same as unbounded, so it carries its own deadline.
	ms.cancelRoot()

	ctx, cancel := context.WithTimeout(context.Background(), ms.teardownBudget())
	defer cancel()
	if err := ms.teardown(ctx); err != nil {
		ms.finished(err)
		return
	}

	// A clean teardown does not make a FailNow orderly: the reason the process is
	// going away is the caller's error, not how well it packed up.
	ms.finished(fatal)
}

// Wait for microservice to shut down, returning how it ended.
func (ms *Microservice) waitForShutdown() error {
	return <-ms.outcomeCh()
}

// InstanceConfigPath is where the chart mounts the instance-wide configuration
// document. The chart renders it into a Secret (it carries persistence credentials and
// the secret-store root key) and mounts that Secret at /etc/dci-config on every pod.
const InstanceConfigPath = "/etc/dci-config/instance"

// LoadInstanceConfiguration reads the instance configuration from the mounted
// config volume. It runs once at startup — a config change is rolled out by the
// chart's checksum annotation restarting the pod, not by an in-place reload (E9),
// hence "Load" rather than "Reload". After decoding it applies defaults and
// validates, failing closed on an invalid instance configuration (E3).
//
// It goes through LoadConfiguration, the same strict decode the per-service documents
// use, so an unknown key here is refused rather than discarded. It used to be a plain
// json.Unmarshal, which meant this — the OPERATOR-FACING document, the one the docs site
// tells people to edit — was the one path that silently dropped a misspelled key and
// started healthy on the default. That is worse than never having set the key: the
// setting was deliberately chosen, written down and deployed, so it is believed to be in
// force and will be trusted in an incident. It also defeated every value check in this
// document, each of which refuses a bad VALUE and never sees a mistyped NAME.
func (ms *Microservice) LoadInstanceConfiguration() error {
	return ms.LoadInstanceConfigurationFrom(InstanceConfigPath)
}

// LoadInstanceConfigurationFrom is LoadInstanceConfiguration against an explicit path,
// for a caller whose mount is not at the chart's, and for the tests that exercise this
// wiring — which is the point of the seam rather than a side effect of it. What has to be
// gated here is that this function reaches the STRICT loader; a test that called
// LoadConfiguration itself would pass just as happily with the plain json.Unmarshal this
// replaced still sitting in the line below.
func (ms *Microservice) LoadInstanceConfigurationFrom(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	cfg := &config.InstanceConfiguration{}
	if err := LoadConfiguration(raw, cfg); err != nil {
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
	return promauto.With(ms.MetricsRegisterer()).NewCounter(prometheus.CounterOpts{
		Namespace: METRICS_NAMESPACE,
		Subsystem: ms.MetricsSubsystem(),
		Name:      name,
		Help:      help,
	})
}

// Create a new counter vector with the namespace and subsystem auto-filled based on microservice
func (ms *Microservice) NewCounterVec(name string, help string, labels []string) *prometheus.CounterVec {
	return promauto.With(ms.MetricsRegisterer()).NewCounterVec(prometheus.CounterOpts{
		Namespace: METRICS_NAMESPACE,
		Subsystem: ms.MetricsSubsystem(),
		Name:      name,
		Help:      help,
	}, labels)
}

// Create a new gauge with the namespace and subsystem auto-filled based on microservice
func (ms *Microservice) NewGauge(name string, help string, labels []string) prometheus.Gauge {
	return promauto.With(ms.MetricsRegisterer()).NewGauge(prometheus.GaugeOpts{
		Namespace: METRICS_NAMESPACE,
		Subsystem: ms.MetricsSubsystem(),
		Name:      name,
		Help:      help,
	})
}

// Create a new gauge vector with the namespace and subsystem auto-filled based on microservice
func (ms *Microservice) NewGaugeVec(name string, help string, labels []string) *prometheus.GaugeVec {
	return promauto.With(ms.MetricsRegisterer()).NewGaugeVec(prometheus.GaugeOpts{
		Namespace: METRICS_NAMESPACE,
		Subsystem: ms.MetricsSubsystem(),
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

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
	"syscall"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/olekukonko/tablewriter"
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
	done      chan bool

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
	ms.done = make(chan bool, 1)
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
	table := tablewriter.NewWriter(os.Stdout)
	table.SetBorder(false)
	table.SetAutoWrapText(false)
	data := [][]string{
		{"Tenant", fmt.Sprintf("%s (%s)", ms.TenantName, ms.TenantId)},
		{"Microservice", fmt.Sprintf("%s (%s)", ms.MicroserviceName, ms.MicroserviceId)},
	}
	for _, v := range data {
		table.Append(v)
	}
	table.Render()
	fmt.Println()
}

// Create microservice and initialize/start it.
func (ms *Microservice) Run() error {
	log.Info().Msg("Creating new microservice and running intialization/startup...")

	go func() {
		ms.Banner()
		err := ms.InitializeAndStart()
		if err != nil {
			ms.done <- true
		}
	}()

	ms.waitForShutdown()
	return nil
}

// Issue initialize and start commands to microservice
func (ms *Microservice) InitializeAndStart() error {
	startedat := time.Now()
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
		ms.done <- true
		return
	}
	err = ms.Terminate(context.Background())
	if err != nil {
		log.Error().Err(err).Msg("Unable to terminate microservice")
		ms.done <- true
		return
	}

	ms.done <- true
}

// Wait for microservice to shut down
func (ms *Microservice) waitForShutdown() {
	<-ms.done
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
	cfgbytes, err := os.ReadFile(fmt.Sprintf("/etc/dct-config/%s", fa))
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

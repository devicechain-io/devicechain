/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/olekukonko/tablewriter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/bsm/redislock"
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

	// Common microservice tooling
	Redis *RedisManager

	// Internal lifeycle processing
	lifecycle LifecycleManager
	shutdown  chan os.Signal
	done      chan bool
}

// Create a new microservice instance
func NewMicroservice(callbacks LifecycleCallbacks) *Microservice {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	ms := &Microservice{}
	ms.StartTime = time.Now()
	ms.InstanceId = os.Getenv(ENV_INSTANCE_ID)
	ms.TenantId = os.Getenv(ENV_TENANT_ID)
	ms.TenantName = os.Getenv(ENV_TENANT_NAME)
	ms.MicroserviceId = os.Getenv(ENV_MICROSERVICE_ID)
	ms.MicroserviceName = os.Getenv(ENV_MICROSERVICE_NAME)
	ms.FunctionalArea = os.Getenv(ENV_MS_FUNCTIONAL_AREA)

	// Create common tooling.
	ms.Redis = NewRedisManager(ms, NewNoOpLifecycleCallbacks())

	// Create lifecycle manager and channels for tracking shutdown.
	ms.lifecycle = NewLifecycleManager(ms.FunctionalArea, ms, callbacks)
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
	err := ms.Initialize(context.Background())
	if err != nil {
		log.Error().Err(err).Msg("Unable to initialize microservice")
		return err
	}
	err = ms.Start(context.Background())
	if err != nil {
		log.Error().Err(err).Msg("Unable to start microservice")
		return err
	}
	elapsed := time.Since(startedat)
	log.Info().Msg(fmt.Sprintf("Microservice started in %s", elapsed.String()))
	return nil
}

// Issue stop and terminate commands to microservice
func (ms *Microservice) ShutDownNow() {
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

// Use Redis to get a lock across all microservice replicas.
func (ms *Microservice) WithDistributedLock(ctx context.Context, duration time.Duration, retries int,
	logic func(ctx context.Context) error) error {
	log.Info().Msg(fmt.Sprintf("Getting distributed lock for %s with duration %+v and %d retries...",
		ms.FunctionalArea, duration, retries))
	lock, err := ms.Redis.RedisLock.Obtain(ctx, ms.FunctionalArea, duration, &redislock.Options{
		RetryStrategy: redislock.LimitRetry(redislock.LinearBackoff(duration), retries),
	})
	if err == redislock.ErrNotObtained {
		return err
	}
	defer lock.Release(ctx)

	log.Info().Msg("Lock obtained. Running guarded logic.")
	return logic(ctx)
}

// Wait for microservice to shut down
func (ms *Microservice) waitForShutdown() {
	<-ms.done
}

// Reloads instance configuration from configmap volume mapping
func (ms *Microservice) ReloadInstanceConfiguration() error {
	bytes, err := os.ReadFile("/etc/dci-config/instance")
	if err != nil {
		return err
	}
	config := &config.InstanceConfiguration{}
	err = json.Unmarshal(bytes, config)
	if err != nil {
		return err
	}
	ms.InstanceConfiguration = *config
	return nil
}

// Reloads microservice configuration from configmap volume mapping
func (ms *Microservice) ReloadMicroserviceConfiguration() error {
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

	// Print configuration to log as json.
	var fmted bytes.Buffer
	json.Indent(&fmted, ms.MicroserviceConfigurationRaw, "", "  ")
	log.Info().Msg(fmt.Sprintf("Using configuration:\n\n%s\n", fmted.String()))
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
	err := ms.ReloadInstanceConfiguration()
	if err != nil {
		return err
	}
	log.Info().Msg("Successfully loaded instance configuration.")

	// Load microservice configuration.
	err = ms.ReloadMicroserviceConfiguration()
	if err != nil {
		return err
	}
	log.Info().Msg("Successfully loaded microservice configuration.")

	// Initialize Redis connectivity.
	err = ms.Redis.Initialize(ctx)
	return err
}

// Start microservice
func (ms *Microservice) Start(ctx context.Context) error {
	return ms.lifecycle.Start(ctx)
}

// Start microservice (as called by lifecycle manager)
func (ms *Microservice) ExecuteStart(ctx context.Context) error {
	// Execute Redis startup.
	err := ms.Redis.Start(ctx)
	return err
}

// Stop microservice
func (ms *Microservice) Stop(ctx context.Context) error {
	return ms.lifecycle.Stop(ctx)
}

// Stop microservice (as called by lifecycle manager)
func (ms *Microservice) ExecuteStop(ctx context.Context) error {
	// Execute Redis shutdown.
	err := ms.Redis.Stop(ctx)
	return err
}

// Terminate microservice
func (ms *Microservice) Terminate(ctx context.Context) error {
	return ms.lifecycle.Terminate(ctx)
}

// Terminate microservice (as called by lifecycle manager)
func (ms *Microservice) ExecuteTerminate(ctx context.Context) error {
	// Execute Redis termination.
	err := ms.Redis.Terminate(ctx)
	return err
}

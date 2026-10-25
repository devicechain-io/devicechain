// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command dc-broker-admin is the broker administrator: the one process that creates, updates and
// deletes the platform's JetStream streams and KV buckets.
//
// The chart runs it as a pre-install/pre-upgrade hook Job, once, before any service starts or
// rolls, over the broker-administration login that only that Job mounts. Services never create or
// change a stream or a bucket; they verify what they open against core/streams and core/kv and
// stop, naming what differs, when it does not match. `dcctl broker reconcile` runs the same Job
// again for recovery (a stream lost at runtime, a bucket deleted by hand).
//
// It does what every service's start used to do for the streams it touched: create what is
// missing, put back the subjects, duplicate window, discard policy and ceilings of what is there,
// lift the replica factor, and (for the one declared case) wait for a reader to drain a stream
// created under an older retention before recreating it. It is idempotent. It holds no right to
// publish into any stream and is not a service: it serves nothing, and it exits.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// Build metadata, stamped with -ldflags -X when released; the defaults are what an un-stamped
// build reports.
var (
	version   = "dev"
	gitCommit = "unknown"
)

func main() {
	log.Logger = zerolog.New(os.Stderr).With().Timestamp().Str("area", "broker-admin").Logger()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "dc-broker-admin: %v\n", err)
		os.Exit(1)
	}
}

// reconcileRun is what reconcile needs from the world, so a test can drive it without a process.
type reconcileRun struct {
	instanceID string
	configPath string
	areas      []string
	timeout    time.Duration
}

func run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: dc-broker-admin reconcile --areas a,b,c | version")
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(out, "dc-broker-admin %s (commit %s)\n", version, gitCommit)
		return nil
	case "reconcile":
		r, err := parseReconcile(args[1:], getenv)
		if err != nil {
			return err
		}
		return r.run(ctx, getenv, out)
	default:
		return fmt.Errorf("unknown command %q (known: reconcile, version)", args[0])
	}
}

func parseReconcile(args []string, getenv func(string) string) (*reconcileRun, error) {
	fs := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	r := &reconcileRun{}
	var areas string
	fs.StringVar(&areas, "areas", "", "comma-separated functional areas the installation deploys")
	fs.StringVar(&r.configPath, "config", core.InstanceConfigPath, "the instance configuration document")
	fs.DurationVar(&r.timeout, "timeout", 8*time.Minute, "how long the whole reconcile may take, including waiting for a retention migration to drain")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	r.instanceID = getenv(core.ENV_INSTANCE_ID)
	if r.instanceID == "" {
		return nil, fmt.Errorf("%s is empty: the administrator reconciles one instance's streams and has to be told which", core.ENV_INSTANCE_ID)
	}
	for _, a := range strings.Split(areas, ",") {
		if a = strings.TrimSpace(a); a != "" {
			r.areas = append(r.areas, a)
		}
	}
	if len(r.areas) == 0 {
		return nil, errors.New("--areas is empty: there is nothing to reconcile for an installation with no areas")
	}
	return r, nil
}

func (r *reconcileRun) run(ctx context.Context, getenv func(string) string, out io.Writer) error {
	ms := &core.Microservice{InstanceId: r.instanceID}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	if err := ms.LoadInstanceConfigurationFrom(r.configPath); err != nil {
		return fmt.Errorf("loading the instance configuration: %w", err)
	}
	if err := ms.InstanceConfiguration.Infrastructure.Nats.Auth.ApplyAdminLogin(getenv); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	admin := messaging.NewBrokerAdmin(ms)
	if err := admin.ExecuteInitialize(ctx); err != nil {
		return fmt.Errorf("connecting to the broker as the broker administrator: %w", err)
	}
	defer admin.Close()

	report, err := admin.ReconcileBroker(ctx, r.areas)
	// What was done is printed even when the pass failed part of the way: the hook's log is what
	// the operator reads, and "stopped after these" is half the diagnosis.
	for _, a := range report.Actions {
		fmt.Fprintf(out, "%-9s %-6s %s\n", a.Action, a.Kind, a.Name)
	}
	if err != nil {
		return fmt.Errorf("reconciling the broker for areas %v: %w", r.areas, err)
	}
	fmt.Fprintf(out, "reconciled %d streams and buckets for %d areas (%d created, %d updated, %d migrated)\n",
		len(report.Actions), len(r.areas), report.Count("created"), report.Count("updated"), report.Count("migrated"))
	return nil
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command dc-edge-agent is the ADR-068 Tier-1 durable store-and-forward edge agent:
// a small standalone binary (NOT the platform — no Kubernetes, Postgres, or NATS
// cluster) that terminates the golden MQTT path locally, buffers device telemetry,
// and forwards it to a cloud Instance. See the module README for the slice status.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/devicechain-io/dc-microservice/core"

	"github.com/devicechain-io/dc-edge-agent/agent"
	"github.com/devicechain-io/dc-edge-agent/config"
)

// Build metadata, stamped by goreleaser via -ldflags -X (see .goreleaser.yaml). The
// defaults are what an un-stamped `go build` / container image reports; a released binary
// carries the tag. Kept as plain package vars (not a version package) — the agent is a
// single small command.
var (
	version   = "dev"
	gitCommit = "unknown"
	buildDate = "unknown"
)

func main() {
	if err := rootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCommand() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:           "dc-edge-agent",
		Short:         "DeviceChain durable store-and-forward edge agent (ADR-068 Tier 1)",
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd.Context(), configPath)
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "path to the agent configuration file (JSON)")
	_ = cmd.MarkFlagRequired("config")
	cmd.AddCommand(versionCommand())
	return cmd
}

// versionCommand prints build metadata. A released binary sitting on an edge box for months
// is exactly where "which build is this?" matters, so it is a first-class subcommand.
func versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build version, commit, and date",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "dc-edge-agent %s (commit %s, built %s)\n", version, gitCommit, buildDate)
		},
	}
}

// run loads and validates the config (fail-closed), then runs the agent until a
// termination signal arrives.
func run(ctx context.Context, configPath string) error {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	raw, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read config %q: %w", configPath, err)
	}
	var cfg config.Configuration
	if err := core.LoadConfiguration(raw, &cfg); err != nil {
		return err // already fail-closed with a specific reason
	}

	a, err := agent.New(cfg, log)
	if err != nil {
		return err
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return a.Run(ctx)
}

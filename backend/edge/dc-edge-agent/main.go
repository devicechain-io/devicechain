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
	return cmd
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

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command playground drives the hosted playground's seeded tenants. It is a
// maintainer-only tool: it is in the workspace so it is built, vetted and tested, and it
// ships in no release. See README.md.
//
//	playground run       rotate to today's slot, seed it, keep its simulators running
//	playground plan      print the rotation for an instant, changing nothing
//	playground restart   roll the driver Deployment (the nightly CronJob)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: playground run|plan|restart [flags]")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	scenarios := fs.String("scenarios", env("DC_PLAYGROUND_SCENARIOS", "sitepulse,buildingpulse,fleetpulse"), "comma-separated dc-simulator scenarios to seed")
	slots := fs.Int("slots", envInt("DC_PLAYGROUND_SLOTS", MinSlots), "rotation slots (minimum 3)")
	resetHour := fs.Int("reset-hour", envInt("DC_PLAYGROUND_RESET_HOUR", 3), "UTC hour the playground's day begins")
	seed := fs.Int64("seed", int64(envInt("DC_PLAYGROUND_SEED", 1)), "sim generation seed")
	dcctl := fs.String("dcctl", env("DC_PLAYGROUND_DCCTL", "dcctl"), "dcctl binary")
	simulator := fs.String("simulator", env("DC_PLAYGROUND_SIMULATOR", "dc-simulator"), "dc-simulator binary")
	port := fs.Int("control-port", envInt("DC_PLAYGROUND_CONTROL_PORT", 8090), "first sim control port")
	// Beside the sim records' parent, not inside ~/.devicechain, whose entries dcctl reads
	// as instances.
	ledger := fs.String("ledger", env("DC_PLAYGROUND_LEDGER", filepath.Join(os.Getenv("HOME"), "playground-days.json")), "run: file recording the playground day each sim was created on")
	at := fs.String("at", "", "plan: the instant to plan for (RFC3339); default now")
	deployment := fs.String("deployment", env("DC_PLAYGROUND_DEPLOYMENT", "playground-driver"), "restart: the Deployment to roll")
	_ = fs.Parse(args)

	cfg := Config{Scenarios: splitList(*scenarios), Slots: *slots, ResetHour: *resetHour, Seed: *seed}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "plan":
		if err := cfg.Validate(); err != nil {
			fatal(err)
		}
		t := time.Now()
		if *at != "" {
			var err error
			if t, err = time.Parse(time.RFC3339, *at); err != nil {
				fatal(err)
			}
		}
		_ = json.NewEncoder(os.Stdout).Encode(PlanRotation(t, cfg))
	case "run":
		d := &Driver{
			Cfg: cfg, Dcctl: *dcctl, Simulator: *simulator,
			ReachArgs:       strings.Fields(os.Getenv("DC_PLAYGROUND_REACH_ARGS")),
			CreateArgs:      strings.Fields(os.Getenv("DC_PLAYGROUND_CREATE_ARGS")),
			BaseControlPort: *port, Runner: execRunner{WaitDelay: 30 * time.Second}, Log: os.Stderr,
			Days: NewDayLedger(*ledger),
		}
		if err := d.Run(ctx, time.Now); err != nil {
			fatal(err)
		}
	case "restart":
		r, err := InClusterRestarter(*deployment)
		if err != nil {
			fatal(err)
		}
		if err := r.Restart(ctx, time.Now()); err != nil {
			fatal(err)
		}
		fmt.Printf("rolled deployment %s/%s\n", r.Namespace, r.Deployment)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		os.Exit(2)
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "playground:", err)
	os.Exit(1)
}

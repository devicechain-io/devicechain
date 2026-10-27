// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command ota-boot is the launcher: this example's stand-in for a bootloader. It picks the slot to
// run, gives a freshly installed slot exactly one try, and falls back to the last confirmed slot
// if the new one does not confirm itself. It is never updated over the air.
//
//	ota-boot --root DIR [--confirm-timeout 2m] -- <arguments passed to ota-device>
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"devicechain.io/examples/ota-device/internal/boot"
)

func main() {
	root := flag.String("root", "", "state directory holding boot.json and slots/")
	confirm := flag.Duration("confirm-timeout", 2*time.Minute, "kill a freshly installed slot that has not confirmed itself within this time")
	flag.Parse()
	if *root == "" {
		fmt.Fprintln(os.Stderr, "ota-boot: --root is required")
		os.Exit(2)
	}
	lg := log.New(os.Stderr, "ota-boot: ", log.LstdFlags)
	if err := boot.Init(*root); err != nil {
		lg.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	sup := &boot.Supervisor{
		Root: *root, Run: boot.ProcessRunner(*root, flag.Args(), *confirm, nil),
		Log:        func(f string, a ...any) { lg.Printf(f, a...) },
		MinBackoff: 500 * time.Millisecond, MaxBackoff: 30 * time.Second,
	}
	if _, err := sup.Loop(ctx, 0); err != nil {
		lg.Fatal(err)
	}
}

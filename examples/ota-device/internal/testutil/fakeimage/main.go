// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command fakeimage is a stand-in for a firmware image, built by the tests with different linker
// settings to play the part of a good image, a crashing one, a hanging one and one that lies about
// what it is. It is a test instrument and is never shipped.
//
// Asked to `describe` it appends its arguments to ran.marker beside itself, so a test can prove
// whether, and how many times, an image was ever executed.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"devicechain.io/examples/ota-device/internal/boot"
)

// Set by the linker.
var (
	version = "0.0.0"
	product = "ota-device-example"
	mode    = "good" // good | crash | hang
)

func main() {
	if self, err := os.Executable(); err == nil {
		f, err := os.OpenFile(filepath.Join(filepath.Dir(self), "ran.marker"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			fmt.Fprintln(f, strings.Join(os.Args[1:], " "))
			f.Close()
		}
	}
	if len(os.Args) == 2 && os.Args[1] == "describe" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"product": product, "version": version})
		return
	}
	switch mode {
	case "crash":
		os.Exit(1)
	case "hang":
		time.Sleep(time.Hour)
	default:
		root, slot := os.Getenv("OTA_ROOT"), os.Getenv("OTA_SLOT")
		if root == "" || slot == "" {
			os.Exit(3)
		}
		if _, err := boot.Confirm(root, slot); err != nil {
			os.Exit(4)
		}
	}
}

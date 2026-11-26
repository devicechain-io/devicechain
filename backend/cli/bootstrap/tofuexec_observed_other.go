// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package bootstrap

import "os/exec"

func observedProcessAttrs(*exec.Cmd) {}

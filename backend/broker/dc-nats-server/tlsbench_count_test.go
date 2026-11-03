// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build dcbench

package main

import "github.com/nats-io/nats-server/v2/server"

func benchRawWrites() (int64, int64) { return server.RawWrites.Load(), server.RawWriteBytes.Load() }

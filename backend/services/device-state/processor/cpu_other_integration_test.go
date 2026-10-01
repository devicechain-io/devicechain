// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration && !unix

package processor

import "time"

// processCPU cannot read this process's CPU time off unix; ok is false, and a benchmark that
// asks reports no CPU metric rather than a zero.
func processCPU() (cpu time.Duration, ok bool) { return 0, false }

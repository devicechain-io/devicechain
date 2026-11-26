// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installchoice

import (
	"os"
	"testing"

	"github.com/devicechain-io/dcctl/internal/kubeisolation"
)

// TestMain runs this package's tests against an empty kubeconfig (kubeisolation.Run): the
// package links the bootstrap code that can build a cluster client, so no test here may be
// able to reach the developer's current-context.
func TestMain(m *testing.M) { os.Exit(kubeisolation.Run(m)) }

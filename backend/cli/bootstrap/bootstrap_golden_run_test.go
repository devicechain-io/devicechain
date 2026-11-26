// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"
)

// goldenStandIn is a stand-in step: one line, through the helpers real steps print with.
func goldenStandIn(_ context.Context, _ *State, name string, failing bool) error {
	doing("standing in for " + name)
	if failing {
		return fail("the stand-in", errGoldenStep)
	}
	done()
	return nil
}

// runPipelineForGolden runs p as a bootstrap runs it, capturing stdout.
func runPipelineForGolden(t *testing.T, st *State, p Pipeline) (string, error) {
	var err error
	out := captureStdout(t, func() { err = p.Run(context.Background(), st) })
	return out, err
}

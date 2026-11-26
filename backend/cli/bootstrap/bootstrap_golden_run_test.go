// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"os"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
)

// goldenStandIn is a stand-in step: one line, through the console real steps print to.
func goldenStandIn(ctx context.Context, _ *State, name string, failing bool) error {
	c := out(ctx)
	c.doing("standing in for " + name)
	if failing {
		return c.fail("the stand-in", errGoldenStep)
	}
	c.done()
	return nil
}

// runPipelineForGolden runs p as `dcctl bootstrap` runs it — inside the bootstrap engine,
// through the plain presenter on the process's stdout — capturing stdout. The goldens
// were captured from Pipeline.Run writing to stdout before there was an engine.
func runPipelineForGolden(t *testing.T, st *State, p Pipeline) (string, error) {
	var err error
	out := captureStdout(t, func() {
		e := &BootstrapEngine{
			Instance: st.Instance, DryRun: st.DryRun, SkipPreflight: true,
			Escrow:   EscrowFlags{NoEscrow: true},
			Target:   func(context.Context, Console, EscrowPlan) (*State, error) { return st, nil },
			Pipeline: p,
		}
		err = installplain.Presenter{Stdout: os.Stdout, Stderr: os.Stderr}.Run(context.Background(), e,
			installflow.Request{Operation: installflow.OperationBootstrap, DryRun: st.DryRun}, nil)
	})
	return out, err
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package testkit

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

func streamWith(progress ...installflow.Envelope) []installflow.Envelope {
	envs := []installflow.Envelope{
		{Kind: installflow.KindRunStarted, Payload: installflow.RunStarted{}},
		{Kind: installflow.KindStepStarted, StepID: "s", Attempt: 1, Payload: installflow.StepStarted{Message: "s"}},
	}
	envs = append(envs, progress...)
	envs = append(envs,
		installflow.Envelope{Kind: installflow.KindStepFinished, StepID: "s", Attempt: 1,
			Payload: installflow.StepFinished{Outcome: installflow.StepSucceeded}},
		installflow.Envelope{Kind: installflow.KindRunSummary, Payload: installflow.Summary{Outcome: installflow.RunSucceeded}})
	for i := range envs {
		envs[i].Schema, envs[i].RunID, envs[i].Seq = installflow.Schema, "r", uint64(i+1)
	}
	return envs
}

func prog(unit installflow.Unit, current int64, total ...int64) installflow.Envelope {
	sp := installflow.StepProgress{MetricID: "m", Mode: installflow.ProgressIndeterminate, Current: current, Unit: unit, Source: "x"}
	if len(total) > 0 {
		t := total[0]
		sp.Mode, sp.Total = installflow.ProgressMeasured, &t
	}
	return installflow.Envelope{Kind: installflow.KindStepProgress, StepID: "s", Attempt: 1, Payload: sp}
}

// TestVerifyStreamHoldsACountToItsAttempt: a count of actions only goes forward
// within its attempt and keeps its total; a readiness snapshot may go down.
func TestVerifyStreamHoldsACountToItsAttempt(t *testing.T) {
	ok := [][]installflow.Envelope{
		{prog(installflow.UnitActions, 0), prog(installflow.UnitActions, 0, 3), prog(installflow.UnitActions, 2, 3)},
		{prog(installflow.UnitReplicas, 3, 3), prog(installflow.UnitReplicas, 2, 3)},
	}
	for i, p := range ok {
		if err := VerifyStream(streamWith(p...)); err != nil {
			t.Errorf("case %d refused: %v", i, err)
		}
	}
	bad := map[string][]installflow.Envelope{
		"went back":      {prog(installflow.UnitActions, 2, 3), prog(installflow.UnitActions, 1, 3)},
		"changed its":    {prog(installflow.UnitActions, 1, 3), prog(installflow.UnitActions, 1, 4)},
		"changed its ":   {prog(installflow.UnitActions, 1, 3), prog(installflow.UnitActions, 2)},
		"reports 4 of 3": {prog(installflow.UnitActions, 4, 3)},
	}
	for want, p := range bad {
		err := VerifyStream(streamWith(p...))
		if err == nil || !strings.Contains(err.Error(), strings.TrimSpace(want)) {
			t.Errorf("%q: got %v", want, err)
		}
	}
}

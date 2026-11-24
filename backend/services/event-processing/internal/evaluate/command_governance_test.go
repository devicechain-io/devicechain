// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package evaluate

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

func TestCommandEvaluationDoesNotSpendLiveAllowances(t *testing.T) {
	command := rules.Action{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "reboot"}}
	var events []Event
	for i := 0; i < 121; i++ {
		at := t0.Add(time.Duration(2*i) * time.Millisecond)
		events = append(events, reading("d1", at, "temperature", 70), reading("d1", at.Add(time.Millisecond), "temperature", 90))
	}
	p := params([]Rule{hotRule(command)}, events)
	for i := 0; i < 2; i++ {
		result := mustRun(t, p)
		if len(result.Intents) != 121 {
			t.Fatalf("evaluation %d produced %d intents; live allowances affected simulation", i, len(result.Intents))
		}
	}
}

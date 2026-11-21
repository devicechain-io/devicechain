// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	api "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"sort"
)

const CompiledProtocolVersion int32 = 1

type Action = api.OperationAction
type Runner func(context.Context, *api.InstanceOperation) error
type Registry map[Action]Runner

// Activation requires a released refusal foundation and explicit provider privileges.
var executable = map[Action]Runner{}

func ExecutableActions() []string {
	actions := make([]string, 0, len(executable))
	for action := range executable {
		actions = append(actions, string(action))
	}
	sort.Strings(actions)
	return actions
}

func Terminal(phase api.OperationPhase) bool {
	return phase == api.OperationRefused || phase == api.OperationSucceeded || phase == api.OperationFailed
}

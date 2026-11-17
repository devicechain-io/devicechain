// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

func TestWithoutInactivityMonitorSkipsSweeps(t *testing.T) {
	ctx := context.Background()
	enabledCalls := make(chan struct{}, 1)
	disabledCalls := make(chan struct{}, 1)
	ms := &core.Microservice{FunctionalArea: "device-state"}
	enabled := NewStateProcessor(ms, eofStateReader{}, core.NewNoOpLifecycleCallbacks(),
		&failingSweepApi{entered: enabledCalls}, nil, nil)
	disabled := NewStateProcessor(ms, eofStateReader{}, core.NewNoOpLifecycleCallbacks(),
		&failingSweepApi{entered: disabledCalls}, nil, nil, WithoutInactivityMonitor())
	for _, sp := range []*StateProcessor{enabled, disabled} {
		sp.inactivityInterval = 5 * time.Millisecond
		require.NoError(t, sp.Initialize(ctx))
		t.Cleanup(func() {
			stopped := make(chan error, 1)
			go func() { stopped <- sp.Stop(ctx) }()
			select {
			case err := <-stopped:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("processor stop hung")
			}
			require.NoError(t, sp.Terminate(ctx))
		})
		require.NoError(t, sp.Start(ctx))
	}
	// Wait for several actual ticks from the enabled control, so the disabled
	// processor has had time to sweep if startup ignored the constructor option.
	for i := 0; i < 5; i++ {
		select {
		case <-enabledCalls:
		case <-time.After(5 * time.Second):
			t.Fatal("the default processor did not sweep")
		}
	}
	select {
	case <-disabledCalls:
		t.Fatal("processor with inactivity monitor disabled called SweepInactive")
	case <-time.After(50 * time.Millisecond):
	}
}

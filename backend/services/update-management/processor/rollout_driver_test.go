// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestRolloutMetricsRecordWhatAStepDid(t *testing.T) {
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{InstanceId: "inst", FunctionalArea: "update-management"}
	ms.UseMetricsRegistry(reg)
	m := NewRolloutMetrics(ms)

	m.Observe(model.RolloutStepResult{Dispatched: 3, Skipped: 1}, nil)
	m.Observe(model.RolloutStepResult{Transition: model.RolloutPaused, Reason: model.PauseFailureThreshold}, nil)
	m.Observe(model.RolloutStepResult{Transition: model.RolloutPaused, Reason: model.PauseArtifactNotAssignable}, nil)
	m.Observe(model.RolloutStepResult{Transition: model.RolloutCompleted}, nil)
	m.Observe(model.RolloutStepResult{Dispatched: 9}, errors.New("database down"))

	require.EqualValues(t, 3, testutil.ToFloat64(m.dispatches.WithLabelValues("dispatched")), "a failed step dispatched nothing")
	require.EqualValues(t, 1, testutil.ToFloat64(m.dispatches.WithLabelValues("skipped")))
	require.EqualValues(t, 1, testutil.ToFloat64(m.transitions.WithLabelValues("paused_failure_threshold")))
	require.EqualValues(t, 1, testutil.ToFloat64(m.transitions.WithLabelValues("paused_artifact_not_assignable")))
	require.EqualValues(t, 1, testutil.ToFloat64(m.transitions.WithLabelValues("completed")))
	require.EqualValues(t, 1, testutil.ToFloat64(m.failures))

	var nilMetrics *RolloutMetrics
	nilMetrics.Observe(model.RolloutStepResult{}, nil) // an unwired observer is a no-op, not a panic
}

func TestTheRolloutDriverRefusesANonPositiveInterval(t *testing.T) {
	require.Panics(t, func() { NewRolloutDriver(nil, 0) })
}

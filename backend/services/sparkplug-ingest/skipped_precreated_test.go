// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// Every skip reason is exported at zero from start, so the first metric skipped for a
// reason after a restart is an increase and not a series appearing at 1. The reasons are
// written out here rather than read from host.SkipReasons, so a reason dropped from that
// list fails this test instead of following it.
func TestEverySkipReasonIsPrecreated(t *testing.T) {
	v := skippedPrecreated(prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "skipped_precreated_test", Help: "h"}, []string{"reason"}))

	require.NoError(t, testutil.CollectAndCompare(v, strings.NewReader(`
# HELP skipped_precreated_test h
# TYPE skipped_precreated_test counter
skipped_precreated_test{reason="non_numeric"} 0
skipped_precreated_test{reason="null"} 0
skipped_precreated_test{reason="oversize"} 0
skipped_precreated_test{reason="unnamed"} 0
skipped_precreated_test{reason="untyped"} 0
`)))
}

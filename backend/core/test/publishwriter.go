// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// PublishWriterLabels gathers g and returns, sorted, the writer label of every
// jetstream_publish_window_full_total series on suffix. A pipelined writer creates its series
// when it is built, so a service's wiring test can build the writer main wires and ask which
// name it reports under. A series with no writer label at all is returned as "", so a
// writer that reports unlabelled reads as a wrong VALUE rather than as nothing.
func PublishWriterLabels(t testing.TB, g prometheus.Gatherer, suffix string) []string {
	t.Helper()
	families, err := g.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var writers []string
	for _, f := range families {
		if !strings.HasSuffix(f.GetName(), "jetstream_publish_window_full_total") {
			continue
		}
		for _, m := range f.GetMetric() {
			var gotSuffix, writer string
			for _, l := range m.GetLabel() {
				switch l.GetName() {
				case "suffix":
					gotSuffix = l.GetValue()
				case "writer":
					writer = l.GetValue()
				}
			}
			if gotSuffix == suffix {
				writers = append(writers, writer)
			}
		}
	}
	sort.Strings(writers)
	return writers
}

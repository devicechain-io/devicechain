// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package analyzer_test

import (
	"testing"

	"github.com/devicechain-io/dc-subconfirm/analyzer"
	"golang.org/x/tools/go/analysis/analysistest"
)

// The testdata package carries both halves deliberately. Reporting a bare subscribe
// is the easy half and the one a grep can also do; NOT reporting JetStream, paho
// lookalikes and our own GraphQL client is the half that justifies a type checker,
// and it is only measured because those cases sit in the same package with no `want`
// against them — an analyzer that over-reports fails here rather than in review.
func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), analyzer.Analyzer, "a")
}

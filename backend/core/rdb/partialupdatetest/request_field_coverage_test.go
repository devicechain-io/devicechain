// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partialupdatetest

import (
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

// A private implementation field must neither require a declaration nor make room
// for a duplicate or stale declaration. Counts alone cannot distinguish these cases.
type coverageUpdateRequest struct {
	Name    dcgraphql.OptionalString
	private dcgraphql.OptionalString
}

func requestCoverageSuite() Suite[*demoApi] {
	s := demoSuite(false)
	s.Families = s.Families[:1]
	s.Families[0].NewRequest = func() any { return &coverageUpdateRequest{} }
	s.Families[0].Fields = s.Families[0].Fields[:1] // name
	return s
}

func TestRequestFieldCoverageIgnoresPrivateFields(t *testing.T) {
	everyRequestFieldIsDeclared(t, requestCoverageSuite())
}

func TestRequestFieldCoverageAcceptsEveryExportedField(t *testing.T) {
	// The existing scalar, reference and list declarations still cover the real
	// toy requests, including case-insensitive Go field / declaration matching.
	everyRequestFieldIsDeclared(t, demoSuite(false))
}

type conditionalCoverageRequest struct {
	Name        dcgraphql.OptionalString
	Conditional dcgraphql.OptionalString
}

func conditionalCoverageSuite() Suite[*demoApi] {
	s := requestCoverageSuite()
	s.Families[0].NewRequest = func() any { return new(conditionalCoverageRequest) }
	s.Families[0].FieldLimitations = map[string]string{
		"conditional": "Only applicable under another discriminator; the complementary fixture drives it.",
	}
	return s
}

func TestRequestFieldCoverageAcceptsConditionalLimitation(t *testing.T) {
	everyRequestFieldIsDeclared(t, conditionalCoverageSuite())
}

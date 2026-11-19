// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partialupdatetest

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

type embeddedCoverageFields struct {
	Kind dcgraphql.OptionalString
}

// The promoted Kind is a field callers can send that no enumeration here would see.
type embeddingCoverageRequest struct {
	embeddedCoverageFields
	Name dcgraphql.OptionalString
}

// otherConditionalCoverageRequest returns a second type named conditionalCoverageRequest.
func otherConditionalCoverageRequest() any {
	type conditionalCoverageRequest struct {
		Name        dcgraphql.OptionalString
		Conditional dcgraphql.OptionalString
	}
	return new(conditionalCoverageRequest)
}

func siblingCoverageSuite() Suite[*demoApi] {
	s := conditionalCoverageSuite()
	s.Families[0].Name = "first"
	sibling := s.Families[0]
	sibling.Name = "second"
	sibling.FieldLimitations = map[string]string{
		"CONDITIONAL": "The first fixture drives it.",
	}
	s.Families = append(s.Families, sibling)
	return s
}

func externallyDrivenCoverageSuite() Suite[*demoApi] {
	s := siblingCoverageSuite()
	s.ExternallyDriven = map[string]string{
		"conditionalCoverageRequest.CoNdItIoNaL": "TestDedicatedConditionalUpdate drives the atomic edit.",
	}
	return s
}

func TestRequestFieldUnionAcceptsSiblingDeclaration(t *testing.T) {
	s := siblingCoverageSuite()
	s.Families[1].FieldLimitations = nil
	s.Families[1].Fields = append(s.Families[1].Fields, Field{Name: "CONDITIONAL"})
	everyRequestFieldIsDeclared(t, s)
	everyRequestFieldIsDriven(t, s)
}

func TestRequestFieldUnionAcceptsExternalCoverage(t *testing.T) {
	s := externallyDrivenCoverageSuite()
	everyRequestFieldIsDeclared(t, s)
	everyRequestFieldIsDriven(t, s)
}

func TestRequestFieldUnionAcceptsCompleteRequests(t *testing.T) {
	everyRequestFieldIsDriven(t, demoSuite(false))
	everyRequestFieldIsDriven(t, requestCoverageSuite()) // private fields need no coverage
	s := conditionalCoverageSuite()
	s.ExternallyDriven = externallyDrivenCoverageSuite().ExternallyDriven
	everyRequestFieldIsDriven(t, s) // an explicit exemption also covers a lone family
}

// Like TestNegativeControls, re-execute this test binary so a check's t.Errorf
// becomes an observable exit status without failing the parent. Match diagnostics
// as well as status, and require the healthy control to pass through the same path.
func TestRequestFieldUnionNegativeControls(t *testing.T) {
	const modeEnv = "DC_PARTIALUPDATETEST_UNION_CONTROL"
	const externalKey = "conditionalCoverageRequest.CoNdItIoNaL"
	cases := []struct {
		name      string
		makeSuite func() Suite[*demoApi]
		want      string
	}{
		{name: "healthy", makeSuite: externallyDrivenCoverageSuite},
		{name: "siblingGap", makeSuite: siblingCoverageSuite,
			want: "conditionalCoverageRequest.conditional is declared by no family (limited by families [first second]); each claims another family drives it; none does"},
		{name: "loneFamilyGap", makeSuite: conditionalCoverageSuite,
			want: "conditionalCoverageRequest.conditional is declared by no family (limited by families [demoThing]); each claims another family drives it; none does"},
		{name: "emptyReason", makeSuite: func() Suite[*demoApi] {
			s := externallyDrivenCoverageSuite()
			s.ExternallyDriven[externalKey] = " \t"
			return s
		}, want: "needs a non-empty reason naming the dedicated tests"},
		{name: "unknownType", makeSuite: func() Suite[*demoApi] {
			s := externallyDrivenCoverageSuite()
			s.ExternallyDriven["UnknownRequest.conditional"] = "TestDedicatedConditionalUpdate"
			return s
		}, want: "ExternallyDriven \"UnknownRequest.conditional\" matches no exported request field used by a family"},
		{name: "unknownField", makeSuite: func() Suite[*demoApi] {
			s := externallyDrivenCoverageSuite()
			s.ExternallyDriven["conditionalCoverageRequest.missing"] = "TestDedicatedConditionalUpdate"
			return s
		}, want: "ExternallyDriven \"conditionalCoverageRequest.missing\" matches no exported request field used by a family"},
		{name: "privateField", makeSuite: func() Suite[*demoApi] {
			s := requestCoverageSuite()
			s.ExternallyDriven = map[string]string{"coverageUpdateRequest.private": "TestPrivateUpdate"}
			return s
		}, want: "ExternallyDriven \"coverageUpdateRequest.private\" matches no exported request field used by a family"},
		{name: "declaredSomewhere", makeSuite: func() Suite[*demoApi] {
			s := externallyDrivenCoverageSuite()
			// Keep every limitation to isolate the union check's stale declaration
			// rule. The separate per-family check rejects the overlap as well.
			s.Families[1].Fields = append(s.Families[1].Fields, Field{Name: "CONDITIONAL"})
			return s
		}, want: "is stale: family \"second\" declares the field"},
		{name: "duplicateKey", makeSuite: func() Suite[*demoApi] {
			s := externallyDrivenCoverageSuite()
			s.ExternallyDriven["conditionalCoverageRequest.conditional"] = "TestDedicatedConditionalUpdate"
			return s
		}, want: "one exemption per field"},
		{name: "embeddedField", makeSuite: func() Suite[*demoApi] {
			s := requestCoverageSuite()
			s.Families[0].NewRequest = func() any { return new(embeddingCoverageRequest) }
			return s
		}, want: "embedded request fields are not supported"},
		{name: "ambiguousType", makeSuite: func() Suite[*demoApi] {
			// Two distinct types sharing the bare name a key uses, as two packages' would.
			s := externallyDrivenCoverageSuite()
			s.Families[1].NewRequest = otherConditionalCoverageRequest
			return s
		}, want: "is ambiguous: more than one request type in this suite is named conditionalCoverageRequest"},
		{name: "notLimitedEverywhere", makeSuite: func() Suite[*demoApi] {
			s := externallyDrivenCoverageSuite()
			s.Families[1].FieldLimitations = nil
			return s
		}, want: "is stale: family \"second\" does not limit the field; every family must limit it"},
	}
	if mode := os.Getenv(modeEnv); mode != "" {
		for _, tc := range cases {
			if tc.name == mode {
				everyRequestFieldIsDriven(t, tc.makeSuite())
				return
			}
		}
		t.Fatalf("unknown union control %q", mode)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestRequestFieldUnionNegativeControls$", "-test.v")
			cmd.Env = append(os.Environ(), modeEnv+"="+tc.name)
			out, err := cmd.CombinedOutput()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("healthy union control failed: %v\n%s", err, out)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s passed; the union check did not reject the gap:\n%s", tc.name, out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Fatalf("%s failed without the expected diagnostic %q:\n%s", tc.name, tc.want, out)
			}
		})
	}
}

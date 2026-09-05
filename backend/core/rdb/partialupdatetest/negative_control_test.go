// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partialupdatetest

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// 🔴 THE NEGATIVE CONTROLS. A CHECK IS WORTH NOTHING UNTIL IT HAS BEEN SHOWN TO FAIL.
//
// Every property in this package is a claim about a defect it would catch, and every one
// of those claims is currently supported by a suite that is GREEN. Green is what a
// harness that asserts nothing also produces. So each mutant below is a deliberately
// broken version of the toy service, and this file asserts that the harness reports it —
// and reports it with the sentence that names the defect, not merely with some failure.
//
// # Why a subprocess
//
// A failing property calls t.Errorf/t.Fatalf on the *testing.T it was handed, and there
// is no way to hand it one whose failures can be collected instead of propagating: a
// subtest's failure marks its parent. So the mutant runs in a re-execution of this same
// test binary, with an environment variable selecting which mutant, and the PARENT
// asserts the child exited non-zero with the expected message. The child's failure is a
// process exit status, which is a value this process can examine.
//
// # 🔴 THE NO-OP CONTROL IS THE FIRST ROW AND IT IS NOT DECORATION
//
// "The child exited non-zero" is evidence only if the child can exit ZERO. A typo in the
// -test.run pattern, a binary that cannot start, a mutant name nothing matches — all of
// them produce a non-zero exit and every row below would "pass" having measured the
// harness not at all. The "healthy" row runs the UNBROKEN suite through the same
// mechanism and requires a green, which is what makes the other rows mean something.

const brokenModeEnv = "DC_PARTIALUPDATETEST_MUTANT"

// mutants are the deliberately broken runs. Each returns nothing and is EXPECTED TO FAIL
// the *testing.T it is given — except "healthy", which is the no-op control.
var mutants = map[string]func(t *testing.T){
	// The no-op control: the real suite and the real guard inputs, through the same
	// subprocess mechanism.
	"healthy": func(t *testing.T) {
		Run(t, demoSuite(false))
		AssertEveryUpdateTakesADedicatedRequest(t, demoSurface())
		AssertEveryUpdateTakesADedicatedRequest(t, shapeSurface())
	},

	// THE DEFECT THE WHOLE HARNESS EXISTS FOR: an update that writes every column from
	// the request whether or not the caller mentioned it.
	"fullreplace": func(t *testing.T) {
		Run(t, demoSuite(true))
	},

	// 🔴 A FAMILY DROPPED FROM A REGISTRY THAT STILL HAS OTHERS. This is the mutant that
	// walked past the earlier name-only version of the guard: the method still takes a
	// well-named type, and the only thing that drove its semantics has simply stopped
	// being asked to.
	//
	// It drops ONE of two rather than emptying the registry, because an empty registry
	// trips the "no families were supplied" precondition first — so with a single-family
	// toy this control was tripping a nil check and calling it a drop-one.
	"unregisteredfamily": func(t *testing.T) {
		s := demoSurface()
		s.Families = s.Families[:1] // demoWidget deleted from the registry; demoThing stays
		AssertEveryUpdateTakesADedicatedRequest(t, s)
	},

	// An update present on the Api, converted by nobody, exempted by nobody.
	"missingexemption": func(t *testing.T) {
		s := demoSurface()
		s.Exempt = nil
		AssertEveryUpdateTakesADedicatedRequest(t, s)
	},

	// An exemption that no longer describes the code.
	"wrongexemptiontype": func(t *testing.T) {
		s := demoSurface()
		s.Exempt = map[string]string{demoOwnerMethod: "SomethingElseRequest"}
		AssertEveryUpdateTakesADedicatedRequest(t, s)
	},

	// An exemption left behind after its family was converted. It matches nothing, and
	// the residual a reader is asked to count stays one too high.
	"staleexemption": func(t *testing.T) {
		s := demoSurface()
		s.Exempt = map[string]string{
			demoOwnerMethod:       demoOwnerExempt,
			"UpdateSomethingElse": "GoneRequest",
		}
		AssertEveryUpdateTakesADedicatedRequest(t, s)
	},

	// The same defect one map along: a NotAnEntityUpdate entry outliving its method.
	"staleexclusion": func(t *testing.T) {
		s := demoSurface()
		s.NotAnEntityUpdate = map[string]string{"UpdateLongGone": "a reason for a method that is not there"}
		AssertEveryUpdateTakesADedicatedRequest(t, s)
	},

	// A method declared both exempt and out of scope. The exclusion would win and the
	// exemption would never be checked against the code, which is a silence built out of
	// two things that each look deliberate.
	"contradictorydeclaration": func(t *testing.T) {
		s := demoSurface()
		s.NotAnEntityUpdate = map[string]string{demoOwnerMethod: "a reason that contradicts the exemption"}
		AssertEveryUpdateTakesADedicatedRequest(t, s)
	},

	// An exclusion with no reason. Without this it is a silent skip with a nicer name,
	// which is what the map replaced.
	"unexplainedexclusion": func(t *testing.T) {
		s := demoSurface()
		s.NotAnEntityUpdate = map[string]string{demoOwnerMethod: "   "}
		s.Exempt = nil
		AssertEveryUpdateTakesADedicatedRequest(t, s)
	},

	// The anti-vacuity floor itself: reflection that has stopped seeing the surface.
	"vacuoussurface": func(t *testing.T) {
		s := demoSurface()
		s.MinUpdateMethods = 99
		AssertEveryUpdateTakesADedicatedRequest(t, s)
	},

	// 🔴 THE SHAPES THE GUARD USED TO SKIP, driven one at a time.
	//
	// An update with no input object must be NAMED, not walked past. Removing its
	// NotAnEntityUpdate entry is exactly the state every such method was in before this
	// change, and the assertion is that it now reports rather than passes.
	"looseScalarsUnnamed": func(t *testing.T) {
		s := shapeSurface()
		delete(s.NotAnEntityUpdate, "UpdateLooseScalars")
		AssertEveryUpdateTakesADedicatedRequest(t, s)
	},

	// Two struct parameters: the locator must refuse rather than pick, because picking
	// wrongly certifies the wrong type and reports success.
	"ambiguousRequest": func(t *testing.T) {
		s := shapeSurface()
		delete(s.NotAnEntityUpdate, "UpdateAmbiguous")
		AssertEveryUpdateTakesADedicatedRequest(t, s)
	},

	// The by-value input, unregistered. It proves the guard REACHES that shape — a
	// certification it could not have made when the shape was skipped, and one the
	// positive control alone cannot distinguish from "still skipped, still green".
	"byValueUnregistered": func(t *testing.T) {
		s := shapeSurface()
		s.Families = s.Families[:1] // drops the byValue row, keeps withPrecondition
		AssertEveryUpdateTakesADedicatedRequest(t, s)
	},

	// The trailing-precondition input, unregistered. Same argument, other shape.
	"preconditionUnregistered": func(t *testing.T) {
		s := shapeSurface()
		s.Families = s.Families[1:] // drops the withPrecondition row, keeps byValue
		AssertEveryUpdateTakesADedicatedRequest(t, s)
	},

	// The floor over the shape service. Four methods are counted; five must fail, or
	// "all four were counted" is indistinguishable from "the floor is not checked".
	"shapefloortoohigh": func(t *testing.T) {
		s := shapeSurface()
		s.MinUpdateMethods = 5
		AssertEveryUpdateTakesADedicatedRequest(t, s)
	},

	// 🔴 SET AND CLEARED AS ONE OBSERVATION. A Clearable field whose replacement renders
	// the same as its cleared reading admits a fold that returns the empty value whenever
	// Set is true, ignoring what the caller sent, while passing every property that drives
	// the field. The Field is built by hand because the list constructor now panics on
	// this shape — which is the same check one layer earlier, not a substitute for it.
	"replaceEqualsCleared": func(t *testing.T) {
		s := demoSuite(false)
		fams := demoFamilies()
		for i := range fams[1].Fields {
			if fams[1].Fields[i].Name == "name" {
				fams[1].Fields[i].Replace = fams[1].Fields[i].Cleared
			}
		}
		s.Families = fams
		Run(t, s)
	},

	// A suite with no families at all. Run must refuse rather than iterate nothing.
	"nofamilies": func(t *testing.T) {
		s := demoSuite(false)
		s.Families = nil
		Run(t, s)
	},

	// A suite whose fixture strictness is unpinned. A harness that never asks the token
	// grammar to do anything cannot notice its registration disappearing.
	"nostrictnessprobe": func(t *testing.T) {
		s := demoSuite(false)
		s.CreateWithToken = nil
		Run(t, s)
	},
}

func TestNegativeControls(t *testing.T) {
	if mode := os.Getenv(brokenModeEnv); mode != "" {
		run, ok := mutants[mode]
		if !ok {
			t.Fatalf("no mutant named %q — the parent and the child disagree about the "+
				"roster, which would make every row below fail for the wrong reason", mode)
		}
		run(t)
		return
	}

	cases := []struct {
		mutant   string
		wantPass bool
		// want is a sentence the harness must produce. Matching the MESSAGE and not just
		// the exit status is what stops a mutant from being "killed" by an unrelated
		// failure — a compile error, a missing table, a panic — which is the way a
		// mutation report comes to overstate what it measured.
		want string
	}{
		{mutant: "healthy", wantPass: true},
		{mutant: "fullreplace", want: "the update is still a full replace"},
		// Named in full, so this row cannot be satisfied by the OTHER method that is
		// also unregistered under a different mutant.
		{mutant: "unregisteredfamily",
			want: "UpdateDemoWidget takes demoWidgetUpdateRequest, which no registered family covers"},
		{mutant: "missingexemption",
			want: "UpdateDemoOwner takes demoOwnerFullReplaceRequest, which no registered family covers"},
		{mutant: "wrongexemptiontype", want: "no longer describes the code"},
		{mutant: "staleexemption", want: "exemptions match no Update* method"},
		{mutant: "staleexclusion", want: "NotAnEntityUpdate entries match no Update* method"},
		{mutant: "unexplainedexclusion", want: "named in NotAnEntityUpdate with no reason"},
		{mutant: "contradictorydeclaration",
			want: "is in both Exempt and NotAnEntityUpdate"},
		{mutant: "vacuoussurface", want: "stopped seeing the surface it certifies"},
		{mutant: "nofamilies", want: "Suite.Families is empty"},
		{mutant: "nostrictnessprobe", want: "Suite.CreateWithToken is nil"},

		// The shapes the guard used to walk past in silence.
		{mutant: "looseScalarsUnnamed",
			want: "UpdateLooseScalars takes no struct parameter"},
		{mutant: "ambiguousRequest",
			want: "UpdateAmbiguous takes 2 struct parameters"},
		{mutant: "byValueUnregistered",
			want: "UpdateByValue takes shapeValueInput, which no registered family covers"},
		{mutant: "preconditionUnregistered",
			want: "UpdateWithPrecondition takes shapeInput, which no registered family covers"},
		{mutant: "shapefloortoohigh", want: "stopped seeing the surface it certifies"},

		{mutant: "replaceEqualsCleared",
			want: "the update SET this\" and \"the update CLEARED it\" are the same observation"},
	}

	for _, tc := range cases {
		t.Run(tc.mutant, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestNegativeControls$", "-test.v")
			cmd.Env = append(os.Environ(), brokenModeEnv+"="+tc.mutant)
			out, err := cmd.CombinedOutput()

			if tc.wantPass {
				if err != nil {
					t.Fatalf("the NO-OP CONTROL failed, so every other row here is measuring "+
						"the subprocess mechanism rather than the harness: %v\n%s", err, out)
				}
				return
			}
			if err == nil {
				t.Fatalf("the %q mutant PASSED — the check it is supposed to trip does not "+
					"trip:\n%s", tc.mutant, out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Fatalf("the %q mutant failed, but not for the reason claimed: no %q in the "+
					"output, so this row does not establish which check killed it:\n%s",
					tc.mutant, tc.want, out)
			}
		})
	}
}

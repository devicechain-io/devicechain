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
	// The no-op control: the real suite, through the same subprocess mechanism.
	"healthy": func(t *testing.T) {
		Run(t, demoSuite(false))
		AssertEveryUpdateTakesADedicatedRequest(t, UpdateSurface[*demoApi]{
			Families:         demoFamilies(),
			Exempt:           map[string]string{"UpdateDemoOwner": "demoOwnerFullReplaceRequest"},
			MinUpdateMethods: 2,
		})
	},

	// THE DEFECT THE WHOLE HARNESS EXISTS FOR: an update that writes every column from
	// the request whether or not the caller mentioned it.
	"fullreplace": func(t *testing.T) {
		Run(t, demoSuite(true))
	},

	// A family dropped from the registry. This is the mutant that walked past the earlier
	// name-only version of the guard: the method still takes a well-named type, and the
	// only thing that drove its semantics has simply stopped being asked to.
	"unregisteredfamily": func(t *testing.T) {
		AssertEveryUpdateTakesADedicatedRequest(t, UpdateSurface[*demoApi]{
			Families:         nil, // as if demoThing had been deleted from the registry
			Exempt:           map[string]string{"UpdateDemoOwner": "demoOwnerFullReplaceRequest"},
			MinUpdateMethods: 2,
		})
	},

	// An update present on the Api, converted by nobody, exempted by nobody.
	"missingexemption": func(t *testing.T) {
		AssertEveryUpdateTakesADedicatedRequest(t, UpdateSurface[*demoApi]{
			Families:         demoFamilies(),
			MinUpdateMethods: 2,
		})
	},

	// An exemption that no longer describes the code.
	"wrongexemptiontype": func(t *testing.T) {
		AssertEveryUpdateTakesADedicatedRequest(t, UpdateSurface[*demoApi]{
			Families:         demoFamilies(),
			Exempt:           map[string]string{"UpdateDemoOwner": "SomethingElseRequest"},
			MinUpdateMethods: 2,
		})
	},

	// An exemption left behind after its family was converted. It matches nothing, and
	// the residual a reader is asked to count stays one too high.
	"staleexemption": func(t *testing.T) {
		AssertEveryUpdateTakesADedicatedRequest(t, UpdateSurface[*demoApi]{
			Families: demoFamilies(),
			Exempt: map[string]string{
				"UpdateDemoOwner":     "demoOwnerFullReplaceRequest",
				"UpdateSomethingElse": "GoneRequest",
			},
			MinUpdateMethods: 2,
		})
	},

	// The anti-vacuity floor itself: reflection that has stopped seeing the surface.
	"vacuoussurface": func(t *testing.T) {
		AssertEveryUpdateTakesADedicatedRequest(t, UpdateSurface[*demoApi]{
			Families:         demoFamilies(),
			Exempt:           map[string]string{"UpdateDemoOwner": "demoOwnerFullReplaceRequest"},
			MinUpdateMethods: 99,
		})
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
		{mutant: "unregisteredfamily", want: "no families were supplied"},
		{mutant: "missingexemption", want: "which no registered family covers"},
		{mutant: "wrongexemptiontype", want: "no longer describes the code"},
		{mutant: "staleexemption", want: "match no Update* method"},
		{mutant: "vacuoussurface", want: "stopped seeing the surface it certifies"},
		{mutant: "nofamilies", want: "Suite.Families is empty"},
		{mutant: "nostrictnessprobe", want: "Suite.CreateWithToken is nil"},
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

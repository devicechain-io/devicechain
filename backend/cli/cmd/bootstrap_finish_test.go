// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/devicechain-io/dcctl/bootstrap"
)

// 🔴 A RUN THAT WORKED BUT COULD NOT RECORD IT FINISHED IS REPORTED AS FAILED. Over an
// instance whose first bootstrap had not finished, the terminal stamp is what removes the
// record that lets a later bootstrap through; finishClaim is where the command learns it
// did not land, and what it returns is what the command exits with. The run's own error
// still wins when there is one, and the lock is given back either way.
func TestFinishClaimReportsWhatTheCommandExitsWith(t *testing.T) {
	recordFailed := errors.New("could not record that its bootstrap finished")
	runFailed := errors.New("helm said no")

	for _, tc := range []struct {
		name      string
		runErr    error
		finishErr error
		want      error
	}{
		{"a success whose finish could not be recorded fails", nil, recordFailed, recordFailed},
		{"a success that was recorded succeeds", nil, nil, nil},
		{"the run's own failure wins", runFailed, recordFailed, runFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := finishBootstrapPhase
			t.Cleanup(func() { finishBootstrapPhase = prev })
			var sawRunErr error
			called := false
			finishBootstrapPhase = func(_ context.Context, _ *bootstrap.State, runErr error) error {
				called, sawRunErr = true, runErr
				return tc.finishErr
			}

			cs := fake.NewClientset()
			claim, err := bootstrap.AcquireClaim(t.Context(), cs, "dc-system", "prod", "kind-test")
			if err != nil {
				t.Fatalf("taking the cluster lock: %v", err)
			}
			st := &bootstrap.State{Instance: "prod", Claim: claim}

			if got := finishClaim(t.Context(), st, tc.runErr); !errors.Is(got, tc.want) || (tc.want == nil && got != nil) {
				t.Errorf("finishClaim returned %v, want %v", got, tc.want)
			}
			if !called || !errors.Is(sawRunErr, tc.runErr) {
				t.Errorf("the outcome was not recorded with the run's error (called=%v, saw %v)", called, sawRunErr)
			}
			if _, err := cs.CoordinationV1().Leases("dc-system").Get(t.Context(), "dcctl", metav1.GetOptions{}); err == nil {
				t.Error("the cluster lock was not given back")
			}
		})
	}

	// A run that never took the lock (a dry run) has nothing to record or release, and
	// its own outcome passes straight through.
	t.Run("no claim", func(t *testing.T) {
		if got := finishClaim(t.Context(), &bootstrap.State{}, runFailed); !errors.Is(got, runFailed) {
			t.Errorf("finishClaim without a claim returned %v, want the run's error", got)
		}
	})
}

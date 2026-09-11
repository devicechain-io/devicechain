// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// lockHeldBy builds the Lease as `dcctl instances reclaim` reads it.
func lockHeldBy(holder, instance string, renewed time.Time) *coordinationv1.Lease {
	l := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "dcctl",
			Annotations: map[string]string{"core.devicechain.io/instance": instance},
		},
	}
	if holder != "" {
		l.Spec.HolderIdentity = &holder
	}
	if !renewed.IsZero() {
		at := metav1.NewMicroTime(renewed)
		l.Spec.RenewTime = &at
	}
	return l
}

// 🔴 THE HOLDER IS TYPED BACK, AND THAT IS THE ONLY DEFENCE THIS COMMAND HAS. A
// reclaim's one real risk is taking the lock from a process that is alive, and
// nothing dcctl can check from here rules that out — so the friction is the
// feature: the operator has to look at whose lock it is and type it.
func TestConfirmHolderRequiresTheHolderToBeTypedBack(t *testing.T) {
	const holder = "colleague@another-laptop/2222/eeff0011"

	t.Run("the typed holder is accepted", func(t *testing.T) {
		var out bytes.Buffer
		got, err := confirmHolder(&out, strings.NewReader(holder+"\n"),
			lockHeldBy(holder, "prod", time.Now().Add(-90*time.Second)))
		if err != nil {
			t.Fatalf("an operator who typed the holder exactly was refused: %v", err)
		}
		if got != holder {
			t.Errorf("confirmed %q", got)
		}

		// 🔴 THE SUMMARY IS THE POINT OF THE PROMPT. Asking someone to type a string
		// they were never shown is a captcha, not a confirmation — and the holder,
		// the instance and the age are the three facts the decision rests on.
		if !strings.Contains(out.String(), holder) {
			t.Errorf("the prompt never showed the holder it demands:\n%s", out.String())
		}
		if !strings.Contains(out.String(), "prod") {
			t.Errorf("the prompt does not say which instance the holder is working on:\n%s", out.String())
		}
		if !strings.Contains(out.String(), "renewed") {
			t.Errorf("the prompt does not say how old the claim is:\n%s", out.String())
		}
	})

	// A terminal that closes without a trailing newline — the operator typed the
	// identity and hit ctrl-D — is a successful confirmation, not a read error.
	t.Run("a typed holder with no trailing newline is accepted", func(t *testing.T) {
		var out bytes.Buffer
		got, err := confirmHolder(&out, strings.NewReader(holder), lockHeldBy(holder, "prod", time.Now()))
		if err != nil {
			t.Fatalf("a confirmation that ended at EOF was rejected: %v", err)
		}
		if got != holder {
			t.Errorf("confirmed %q", got)
		}
	})

	t.Run("anything else aborts and says so", func(t *testing.T) {
		for _, answer := range []string{"y\n", "yes\n", "\n", "colleague@another-laptop\n"} {
			var out bytes.Buffer
			got, err := confirmHolder(&out, strings.NewReader(answer),
				lockHeldBy(holder, "prod", time.Now()))
			if err == nil {
				t.Fatalf("%q was accepted as the holder identity, so the confirmation is a "+
					"keystroke and not a check", answer)
			}
			if got != "" {
				t.Errorf("%q aborted but still returned a holder: %q", answer, got)
			}
			// The operator has to know the lock is untouched, or their next move is
			// to reach for kubectl.
			if !strings.Contains(err.Error(), "left alone") {
				t.Errorf("the abort does not say the lock was untouched: %v", err)
			}
		}
	})

	// Closed stdin — a reclaim in a pipeline or a CI job. It must fail, and it must
	// say what it could not read rather than reporting a mismatch against nothing.
	t.Run("closed input is a read failure, not a mismatch", func(t *testing.T) {
		var out bytes.Buffer
		if _, err := confirmHolder(&out, strings.NewReader(""),
			lockHeldBy(holder, "prod", time.Now())); err == nil {
			t.Fatal("an unattended reclaim went through on a closed stdin")
		} else if !strings.Contains(err.Error(), "reading confirmation") {
			t.Errorf("the failure does not say what could not be read: %v", err)
		}
	})
}

// 🔴 A LOCK WITH NO HOLDER MAKES THE CONFIRMATION A BARE ENTER, which turns the
// one deliberate piece of friction in this command into none at all. dcctl never
// writes such a Lease, so it is refused rather than given a ceremony to satisfy.
func TestConfirmHolderRefusesALockThatNamesNobody(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lease *coordinationv1.Lease
	}{
		{"no holder field at all", lockHeldBy("", "prod", time.Now())},
		{"an empty holder string", func() *coordinationv1.Lease {
			l := lockHeldBy("", "prod", time.Now())
			empty := ""
			l.Spec.HolderIdentity = &empty
			return l
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			// An empty reader stands in for the bare Enter: if the refusal were
			// dropped, this input would satisfy the comparison and take the lock.
			got, err := confirmHolder(&out, strings.NewReader("\n"), tc.lease)
			if err == nil {
				t.Fatalf("a lock naming nobody was confirmed by pressing Enter (holder %q)", got)
			}
			if !strings.Contains(err.Error(), "names no holder") {
				t.Errorf("the refusal does not say what is wrong with the lock: %v", err)
			}
			// It refuses BEFORE the ceremony: prompting for a confirmation that
			// cannot succeed teaches the operator that the prompt is noise.
			if out.Len() != 0 {
				t.Errorf("the operator was prompted for an identity that does not exist:\n%s", out.String())
			}
		})
	}
}

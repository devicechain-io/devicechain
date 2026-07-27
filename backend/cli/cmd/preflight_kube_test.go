// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import "testing"

// The Kubernetes floor exists because both CloudNativePG charts declare
// `kubeVersion: '>=1.29.0-0'`, and helm refuses the release below it — during the
// infra apply, which is step 2 of a bootstrap, i.e. AFTER the instance credentials
// and the root-key escrow file have been written.
//
// These cases are about the REJECTING half. Running the check against a real
// cluster only ever exercises acceptance (every cluster to hand is far above the
// floor), and a version gate that has never been observed to reject is
// indistinguishable from no gate at all.
func TestTheKubernetesFloorRejectsVersionsBelowIt(t *testing.T) {
	for _, tc := range []struct {
		name         string
		major, minor string
		want         bool
	}{
		{"exactly the floor", "1", "29", true},
		{"one minor below the floor", "1", "28", false},
		{"far below", "1", "21", false},
		{"comfortably above", "1", "33", true},

		// Managed distributions append a "+" to the minor. Dropping the suffix
		// naively with Atoi would error and take the WARN path, which reads as
		// "could not check" on a cluster that is in fact fine — a false alarm on
		// EKS and GKE, i.e. on most production clusters.
		{"GKE/EKS-style minor suffix, above", "1", "30+", true},
		{"GKE/EKS-style minor suffix, exactly the floor", "1", "29+", true},
		{"GKE/EKS-style minor suffix, below", "1", "28+", false},

		// A future major must not be read as below the floor by a minor-only
		// comparison — 2.0 is newer than 1.29, not fifteen years older.
		{"a future major with a low minor", "2", "0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := kubeVersionMeetsFloor(tc.major, tc.minor)
			if err != nil {
				t.Fatalf("kubeVersionMeetsFloor(%q, %q): %v", tc.major, tc.minor, err)
			}
			if got != tc.want {
				t.Errorf("kubeVersionMeetsFloor(%q, %q) = %v, want %v.\n"+
					"  The floor is %d.%d; getting this wrong either blocks a good cluster or lets a\n"+
					"  bad one reach the infra apply, where it fails after the escrow is already written.",
					tc.major, tc.minor, got, tc.want, minKubeMajor, minKubeMinor)
			}
		})
	}
}

// Garbage must surface as an error so the caller can WARN rather than silently
// deciding. Returning false on unparseable input would fail a perfectly good
// cluster; returning true would wave through a bad one.
func TestAnUnreadableKubernetesVersionIsAnErrorRatherThanAVerdict(t *testing.T) {
	for _, tc := range []struct{ name, major, minor string }{
		{"empty minor", "1", ""},
		{"empty major", "", "29"},
		{"non-numeric", "v1", "29"},
		{"a suffix with no leading digits", "1", "+29"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := kubeVersionMeetsFloor(tc.major, tc.minor); err == nil {
				t.Errorf("kubeVersionMeetsFloor(%q, %q) returned a verdict for unreadable input.\n"+
					"  A guess here is worse than an admission: the caller warns on an error and can\n"+
					"  fail or pass a cluster it should not on a verdict.", tc.major, tc.minor)
			}
		})
	}
}

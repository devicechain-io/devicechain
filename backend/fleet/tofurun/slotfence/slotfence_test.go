// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package slotfence

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-fleet/tofurun"
	"github.com/devicechain-io/dc-fleet/writerslot"
)

// The runner and the slot are separate packages with separate vocabularies; they must be the same
// words, or a kind or class the runner means would be a different one to the slot.
func TestVocabulariesAreTheSameWords(t *testing.T) {
	for k, want := range map[tofurun.Kind]string{
		tofurun.KindCreate: writerslot.KindCreate, tofurun.KindChange: writerslot.KindChange, tofurun.KindUpgrade: writerslot.KindUpgrade,
		tofurun.KindDriftCheck: writerslot.KindDriftCheck, tofurun.KindDestroy: writerslot.KindDestroy, tofurun.KindRecover: writerslot.KindRecover,
	} {
		got, err := slotKind(k)
		require.NoError(t, err)
		require.Equal(t, want, got)
		require.Equal(t, want, string(k))
	}
	_, err := slotKind("UNPROTECT")
	require.ErrorIs(t, err, tofurun.ErrUnknownKind)

	require.Equal(t, writerslot.BackendPG, string(tofurun.BackendPG))
	require.Equal(t, writerslot.BackendGCS, string(tofurun.BackendGCS))
	// The classes are the two the slot accepts as cloud_identity_role.
	for _, c := range []tofurun.IdentityClass{tofurun.ClassApply, tofurun.ClassDestroy} {
		require.Contains(t, []string{"apply", "destroy"}, string(c))
	}
}

func TestNewRequiresItsParts(t *testing.T) {
	_, err := New(nil, Config{Pods: nil})
	require.Error(t, err)
}

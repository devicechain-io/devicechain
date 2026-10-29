// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-user-management/iam"
)

func TestErasedIsOnlyTheRecordedClaimOrCompletion(t *testing.T) {
	at := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)

	partial := &AdminTenantDeletionResolver{M: iam.TenantPurge{Token: "acme", Epoch: at}}
	require.False(t, partial.Erased(), "no claim and no completion: not erased, whatever the ledger says")
	require.Nil(t, partial.ErasedAt())

	claimed := &AdminTenantDeletionResolver{M: iam.TenantPurge{Token: "acme", Epoch: at, ErasedAt: &at}}
	require.True(t, claimed.Erased())
	require.NotNil(t, claimed.ErasedAt())
	require.Nil(t, claimed.CompletedAt(), "erased precedes the token release")

	done := &AdminTenantDeletionResolver{M: iam.TenantPurge{Token: "acme", Epoch: at, CompletedAt: &at}}
	require.True(t, done.Erased())
}

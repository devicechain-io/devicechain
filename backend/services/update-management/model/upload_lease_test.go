// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/stretchr/testify/require"
)

func TestUploadLeaseOwnership(t *testing.T) {
	testUploadLeaseOwnership(t, func(t *testing.T) *Api { return newTestEnv(t).api })
}

func testUploadLeaseOwnership(t *testing.T, newAPI func(*testing.T) *Api) {
	t.Helper()
	for _, action := range []string{"release", "complete"} {
		t.Run(action, func(t *testing.T) {
			api := newAPI(t)
			ctx := tenantCtx("acme")
			a, err := api.CreateArtifact(ctx, validRequest("lease-"+action, []byte("data")))
			require.NoError(t, err)
			stampA, err := api.ClaimUpload(ctx, a)
			require.NoError(t, err)
			stale := time.Now().Add(-UploadWindow(a.SizeBytes) - claimGrace - time.Minute)
			require.NoError(t, api.RDB.DB(ctx).Model(identified(a)).Where("id = ?", a.ID).
				Update("uploading_since", stale).Error)
			b, err := api.ArtifactByToken(ctx, a.Token)
			require.NoError(t, err)
			stampB, err := api.ClaimUpload(ctx, b)
			require.NoError(t, err)
			require.Zero(t, stampB.Nanosecond()%1000, "lease stamps must fit PostgreSQL microsecond precision")
			stored, err := api.ArtifactByToken(ctx, b.Token)
			require.NoError(t, err)
			require.True(t, stored.UploadingSince.Valid)
			require.True(t, stampB.Equal(stored.UploadingSince.Time), "claim must return the stored stamp")
			ref := blob.Ref{Backend: "filesystem", Key: "inst/acme/artifacts/lease"}
			if action == "release" {
				require.NoError(t, api.ReleaseUpload(ctx, a, stampA))
			} else {
				requireRefusal(t, api.CompleteUpload(ctx, a, stampA, ref), CodeArtifactUploadRaceLost)
			}
			_, err = api.ClaimUpload(ctx, a)
			requireRefusal(t, err, CodeUploadInProgress)
			// A failed claim must not stop the actual holder from releasing.
			require.NoError(t, api.ReleaseUpload(ctx, b, stampB))
			stampB, err = api.ClaimUpload(ctx, b)
			require.NoError(t, err)
			require.NoError(t, api.CompleteUpload(ctx, b, stampB, ref))
		})
	}
}

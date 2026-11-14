// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package identity

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/internal/concurrencytest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPostgresInvitationIsRedeemedOnceConcurrently(t *testing.T) {
	for _, unlocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("without_invitation_lock=%t", unlocked), func(t *testing.T) {
			e := newMfaPgEnv(t)
			s := admin.NewService(e.store, 0, 0, nil)
			s.SetViewerBaseline(ViewerAuthorities())
			_, err := s.CreateIdentity(context.Background(), admin.CreateIdentityInput{Email: "boss@example.com", Password: "pw", Enabled: true})
			require.NoError(t, err)
			_, err = s.AddMembership(context.Background(), "boss@example.com", "acme", []string{iam.TenantAdminRoleToken})
			require.NoError(t, err)
			boss := auth.WithClaims(context.Background(), &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: "boss@example.com", Authorities: []string{"*"}})
			_, code, err := s.InviteMember(boss, admin.InviteInput{Email: "new@example.com"})
			require.NoError(t, err)
			var accepted atomic.Int32
			require.NoError(t, e.db.Callback().Update().After("gorm:update").Register("test:accepted", func(tx *gorm.DB) {
				if tx.Statement.Table == "iam_invitations" && tx.Error == nil {
					accepted.Add(1)
				}
			}))
			t.Cleanup(func() { e.db.Callback().Update().Remove("test:accepted") })
			release := concurrencytest.Park(t, e.db, true, func(tx *gorm.DB) bool { return tx.Statement.Table == "iam_identities" })
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			const n = 8
			start := make(chan struct{})
			errs := make(chan struct {
				i   int
				err error
			}, n)
			for i := 0; i < n; i++ {
				go func(i int) {
					<-start
					pw := fmt.Sprintf("invitation-password-%d", i)
					if unlocked {
						// Keep the post-commit Login from hiding a second redemption
						// behind another worker's password overwrite in the control.
						pw = "shared-negative-control-password"
					}
					_, err := e.m.AcceptInvitation(concurrencytest.Context(ctx, unlocked), code, &pw, nil)
					errs <- struct {
						i   int
						err error
					}{i, err}
				}(i)
			}
			close(start)
			release(n)
			winner, wins := -1, 0
			for i := 0; i < n; i++ {
				r := <-errs
				if r.err == nil {
					winner, wins = r.i, wins+1
				} else if !unlocked {
					require.True(t, isInvalidInvitation(r.err), "worker %d: %v", r.i, r.err)
				} else {
					require.NoError(t, r.err, "negative control worker %d", r.i)
				}
			}
			if unlocked {
				require.Equal(t, n, wins, "negative control must accept the same code repeatedly")
				require.EqualValues(t, n, accepted.Load(), "negative control did not redeem the code repeatedly")
				return
			}
			require.Equal(t, 1, wins)
			require.EqualValues(t, 1, accepted.Load())
			for i := 0; i < n; i++ {
				_, err := e.m.Login(ctx, "new@example.com", fmt.Sprintf("invitation-password-%d", i))
				if i == winner {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, ErrInvalidCredentials)
				}
			}
		})
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

func TestPostgresDashboardCapConcurrentCreate(t *testing.T) {
	api := pgApi(t)
	api.dashboardLimit = 5
	for round := 0; round < 3; round++ {
		t.Run(fmt.Sprintf("round-%d", round), func(t *testing.T) {
			ctx := core.WithTenant(context.Background(), fmt.Sprintf("cap-round-%d", round))
			for i := 0; i < 4; i++ {
				_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: fmt.Sprintf("existing-%d", i), Definition: defA})
				require.NoError(t, err)
			}
			start := make(chan struct{})
			results := make(chan error, 8)
			var ready, workers sync.WaitGroup
			ready.Add(8)
			workers.Add(8)
			for i := 0; i < 8; i++ {
				go func(i int) {
					defer workers.Done()
					ready.Done()
					<-start
					_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: fmt.Sprintf("concurrent-%d", i), Definition: defA})
					results <- err
				}(i)
			}
			ready.Wait()
			close(start)
			workers.Wait()
			close(results)
			successes, limited := 0, 0
			for err := range results {
				switch {
				case err == nil:
					successes++
				case errors.Is(err, ErrDashboardLimit):
					limited++
				default:
					t.Errorf("unexpected create error: %v", err)
				}
			}
			require.Equal(t, 1, successes)
			require.Equal(t, 7, limited)
			var count int64
			require.NoError(t, api.RDB.DB(ctx).Model(&Dashboard{}).Count(&count).Error)
			require.Equal(t, int64(5), count)
		})
	}
}

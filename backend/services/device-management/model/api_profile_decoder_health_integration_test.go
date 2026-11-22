// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The decoder health tally is summed in SQL, and its last-error rule is an ORDER BY over a
// nullable column. Postgres sorts NULLs FIRST in a DESC order where SQLite sorts them last, so the
// comparison has to run on the real server too: a query that forgot to exclude rows with no
// failure passes on SQLite and reports no failure on Postgres. This also runs the area's real
// migrations, the appended window indexes included.
//
// Run the same way as the other integration tests in this package:
//
//	docker run -d --name dc-it-health -e POSTGRES_PASSWORD=postgres -P postgres:17
//	DC_IT_PGPORT=$(docker port dc-it-health 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run 'DecoderHealth' -v
//	docker rm -f dc-it-health
package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

func TestTheSQLDecoderHealthTallyEqualsTheGoTallyOnPostgres(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	round := 0
	checkSQLDecoderHealthTally(t, 15, func() (*Api, context.Context) {
		round++
		ctx := core.WithTenant(context.Background(), fmt.Sprintf("dh%d-%d", time.Now().UnixNano(), round))
		seedDeviceProfile(t, api, ctx, "meter")
		return api, ctx
	})
}

// The appended migration created both indexes the reads use.
func TestTheDecoderHealthWindowIndexesExist(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	ctx := core.WithSystemContext(context.Background())
	for _, name := range []string{"ix_profile_decoder_healths_profile_window", "ix_profile_decoder_healths_window_start"} {
		var n int
		require.NoError(t, api.RDB.DB(ctx).Raw(
			`SELECT count(*) FROM pg_indexes WHERE schemaname = 'device-management' AND indexname = ?`, name).Scan(&n).Error)
		require.Equal(t, 1, n, "index %s", name)
	}
}

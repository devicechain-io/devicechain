// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-microservice/secrets"
)

// THIS SERVICE'S HALF OF THE PARTIAL-UPDATE HARNESS.
//
// The properties, the anti-vacuity controls and the exhaustiveness check live in core's
// rdb/partialupdatetest, because the three-state update semantic is one platform rule and
// a per-service copy of the harness is the per-family copy one level up — the eighth copy
// certifies less than the first, and every one of them is green. Read that package's
// harness.go for what each property catches.
//
// What stays here is what is genuinely local: the fixture that builds THIS service's Api
// (which, unlike every other service's, needs a SECRET STORE as well as a database), the
// tenant it runs under, and the registry of converted families
// (partial_update_families_test.go).

// partialUpdateRootKey is a fixed 32-byte instance root key for the harness's secret
// store (never a real key). It only has to be 256-bit and stable across the test.
var partialUpdateRootKey = []byte("0123456789abcdef0123456789abcdef")

// newPartialUpdateApi builds a SQLite-backed Api migrated for one family. The database
// half — the named shared-cache DSN, the pool close, the token-grammar registration, and
// the reasons all three are load-bearing — is putest.NewSQLiteDB's.
//
// 🔴 THE SECRET STORE IS MIGRATED UNCONDITIONALLY, EVEN FOR THE POLICY FAMILY, and that
// is not laziness. A channel's delivery secret is a FIELD of the channel update as far as
// a caller is concerned — omit it and it is preserved, null it and it is gone — but it is
// not a column, so the harness can only observe it through a live store. A fixture that
// built the store only when it thought it was needed would make the channel family's
// secret readings depend on a decision made here rather than on the field table, which is
// the kind of quiet skip the harness exists to refuse.
func newPartialUpdateApi(t *testing.T, tables ...any) *Api {
	t.Helper()
	db := putest.NewSQLiteDB(t, tables...)
	if err := secrets.NewSecretStoreSchema().Migrate(db); err != nil {
		t.Fatalf("migrate secrets: %v", err)
	}
	kek, err := secrets.NewInstanceKeyProvider(partialUpdateRootKey)
	if err != nil {
		t.Fatalf("kek: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db}, secrets.NewStore(db, kek))
}

// partialUpdateTenant is the one place this service's fixture tenant is spelled, so the
// suite and the families cannot drift onto different tenants — which under the fail-closed
// tenant-scope callback would surface as a family that seeds into one database and reads
// from another.
const partialUpdateTenant = "acme"

func partialUpdateCtx() context.Context {
	return putest.TenantContext(partialUpdateTenant)()
}

// Local spellings of the harness's shared readings. They are aliases rather than
// re-implementations: a family's read closure calls them once per column, and the harness
// compares the rendered strings, so a second definition here would be a way for the two
// halves to disagree about what "cleared" looks like.
const nullMarker = putest.NullMarker

var (
	nullStr = putest.NullString
	jsonStr = putest.JSONString
	intStr  = putest.IntString
)

// nullIntStr renders a nullable bigint column for the Read contract. Core's field
// constructors carry IntString for an int32 but nothing for a sql.NullInt64, because a
// policy's throttle and escalation columns are the only nullable integers on the platform
// stored that way.
func nullIntStr(v sql.NullInt64) string {
	if !v.Valid {
		return nullMarker
	}
	return intStr(int32(v.Int64))
}

// requireOne is the reload guard every family's read shares: exactly one row, or the test
// is measuring something other than what it seeded.
func requireOne[E any](t *testing.T, what string, rows []*E, err error) *E {
	t.Helper()
	return putest.RequireOne(t, what, rows, err)
}

// TestPartialUpdate drives every property over every converted family in this service.
//
// A single property or family is still addressable:
//
//	go test ./model -run 'TestPartialUpdate/SettingOneFieldLeavesEveryOtherAlone/notificationPolicy'
func TestPartialUpdate(t *testing.T) {
	putest.Run(t, putest.Suite[*Api]{
		NewApi:   newPartialUpdateApi,
		Context:  putest.TenantContext(partialUpdateTenant),
		Families: partialUpdateFamilies(),

		// The strictness probe. A channel with no secret is the cheapest token-keyed
		// create in this service — one table, no references, and it never reaches the
		// secret store — so a refusal can only be the token grammar's.
		CreateWithToken: func(t *testing.T, api *Api, ctx context.Context, token string) error {
			_, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
				Token: token, ChannelType: ChannelTypeSMTP, Enabled: true,
			})
			return err
		},
		StrictnessTables: []any{&NotificationChannel{}},
		ValidToken:       "a-valid_token1",
	})
}

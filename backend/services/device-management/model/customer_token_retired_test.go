// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A DELETED CUSTOMER'S TOKEN IS RETIRED, NOT FREED.
//
// Customer-scoped records outlive the customer: commands, alarms and update assignments
// carry a scope stamp, dashboards are shared to it, and event history is anchored on it,
// all as the string "customer:<token>". A customer is hard-deleted, so before the issued-
// token ledger nothing remembered the token, and a new customer could take it the moment
// the old one was gone. Every member scoped to the new customer then saw the old
// customer's history as its own.

// newRetiredTokenApi builds the tables CreateCustomer and DeleteCustomer touch, and seeds
// the customer type every create here names.
func newRetiredTokenApi(t *testing.T) (*Api, context.Context) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err, "open sqlite")
	require.NoError(t, rdb.RegisterTenantScoping(db), "register tenant scoping")
	require.NoError(t, db.AutoMigrate(
		&CustomerType{}, &Customer{}, &IssuedCustomerToken{}, &Device{}, &DeviceType{},
		&EntityRelationship{}, &EntityAttribute{}, &Alarm{}, &EntityGroupMembership{},
	), "migrate")
	api := NewApi(&rdb.RdbManager{Database: db})
	ctx := core.WithTenant(context.Background(), "acme")
	seedRetailType(t, api, ctx)
	return api, ctx
}

func seedRetailType(t *testing.T, api *Api, ctx context.Context) {
	t.Helper()
	ct := &CustomerType{}
	ct.Token = "retail"
	require.NoError(t, api.RDB.DB(ctx).Create(ct).Error, "seed customer type")
}

func createCustomer(api *Api, ctx context.Context, token string) error {
	_, err := api.CreateCustomer(ctx, &CustomerCreateRequest{Token: token, CustomerTypeToken: "retail"})
	return err
}

func deleteCustomer(t *testing.T, api *Api, ctx context.Context, token string) {
	t.Helper()
	deleted, err := api.DeleteCustomer(ctx, token)
	require.NoError(t, err)
	require.True(t, deleted, "the fixture's customer was not there to delete")
}

func liveCustomers(t *testing.T, api *Api, ctx context.Context, token string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&Customer{}).Where("token = ?", token).Count(&n).Error)
	return n
}

// requireRetired asserts err is the retired-token refusal, answered INVALID_VALUE and
// never CONFLICT: a client that treats CONFLICT as "it already exists, carry on" would
// otherwise believe it holds a customer it does not.
func requireRetired(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err, "a deleted customer's token was handed to a new customer")
	require.ErrorIs(t, err, ErrCustomerTokenRetired)
	class, ok := integrity.Refused(err)
	require.True(t, ok, "the refusal is not an integrity refusal, so it carries no code: %v", err)
	require.Equal(t, integrity.ClassInvalid, class)
	_, isConflict := conflict.As(err)
	require.False(t, isConflict, "the retired-token refusal must never read as an already-exists: %v", err)
}

func TestCreatingACustomerAtADeletedCustomersTokenIsRefused(t *testing.T) {
	api, ctx := newRetiredTokenApi(t)
	require.NoError(t, createCustomer(api, ctx, "acme-corp"))
	deleteCustomer(t, api, ctx, "acme-corp")

	requireRetired(t, createCustomer(api, ctx, "acme-corp"))
	require.Zero(t, liveCustomers(t, api, ctx, "acme-corp"),
		"the refusal must write nothing, yet a live customer now holds the retired token")

	// Permanent: asking again changes nothing.
	requireRetired(t, createCustomer(api, ctx, "acme-corp"))
}

// The retirement is per tenant: another tenant never saw the deleted customer, and its
// own namespace is untouched.
func TestARetiredTokenIsStillFreeInAnotherTenant(t *testing.T) {
	api, ctx := newRetiredTokenApi(t)
	require.NoError(t, createCustomer(api, ctx, "acme-corp"))
	deleteCustomer(t, api, ctx, "acme-corp")

	other := core.WithTenant(context.Background(), "globex")
	seedRetailType(t, api, other)
	require.NoError(t, createCustomer(api, other, "acme-corp"),
		"a token retired in one tenant must stay free in another")
}

// The ledger row carries the tenant, which is what lets the tenant purge — the one thing
// that frees a retired token — find and remove it.
func TestTheIssuedTokenIsRecordedUnderItsTenant(t *testing.T) {
	api, ctx := newRetiredTokenApi(t)
	require.NoError(t, createCustomer(api, ctx, "acme-corp"))
	type row struct{ TenantId, Token string }
	var rows []row
	require.NoError(t, api.RDB.Database.Raw(
		"SELECT tenant_id, token FROM issued_customer_tokens").Scan(&rows).Error)
	require.Equal(t, []row{{TenantId: "acme", Token: "acme-corp"}}, rows)
}

// The control on the other side: a LIVE customer at the token is an already-exists, and
// keeps answering CONFLICT so a re-create stays idempotent for clients that rely on it.
func TestALiveCustomersTokenStillAnswersConflict(t *testing.T) {
	api, ctx := newRetiredTokenApi(t)
	require.NoError(t, createCustomer(api, ctx, "acme-corp"))
	err := createCustomer(api, ctx, "acme-corp")
	require.Error(t, err)
	require.True(t, conflict.Is(err), "a live duplicate must answer CONFLICT: %v", err)
	require.False(t, errors.Is(err, ErrCustomerTokenRetired),
		"a live duplicate was reported as a retired token: %v", err)
	require.Equal(t, int64(1), liveCustomers(t, api, ctx, "acme-corp"))
}

// A customer that reached the table WITHOUT CreateCustomer — any write path but the
// service's own — has no ledger row. Deleting it must still retire its token.
func TestDeletingACustomerTheLedgerNeverSawStillRetiresItsToken(t *testing.T) {
	api, ctx := newRetiredTokenApi(t)
	c := &Customer{CustomerTypeId: 1}
	c.Token = "legacy"
	require.NoError(t, api.RDB.DB(ctx).Create(c).Error, "seed a customer behind CreateCustomer's back")
	deleteCustomer(t, api, ctx, "legacy")

	requireRetired(t, createCustomer(api, ctx, "legacy"))
}

// The token is issued in the SAME transaction as the customer: a create whose customer
// write fails must not leave the token issued, or the corrected retry would be refused
// as retired for a customer that never existed.
func TestACreateWhoseCustomerWriteFailsDoesNotIssueTheToken(t *testing.T) {
	api, ctx := newRetiredTokenApi(t)
	db := api.RDB.Database
	fail := true
	name := "test:fail_customer_insert"
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if fail && tx.Statement.Table == "customers" {
			_ = tx.AddError(errors.New("injected customer write failure"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove(name) })

	require.Error(t, createCustomer(api, ctx, "acme-corp"), "the injected failure did not fire")
	fail = false
	require.NoError(t, createCustomer(api, ctx, "acme-corp"),
		"a create that failed issued the token anyway")
}

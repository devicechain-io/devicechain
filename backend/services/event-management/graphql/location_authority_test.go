// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A device's POSITION is gated on location:read, not on event:read. The two are
// separate authorities on purpose, and the separation only means something if BOTH
// directions hold: location:read alone reads position, and event:read alone does
// not. A test that only checked the happy path would pass just as well against the
// old event:read gate.

const authTestTenant = "tenant1"

// newLocationGateApi builds an Api over in-memory sqlite with the payload tables
// migrated, so a caller that CLEARS the gate reaches a real read rather than a nil
// dependency. Supplying it on every context — including the ones expected to be
// refused — is what makes a refusal attributable to the authority check and not to a
// missing api.
//
// measurement_events is migrated alongside location_events for the same reason: the
// telemetry query has to be able to SUCCEED here, or the test asserting that
// location:read cannot read telemetry would pass on a database error whichever
// authority the resolver checked.
func newLocationGateApi(t *testing.T) *model.Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err, "open sqlite")
	require.NoError(t, rdb.RegisterTenantScoping(db), "register tenant scoping")
	require.NoError(t, db.AutoMigrate(&model.Event{}, &model.LocationEvent{}, &model.MeasurementEvent{}),
		"migrate")
	return model.NewApi(&rdb.RdbManager{Database: db})
}

// The fixture's own control: with the right authority BOTH queries reach storage and
// return cleanly, so every refusal asserted below is the gate speaking and not a
// missing table.
func TestGateFixtureReadsBothWhenFullyAuthorized(t *testing.T) {
	api := newLocationGateApi(t)
	r := &SchemaResolver{}
	ctx := ctxHolding(t, api, string(auth.LocationRead), string(auth.EventRead))

	_, err := r.LocationEvents(ctx, emptyCriteria())
	require.NoError(t, err, "the location read must reach storage in this fixture")
	_, err = r.MeasurementEvents(ctx, emptyCriteria())
	require.NoError(t, err, "the telemetry read must reach storage in this fixture")
}

// ctxHolding builds a request context carrying a verified TENANT ACCESS token that
// grants exactly the listed authorities — the credential a human holds inside one
// tenant, which is the tier this gate has to be right for.
func ctxHolding(t *testing.T, api *model.Api, authorities ...string) context.Context {
	t.Helper()
	ctx := core.WithTenant(context.Background(), authTestTenant)
	ctx = auth.WithClaims(ctx, &auth.Claims{
		Tenant:      authTestTenant,
		Username:    "operator@example.com",
		TokenType:   auth.TokenTypeAccess,
		Authorities: authorities,
	})
	return context.WithValue(ctx, gqlcore.ContextApiKey, api)
}

// emptyCriteria is a well-formed, bounded criteria input — nothing about the query
// itself can be the reason a call succeeds or fails.
func emptyCriteria() struct{ Criteria EventSearchCriteriaInput } {
	return struct{ Criteria EventSearchCriteriaInput }{
		Criteria: EventSearchCriteriaInput{PageNumber: 1, PageSize: 10},
	}
}

// A caller holding location:read — and NOTHING else, not even event:read — reads
// position. Someone granted location access must not also need telemetry access.
func TestLocationEventsAllowedByLocationRead(t *testing.T) {
	api := newLocationGateApi(t)
	r := &SchemaResolver{}

	results, err := r.LocationEvents(ctxHolding(t, api, string(auth.LocationRead)), emptyCriteria())

	require.NoError(t, err, "location:read alone must be sufficient to read position")
	require.NotNil(t, results, "an authorized read returns results, not nil")
}

// 🔴 The case the separate authority exists for: a caller holding event:read — the
// authority every other event read is gated on — is REFUSED position. Without this
// assertion the change is untested, because a gate left on event:read passes every
// other test in this file.
func TestLocationEventsRefusedWithOnlyEventRead(t *testing.T) {
	api := newLocationGateApi(t)
	r := &SchemaResolver{}

	_, err := r.LocationEvents(ctxHolding(t, api, string(auth.EventRead)), emptyCriteria())

	assert.ErrorIs(t, err, auth.ErrForbidden,
		"event:read must NOT grant position; that equivalence is what location:read ends")
}

// The converse, so the split is shown to be a split and not a rename: a caller
// holding only location:read cannot read TELEMETRY either. A change that swapped
// every gate in the file over to location:read would pass the two tests above and
// fail here.
func TestMeasurementEventsRefusedWithOnlyLocationRead(t *testing.T) {
	api := newLocationGateApi(t)
	r := &SchemaResolver{}

	_, err := r.MeasurementEvents(ctxHolding(t, api, string(auth.LocationRead)), emptyCriteria())

	assert.ErrorIs(t, err, auth.ErrForbidden,
		"location:read must not carry telemetry access")
}

// An unauthenticated request is refused before anything is read, and is refused
// DISTINGUISHABLY (401 vs 403 at the transport).
func TestLocationEventsRefusedUnauthenticated(t *testing.T) {
	api := newLocationGateApi(t)
	r := &SchemaResolver{}

	ctx := context.WithValue(core.WithTenant(context.Background(), authTestTenant),
		gqlcore.ContextApiKey, api)
	_, err := r.LocationEvents(ctx, emptyCriteria())

	assert.ErrorIs(t, err, auth.ErrUnauthenticated,
		"a request with no verified claims must fail closed as unauthenticated")
}

// A subject holding the super-authority still reads position — the split narrows
// what a scoped grant carries, it does not carve an exception out of "*".
func TestLocationEventsAllowedByAuthorityAll(t *testing.T) {
	api := newLocationGateApi(t)
	r := &SchemaResolver{}

	_, err := r.LocationEvents(ctxHolding(t, api, string(auth.AuthorityAll)), emptyCriteria())

	require.NoError(t, err, `a holder of "*" must still read position`)
}

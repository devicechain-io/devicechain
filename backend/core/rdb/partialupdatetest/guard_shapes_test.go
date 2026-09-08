// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partialupdatetest

import (
	"context"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

// EVERY UPDATE SIGNATURE SHAPE ON THE PLATFORM, MEASURED RATHER THAN REASONED ABOUT.
//
// 🔴 THIS FILE EXISTS BECAUSE THE GUARD SILENTLY SKIPPED SEVEN REAL UPDATES. It located
// the request at "parameter 3 of exactly 4", and every method outside that shape fell
// through a `continue` into nothing: not counted, not exempt, not certified, and
// invisible to the anti-vacuity floor because the floor bounds what was counted.
//
// The shapes below are copied from the four services the sibling slices convert, so the
// guard's answer to each is a measurement of this code rather than an argument about it:
//
//	dashboard-management  UpdateDashboard(ctx, token string, request *DashboardCreateRequest, expectedUpdatedAt *string)
//	outbound-connectors   UpdateConnector(ctx, token string, request *ConnectorCreateRequest, expectedUpdatedAt *string)
//	ai-inference          UpdateAIProvider(ctx, token string, request *AIProviderCreateRequest, expectedUpdatedAt *string)
//	user-management       UpdateRole(ctx, scope, token string, in RoleMutableInput)
//	user-management       UpdateTenant(ctx, token string, in TenantMutableInput)
//	user-management       UpdateTenantTier(ctx, token string, in TierMutableInput)
//	user-management       UpdateOAuthClient(ctx, clientId string, in OAuthClientMutableInput)
//	user-management       UpdateProfile(ctx, email string, firstName, lastName *string)
//
// The first three are one shape (a trailing precondition pointer), the next four are one
// shape (an input by value, sometimes behind a leading scope argument), and the last is
// its own: loose scalars with no input object at all. All three are represented here,
// plus the ambiguous case the locator must refuse rather than guess at.

type shapeInput struct {
	Name dcgraphql.OptionalString
}

type shapeValueInput struct {
	Name dcgraphql.OptionalString
}

type shapeApi struct{}

// The dashboard/connector/aiProvider shape: the request is parameter 3 of FIVE, with a
// trailing optimistic-concurrency pointer the old positional rule tripped over.
func (a *shapeApi) UpdateWithPrecondition(ctx context.Context, token string,
	request *shapeInput, expectedUpdatedAt *string) error {
	return nil
}

// The user-management admin shape: the input arrives BY VALUE, behind a leading scope
// argument. Two independent reasons the positional rule missed it.
func (a *shapeApi) UpdateByValue(ctx context.Context, scope, token string,
	in shapeValueInput) error {
	return nil
}

// The UpdateProfile shape: loose scalars, no input object anywhere. There is nothing here
// for this rule to certify, which is a thing to SAY rather than to skip.
func (a *shapeApi) UpdateLooseScalars(ctx context.Context, email string,
	firstName, lastName *string) error {
	return nil
}

// Not a real shape on the platform — the locator's refusal case. Two struct parameters
// and the guard must not pick one.
func (a *shapeApi) UpdateAmbiguous(ctx context.Context, a1 shapeValueInput,
	a2 *shapeInput) error {
	return nil
}

// registryRow builds a Family carrying only what the GUARD reads — its name and the
// request type NewRequest returns. Seed/Read/Update are nil on purpose: the guard never
// calls them, and giving these shapes a database would be building fixtures for an
// assertion about reflection.
func registryRow[R any](name string) Family[*shapeApi] {
	return Family[*shapeApi]{Name: name, NewRequest: func() any { return new(R) }}
}

func shapeSurface() UpdateSurface[*shapeApi] {
	return UpdateSurface[*shapeApi]{
		Families: []Family[*shapeApi]{
			registryRow[shapeInput]("withPrecondition"),
			registryRow[shapeValueInput]("byValue"),
		},
		NotAnEntityUpdate: map[string]string{
			"UpdateLooseScalars": "takes loose scalars rather than an input object, so there " +
				"is no update input to convert; collecting them into one is its own change",
			"UpdateAmbiguous": "a locator refusal case declared here so the positive control " +
				"can cover the other three shapes without it",
		},
		MinUpdateMethods: 4,
	}
}

// THE POSITIVE CONTROL FOR THE LOCATOR. The two shapes that DO carry an input object are
// found and certified; the two that do not are named, with reasons.
func TestGuardFindsTheRequestInEveryRealParameterShape(t *testing.T) {
	AssertEveryUpdateTakesADedicatedRequest(t, shapeSurface())
}

// And the reading that matters most, stated as its own assertion rather than left implied
// by the green above: the guard COUNTS all four, so the anti-vacuity floor now bounds
// every Update* method rather than only the ones that happened to match a shape. Under
// the old positional rule this count was 0.
func TestTheFloorCountsEveryUpdateMethodNotOnlyTheMatchingOnes(t *testing.T) {
	s := shapeSurface()
	s.MinUpdateMethods = 4
	AssertEveryUpdateTakesADedicatedRequest(t, s)

	// The counterweight: a floor of five must FAIL, or "four were counted" is a claim the
	// test above cannot distinguish from "the floor is not checked". That failure is a
	// t.Fatal, so it is driven as a mutant in negative_control_test.go
	// ("shapefloortoohigh") rather than here.
}

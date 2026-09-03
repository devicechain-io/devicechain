// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-dashboard-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	core "github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// The authorities every enabled tenant member receives whether or not they hold any
// role — user-management's viewer baseline (identity.viewerAuthorities). It is spelled
// out here rather than imported because it lives in another module; a test over there
// pins its exact membership, which is what keeps this list an accurate stand-in for "a
// read-only user".
var viewerBaseline = []auth.Authority{
	auth.DeviceRead, auth.EventRead, auth.StateRead, auth.CommandRead, auth.AlarmRead,
	auth.DashboardRead,
}

// dashboardTestCtx builds a context carrying a real sqlite-backed Api and a tenant —
// what the dashboard resolvers read out of context once past the gate. It carries NO
// claims, which is how an unauthenticated request arrives. The Api is real so an
// authorized call runs the whole query path rather than proving only that it got past
// the gate.
func dashboardTestCtx(t *testing.T) context.Context {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.Dashboard{}, &model.DashboardVersion{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	return context.WithValue(ctx, gqlcore.ContextApiKey, model.NewApi(&rdb.RdbManager{Database: db}))
}

func withAuthorities(ctx context.Context, authorities ...auth.Authority) context.Context {
	strs := make([]string, 0, len(authorities))
	for _, a := range authorities {
		strs = append(strs, string(a))
	}
	return auth.WithClaims(ctx, &auth.Claims{Authorities: strs})
}

// callAllThree exercises every query that gates on dashboard:read.
func callAllThree(t *testing.T, ctx context.Context, check func(name string, err error)) {
	t.Helper()
	r := &SchemaResolver{}

	_, err := r.Dashboard(ctx, struct{ Token string }{Token: "nothing"})
	check("dashboard", err)

	_, err = r.Dashboards(ctx, struct{ Criteria model.DashboardSearchCriteria }{
		Criteria: model.DashboardSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}},
	})
	check("dashboards", err)

	_, err = r.DashboardVersions(ctx, struct{ Token string }{Token: "nothing"})
	check("dashboardVersions", err)
}

// A member holding only the read-only viewer baseline can open a dashboard.
//
// 🔴 THIS IS THE TEST THE BASELINE CHANGE ACTUALLY NEEDED, AND ITS ABSENCE WAS INVISIBLE.
// user-management pins what the baseline CONTAINS, and pins that a member's token carries
// dashboard:read. Neither says anything about what this service does with it. A review
// flipped all three gates below to auth.DashboardWrite and the whole estate stayed green:
// every test that mentioned the baseline lived on the other side of the module boundary,
// and nothing here had ever been handed one.
//
// So the two halves are now joined the way device-management's credential test joins
// them: the caller is given the FULL baseline rather than a bare dashboard:read, so this
// cannot pass merely because some other read authority happened to be missing.
func TestDashboardQueriesAdmitTheViewerBaseline(t *testing.T) {
	ctx := withAuthorities(dashboardTestCtx(t), viewerBaseline...)

	callAllThree(t, ctx, func(name string, err error) {
		// Not-forbidden rather than nil: dashboardVersions answers "record not found"
		// for a token nothing seeded, which is the query path REPLYING and therefore
		// proof the caller cleared the gate. Demanding nil would make this test about
		// fixture setup instead of authorization.
		if errors.Is(err, auth.ErrForbidden) {
			t.Errorf("%s refused a caller holding the read-only viewer baseline — an ordinary "+
				"tenant member cannot open a dashboard, which is the defect dashboard:read was "+
				"added to the baseline to fix", name)
		}
	})

	// And the whole path, not just the gate: a real dashboard read back by a caller who
	// holds nothing but the baseline.
	//
	// What this adds is narrow, so it is worth saying exactly: it shows `dashboard` really
	// answers for a baseline-only caller, rather than clearing the gate and then failing
	// for some unrelated reason. It does NOT exercise the gate — with the gates removed it
	// passes — and the other two queries have no happy-path assertion of their own. The
	// test below is what makes a missing gate fail.
	r := &SchemaResolver{}
	name := "Ops overview"
	if _, err := r.GetApi(ctx).CreateDashboard(ctx, &model.DashboardCreateRequest{
		Token: "ops-overview", Name: &name, Definition: "{}",
	}); err != nil {
		t.Fatalf("seed dashboard: %v", err)
	}
	got, err := r.Dashboard(ctx, struct{ Token string }{Token: "ops-overview"})
	if err != nil {
		t.Fatalf("reading a seeded dashboard as a baseline-only caller: %v", err)
	}
	if got == nil {
		t.Fatal("a baseline-only caller read no dashboard back, though one was seeded")
	}
}

// The counterweight: the gates still exist. Without this, deleting every Authorize call
// would satisfy the test above perfectly.
func TestDashboardQueriesRefuseACallerWithoutTheAuthority(t *testing.T) {
	// Every baseline authority EXCEPT dashboard:read, so the refusal is attributable to
	// the one authority under test rather than to holding nothing at all.
	ctx := withAuthorities(dashboardTestCtx(t),
		auth.DeviceRead, auth.EventRead, auth.StateRead, auth.CommandRead, auth.AlarmRead)

	callAllThree(t, ctx, func(name string, err error) {
		if !errors.Is(err, auth.ErrForbidden) {
			t.Errorf("%s admitted a caller holding every read authority EXCEPT dashboard:read "+
				"(err=%v); the gate is not gating on what it claims to", name, err)
		}
	})
}

// An unauthenticated caller is refused, and refused DISTINGUISHABLY from one who
// authenticated but lacks the authority.
//
// 🔴 `err != nil` is not enough, and a review proved it: with only the DashboardVersions
// gate removed, an anonymous caller reaches the query path and comes back with
// gorm.ErrRecordNotFound — non-nil, so the weaker assertion stayed green while the gate
// was gone. The same "a not-found is the query path replying" that makes the admit test
// sound makes this one vacuous, in the opposite direction. Asserting the specific
// sentinel is what closes it, as device-state's location test already does.
func TestDashboardQueriesRefuseAnAnonymousCaller(t *testing.T) {
	callAllThree(t, dashboardTestCtx(t), func(name string, err error) {
		if !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("%s answered %v for a caller with no claims at all, want %v — an "+
				"unauthenticated caller must be refused, and refused distinguishably from "+
				"one who authenticated and lacks the authority", name, err, auth.ErrUnauthenticated)
		}
	})
}

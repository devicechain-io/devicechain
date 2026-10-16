// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package isolationsweep

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
)

const sweepSDL = `
	schema { query: Query mutation: Mutation subscription: Subscription }
	type Query {
		devices: [Device!]!
		device(token: String!, verbose: Boolean): Device
		echo(token: String!): String
		customers: [String!]!
	}
	type Mutation { rename(token: String!): Boolean! }
	type Subscription { stream: String! watch(token: String!): String! }
	type Device { token: String! owner: String! }
`

// sweepRoot serves two customers' devices. leaky=true makes `devices` ignore the scope —
// the defect the sweep exists to catch.
type sweepRoot struct{ leaky, oracle, leakyCustomers, oracleStream, oracleMessage bool }

type device struct{ token, owner string }

var sweepDevices = []device{{"acme-pump", "acme"}, {"globex-pump", "globex"}}

type deviceResolver struct{ d device }

func (r deviceResolver) Token() string { return r.d.token }
func (r deviceResolver) Owner() string { return r.d.owner }

func visible(ctx context.Context, d device) bool {
	s, scoped := auth.ScopeFromContext(ctx)
	if !scoped {
		return true
	}
	for _, a := range s.Anchors() {
		if a == "customer:"+d.owner {
			return true
		}
	}
	return false
}

func (r *sweepRoot) Devices(ctx context.Context) ([]deviceResolver, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	out := []deviceResolver{}
	for _, d := range sweepDevices {
		if r.leaky || visible(ctx, d) {
			out = append(out, deviceResolver{d})
		}
	}
	return out, nil
}

func (r *sweepRoot) Device(ctx context.Context, args struct {
	Token   string
	Verbose *bool
}) (*deviceResolver, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	for _, d := range sweepDevices {
		if d.token == args.Token && visible(ctx, d) {
			return &deviceResolver{d}, nil
		}
		if d.token == args.Token && r.oracle {
			return nil, auth.ErrForbidden // confirms the row exists
		}
		if d.token == args.Token && r.oracleMessage {
			// The same code as a miss, but the wording confirms the row exists.
			return nil, fmt.Errorf("device %q: not found (it belongs to another customer)", args.Token)
		}
	}
	// Echoes the caller's own argument: not a leak.
	return nil, fmt.Errorf("device %q: not found", args.Token)
}

// Customers names the caller's customers; leakyCustomers names every one.
func (r *sweepRoot) Customers(ctx context.Context) []string {
	if r.leakyCustomers {
		return []string{"acme", "globex"}
	}
	if s, scoped := auth.ScopeFromContext(ctx); scoped && len(s.Anchors()) > 0 {
		return []string{"acme"}
	}
	return []string{"acme", "globex"}
}

func (r *sweepRoot) Echo(args struct{ Token string }) *string { return &args.Token }

func (r *sweepRoot) Rename(ctx context.Context, args struct{ Token string }) (bool, error) {
	return false, auth.Authorize(ctx, auth.DeviceWrite)
}

func (r *sweepRoot) Stream(ctx context.Context) (<-chan string, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	ch := make(chan string, 1)
	ch <- "globex"
	close(ch)
	return ch, nil
}

// Watch streams one device's readings. oracleStream makes it refuse a hidden device
// FORBIDDEN where a missing one is not found — the same existence oracle as Device's,
// answered at subscribe time rather than in a response.
func (r *sweepRoot) Watch(ctx context.Context, args struct{ Token string }) (<-chan string, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	for _, d := range sweepDevices {
		if d.token == args.Token && visible(ctx, d) {
			ch := make(chan string, 1)
			ch <- "reading"
			close(ch)
			return ch, nil
		}
		if d.token == args.Token && r.oracleStream {
			return nil, auth.ErrForbidden // confirms the device exists
		}
	}
	return nil, fmt.Errorf("device %q: not found", args.Token)
}

func memberCtx(anchors ...string) context.Context {
	return auth.WithClaims(core.WithTenant(context.Background(), "t1"), &auth.Claims{
		TokenType: auth.TokenTypeAccess, Tenant: "t1", Authorities: []string{"*"}, ScopeAnchors: anchors})
}

func config(root *sweepRoot) Config {
	return Config{
		Schema:   gqlcore.MustParseSchema(sweepSDL, root),
		Scoped:   memberCtx("customer:acme"),
		Unscoped: memberCtx(),
		Probe:    "globex-pump",
		Hidden:   []string{"globex"}, // the hidden owner; "globex-pump" contains it too
	}
}

// recorder captures what the sweep reports; Fatalf ends the sweep, as on a *testing.T.
type recorder struct{ errors, fatals []string }

type fatal struct{}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, a ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, a...))
}
func (r *recorder) Fatalf(format string, a ...any) {
	r.fatals = append(r.fatals, fmt.Sprintf(format, a...))
	panic(fatal{})
}

func runRecorded(cfg Config) (rec *recorder, out []Outcome) {
	rec = &recorder{}
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(fatal); !ok {
				panic(p)
			}
		}
	}()
	out = Run(rec, cfg)
	return rec, out
}

// A correct service sweeps clean, every operation is reached, and the probe echoed back in
// an error is not mistaken for a leak.
func TestACorrectServiceSweepsClean(t *testing.T) {
	rec, out := runRecorded(config(&sweepRoot{}))
	if len(rec.errors)+len(rec.fatals) > 0 {
		t.Fatalf("a correct service failed the sweep: %v %v", rec.errors, rec.fatals)
	}
	for _, name := range []string{"query devices", "query device", "query echo", "mutation rename", "subscription stream", "subscription watch",
		"query device (all arguments)"} {
		if _, ok := OutcomeOf(out, name); !ok {
			t.Errorf("the sweep never reached %s", name)
		}
	}
	// A refused stream answers an error (its code is added by the transport, which this
	// in-process sweep does not run), and no data.
	if o, _ := OutcomeOf(out, "subscription stream"); len(o.Codes) == 0 || o.Data {
		t.Errorf("the gated stream answered %+v, want a refusal", o)
	}
}

// 🔴 The sweep catches a resolver that opted in and then forgot the scope.
func TestTheSweepCatchesALeakingResolver(t *testing.T) {
	rec, _ := runRecorded(config(&sweepRoot{leaky: true}))
	if len(rec.errors) == 0 || !strings.Contains(strings.Join(rec.errors, "\n"), "query devices") {
		t.Fatalf("a resolver that served globex's device to acme's member passed the sweep: %v", rec.errors)
	}
}

// 🔴 A fixture with nothing to leak refuses to report clean.
func TestTheSweepRefusesAFixtureWithNothingToLeak(t *testing.T) {
	cfg := config(&sweepRoot{})
	cfg.Hidden = []string{"initech"}
	rec, _ := runRecorded(cfg)
	if len(rec.fatals) == 0 || !strings.Contains(rec.fatals[0], "NEGATIVE CONTROL") {
		t.Fatalf("a sweep whose control saw nothing reported %v %v", rec.errors, rec.fatals)
	}
}

// 🔴 The sweep catches an existence oracle: a hidden row refused FORBIDDEN where a missing
// one is not found tells the caller the row exists, though no data of it is served.
func TestTheSweepCatchesAnExistenceOracle(t *testing.T) {
	rec, _ := runRecorded(config(&sweepRoot{oracle: true}))
	if len(rec.errors) == 0 || !strings.Contains(strings.Join(rec.errors, "|"), "query device ") {
		t.Fatalf("a resolver that refused a hidden row differently from a missing one passed: %v", rec.errors)
	}
}

// 🔴 The sweep catches a leak that never mentions the probe: another customer's record
// served by a read that takes no argument at all, identically on both calls.
func TestTheSweepCatchesALeakThatNeverNamesTheProbe(t *testing.T) {
	rec, _ := runRecorded(config(&sweepRoot{leakyCustomers: true}))
	if len(rec.errors) == 0 || !strings.Contains(strings.Join(rec.errors, "|"), "query customers") {
		t.Fatalf("a read that listed globex for acme's member passed the sweep: %v", rec.errors)
	}
}

// 🔴 The sweep catches an existence oracle answered at subscribe time: a stream that
// refuses a hidden device FORBIDDEN where a missing one is not found. A subscription is
// judged against its missing twin exactly as a query is, so the refusal's text is read,
// not just whether a response arrived.
func TestTheSweepCatchesASubscriptionExistenceOracle(t *testing.T) {
	rec, _ := runRecorded(config(&sweepRoot{oracleStream: true}))
	if len(rec.errors) == 0 || !strings.Contains(strings.Join(rec.errors, "|"), "subscription watch") {
		t.Fatalf("a stream that refused a hidden device differently from a missing one passed: %v", rec.errors)
	}
}

// 🔴 The sweep catches an oracle that differs in WORDING only: the same code and the same
// shape as a miss, with a message that says the row exists. Codes are not the answer;
// everything the caller can read is, and the two transcripts must be byte-identical.
func TestTheSweepCatchesAMessageOnlyOracle(t *testing.T) {
	rec, _ := runRecorded(config(&sweepRoot{oracleMessage: true}))
	if len(rec.errors) == 0 || !strings.Contains(strings.Join(rec.errors, "|"), "query device ") {
		t.Fatalf("a resolver whose refusal differed from a miss only in wording passed: %v", rec.errors)
	}
}

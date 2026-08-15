// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
)

// This file exercises drainableCommands THROUGH THE SCHEMA — real GraphQL, real
// resolver, real (sqlite) database, arguments sent as VARIABLES.
//
// Variables rather than query literals for the reason statuses_wire_test.go sets out at
// length: every real client sends variables, and it is the path where this library
// historically diverged from the spec. It matters more here than usual, because the whole
// point of this field is to move work OUT of the client — if the argument binding is
// wrong, the drain silently reads somebody else's backlog or an unbounded one.

// antiTokenBase makes a seeded row's token sort OPPOSITE to its id.
//
// 🔴 THIS IS WHAT MAKES THE ORDERING ASSERTION AN INSTRUMENT RATHER THAN A DECORATION,
// and it was arrived at only after the obvious fixtures were caught passing against a
// query with NO ORDER BY at all. sqlite hands back a scan in rowid order and id IS the
// rowid, so explicit descending inserts are re-sorted ascending for free; and with a
// token derived from the id, the planner's choice of the (tenant_id, token) unique index
// re-supplies the same order a second way. Anti-correlating the token defeats both: every
// order sqlite could substitute is now visibly wrong.
const antiTokenBase = 99999

// antiToken is the token seeded for a given id, and the form every assertion below
// compares against — so a fixture and its expectation cannot drift apart.
func antiToken(id uint) string {
	return fmt.Sprintf("wire-drain-%05d", antiTokenBase-id)
}

// seedDrainRow writes one command with an EXPLICIT id and expiry, bypassing
// createCommand — the point is to control the primary key, which the enqueue path
// (correctly) will not let a caller do.
func seedDrainRow(t *testing.T, ctx context.Context, api *model.Api, id uint,
	deviceToken string, status model.CommandStatus, expiresAt sql.NullTime) {
	t.Helper()
	cmd := &model.Command{
		DeviceToken: deviceToken,
		Name:        "reboot",
		Status:      status.String(),
		ExpiresAt:   expiresAt,
	}
	cmd.ID = id
	cmd.Token = antiToken(id)
	if err := api.RDB.DB(ctx).Create(cmd).Error; err != nil {
		t.Fatalf("seeding command %d: %v", id, err)
	}
}

const drainQuery = `query($deviceToken: String!, $limit: Int) {
  drainableCommands(deviceToken: $deviceToken, limit: $limit) { token status }
}`

// decodeDrainTokens reads the token list IN THE ORDER RETURNED. A map would lose exactly
// the property this suite exists to pin.
func decodeDrainTokens(t *testing.T, data json.RawMessage) []string {
	t.Helper()
	var out struct {
		DrainableCommands []struct {
			Token  string `json:"token"`
			Status string `json:"status"`
		} `json:"drainableCommands"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decoding drainableCommands failed: %v", err)
	}
	tokens := make([]string, 0, len(out.DrainableCommands))
	for _, r := range out.DrainableCommands {
		tokens = append(tokens, r.Token)
	}
	return tokens
}

// TestDrainableCommandsReturnsOldestFirstOverTheWire.
//
// The ORDER is the assertion, and it survives all the way to the JSON: the transport
// dispatches the list as it arrives and does not re-sort it — that client-side sort is
// exactly what this field replaced. A GraphQL list resolver that built its result from a
// map, or a schema that returned a paged wrapper with its own default order, would return
// the right rows in the wrong sequence and hand a waking device a firmware image out of
// order.
func TestDrainableCommandsReturnsOldestFirstOverTheWire(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	for _, id := range []uint{50, 20, 40, 10, 30} {
		seedDrainRow(t, ctx, api, id, "device-1", model.CommandHeld, sql.NullTime{})
	}

	got := decodeDrainTokens(t, exec(t, ctx, drainQuery, map[string]any{
		"deviceToken": "device-1",
		"limit":       10,
	}))
	want := []string{antiToken(10), antiToken(20), antiToken(30), antiToken(40), antiToken(50)}
	if len(got) != len(want) {
		t.Fatalf("drained %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("drained %v, want %v — the backlog must reach the wire in ENQUEUE order", got, want)
		}
	}
}

// TestDrainableCommandsOverTheWireDropsExpiredAndKeepsAnAbsentHorizon.
//
// PARKED is in the fixture beside HELD because the two together are the drain set, and
// PARKED is the half no existing index or query covered — a filter that quietly reduced to
// HELD alone would still look correct on a HELD-only fixture.
func TestDrainableCommandsOverTheWireDropsExpiredAndKeepsAnAbsentHorizon(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	// Local time, deliberately: the sqlite harness stores a time.Time as offset-bearing
	// TEXT and compares it lexically, so a fixture written in UTC would be compared
	// string-first against the resolver's local time.Now() and every expired row would
	// come back live. Production is Postgres, where the comparison is instant-based; this
	// is a property of the harness, and getting it wrong measures the driver instead of
	// the filter.
	now := time.Now()
	seedDrainRow(t, ctx, api, 10, "device-1", model.CommandHeld,
		sql.NullTime{Time: now.Add(-time.Hour), Valid: true})
	seedDrainRow(t, ctx, api, 20, "device-1", model.CommandHeld, sql.NullTime{})
	seedDrainRow(t, ctx, api, 30, "device-1", model.CommandParked,
		sql.NullTime{Time: now.Add(-time.Minute), Valid: true})
	seedDrainRow(t, ctx, api, 40, "device-1", model.CommandParked,
		sql.NullTime{Time: now.Add(time.Hour), Valid: true})
	// A QUEUED row, which the sweep still owns: draining it would put one command in
	// front of two dispatchers on two transports.
	seedDrainRow(t, ctx, api, 50, "device-1", model.CommandQueued, sql.NullTime{})

	got := decodeDrainTokens(t, exec(t, ctx, drainQuery, map[string]any{
		"deviceToken": "device-1",
		"limit":       10,
	}))
	want := []string{antiToken(20), antiToken(40)}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("drained %v, want %v: the NULL horizon means NO horizon and must be drained, the "+
			"future horizon is still live, the two past horizons must not actuate the device, and "+
			"the QUEUED row belongs to the sweep", got, want)
	}
}

// TestDrainableCommandsOmittedLimitTakesTheServerDefault: the bound is the SERVER'S now.
//
// A null/absent variable must reach the model as "use the default" rather than as a
// literal 0, which is how an optional Int argument silently becomes an unbounded read —
// the caller asks for nothing and gets everything.
func TestDrainableCommandsOmittedLimitTakesTheServerDefault(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	const total = model.DefaultDrainLimit + 5
	for i := total; i >= 1; i-- {
		seedDrainRow(t, ctx, api, uint(i), "device-1", model.CommandHeld, sql.NullTime{})
	}

	// The variable is declared but sent as null, which is the shape a client that omits an
	// optional field actually puts on the wire.
	got := decodeDrainTokens(t, exec(t, ctx, drainQuery, map[string]any{
		"deviceToken": "device-1",
		"limit":       nil,
	}))
	if len(got) != model.DefaultDrainLimit {
		t.Fatalf("a null limit drained %d rows, want the server default %d",
			len(got), model.DefaultDrainLimit)
	}
	if got[0] != antiToken(1) {
		t.Fatalf("the default drain starts at %s, want the OLDEST row %s — a bound that truncates "+
			"the wrong end drops the beginning of every sequence", got[0], antiToken(1))
	}
}

// TestDrainableCommandsHonoursTheLimitArgumentOverTheWire.
//
// 🔴 THIS EXISTS BECAUSE ITS ABSENCE WAS CAUGHT BY A NEGATIVE CONTROL. Deleting the
// resolver's handling of args.Limit — so that every request silently fell through to the
// server default — left the whole wire suite GREEN, because every other fixture here is
// smaller than the default and therefore cannot tell 5 rows from "at most 32". A caller
// asking for 2 and receiving 40 is the exact failure the argument exists to prevent, and
// nothing was measuring it.
//
// The fixture is therefore deliberately LARGER than the limit under test, and the
// assertion is on the identity of the rows as well as the count: an ignored limit returns
// more rows, and a limit applied to the wrong end returns the right number of the wrong
// ones.
func TestDrainableCommandsHonoursTheLimitArgumentOverTheWire(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	for _, id := range []uint{50, 20, 40, 10, 30} {
		seedDrainRow(t, ctx, api, id, "device-1", model.CommandHeld, sql.NullTime{})
	}

	got := decodeDrainTokens(t, exec(t, ctx, drainQuery, map[string]any{
		"deviceToken": "device-1",
		"limit":       2,
	}))
	want := []string{antiToken(10), antiToken(20)}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("limit:2 drained %v, want the oldest two %v. More rows than asked for means the "+
			"argument never reached the model; different rows mean the bound took the wrong end", got, want)
	}
}

// TestDrainableCommandsRequiresTheClaimAuthority.
//
// 🔑 THIS IS A DELIBERATE TIGHTENING, NOT A COPY OF THE READS BESIDE IT. Every other
// command read on this schema takes command:read, which is TENANT-tier and therefore
// reachable by an ordinary user token. This one takes command:claim, which is
// SYSTEM-tier — so the field is machine-only, exactly like markCommandSent and
// parkCommand. It costs the real caller nothing, because lwm2m-ingest already mints
// command:claim to claim each row it drains.
//
// The sharp cases are the tier ones. A tenant token holding command:claim outright, and a
// tenant token holding "*", must BOTH be refused: "*" means every authority at the
// bearer's own tier and must never reach across to a system-tier one. A gate written as a
// token-type check instead of an authority could not express that, and one written on
// command:read would let any console user ask a machine question.
func TestDrainableCommandsRequiresTheClaimAuthority(t *testing.T) {
	cases := []struct {
		name         string
		claims       *auth.Claims
		mustBeDenied string
	}{
		{
			name: "a tenant token holding command:read",
			claims: &auth.Claims{
				Authorities: []string{string(auth.CommandRead)},
				TokenType:   auth.TokenTypeAccess,
			},
			mustBeDenied: "command:read is tenant-tier; gating this drain on it would make the " +
				"dispatch-shaped view of a device's backlog readable by any console user",
		},
		{
			name: "a service token holding command:write but not command:claim",
			claims: &auth.Claims{
				Authorities: []string{string(auth.CommandRead), string(auth.CommandWrite)},
				TokenType:   auth.TokenTypeService,
			},
			mustBeDenied: "command:write must NOT confer the drain; REACT's send-command sink mints " +
				"exactly command:write and has no business reading a device's dispatch queue",
		},
		{
			name: "a tenant access token holding command:claim",
			claims: &auth.Claims{
				Authorities: []string{string(auth.CommandClaim)},
				TokenType:   auth.TokenTypeAccess,
			},
			mustBeDenied: "command:claim is system-tier, so a tenant access token must never satisfy it",
		},
		{
			name: "a tenant access token holding the super-authority",
			claims: &auth.Claims{
				Authorities: []string{string(auth.AuthorityAll)},
				TokenType:   auth.TokenTypeAccess,
			},
			mustBeDenied: `"*" means every authority at the BEARER'S tier; it must not reach a system-tier one`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, api := newWireTestCtx(t)
			seedDrainRow(t, ctx, api, 10, "device-1", model.CommandHeld, sql.NullTime{})

			denied := auth.WithClaims(ctx, tc.claims)
			schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
			res := schema.Exec(denied, drainQuery, "", map[string]any{
				"deviceToken": "device-1",
				"limit":       10,
			})
			if len(res.Errors) == 0 {
				t.Fatalf("%s (the drain was allowed)", tc.mustBeDenied)
			}
		})
	}

	// The counterweight, without which every assertion above is satisfied by a field that
	// is simply broken for everybody — including its one real caller.
	t.Run("a service token holding command:claim is allowed", func(t *testing.T) {
		ctx, api := newWireTestCtx(t)
		seedDrainRow(t, ctx, api, 10, "device-1", model.CommandHeld, sql.NullTime{})

		svc := auth.WithClaims(ctx, &auth.Claims{
			Authorities: []string{string(auth.CommandClaim)},
			TokenType:   auth.TokenTypeService,
		})
		got := decodeDrainTokens(t, exec(t, svc, drainQuery, map[string]any{
			"deviceToken": "device-1",
			"limit":       10,
		}))
		if len(got) != 1 || got[0] != antiToken(10) {
			t.Fatalf("a service token holding command:claim drained %v, want the one held row; "+
				"lwm2m-ingest is the only caller and the drain would be dead", got)
		}
	})
}

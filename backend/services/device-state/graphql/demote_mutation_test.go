// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/presence"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"gorm.io/gorm"
)

// demoteWriter captures what the mutation publishes, so the tests below assert on the
// events rather than only on the counts the resolver returns.
type demoteWriter struct{ msgs []messaging.Message }

func (w *demoteWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	w.msgs = append(w.msgs, msgs...)
	return nil
}
func (w *demoteWriter) WriteToDevice(context.Context, string, ...messaging.Message) error {
	panic("a demotion is tenant-shaped and must never take the per-device subject")
}
func (w *demoteWriter) HandleResponse(error) {}

// demoteArgs is the resolver's argument struct, named once so the tests read as calls.
//
// 🔴 DeviceTokens IS A POINTER, AND THAT IS THE WHOLE POINT OF NAMING IT HERE. Omitted
// and empty are different requests — the first demotes the whole source, the second
// demotes nothing — and a []string would collapse them onto one nil slice.
type demoteArgs = struct {
	Source       string
	DeviceTokens *[]string
	Limit        int32
	AfterId      *gql.ID
	Reason       string
}

// demoteTestCtx builds a context with a real device-state Api over in-memory sqlite, a
// capturing writer, a tenant, and n seeded ASSERTED rows for "mqtt1". It carries NO
// claims — authorities are layered on with withAuthorities, as the middleware would.
func demoteTestCtx(t *testing.T, n int) (context.Context, *demoteWriter) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.DeviceState{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	api := model.NewApi(&rdb.RdbManager{Database: db})
	w := &demoteWriter{}
	api.SetDemotionEmitter(model.NewDemotionEmitter(w, func() time.Time {
		return time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	}))

	ctx := core.WithTenant(context.Background(), "acme")
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, api)

	at := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	for i := range n {
		token := "mqtt1-dev-" + string(rune('a'+i))
		if _, err := api.MergeDeviceState(ctx, token, at, &model.PresenceTransition{
			Claim: presence.ClaimConnected, SessionId: uint64(i + 1), OccurredAt: at,
		}, model.DeviceIdentity{Source: "mqtt1"}); err != nil {
			t.Fatalf("seed %s: %v", token, err)
		}
	}
	return ctx, w
}

// TestTheDemotionRefusesEveryCallerWithoutStateDemote is the gate.
//
// 🔴 THE VIEWER CASE IS THE ONE THAT MATTERS. The caller is not a stranger: they hold the
// entire read-only baseline every enabled tenant member receives, including the state:read
// that serves every other operation on this service. They still must not be able to hand a
// whole event source's fleet back to inferred presence — this is the only door outside the
// event pipeline that writes this projection.
func TestTheDemotionRefusesEveryCallerWithoutStateDemote(t *testing.T) {
	r := &SchemaResolver{}
	args := demoteArgs{Source: "mqtt1", Limit: 10, Reason: "repair"}

	t.Run("anonymous", func(t *testing.T) {
		ctx, w := demoteTestCtx(t, 3)
		if _, err := r.DemoteAssertedPresence(ctx, args); err != auth.ErrUnauthenticated {
			t.Fatalf("an anonymous caller got %v, want ErrUnauthenticated", err)
		}
		if len(w.msgs) != 0 {
			t.Fatalf("%d demotion(s) published for an unauthenticated caller", len(w.msgs))
		}
	})

	t.Run("the read-only viewer baseline", func(t *testing.T) {
		ctx, w := demoteTestCtx(t, 3)
		ctx = withAuthorities(ctx, viewerBaseline...)
		if _, err := r.DemoteAssertedPresence(ctx, args); err != auth.ErrForbidden {
			t.Fatalf("the viewer baseline got %v, want ErrForbidden — state:read now grants a "+
				"fleet-wide presence write", err)
		}
		if len(w.msgs) != 0 {
			t.Fatalf("%d demotion(s) published for a read-only caller", len(w.msgs))
		}
	})

	t.Run("the authority check runs before argument validation", func(t *testing.T) {
		ctx, _ := demoteTestCtx(t, 0)
		// An unauthorized caller AND an unusable limit: the authority error is the one
		// that must come back, or a stranger learns which limits are legal.
		_, err := r.DemoteAssertedPresence(ctx, demoteArgs{Source: "mqtt1", Limit: 0, Reason: "x"})
		if err == nil {
			t.Fatal("an unauthenticated caller was admitted")
		}
		if strings.Contains(err.Error(), "limit") {
			t.Fatalf("argument validation ran ahead of the authority check, got %v", err)
		}
	})
}

// TestTheDemotionAdmitsStateDemote is the counterweight, without which the gate tests
// above are satisfied by a resolver that refuses everyone.
func TestTheDemotionAdmitsStateDemote(t *testing.T) {
	ctx, w := demoteTestCtx(t, 3)
	ctx = withAuthorities(ctx, auth.StateDemote)

	got, err := (&SchemaResolver{}).DemoteAssertedPresence(ctx, demoteArgs{
		Source: "mqtt1", Limit: 10, Reason: "the broker was retired",
	})
	if err != nil {
		t.Fatalf("a caller holding state:demote was refused: %v", err)
	}
	if got.Scanned() != 3 || got.Demoted() != 3 || got.Skipped() != 0 {
		t.Fatalf("scanned/demoted/skipped = %d/%d/%d, want 3/3/0",
			got.Scanned(), got.Demoted(), got.Skipped())
	}
	if len(w.msgs) != 3 {
		t.Fatalf("published %d demotions, want 3", len(w.msgs))
	}
	if got.LastId() == nil {
		t.Fatal("lastId is null on a page that scanned rows; the caller has no cursor to continue with")
	}
}

// TestTheActorOnTheWireIsTheAuthenticatedSubject. The reason stamped on every emitted
// event is the only record of WHO released a source's custody, and it is read off the
// verified claims rather than taken from an argument — so it cannot be forged by the
// caller who is performing the write.
//
// It is asserted on the DECODED EVENT. Asserting it on the resolver's return value would
// pass on an implementation that composed the string and never put it on the wire, which
// is exactly the half that matters: the counts are ephemeral, the event is the record.
func TestTheActorOnTheWireIsTheAuthenticatedSubject(t *testing.T) {
	ctx, w := demoteTestCtx(t, 1)
	ctx = auth.WithClaims(ctx, &auth.Claims{
		Username:    "ops@acme.test",
		Authorities: []string{string(auth.StateDemote)},
	})

	if _, err := (&SchemaResolver{}).DemoteAssertedPresence(ctx, demoteArgs{
		Source: "mqtt1", Limit: 10, Reason: "the broker was retired",
	}); err != nil {
		t.Fatalf("demote: %v", err)
	}
	if len(w.msgs) != 1 {
		t.Fatalf("published %d demotions, want 1", len(w.msgs))
	}
	ev, err := esproto.UnmarshalUnresolvedEvent(w.msgs[0].Value)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	payload, ok := ev.Payload.(*esmodel.UnresolvedStateChangePayload)
	if !ok {
		t.Fatalf("payload is %T", ev.Payload)
	}
	const want = "operator-demotion(actor=ops@acme.test): the broker was retired"
	if payload.Reason != want {
		t.Fatalf("Reason on the wire = %q, want %q", payload.Reason, want)
	}
}

// TestOmittedAndEmptyDeviceTokensAreDifferentRequests carries the three-state argument
// through the resolver, which is where a []string would have collapsed it.
//
// 🔴 THE EMPTY CASE MUST DEMOTE NOTHING. That is the opposite of what
// CommandSearchCriteria.statuses does with an empty list, and the divergence is
// deliberate: that one is a read filter where an empty list almost always means "no
// preference", while this is a write over a whole fleet — so the same slip has to fail in
// the direction that does nothing rather than the direction that does everything.
func TestOmittedAndEmptyDeviceTokensAreDifferentRequests(t *testing.T) {
	r := &SchemaResolver{}

	t.Run("an empty list demotes nothing", func(t *testing.T) {
		ctx, w := demoteTestCtx(t, 4)
		ctx = withAuthorities(ctx, auth.StateDemote)
		empty := []string{}
		got, err := r.DemoteAssertedPresence(ctx, demoteArgs{
			Source: "mqtt1", DeviceTokens: &empty, Limit: 10, Reason: "typo guard",
		})
		if err != nil {
			t.Fatalf("an empty list is a legitimate request, not an error: %v", err)
		}
		if got.Scanned() != 0 || got.Demoted() != 0 {
			t.Fatalf("deviceTokens: [] scanned %d and demoted %d, want 0/0 — it collapsed onto "+
				"\"no narrowing\" and took the whole source", got.Scanned(), got.Demoted())
		}
		if got.LastId() != nil {
			t.Fatalf("lastId = %v on an empty page, want null", *got.LastId())
		}
		if len(w.msgs) != 0 {
			t.Fatalf("%d demotion(s) published for an empty device list", len(w.msgs))
		}
	})

	t.Run("an omitted list takes the whole source", func(t *testing.T) {
		ctx, w := demoteTestCtx(t, 4)
		ctx = withAuthorities(ctx, auth.StateDemote)
		got, err := r.DemoteAssertedPresence(ctx, demoteArgs{
			Source: "mqtt1", DeviceTokens: nil, Limit: 10, Reason: "whole source",
		})
		if err != nil {
			t.Fatalf("omitted deviceTokens: %v", err)
		}
		if got.Demoted() != 4 {
			t.Fatalf("an omitted list demoted %d of 4 — omitted and empty are not the same request",
				got.Demoted())
		}
		if len(w.msgs) != 4 {
			t.Fatalf("published %d demotions, want 4", len(w.msgs))
		}
	})

	t.Run("a named list narrows within the source", func(t *testing.T) {
		ctx, w := demoteTestCtx(t, 4)
		ctx = withAuthorities(ctx, auth.StateDemote)
		named := []string{"mqtt1-dev-b"}
		got, err := r.DemoteAssertedPresence(ctx, demoteArgs{
			Source: "mqtt1", DeviceTokens: &named, Limit: 10, Reason: "one machine",
		})
		if err != nil {
			t.Fatalf("named deviceTokens: %v", err)
		}
		if got.Demoted() != 1 {
			t.Fatalf("a one-device list demoted %d, want 1", got.Demoted())
		}
		if len(w.msgs) != 1 || string(w.msgs[0].Key) != "mqtt1-dev-b" {
			t.Fatalf("published %d demotions, first for %q, want 1 for mqtt1-dev-b",
				len(w.msgs), string(w.msgs[0].Key))
		}
	})
}

// TestTheDemotionRefusesAMalformedCursor. The cursor is shared with the asserted walk and
// parsed by the same strconv path, so "12abc" is refused whole rather than read as 12 —
// which here would silently skip every device before the id the caller thought it named.
func TestTheDemotionRefusesAMalformedCursor(t *testing.T) {
	ctx, w := demoteTestCtx(t, 3)
	ctx = withAuthorities(ctx, auth.StateDemote)
	bad := gql.ID("12abc")
	if _, err := (&SchemaResolver{}).DemoteAssertedPresence(ctx, demoteArgs{
		Source: "mqtt1", Limit: 10, AfterId: &bad, Reason: "walk",
	}); err == nil {
		t.Fatal("a cursor that is not a row id must be refused, not parsed as its numeric prefix")
	}
	if len(w.msgs) != 0 {
		t.Fatalf("%d demotion(s) published on a refused cursor", len(w.msgs))
	}
}

// demoteDocument is the request a client actually sends — dcctl's walk, with the cursor
// and the optional device list both bound as variables.
const demoteDocument = `
mutation DemoteAssertedPresence($source: String!, $deviceTokens: [String!], $limit: Int!, $afterId: ID, $reason: String!) {
  demoteAssertedPresence(source: $source, deviceTokens: $deviceTokens, limit: $limit, afterId: $afterId, reason: $reason) {
    scanned
    demoted
    skipped
    lastId
  }
}`

// TestTheDemotionDocumentValidatesAgainstTheServedSchema runs the real request through
// the SERVER'S OWN validator against the SDL this service actually serves.
//
// 🔴 A MUTATION HARNESS MEASURES THE LOGIC AROUND A DOCUMENT AND NEVER THE DOCUMENT. Every
// other test in this package calls the resolver method directly, so a schema whose
// argument names, nullability or field names do not match would pass all of them and fail
// on the first real request. This is the only thing here that reads the SDL.
//
// The three variable shapes are the three requests a caller makes: the first page (no
// cursor), a continuation (cursor bound), and the narrowed form. A document validated only
// with every variable present would never have checked the call that starts the walk.
func TestTheDemotionDocumentValidatesAgainstTheServedSchema(t *testing.T) {
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})

	for _, c := range []struct {
		name string
		vars map[string]any
	}{
		{"the first page — no cursor, no device list", map[string]any{
			"source": "mqtt1", "limit": 200, "reason": "the broker was retired",
		}},
		{"a continuation — the previous page's lastId", map[string]any{
			"source": "mqtt1", "limit": 200, "afterId": "4096", "reason": "the broker was retired",
		}},
		{"narrowed to named devices", map[string]any{
			"source": "mqtt1", "limit": 200, "reason": "one machine",
			"deviceTokens": []any{"dozer-01", "dozer-02"},
		}},
		{"narrowed to NO devices — the empty list is a legal request", map[string]any{
			"source": "mqtt1", "limit": 200, "reason": "typo guard",
			"deviceTokens": []any{},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if errs := schema.ValidateWithVariables(demoteDocument, c.vars); len(errs) > 0 {
				t.Fatalf("the demotion document does not validate against the served schema: %v", errs)
			}
		})
	}
}

// TestTheServedSchemaRejectsTheDemotionDefectsItExistsFor is the negative control. Every
// case above is a check that passes, and a check that has never been shown to FAIL is
// indistinguishable from one that cannot.
func TestTheServedSchemaRejectsTheDemotionDefectsItExistsFor(t *testing.T) {
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})

	for _, c := range []struct {
		name string
		doc  string
		vars map[string]any
		want string
	}{
		{
			// limit is Int! and required. Omitting it is how a fleet-wide write silently
			// takes the server's idea of a page instead of the bounded one.
			name: "a required argument the document omits",
			doc:  `mutation($source:String!,$reason:String!){demoteAssertedPresence(source:$source,reason:$reason){demoted}}`,
			vars: map[string]any{"source": "mqtt1", "reason": "r"},
			want: "limit",
		},
		{
			// reason is required, and a demotion with no recorded reason is the one thing
			// the audit trail cannot reconstruct afterwards.
			name: "the reason omitted",
			doc:  `mutation($source:String!,$limit:Int!){demoteAssertedPresence(source:$source,limit:$limit){demoted}}`,
			vars: map[string]any{"source": "mqtt1", "limit": 10},
			want: "reason",
		},
		{
			// deviceTokens is [String!], not String. A caller passing one bare token is
			// the natural mistake, and it must be a request error rather than a demotion
			// of something else.
			name: "deviceTokens typed as a bare String",
			doc:  `mutation($source:String!,$limit:Int!,$reason:String!,$t:String){demoteAssertedPresence(source:$source,deviceTokens:$t,limit:$limit,reason:$reason){demoted}}`,
			vars: map[string]any{"source": "mqtt1", "limit": 10, "reason": "r", "t": "dozer-01"},
			want: "[String!]",
		},
		{
			name: "a selected field the result type does not declare",
			doc:  `mutation($source:String!,$limit:Int!,$reason:String!){demoteAssertedPresence(source:$source,limit:$limit,reason:$reason){released}}`,
			vars: map[string]any{"source": "mqtt1", "limit": 10, "reason": "r"},
			want: "released",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			errs := schema.ValidateWithVariables(c.doc, c.vars)
			if len(errs) == 0 {
				t.Fatalf("the validator accepted %q — it cannot be what catches this class", c.name)
			}
			var joined strings.Builder
			for _, e := range errs {
				joined.WriteString(e.Error())
				joined.WriteString("; ")
			}
			if !strings.Contains(joined.String(), c.want) {
				t.Errorf("the rejection does not mention %q, so it may be failing for another reason: %s",
					c.want, joined.String())
			}
		})
	}
}

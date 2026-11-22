// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-notification-management/model"
	gql "github.com/graph-gophers/graphql-go"
)

// The four channel signing-secret mutations and the notificationChannelTypes query, driven
// through the served schema as production parses it, with the variables a client sends. The
// model half (keyring behaviour) lives in model/channel_signing_test.go; what is only
// observable here is the authority each operation demands, that the secret is revealed once
// and never again, and that a tenant cannot reach another tenant's channel.

const (
	generateSecretDoc = `mutation($t: String!) { generateNotificationChannelSigningSecret(token: $t) { secret rotating } }`
	rotateSecretDoc   = `mutation($t: String!) { rotateNotificationChannelSigningSecret(token: $t) { secret rotating } }`
	endRotationDoc    = `mutation($t: String!) { endNotificationChannelSigningRotation(token: $t) { token hasSigningSecret signingRotationOpen } }`
	deleteSecretDoc   = `mutation($t: String!) { deleteNotificationChannelSigningSecret(token: $t) { token hasSigningSecret signingRotationOpen } }`
	channelReadDoc    = `query($ts: [String!]!) { notificationChannelsByToken(tokens: $ts) { id token channelType config hasSecret hasSigningSecret signingRotationOpen } }`
)

var signingDocs = map[string]string{
	"generate": generateSecretDoc, "rotate": rotateSecretDoc, "endRotation": endRotationDoc, "delete": deleteSecretDoc,
}

func execSigning(ctx context.Context, doc, token string) *gql.Response {
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	return schema.Exec(ctx, doc, "", map[string]any{"t": token})
}

func seedWebhookChannel(t *testing.T, ctx context.Context, token string) {
	t.Helper()
	if _, err := apiOf(t, ctx).CreateNotificationChannel(ctx, &model.NotificationChannelCreateRequest{
		Token: token, ChannelType: model.ChannelTypeWebhook, Enabled: true,
		Config: strptr(`{"url":"https://hook.example.invalid/a","auth":"none"}`),
	}); err != nil {
		t.Fatalf("seed webhook channel: %v", err)
	}
}

func issued(t *testing.T, res *gql.Response, field string) (secret string, rotating bool) {
	t.Helper()
	requireNoErrors(t, res)
	var data map[string]struct {
		Secret   string `json:"secret"`
		Rotating bool   `json:"rotating"`
	}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return data[field].Secret, data[field].Rotating
}

type channelFlags struct {
	Token               string `json:"token"`
	HasSigningSecret    bool   `json:"hasSigningSecret"`
	SigningRotationOpen bool   `json:"signingRotationOpen"`
}

func flagsOf(t *testing.T, res *gql.Response, field string) channelFlags {
	t.Helper()
	requireNoErrors(t, res)
	var data map[string]channelFlags
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return data[field]
}

func readChannelFlags(t *testing.T, ctx context.Context, token string) (channelFlags, string) {
	t.Helper()
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, channelReadDoc, "", map[string]any{"ts": []any{token}})
	requireNoErrors(t, res)
	var data struct {
		Rows []channelFlags `json:"notificationChannelsByToken"`
	}
	if err := json.Unmarshal(res.Data, &data); err != nil || len(data.Rows) != 1 {
		t.Fatalf("decode channel read: %v rows=%d", err, len(data.Rows))
	}
	return data.Rows[0], string(res.Data)
}

// The whole lifecycle: generate reveals the key once and flips hasSigningSecret, a second
// generate is refused, rotate opens the overlap, end-rotation closes it, delete clears.
func TestChannelSigningSecretLifecycleOnTheWire(t *testing.T) {
	ctx := newWireCtx(t)
	seedWebhookChannel(t, ctx, "hook")

	if st, _ := readChannelFlags(t, ctx, "hook"); st.HasSigningSecret || st.SigningRotationOpen {
		t.Fatalf("a fresh channel reports a signing secret: %+v", st)
	}

	first, rotating := issued(t, execSigning(ctx, generateSecretDoc, "hook"), "generateNotificationChannelSigningSecret")
	if first == "" || rotating {
		t.Fatalf("generate: secret %q rotating %v, want a secret and no rotation", first, rotating)
	}
	st, body := readChannelFlags(t, ctx, "hook")
	if !st.HasSigningSecret || st.SigningRotationOpen {
		t.Fatalf("after generate: %+v, want hasSigningSecret and no open rotation", st)
	}
	if strings.Contains(body, first) {
		t.Fatalf("a channel read carries the signing secret: %s", body)
	}

	// Generating again never replaces the key.
	res := execSigning(ctx, generateSecretDoc, "hook")
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "already has a signing secret") {
		t.Fatalf("second generate: want the already-has refusal, got %v", res.Errors)
	}

	// Rotation installs a different key and opens the overlap.
	second, rotating := issued(t, execSigning(ctx, rotateSecretDoc, "hook"), "rotateNotificationChannelSigningSecret")
	if second == "" || second == first || !rotating {
		t.Fatalf("rotate: secret %q (first %q) rotating %v, want a new key and rotating", second, first, rotating)
	}
	st, body = readChannelFlags(t, ctx, "hook")
	if !st.HasSigningSecret || !st.SigningRotationOpen {
		t.Fatalf("after rotate: %+v, want the secret kept and the rotation open", st)
	}
	if strings.Contains(body, first) || strings.Contains(body, second) {
		t.Fatalf("a channel read carries a signing secret: %s", body)
	}

	end := flagsOf(t, execSigning(ctx, endRotationDoc, "hook"), "endNotificationChannelSigningRotation")
	if end.Token != "hook" || !end.HasSigningSecret || end.SigningRotationOpen {
		t.Fatalf("end rotation: %+v, want the secret kept and the rotation closed", end)
	}

	del := flagsOf(t, execSigning(ctx, deleteSecretDoc, "hook"), "deleteNotificationChannelSigningSecret")
	if del.Token != "hook" || del.HasSigningSecret || del.SigningRotationOpen {
		t.Fatalf("delete: %+v, want no secret", del)
	}
	if st, _ := readChannelFlags(t, ctx, "hook"); st.HasSigningSecret {
		t.Fatalf("hasSigningSecret still true after delete")
	}
}

// Rotate and end-rotation need a key to act on; an unknown token and a non-webhook channel
// are refused.
func TestChannelSigningSecretRefusalsOnTheWire(t *testing.T) {
	ctx := newWireCtx(t)
	seedWebhookChannel(t, ctx, "hook")
	if _, err := apiOf(t, ctx).CreateNotificationChannel(ctx, &model.NotificationChannelCreateRequest{
		Token: "mail", ChannelType: model.ChannelTypeSMTP, Enabled: true,
	}); err != nil {
		t.Fatalf("seed smtp channel: %v", err)
	}

	for name, doc := range map[string]string{"rotate": rotateSecretDoc, "endRotation": endRotationDoc} {
		res := execSigning(ctx, doc, "hook")
		if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "has no signing secret") {
			t.Errorf("%s with no key: want the no-secret refusal, got %v", name, res.Errors)
		}
	}
	for name, doc := range signingDocs {
		if res := execSigning(ctx, doc, "no-such-channel"); len(res.Errors) != 1 {
			t.Errorf("%s on an unknown channel: want one error, got %v", name, res.Errors)
		}
		res := execSigning(ctx, doc, "mail")
		if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "only a webhook channel can sign") {
			t.Errorf("%s on an smtp channel: want the webhook-only refusal, got %v", name, res.Errors)
		}
	}
}

// Every one of the four needs notification:write; a read-only caller and a caller with no
// claims are refused and nothing changes.
func TestChannelSigningSecretNeedsWriteAuthority(t *testing.T) {
	ctx := newWireCtx(t)
	seedWebhookChannel(t, ctx, "hook")
	issued(t, execSigning(ctx, generateSecretDoc, "hook"), "generateNotificationChannelSigningSecret")

	readOnly := auth.WithClaims(ctx, &auth.Claims{Authorities: []string{string(auth.NotificationRead)}})
	anonymous := auth.WithClaims(ctx, nil)

	for name, doc := range signingDocs {
		for who, c := range map[string]context.Context{"read-only": readOnly, "anonymous": anonymous} {
			res := execSigning(c, doc, "hook")
			if len(res.Errors) != 1 {
				t.Errorf("%s as %s: want one refusal, got %v (data %s)", name, who, res.Errors, res.Data)
			}
		}
	}
	// Nothing moved: still one key, no overlap.
	if st, _ := readChannelFlags(t, ctx, "hook"); !st.HasSigningSecret || st.SigningRotationOpen {
		t.Fatalf("a refused call changed the channel: %+v", st)
	}
}

// A channel belongs to its tenant: another tenant cannot mint, rotate or delete its key, and
// its own channel with the same token is a different channel.
func TestChannelSigningSecretIsTenantScoped(t *testing.T) {
	acme := newWireCtx(t)
	seedWebhookChannel(t, acme, "hook")
	issued(t, execSigning(acme, generateSecretDoc, "hook"), "generateNotificationChannelSigningSecret")

	other := core.WithTenant(acme, "globex")
	for name, doc := range signingDocs {
		if res := execSigning(other, doc, "hook"); len(res.Errors) != 1 {
			t.Errorf("%s across tenants: want not-found, got %v", name, res.Errors)
		}
	}
	if st, _ := readChannelFlags(t, acme, "hook"); !st.HasSigningSecret {
		t.Fatal("another tenant's call removed the key")
	}

	// The same token in the other tenant is its own channel with its own, empty, signing state.
	seedWebhookChannel(t, other, "hook")
	if st, _ := readChannelFlags(t, other, "hook"); st.HasSigningSecret {
		t.Fatal("a same-token channel in another tenant inherits the key")
	}
	issued(t, execSigning(other, generateSecretDoc, "hook"), "generateNotificationChannelSigningSecret")
}

// notificationChannelTypes serves the catalog: every registered type, in display order, each
// with a label and description, and every one available (each has a delivery adapter).
func TestNotificationChannelTypesOnTheWire(t *testing.T) {
	ctx := newWireCtx(t)
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, `{ notificationChannelTypes { id label description available } }`, "", nil)
	requireNoErrors(t, res)
	var data struct {
		Types []struct {
			ID          string `json:"id"`
			Label       string `json:"label"`
			Description string `json:"description"`
			Available   bool   `json:"available"`
		} `json:"notificationChannelTypes"`
	}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []string{"smtp", "webhook", "slack", "teams", "pagerduty"}
	if len(data.Types) != len(want) || len(data.Types) != len(model.SupportedChannelTypes) {
		t.Fatalf("got %d types, want %v", len(data.Types), want)
	}
	for i, ty := range data.Types {
		if ty.ID != want[i] || ty.ID != model.SupportedChannelTypes[i].Id {
			t.Errorf("type %d is %q, want %q", i, ty.ID, want[i])
		}
		if ty.Label == "" || ty.Description == "" || !ty.Available {
			t.Errorf("type %q: label %q description %q available %v", ty.ID, ty.Label, ty.Description, ty.Available)
		}
		if !model.IsKnownChannelType(ty.ID) {
			t.Errorf("type %q is listed but not accepted by channel creation", ty.ID)
		}
	}
}

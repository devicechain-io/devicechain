// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	gql "github.com/graph-gophers/graphql-go"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"
	"github.com/stretchr/testify/require"
)

func TestResolversWithoutNatsManager(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{name: "absent"},
		{name: "typed nil", value: (*messaging.NatsManager)(nil)},
		{name: "wrong type", value: "not a manager"},
	} {
		withManager := func(ctx context.Context) context.Context {
			if tc.value != nil {
				return context.WithValue(ctx, gqlcore.ContextNatsKey, tc.value)
			}
			return ctx
		}
		t.Run(tc.name+"/GetNats", func(t *testing.T) {
			manager, err := (&SchemaResolver{}).GetNats(withManager(context.Background()))
			require.ErrorIs(t, err, ErrNoNatsManager)
			require.Nil(t, manager)
		})
		t.Run(tc.name+"/subscription", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(withManager(previewCtx(auth.DeviceRead)), time.Second)
			defer cancel()
			schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
			ch, err := schema.Subscribe(ctx, `subscription { detectionStream(profileToken:"p") { ruleToken } }`, "", nil)
			if err == nil {
				select {
				case value, ok := <-ch:
					require.True(t, ok, "missing subscribe-time refusal")
					response, ok := value.(*gql.Response)
					require.True(t, ok)
					require.Len(t, response.Errors, 1)
					err = response.Errors[0]
				case <-ctx.Done():
					t.Fatal("subscription did not refuse the missing manager")
				}
			}
			assertNatsQueryError(t, err)
		})
		t.Run(tc.name+"/preview", func(t *testing.T) {
			r := &SchemaResolver{Profiles: publishedProfiles(t, true)}
			schema := gqlcore.MustParseSchema(SchemaContent, r)
			response := schema.Exec(withManager(previewCtx(auth.DeviceRead)),
				`query($input: PreviewRuleInput!) { previewRule(input:$input) { ok } }`, "", map[string]any{
					"input": map[string]any{"profileToken": "p", "ruleDefinition": plainDraft,
						"start": "2026-08-01T00:00:00Z", "end": "2026-08-02T00:00:00Z"},
				})
			require.Len(t, response.Errors, 1)
			assertNatsQueryError(t, response.Errors[0])
		})
		t.Run(tc.name+"/compare", func(t *testing.T) {
			r, input, _ := compareFixture(t)
			r.Replay = nil
			response := gqlcore.MustParseSchema(SchemaContent, r).Exec(withManager(compareContext(compareServiceClaims())),
				compareQuery, "", map[string]any{"input": input})
			require.Len(t, response.Errors, 1)
			assertNatsQueryError(t, response.Errors[0])
		})
	}
}

func assertNatsQueryError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	require.NotContains(t, err.Error(), "panic", "must refuse without a recovered panic")
	var queryError *gqlerrors.QueryError
	require.ErrorAs(t, err, &queryError)
	require.ErrorIs(t, queryError.ResolverError, ErrNoNatsManager)
}

func TestGetNatsManager(t *testing.T) {
	want := &messaging.NatsManager{}
	ctx := context.WithValue(context.Background(), gqlcore.ContextNatsKey, want)
	got, err := (&SchemaResolver{}).GetNats(ctx)
	require.NoError(t, err)
	require.Same(t, want, got)
}

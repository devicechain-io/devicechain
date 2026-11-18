// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"testing"
	"time"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	graphql "github.com/graph-gophers/graphql-go"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAlarmStreamWithoutNatsManager(t *testing.T) {
	f := newScopeFixture(t)
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	for _, tc := range []struct {
		name  string
		value any
	}{
		{name: "absent"},
		{name: "typed nil", value: (*messaging.NatsManager)(nil)},
		{name: "wrong type", value: "not a manager"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := f.operator()
			if tc.value != nil {
				ctx = context.WithValue(ctx, gqlcore.ContextNatsKey, tc.value)
			}
			ctx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			ch, err := schema.Subscribe(ctx, `subscription { alarmStream { alarmToken } }`, "", nil)
			if err == nil {
				select {
				case value, ok := <-ch:
					require.True(t, ok, "missing subscribe-time refusal")
					response, ok := value.(*graphql.Response)
					require.True(t, ok)
					require.Len(t, response.Errors, 1)
					err = response.Errors[0]
				case <-ctx.Done():
					t.Fatal("subscription did not refuse the missing manager")
				}
			}
			require.Error(t, err)
			assert.NotContains(t, strings.ToLower(err.Error()), "panic", "the resolver must refuse explicitly, without graphql-go recovering a panic")
			var queryError *gqlerrors.QueryError
			require.ErrorAs(t, err, &queryError)
			require.ErrorIs(t, queryError.ResolverError, ErrNoNatsManager)
			manager, err := (&SchemaResolver{}).GetNats(ctx)
			require.ErrorIs(t, err, ErrNoNatsManager)
			require.Nil(t, manager)
		})
	}
}

func TestGetNatsManager(t *testing.T) {
	want := &messaging.NatsManager{}
	ctx := context.WithValue(context.Background(), gqlcore.ContextNatsKey, want)
	got, err := (&SchemaResolver{}).GetNats(ctx)
	require.NoError(t, err)
	require.Same(t, want, got)
}

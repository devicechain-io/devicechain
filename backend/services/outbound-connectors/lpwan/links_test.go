// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/secrets"

	"github.com/devicechain-io/dc-outbound-connectors/model"
)

type connRow struct {
	token     string
	id        uint
	typ       string
	cfg       string
	published bool
	listErr   error
	readErr   error
}

type fakeStore struct {
	rows       []connRow
	listCalls  int
	tenantSeen string
	listErr    error
}

func (s *fakeStore) Connectors(ctx context.Context, c model.ConnectorSearchCriteria) (*model.ConnectorSearchResults, error) {
	s.listCalls++
	s.tenantSeen, _ = core.TenantFromContext(ctx)
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := &model.ConnectorSearchResults{}
	if c.Pagination.PageNumber > 1 {
		return out, nil
	}
	for _, r := range s.rows {
		if c.Type != nil && r.typ != *c.Type {
			continue
		}
		var c model.Connector
		c.Token = r.token
		out.Results = append(out.Results, c)
	}
	return out, nil
}

func (s *fakeStore) LatestPublishedConnector(_ context.Context, token string) (*model.ConnectorVersion, error) {
	for _, r := range s.rows {
		if r.token != token {
			continue
		}
		if r.readErr != nil {
			return nil, r.readErr
		}
		if !r.published {
			return nil, model.ErrNotPublished
		}
		return &model.ConnectorVersion{ConnectorID: r.id, Type: r.typ, Config: datatypes.JSON(r.cfg)}, nil
	}
	return nil, gorm.ErrRecordNotFound
}

type fakeSecrets struct {
	byHandle map[string]string
	err      error
	calls    int
}

func (f *fakeSecrets) Resolve(_ context.Context, _, handle string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	if v, ok := f.byHandle[handle]; ok {
		return v, nil
	}
	return "", secrets.ErrSecretNotFound
}

func cfg(integration string) string {
	return fmt.Sprintf(`{"networkServer":"chirpstack_v4","baseUrl":"https://cs.example.com/","integration":%q}`, integration)
}

const lw = "lorawan_downlink"

func TestStoreLinksResolvesThePublishedLinkForTheIntegration(t *testing.T) {
	st := &fakeStore{rows: []connRow{
		{token: "a", id: 1, typ: lw, cfg: cfg("cs1"), published: true},
		{token: "b", id: 2, typ: lw, cfg: cfg("other"), published: true},
		{token: "mqtt", id: 3, typ: "mqtt", cfg: `{}`, published: true},
	}}
	sec := &fakeSecrets{byHandle: map[string]string{model.ConnectorSecretName(1): "KEY-1", model.ConnectorSecretName(2): "KEY-2"}}
	l := NewStoreLinks(st, sec)

	tg, err := l.Resolve(context.Background(), "acme", "cs1")
	require.NoError(t, err)
	require.Equal(t, "KEY-1", tg.APIKey, "the secret is the matched connector's, keyed by its immutable id")
	require.Equal(t, "https://cs.example.com", tg.BaseURL.String(), "trailing slash trimmed")
	require.Equal(t, "cs1", tg.Integration)
	require.Equal(t, "acme", st.tenantSeen, "the lookup runs in the command's tenant")
}

func TestStoreLinksDefiniteAbsences(t *testing.T) {
	sec := &fakeSecrets{byHandle: map[string]string{model.ConnectorSecretName(1): "K", model.ConnectorSecretName(2): "K"}}
	t.Run("none", func(t *testing.T) {
		_, err := NewStoreLinks(&fakeStore{}, sec).Resolve(context.Background(), "acme", "cs1")
		require.ErrorIs(t, err, ErrNoLink)
	})
	t.Run("a draft is not a link", func(t *testing.T) {
		st := &fakeStore{rows: []connRow{{token: "a", id: 1, typ: lw, cfg: cfg("cs1"), published: false}}}
		_, err := NewStoreLinks(st, sec).Resolve(context.Background(), "acme", "cs1")
		require.ErrorIs(t, err, ErrNoLink)
	})
	t.Run("deleted between list and read", func(t *testing.T) {
		st := &fakeStore{rows: []connRow{{token: "a", id: 1, typ: lw, cfg: cfg("cs1"), published: true, readErr: gorm.ErrRecordNotFound}}}
		_, err := NewStoreLinks(st, sec).Resolve(context.Background(), "acme", "cs1")
		require.ErrorIs(t, err, ErrNoLink)
	})
	t.Run("two links name the integration: fail closed, never pick one", func(t *testing.T) {
		st := &fakeStore{rows: []connRow{
			{token: "a", id: 1, typ: lw, cfg: cfg("cs1"), published: true},
			{token: "b", id: 2, typ: lw, cfg: cfg("cs1"), published: true},
		}}
		_, err := NewStoreLinks(st, sec).Resolve(context.Background(), "acme", "cs1")
		require.ErrorIs(t, err, ErrAmbiguous)
	})
	t.Run("a draft duplicate does not make it ambiguous", func(t *testing.T) {
		st := &fakeStore{rows: []connRow{
			{token: "a", id: 1, typ: lw, cfg: cfg("cs1"), published: true},
			{token: "b", id: 2, typ: lw, cfg: cfg("cs1"), published: false},
		}}
		_, err := NewStoreLinks(st, sec).Resolve(context.Background(), "acme", "cs1")
		require.NoError(t, err)
	})
	t.Run("no credential: unusable", func(t *testing.T) {
		st := &fakeStore{rows: []connRow{{token: "a", id: 9, typ: lw, cfg: cfg("cs1"), published: true}}}
		_, err := NewStoreLinks(st, &fakeSecrets{}).Resolve(context.Background(), "acme", "cs1")
		require.ErrorIs(t, err, ErrBadLink)
	})
	t.Run("a stored config that fails the parser: unusable", func(t *testing.T) {
		st := &fakeStore{rows: []connRow{{token: "a", id: 1, typ: lw, published: true,
			cfg: `{"networkServer":"chirpstack_v4","baseUrl":"ftp://x","integration":"cs1"}`}}}
		_, err := NewStoreLinks(st, sec).Resolve(context.Background(), "acme", "cs1")
		require.ErrorIs(t, err, ErrBadLink)
	})
}

func TestStoreLinksTransientFailuresAreNotCachedAndAreNotAbsences(t *testing.T) {
	st := &fakeStore{listErr: errors.New("db down")}
	l := NewStoreLinks(st, &fakeSecrets{})
	_, err := l.Resolve(context.Background(), "acme", "cs1")
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrNoLink), "an unreadable store is not 'no link'")
	_, _ = l.Resolve(context.Background(), "acme", "cs1")
	require.Equal(t, 2, st.listCalls, "a failure is asked again")

	sec := &fakeSecrets{err: errors.New("secret store down")}
	st2 := &fakeStore{rows: []connRow{{token: "a", id: 1, typ: lw, cfg: cfg("cs1"), published: true}}}
	_, err = NewStoreLinks(st2, sec).Resolve(context.Background(), "acme", "cs1")
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrBadLink), "a secret-store outage is not an unusable link")
}

func TestStoreLinksCachesDefiniteAnswersBrieflyPerTenantAndIntegration(t *testing.T) {
	st := &fakeStore{rows: []connRow{{token: "a", id: 1, typ: lw, cfg: cfg("cs1"), published: true}}}
	sec := &fakeSecrets{byHandle: map[string]string{model.ConnectorSecretName(1): "K"}}
	l := NewStoreLinks(st, sec)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	_, err := l.Resolve(context.Background(), "acme", "cs1")
	require.NoError(t, err)
	_, err = l.Resolve(context.Background(), "acme", "cs1")
	require.NoError(t, err)
	require.Equal(t, 1, st.listCalls, "served from the cache")

	// Another tenant never reads acme's entry.
	_, err = l.Resolve(context.Background(), "globex", "cs1")
	require.NoError(t, err)
	require.Equal(t, 2, st.listCalls)

	now = now.Add(linkCacheTTL + time.Second)
	_, err = l.Resolve(context.Background(), "acme", "cs1")
	require.NoError(t, err)
	require.Equal(t, 3, st.listCalls, "expired")
}

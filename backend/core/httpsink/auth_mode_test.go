// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package httpsink

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every refusal here is asserted on the SERVER's state as well as the error: "was the
// endpoint reached" is the question, and an error alone would also come from a broken URL.

func authCountingServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// The zero Auth used to mean "Bearer if there is a secret, nothing otherwise", which is how
// a missing secret became an unauthenticated request. It now means nothing, and is refused.
func TestSendRefusesAnUnstatedMode(t *testing.T) {
	srv, hits := authCountingServer(t)
	for _, secret := range []string{"", "s"} {
		err := Send(context.Background(), srv.Client(), Request{URL: srv.URL, Secret: secret})
		assert.ErrorIs(t, err, ErrAuthModeUnstated, "secret %q", secret)
	}
	assert.Equal(t, int32(0), hits.Load())
}

func TestSendRefusesADeclaredModeWithNoCredential(t *testing.T) {
	srv, hits := authCountingServer(t)
	for _, auth := range []Auth{{Mode: AuthBearer}, {Mode: AuthHeader, Header: "X-API-Key"}} {
		err := Send(context.Background(), srv.Client(), Request{URL: srv.URL, Auth: auth})
		assert.ErrorIs(t, err, ErrMissingCredential, "auth %+v", auth)
	}
	assert.Equal(t, int32(0), hits.Load())
}

func TestSendRefusesACredentialWithModeNone(t *testing.T) {
	srv, hits := authCountingServer(t)
	err := Send(context.Background(), srv.Client(), Request{URL: srv.URL, Secret: "s", Auth: Auth{Mode: AuthNone}})
	assert.ErrorIs(t, err, ErrUnexpectedCredential)
	assert.Equal(t, int32(0), hits.Load())
}

// Callers classify every one of these as terminal with a single errors.Is; a refusal that
// did not wrap the parent would silently fall into their retry branch.
func TestEveryAuthRefusalIsAnErrAuthRefused(t *testing.T) {
	for name, err := range map[string]error{
		"unstated":            ErrAuthModeUnstated,
		"missing credential":  ErrMissingCredential,
		"unexpected":          ErrUnexpectedCredential,
		"validate: no header": Auth{Mode: AuthHeader}.Validate(),
		"validate: reserved":  Auth{Mode: AuthHeader, Header: "X-DC-Service"}.Validate(),
		"validate: bad name":  Auth{Mode: AuthHeader, Header: "Bad Header"}.Validate(),
		"validate: stray hdr": Auth{Mode: AuthBearer, Header: "X-API-Key"}.Validate(),
	} {
		require.Error(t, err, name)
		assert.True(t, errors.Is(err, ErrAuthRefused), "%s: %v does not wrap ErrAuthRefused", name, err)
	}
}

func TestAuthValidateTable(t *testing.T) {
	for _, bad := range []Auth{
		{},
		{Mode: AuthMode(99)},
		{Mode: AuthNone, Header: "X-API-Key"},
		{Mode: AuthNone, Scheme: "Token"},
		{Mode: AuthBearer, Header: "Authorization"},
		{Mode: AuthBearer, Scheme: "Bearer"},
		{Mode: AuthHeader},
		{Mode: AuthHeader, Scheme: "Token"},
	} {
		assert.Error(t, bad.Validate(), "%+v", bad)
	}
	for _, ok := range []Auth{
		{Mode: AuthNone},
		{Mode: AuthBearer},
		{Mode: AuthHeader, Header: "X-API-Key"},
		{Mode: AuthHeader, Header: "Authorization", Scheme: "Token"},
	} {
		assert.NoError(t, ok.Validate(), "%+v", ok)
	}
}

func TestCheckCredentialTable(t *testing.T) {
	for _, c := range []struct {
		auth    Auth
		present bool
		want    error
	}{
		{Auth{Mode: AuthNone}, false, nil},
		{Auth{Mode: AuthNone}, true, ErrUnexpectedCredential},
		{Auth{Mode: AuthBearer}, true, nil},
		{Auth{Mode: AuthBearer}, false, ErrMissingCredential},
		{Auth{Mode: AuthHeader, Header: "X-API-Key"}, true, nil},
		{Auth{Mode: AuthHeader, Header: "X-API-Key"}, false, ErrMissingCredential},
		{Auth{}, true, ErrAuthModeUnstated},
		{Auth{}, false, ErrAuthModeUnstated},
	} {
		got := c.auth.CheckCredential(c.present)
		if c.want == nil {
			assert.NoError(t, got, "%+v present=%v", c.auth, c.present)
		} else {
			assert.ErrorIs(t, got, c.want, "%+v present=%v", c.auth, c.present)
		}
	}
}

// The counterweight: AuthNone with no secret is delivered, with no Authorization header.
func TestSendWithModeNoneSendsNoAuthorization(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Values("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	require.NoError(t, Send(context.Background(), srv.Client(), Request{URL: srv.URL, Auth: Auth{Mode: AuthNone}}))
	assert.Empty(t, got)
}

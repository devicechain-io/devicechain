// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
)

// logoServer serves /branding/logo exactly as the service registers it — through
// RegisterBrandingLogoHandler onto a mux — over a filesystem object store and a tenant
// "acme", and returns it with a token for that tenant holding branding:write.
func logoServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	db := putest.NewSQLiteDB(t, &iam.Role{}, &iam.TenantTier{}, &iam.Tenant{},
		&iam.OAuthClient{}, &iam.Identity{}, &iam.Membership{})
	rdbm := &rdb.RdbManager{Database: db}
	store := iam.NewStore(rdbm)
	tier := &iam.TenantTier{Token: "silver"}
	require.NoError(t, store.CreateTenantTier(context.Background(), tier))
	require.NoError(t, store.CreateTenant(context.Background(), &iam.Tenant{
		Token: "acme", Enabled: true, TierID: tier.ID, PurgeState: iam.PurgeActive,
	}))
	mgr := identity.NewManager(nil, rdbm, nil, nil, 0, 0, "", identity.BootstrapConfig{}, nil)

	blobs, err := blob.New(context.Background(), blob.Config{Backend: blob.BackendFilesystem, Directory: t.TempDir()}, "inst")
	require.NoError(t, err)

	key, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	tok, err := auth.NewIssuer(key, "https://as.example.com", 0, 0).
		IssueTenantAccess("acme", "a@b.c", nil, []string{string(auth.BrandingWrite)}, false, "j-logo")
	require.NoError(t, err)

	mux := http.NewServeMux()
	RegisterBrandingLogoHandler(mux, blobs, mgr, auth.NewValidator(&key.PublicKey))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, tok.Token
}

// A legitimate upload of a few hundred KiB — a real logo, well under the ceiling but far
// over any small-form body limit — is stored, through the handler as registered.
func TestALogoUploadOfSeveralHundredKiBSucceeds(t *testing.T) {
	ts, token := logoServer(t)

	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 600<<10)...)
	req, err := http.NewRequest(http.MethodPost, ts.URL+brandingLogoPath, bytes.NewReader(png))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "a %d KiB logo upload was refused", len(png)>>10)
}

// A caller without a token gets nothing buffered: its stalled upload is refused 401 at
// once, with the connection closed, rather than held until the read deadline (which is
// what reading the body before authenticating would do).
func TestAnUnauthenticatedLogoUploadIsNotBuffered(t *testing.T) {
	ts, _ := logoServer(t)

	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.Write([]byte("POST " + brandingLogoPath + " HTTP/1.1\r\nHost: t\r\nContent-Length: 500000\r\n\r\n\x89PNG"))
	require.NoError(t, err)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	started := time.Now()
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	elapsed := time.Since(started)
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("no response after %v: the server is waiting on the body of an unauthenticated upload", elapsed)
	}
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.True(t, resp.Close, "the connection was left open with the rest of the body unread")
	require.Less(t, elapsed, 2*time.Second, "answered only after waiting on the body")
}

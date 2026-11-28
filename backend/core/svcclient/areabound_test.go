// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package svcclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
)

const testAreaCredential = "automation-credential-0123456789abcdefghijklmnopq"

// mintRecord is one request a recording user-management received.
type mintRecord struct {
	Path, Credential, Secret string
	Body                     map[string]any
}

// recordingUM stands in for user-management. status decides each answer: a current release
// (200 at the area path), a previous release (404 there), or anything else. It records every request,
// so a test can assert what was NOT sent as well as what was.
type recordingUM struct {
	srv    *httptest.Server
	mu     sync.Mutex
	got    []mintRecord
	status func(path string) int
}

func newRecordingUM(t *testing.T, status func(path string) int) *recordingUM {
	t.Helper()
	u := &recordingUM{status: status}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		u.mu.Lock()
		u.got = append(u.got, mintRecord{r.URL.Path, r.Header.Get(auth.AreaCredentialHeader), r.Header.Get(auth.ServiceSecretHeader), body})
		u.mu.Unlock()
		code := u.status(r.URL.Path)
		if code != http.StatusOK {
			http.Error(w, http.StatusText(code), code)
			return
		}
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "area-token", ExpiresAt: 1 << 40})
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *recordingUM) cfg() config.UserManagementConfiguration {
	host, portStr, _ := net.SplitHostPort(u.srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return config.UserManagementConfiguration{Hostname: host, Port: uint32(port), InternalPort: uint32(port)}
}

func (u *recordingUM) requests() []mintRecord {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]mintRecord(nil), u.got...)
}

// withAreaCredential points the credential directory at a temporary one holding automation's file
// (content "" writes no file at all).
func withAreaCredential(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	prev := areaCredentialDir
	t.Cleanup(func() { areaCredentialDir = prev })
	areaCredentialDir = dir
	if content == "" {
		return
	}
	if err := os.MkdirAll(filepath.Join(dir, "automation"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "automation", auth.AreaMintCredentialKey), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func okTarget(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// An area-bound client mints at the area-bound path with its own credential, sends no subject, and
// never sends the shared secret — even when the caller handed it one.
func TestAreaBoundClientMintsWithItsOwnCredentialOnly(t *testing.T) {
	withAreaCredential(t, testAreaCredential+"\n")
	um := newRecordingUM(t, func(string) int { return http.StatusOK })
	c := New(um.cfg(), "the-shared-secret", "automation", []string{string(auth.DeviceRead)})
	if c.secret != "" {
		t.Fatal("an area-bound client holds the shared secret it was handed")
	}
	if err := c.Query(context.Background(), okTarget(t).URL, "tenant-a", "query{x}", nil, nil); err != nil {
		t.Fatalf("Query: %v", err)
	}
	got := um.requests()
	if len(got) != 1 {
		t.Fatalf("mint requests = %+v, want exactly one", got)
	}
	r := got[0]
	if r.Path != auth.AreaServiceTokenPath {
		t.Fatalf("minted at %s, want %s", r.Path, auth.AreaServiceTokenPath)
	}
	if r.Credential != testAreaCredential {
		t.Fatalf("area credential header = %q", r.Credential)
	}
	if r.Secret != "" {
		t.Fatal("the shared secret was sent by an area-bound client")
	}
	if _, named := r.Body["subject"]; named {
		t.Fatalf("the body names a subject: %v", r.Body)
	}
	if auths, _ := r.Body["authorities"].([]any); len(auths) != 1 || auths[0] != "device:read" {
		t.Fatalf("authorities = %v", r.Body["authorities"])
	}
}

// §6.7 / §8.2: against a previous-release user-management, which has no area-bound endpoint and
// answers 404, there is NO token and NO second attempt at the shared-secret endpoint.
func TestAreaBoundClientAgainstAnOldIssuerGetsNoTokenAndNoFallback(t *testing.T) {
	withAreaCredential(t, testAreaCredential)
	// The old issuer: only the shared endpoint exists, and it would mint anything for the secret.
	um := newRecordingUM(t, func(path string) int {
		if path == auth.ServiceTokenPath {
			return http.StatusOK
		}
		return http.StatusNotFound
	})
	c := New(um.cfg(), "the-shared-secret", "automation", []string{string(auth.DeviceRead)})
	target := okTarget(t)
	for i := 0; i < 3; i++ {
		err := c.Query(context.Background(), target.URL, "tenant-a", "query{x}", nil, nil)
		if !errors.Is(err, ErrServiceToken) {
			t.Fatalf("Query err = %v, want ErrServiceToken", err)
		}
		var se *StatusError
		if !errors.As(err, &se) || se.Status != http.StatusNotFound {
			t.Fatalf("the 404 is not carried: %v", err)
		}
	}
	for _, r := range um.requests() {
		if r.Path != auth.AreaServiceTokenPath || r.Secret != "" {
			t.Fatalf("an area-bound client fell back: %+v", r)
		}
	}
	if c.token != "" {
		t.Fatal("a token was cached")
	}
}

// No credential mounted: the client fails closed without contacting user-management at all, and
// Peers is not ready — a shared secret in the configuration changes neither.
func TestAreaBoundWithoutCredentialFailsClosed(t *testing.T) {
	for name, content := range map[string]string{"absent": "", "too short": "short"} {
		t.Run(name, func(t *testing.T) {
			withAreaCredential(t, content)
			um := newRecordingUM(t, func(string) int { return http.StatusOK })
			infra := config.InfrastructureConfiguration{UserManagement: um.cfg()}
			infra.ServiceAuth.Secret = "the-shared-secret"
			p := NewPeers(infra, "automation")
			if err := p.Ready(); !errors.Is(err, ErrPeerNotConfigured) {
				t.Fatalf("Ready() = %v, want not configured", err)
			}
			if _, err := p.UserManagement(auth.TenantRead); !errors.Is(err, ErrPeerNotConfigured) {
				t.Fatalf("UserManagement() err = %v", err)
			}
			if _, err := p.URL(infra.UserManagement.Endpoint()); !errors.Is(err, ErrPeerNotConfigured) {
				t.Fatalf("URL() err = %v", err)
			}
			err := p.Client(auth.DeviceRead).Query(context.Background(), okTarget(t).URL, "t", "query{x}", nil, nil)
			if !errors.Is(err, ErrServiceToken) {
				t.Fatalf("Query err = %v", err)
			}
			if got := um.requests(); len(got) != 0 {
				t.Fatalf("user-management was contacted: %+v", got)
			}
		})
	}
}

// Peers for an area-bound area is ready on its credential ALONE, and its clients are area-bound.
func TestAreaBoundPeersAreReadyOnTheCredentialAlone(t *testing.T) {
	withAreaCredential(t, testAreaCredential)
	infra := config.InfrastructureConfiguration{UserManagement: config.UserManagementConfiguration{Hostname: "um", Port: 8080, InternalPort: 8082}}
	p := NewPeers(infra, "automation")
	if err := p.Ready(); err != nil {
		t.Fatalf("Ready() = %v", err)
	}
	s, err := p.UserManagement(auth.TenantRead)
	if err != nil {
		t.Fatal(err)
	}
	if s.Client.mintURL != "http://um:8082"+auth.AreaServiceTokenPath || s.Client.secret != "" {
		t.Fatalf("not an area-bound client: %#v", s.Client)
	}

	// A non-area-bound area is untouched: still the shared endpoint, still needs the secret.
	q := NewPeers(infra, "dashboard-management")
	if err := q.Ready(); !errors.Is(err, ErrPeerNotConfigured) {
		t.Fatalf("dashboard-management Ready() = %v", err)
	}
	infra.ServiceAuth.Secret = "s"
	if c := NewPeers(infra, "dashboard-management").Client(auth.TenantRead); c.mintURL != "http://um:8082"+auth.ServiceTokenPath || c.secret != "s" || c.credentialFile != "" {
		t.Fatalf("a shared-secret client changed: %#v", c)
	}
}

// The credential reaches only the mint request's header: not an error, not the target.
func TestAreaBoundCredentialIsNeverEchoed(t *testing.T) {
	withAreaCredential(t, testAreaCredential)
	um := newRecordingUM(t, func(string) int { return http.StatusForbidden })
	var targetSaw bytes.Buffer
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.Header.Write(&targetSaw)
	}))
	defer target.Close()
	err := New(um.cfg(), "", "automation", []string{"device:write"}).Query(context.Background(), target.URL, "t", "q", nil, nil)
	if err == nil {
		t.Fatal("a refused mint produced no error")
	}
	if strings.Contains(err.Error(), testAreaCredential) {
		t.Fatalf("the credential is in the error: %v", err)
	}
	if targetSaw.Len() != 0 {
		t.Fatal("the target was called without a token")
	}

	// An unreadable file's error names the path, not anything inside it.
	withAreaCredential(t, "short-secret-value")
	err = New(um.cfg(), "", "automation", nil).Query(context.Background(), target.URL, "t", "q", nil, nil)
	if err == nil || strings.Contains(err.Error(), "short-secret-value") {
		t.Fatalf("err = %v", err)
	}
}

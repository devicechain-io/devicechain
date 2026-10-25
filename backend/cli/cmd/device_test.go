// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// registerStub answers the endpoint the way the platform does, under the /api/device-management
// prefix the dcctl --server convention puts in front of it.
type registerStub struct {
	srv      *httptest.Server
	requests atomic.Int64
	last     map[string]any
	answer   func(n int64, w http.ResponseWriter)
}

func newRegisterStub(t *testing.T, answer func(n int64, w http.ResponseWriter)) *registerStub {
	s := &registerStub{answer: answer}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/device-management/provisioning/acme/register" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &s.last)
		s.answer(s.requests.Add(1), w)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func created(w http.ResponseWriter, outcome string) {
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, `{"outcome":%q,"tenant":"acme","deviceToken":"pv-sn-1-aa","externalId":"SN-1","credentialType":"ACCESS_TOKEN","mqtt":{"clientId":"i:acme:pv-sn-1-aa"}}`, outcome)
}

func runRegister(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	c := newDeviceRegisterCmd()
	var out, errOut bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errOut)
	c.SetArgs(args)
	err = c.Execute()
	return out.String(), errOut.String(), err
}

func baseArgs(s *registerStub, dir string) []string {
	return []string{"--server", strings.TrimPrefix(s.srv.URL, "http://"), "--insecure-plaintext", "--tenant", "acme", "--key", "fleet",
		"--secret", "s3cret", "--external-id", "SN-1", "--material-file", filepath.Join(dir, "sn-1.credential")}
}

// The command registers a device, prints its details, and prints the credential it saved
// exactly once: the second run reports a replay and does not print it again.
func TestDeviceRegisterPrintsTheCredentialOnce(t *testing.T) {
	s := newRegisterStub(t, func(n int64, w http.ResponseWriter) {
		if n == 1 {
			created(w, "CREATED")
			return
		}
		created(w, "REPLAYED")
	})
	args := baseArgs(s, t.TempDir())

	out, _, err := runRegister(t, args...)
	require.NoError(t, err)
	saved, rerr := os.ReadFile(args[len(args)-1])
	require.NoError(t, rerr)
	material := strings.TrimSpace(string(saved))
	assert.Equal(t, material, s.last["credentialSecret"])
	assert.Contains(t, out, "Registered: CREATED")
	assert.Contains(t, out, "pv-sn-1-aa")
	assert.Contains(t, out, "i:acme:pv-sn-1-aa")
	assert.Equal(t, 1, strings.Count(out, material), "the credential is printed once")

	out, _, err = runRegister(t, args...)
	require.NoError(t, err)
	assert.Contains(t, out, "Registered: REPLAYED")
	assert.NotContains(t, out, material, "a replay printed the credential again")
	assert.Equal(t, material, s.last["credentialSecret"], "a re-run presented different material")
}

// The provision secret comes from a file or the environment, never both flags, and a missing
// one is an error before anything is sent.
func TestDeviceRegisterProvisionSecretSources(t *testing.T) {
	s := newRegisterStub(t, func(n int64, w http.ResponseWriter) { created(w, "CREATED") })
	dir := t.TempDir()
	withoutSecret := func(extra ...string) []string {
		a := baseArgs(s, dir)
		for i := range a {
			if a[i] == "--secret" {
				a = append(a[:i], a[i+2:]...)
				break
			}
		}
		return append(a, extra...)
	}
	secretFile := filepath.Join(dir, "secret")
	require.NoError(t, os.WriteFile(secretFile, []byte("from-file\n"), 0o600))

	_, _, err := runRegister(t, withoutSecret()...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provision secret is required")
	assert.Zero(t, s.requests.Load())

	_, _, err = runRegister(t, withoutSecret("--secret", "a", "--secret-file", secretFile)...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both set")
	assert.Zero(t, s.requests.Load())

	_, _, err = runRegister(t, withoutSecret("--secret-file", secretFile)...)
	require.NoError(t, err)
	assert.Equal(t, "from-file", s.last["provisionSecret"])

	t.Setenv(provisionSecretEnv, "from-env")
	_ = os.Remove(filepath.Join(dir, "sn-1.credential"))
	_, _, err = runRegister(t, withoutSecret()...)
	require.NoError(t, err)
	assert.Equal(t, "from-env", s.last["provisionSecret"])
}

// Required flags are checked before anything is generated or sent.
func TestDeviceRegisterRequiredFlags(t *testing.T) {
	s := newRegisterStub(t, func(n int64, w http.ResponseWriter) { created(w, "CREATED") })
	for _, missing := range []string{"--tenant", "--key", "--external-id", "--material-file"} {
		a := baseArgs(s, t.TempDir())
		for i := range a {
			if a[i] == missing {
				a = append(a[:i], a[i+2:]...)
				break
			}
		}
		_, _, err := runRegister(t, a...)
		require.Error(t, err, missing)
		assert.Contains(t, err.Error(), missing)
	}
	assert.Zero(t, s.requests.Load())
}

// A refusal is surfaced, says the saved credential is untouched, and is not retried.
func TestDeviceRegisterSurfacesARefusal(t *testing.T) {
	s := newRegisterStub(t, func(n int64, w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"code":"PROVISIONING_FAILED","message":"provisioning failed: the provision key or secret is not valid"}`)
	})
	args := baseArgs(s, t.TempDir())
	_, _, err := runRegister(t, args...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401 PROVISIONING_FAILED")
	assert.Contains(t, err.Error(), "unchanged")
	assert.EqualValues(t, 1, s.requests.Load())
	_, statErr := os.Stat(args[len(args)-1])
	assert.NoError(t, statErr, "the credential was removed")
}

// The request goes over https unless plaintext is asked for by name.
func TestDeviceRegisterIsHTTPSByDefault(t *testing.T) {
	assert.Equal(t, "https://dc.example.com/api/device-management", registerBaseURL(&deviceRegisterFlags{server: "dc.example.com"}))
	assert.Equal(t, "http://localhost:8080/api/device-management", registerBaseURL(&deviceRegisterFlags{server: "localhost:8080", insecure: true}))
}

// Against a server that speaks only plaintext, the default (https) never sends the secret: the
// TLS handshake fails before a request exists, and the stub sees nothing.
func TestDeviceRegisterWithoutTheOptInNeverSendsPlaintext(t *testing.T) {
	s := newRegisterStub(t, func(n int64, w http.ResponseWriter) { created(w, "CREATED") })
	args := baseArgs(s, t.TempDir())
	for i, a := range args {
		if a == "--insecure-plaintext" {
			args = append(args[:i], args[i+1:]...)
			break
		}
	}
	_, _, err := runRegister(t, append(args, "--max-attempts", "1")...)
	require.Error(t, err)
	assert.Zero(t, s.requests.Load(), "the provision secret reached a plaintext server")
}

// A redirect is an error, and the body is not sent a second time.
func TestDeviceRegisterRefusesARedirect(t *testing.T) {
	target := newRegisterStub(t, func(n int64, w http.ResponseWriter) { created(w, "CREATED") })
	from := newRegisterStub(t, func(n int64, w http.ResponseWriter) {
		w.Header().Set("Location", target.srv.URL+"/api/device-management/provisioning/acme/register")
		w.WriteHeader(http.StatusPermanentRedirect)
	})
	_, _, err := runRegister(t, append(baseArgs(from, t.TempDir()), "--max-attempts", "3")...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to follow")
	assert.EqualValues(t, 1, from.requests.Load())
	assert.Zero(t, target.requests.Load(), "the secret was re-sent to the redirect target")
}

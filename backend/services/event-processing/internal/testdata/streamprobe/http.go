// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package streamprobe

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/stretchr/testify/require"
)

// HTTPProbe counts command enqueues through the production service-token transport.
type HTTPProbe struct {
	Client   *svcclient.Client
	URL      string
	enqueues atomic.Int64
}

func NewHTTP(t testing.TB) *HTTPProbe {
	t.Helper()
	p := &HTTPProbe{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == auth.ServiceTokenPath {
			if r.Header.Get(auth.ServiceSecretHeader) != "probe-secret" {
				http.Error(w, "bad secret", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "probe-token", ExpiresAt: 1 << 40})
			return
		}
		if r.URL.Path != "/graphql" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer probe-token" || r.Header.Get(auth.ServiceTenantHeader) == "" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		var request struct {
			Query     string `json:"query"`
			Variables struct {
				Request struct {
					Token string `json:"token"`
					Name  string `json:"name"`
				} `json:"request"`
			} `json:"variables"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || !strings.Contains(request.Query, "createCommand") || request.Variables.Request.Token == "" || request.Variables.Request.Name == "" {
			http.Error(w, "expected command enqueue", http.StatusBadRequest)
			return
		}
		p.enqueues.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"createCommand": map[string]any{"command": map[string]any{"token": request.Variables.Request.Token}, "rejection": nil}}})
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	require.NoError(t, err)
	p.Client = svcclient.New(config.UserManagementConfiguration{Hostname: u.Hostname(), Port: uint32(port), InternalPort: uint32(port)}, "probe-secret", "event-processing", []string{string(auth.DeviceRead)})
	p.URL = srv.URL + "/graphql"
	return p
}

func (p *HTTPProbe) CommandEnqueues() int64 { return p.enqueues.Load() }

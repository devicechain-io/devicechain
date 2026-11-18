// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/stretchr/testify/require"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

type recordingPositionSeams struct {
	grants   []auth.Authority
	endpoint config.ServiceEndpoint
	seam     svcclient.Seam
	err      error
}

func (s *recordingPositionSeams) Seam(endpoint config.ServiceEndpoint, grants ...auth.Authority) (svcclient.Seam, error) {
	s.endpoint = endpoint
	s.grants = append([]auth.Authority{}, grants...)
	return s.seam, s.err
}

func TestPositionOracleOwnSeamHasOnlyAlarmRead(t *testing.T) {
	endpoint := config.ServiceEndpoint{Hostname: "dm", Port: 80}
	seams := &recordingPositionSeams{}
	oracle, err := NewPositionOracle(seams, endpoint)
	require.NoError(t, err)
	require.NotNil(t, oracle)
	require.Equal(t, endpoint, seams.endpoint)
	require.Equal(t, []auth.Authority{auth.AlarmRead}, seams.grants)
	seams.err = errors.New("unconfigured")
	oracle, err = NewPositionOracle(seams, endpoint)
	require.Error(t, err)
	require.Nil(t, oracle)
}

func TestPositionOracleRequestAndMissingToken(t *testing.T) {
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, auth.ServiceTokenPath, r.URL.Path)
		var req auth.ServiceTokenRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, []string{string(auth.AlarmRead)}, req.Authorities)
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "oracle", ExpiresAt: 1 << 40})
	}))
	defer mint.Close()
	host, portString, err := net.SplitHostPort(mint.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portString)
	require.NoError(t, err)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "A", r.Header.Get(auth.ServiceTenantHeader))
		require.Equal(t, "Bearer oracle", r.Header.Get("Authorization"))
		var req struct {
			Query     string
			Variables struct{ Tokens []string }
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, `query($tokens:[String!]!){ alarmsByToken(tokens:$tokens){ token } }`, req.Query)
		require.Equal(t, []string{"plain", "gone"}, req.Variables.Tokens)
		_, _ = w.Write([]byte(`{"data":{"alarmsByToken":[{"token":"plain"}]}}`))
	}))
	defer target.Close()
	client := svcclient.New(config.UserManagementConfiguration{Hostname: host, Port: uint32(port), InternalPort: uint32(port)}, "secret", "notification-management", []string{string(auth.AlarmRead)})
	oracle, err := NewPositionOracle(&recordingPositionSeams{seam: svcclient.Seam{Client: client, URL: target.URL}}, config.ServiceEndpoint{})
	require.NoError(t, err)
	visible, err := oracle.VisibleWithoutLocation(core.WithTenant(context.Background(), "A"), []string{"plain", "gone"})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"plain": true}, visible)
	require.False(t, visible["gone"])
}

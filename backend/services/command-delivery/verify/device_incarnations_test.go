// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeviceIncarnationsLookupChunksAndKeepsIdentities(t *testing.T) {
	const createdAt = "2026-01-02T03:04:05.123456+02:00"
	calls := 0
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, "acme", r.Header.Get(auth.ServiceTenantHeader))
		var body struct {
			Query     string `json:"query"`
			Variables struct {
				Tokens []string `json:"tokens"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		assert.True(t, strings.Contains(body.Query, "{ token id createdAt }"))
		assert.LessOrEqual(t, len(body.Variables.Tokens), disabledPageSize)
		devices := make([]map[string]string, 0, len(body.Variables.Tokens))
		for _, token := range body.Variables.Tokens {
			if token != "gone" {
				devices = append(devices, map[string]string{"token": token, "id": "42", "createdAt": createdAt})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"devicesByToken": devices}})
	}))
	defer dm.Close()
	tokens := []string{"gone"}
	for i := 0; i < disabledPageSize; i++ {
		tokens = append(tokens, fmt.Sprintf("dev-%d", i))
	}
	source := NewDisabledSource(newSvcClient(t), dm.URL, "", "")
	ids, err := source.DeviceIncarnations(context.Background(), "acme", tokens)
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.Len(t, ids, disabledPageSize)
	wantCreatedAt, err := time.Parse(time.RFC3339, createdAt)
	require.NoError(t, err)
	want := DeviceIdentity{ID: 42, CreatedAt: wantCreatedAt}
	assert.Equal(t, want, ids["dev-0"])
	assert.Equal(t, want, ids[fmt.Sprintf("dev-%d", disabledPageSize-1)])
	_, exists := ids["gone"]
	assert.False(t, exists)
}

func TestDeviceIncarnationsLookupRejectsUnavailableOrInvalidIdentity(t *testing.T) {
	for _, body := range []string{
		`{"data":{}}`,
		`{"data":{"devicesByToken":[{"token":"dev","id":"bad","createdAt":"2026-01-02T03:04:05Z"}]}}`,
		`{"data":{"devicesByToken":[{"token":"dev","id":"42"}]}}`,
		`{"data":{"devicesByToken":[{"token":"dev","id":"42","createdAt":"bad"}]}}`,
		`{"data":{"devicesByToken":[{"token":"valid","id":"41","createdAt":"2026-01-02T03:04:05Z"},{"token":"dev","id":"42"}]}}`,
		`{"errors":[{"message":"unavailable"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer dm.Close()
			source := NewDisabledSource(newSvcClient(t), dm.URL, "", "")
			ids, err := source.DeviceIncarnations(context.Background(), "acme", []string{"dev"})
			require.Error(t, err)
			assert.Nil(t, ids)
		})
	}
}

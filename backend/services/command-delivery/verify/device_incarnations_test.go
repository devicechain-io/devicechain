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

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeviceIncarnationsLookupChunksAndKeepsIdentities(t *testing.T) {
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
		assert.True(t, strings.Contains(body.Query, "{ token id }"))
		assert.LessOrEqual(t, len(body.Variables.Tokens), disabledPageSize)
		devices := make([]map[string]string, 0, len(body.Variables.Tokens))
		for _, token := range body.Variables.Tokens {
			if token != "gone" {
				devices = append(devices, map[string]string{"token": token, "id": "42"})
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
	assert.EqualValues(t, 42, ids["dev-0"])
	assert.EqualValues(t, 42, ids[fmt.Sprintf("dev-%d", disabledPageSize-1)])
	_, exists := ids["gone"]
	assert.False(t, exists)
}

func TestDeviceIncarnationsLookupRejectsUnavailableOrInvalidIdentity(t *testing.T) {
	for _, body := range []string{
		`{"data":{}}`,
		`{"data":{"devicesByToken":[{"token":"dev","id":"bad"}]}}`,
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

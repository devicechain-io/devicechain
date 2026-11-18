// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Existing verdict fixtures answer the separate identity lookup with an empty roster.
func identityAwareHandler(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var request struct{ Query string }
		_ = json.Unmarshal(body, &request)
		if request.Query == enqueueDeviceIdentities {
			_, _ = w.Write([]byte(`{"data":{"devicesByToken":[]}}`))
			return
		}
		next(w, r)
	}
}

func TestEnqueueIdentityPrecedesValidation(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "single"
		if batch {
			name = "batch"
		}
		t.Run(name, func(t *testing.T) {
			var calls []string
			dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct{ Query string }
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				if request.Query == enqueueDeviceIdentities {
					calls = append(calls, "identity")
					_, _ = w.Write([]byte(`{"data":{"devicesByToken":[{"token":"dev","id":"17"}]}}`))
					return
				}
				calls = append(calls, "validation")
				assert.NotContains(t, request.Query, "devicesByToken")
				// A newer identity in this answer must never replace the earlier stamp.
				_, _ = w.Write([]byte(`{"data":{"validateCommandEnqueue":{"allowed":true},"deviceScopeAnchors":[{"deviceToken":"dev","anchors":[]}],"validateCommandEnqueueBatch":[],"devicesByToken":[{"token":"dev","id":"23"}]}}`))
			}))
			defer dm.Close()
			if batch {
				_, ids, err := NewBatchValidator(newSvcClient(t), dm.URL).ValidateEnqueueBatch(batchCtx(), []string{"dev"}, "reboot", nil)
				require.NoError(t, err)
				assert.EqualValues(t, 17, ids["dev"])
			} else {
				db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
				require.NoError(t, err)
				require.NoError(t, rdb.RegisterTenantScoping(db))
				require.NoError(t, db.AutoMigrate(&model.Command{}, &model.DeviceGate{}))
				api := model.NewApi(&rdb.RdbManager{Database: db})
				api.EnqueueValidator = NewEnqueueValidator(newSvcClient(t), dm.URL)
				command, err := api.CreateCommand(batchCtx(), &model.CommandCreateRequest{Token: "first-identity", DeviceToken: "dev", Name: "reboot"})
				require.NoError(t, err)
				rows, err := api.CommandsByToken(batchCtx(), []string{command.Token})
				require.NoError(t, err)
				require.Len(t, rows, 1)
				assert.True(t, rows[0].DeviceIncarnation.Valid)
				assert.EqualValues(t, 17, rows[0].DeviceIncarnation.Int64)
			}
			assert.Equal(t, []string{"identity", "validation"}, calls)
		})
	}
}

func TestEnqueueIdentityFailureStopsBeforeValidation(t *testing.T) {
	for _, body := range []string{`{"errors":[{"message":"unavailable"}]}`, `{"data":{}}`, `{"data":{"devicesByToken":[{"token":"dev","id":"bad"}]}}`} {
		t.Run(body, func(t *testing.T) {
			calls := 0
			dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var request struct{ Query string }
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				assert.Equal(t, enqueueDeviceIdentities, request.Query)
				_, _ = w.Write([]byte(body))
			}))
			defer dm.Close()
			_, _, err := NewEnqueueValidator(newSvcClient(t), dm.URL).ValidateEnqueue(batchCtx(), "dev", "reboot", nil)
			require.Error(t, err)
			_, _, err = NewBatchValidator(newSvcClient(t), dm.URL).ValidateEnqueueBatch(batchCtx(), []string{"dev"}, "reboot", nil)
			require.Error(t, err)
			assert.Equal(t, 2, calls)
		})
	}
}

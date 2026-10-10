// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

func TestCreateCommandRefusesOversizedInput(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	big := `{"k":"` + strings.Repeat("x", rdb.MaxJSONInputBytes) + `"}`

	cases := []struct {
		name string
		req  CommandCreateRequest
		want RejectionCode
	}{
		{"payload", CommandCreateRequest{Token: "c1", DeviceToken: "d1", Name: "reboot", Payload: &big}, RejectPayloadTooLarge},
		{"metadata", CommandCreateRequest{Token: "c2", DeviceToken: "d1", Name: "reboot", Metadata: &big}, RejectMetadataTooLarge},
		{"name", CommandCreateRequest{Token: "c3", DeviceToken: "d1", Name: strings.Repeat("n", MaxCommandNameLength+1)}, RejectNameTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := api.CreateCommand(ctx, &tc.req)
			var rej *EnqueueRejected
			if !errors.As(err, &rej) || rej.Code != tc.want {
				t.Fatalf("want rejection %s, got %v", tc.want, err)
			}
		})
	}
	// Control: the same request within the bounds is accepted.
	small := `{"k":"v"}`
	if _, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "ok", DeviceToken: "d1",
		Name: strings.Repeat("n", MaxCommandNameLength), Payload: &small}); err != nil {
		t.Fatalf("a request within the bounds must be accepted: %v", err)
	}
}

func TestBatchRequestRefusesOversizedInput(t *testing.T) {
	big := `{"k":"` + strings.Repeat("x", rdb.MaxJSONInputBytes) + `"}`
	tokens := []string{"d1"}
	req := &CommandBatchCreateRequest{Token: "b1", Name: "reboot", DeviceTokens: &tokens, Payload: &big}
	var rej *EnqueueRejected
	if err := validateBatchRequest(req); !errors.As(err, &rej) || rej.Code != RejectPayloadTooLarge {
		t.Fatalf("want PAYLOAD_TOO_LARGE, got %v", err)
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// retryCommandBatch goes through the schema as a VARIABLE, like every real client, so an input
// field the schema does not define is rejected by graphql-go rather than silently dropped.
const retryBatchMutation = `mutation($request: CommandBatchRetryRequest!) {
  retryCommandBatch(request: $request) {
    batch { token name retryOf accepted retries }
    rejection { code reason refusals { deviceToken code } }
  }
}`

type retryResult struct {
	RetryCommandBatch struct {
		Batch *struct {
			Token    string   `json:"token"`
			Name     string   `json:"name"`
			RetryOf  *string  `json:"retryOf"`
			Accepted int      `json:"accepted"`
			Retries  []string `json:"retries"`
		}
		Rejection *struct {
			Code     string `json:"code"`
			Refusals []wireRefusal
		}
	}
}

func TestRetryCommandBatchOnTheWire(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	created := createBatch(t, ctx, listRequest("wire-orig", "pump-a", "pump-b", "pump-c"))
	if created.CreateCommandBatch.Batch == nil {
		t.Fatalf("setup batch refused: %+v", created.CreateCommandBatch.Rejection)
	}

	// pump-a never left the platform (NOT_DELIVERED); pump-b stays in flight; pump-c likewise.
	var firstID uint
	origToken := "wire-orig"
	cmds, err := api.Commands(ctx, model.CommandSearchCriteria{BatchToken: &origToken})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cmds.Results {
		if c.DeviceToken == "pump-a" {
			firstID = c.ID
		}
	}
	if ok, err := api.MarkUndeliverable(ctx, firstID, "no command path"); err != nil || !ok {
		t.Fatalf("MarkUndeliverable: %v %v", ok, err)
	}

	// Outcome is a stored field, filterable and selectable on the wire.
	data := exec(t, ctx, `query($criteria: CommandSearchCriteria!) {
	  commands(criteria: $criteria) { results { deviceToken status outcome } }
	}`, map[string]any{"criteria": map[string]any{
		"pageNumber": 1, "pageSize": 100, "batchToken": "wire-orig", "outcomes": []any{"NOT_DELIVERED"},
	}})
	var found struct {
		Commands struct {
			Results []struct {
				DeviceToken string  `json:"deviceToken"`
				Status      string  `json:"status"`
				Outcome     *string `json:"outcome"`
			}
		}
	}
	if err := json.Unmarshal(data, &found); err != nil {
		t.Fatal(err)
	}
	if len(found.Commands.Results) != 1 || found.Commands.Results[0].DeviceToken != "pump-a" ||
		found.Commands.Results[0].Outcome == nil || *found.Commands.Results[0].Outcome != "NOT_DELIVERED" {
		t.Fatalf("outcome filter/field = %+v", found.Commands.Results)
	}

	retry := func(token string, devices ...any) retryResult {
		data := exec(t, ctx, retryBatchMutation, map[string]any{"request": map[string]any{
			"token": token, "of": "wire-orig", "deviceTokens": devices,
			"includeUnknown": false, "allowPartial": false,
		}})
		var out retryResult
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	ok := retry("wire-retry", "pump-a")
	if ok.RetryCommandBatch.Batch == nil || ok.RetryCommandBatch.Batch.RetryOf == nil ||
		*ok.RetryCommandBatch.Batch.RetryOf != "wire-orig" || ok.RetryCommandBatch.Batch.Name != "reboot" ||
		ok.RetryCommandBatch.Batch.Accepted != 1 {
		t.Fatalf("retry = %+v", ok.RetryCommandBatch)
	}

	// An in-flight device is refused with a coded rejection, not an error.
	refused := retry("wire-retry-2", "pump-b")
	if refused.RetryCommandBatch.Rejection == nil || refused.RetryCommandBatch.Rejection.Code != "RETRY_NOT_ELIGIBLE" ||
		refused.RetryCommandBatch.Rejection.Refusals[0].DeviceToken != "pump-b" {
		t.Fatalf("refusal = %+v", refused.RetryCommandBatch)
	}

	// The original lists its retries.
	data = exec(t, ctx, `query { commandBatchesByToken(tokens: ["wire-orig"]) { token retryOf retries } }`, nil)
	var orig struct {
		CommandBatchesByToken []struct {
			RetryOf *string  `json:"retryOf"`
			Retries []string `json:"retries"`
		}
	}
	if err := json.Unmarshal(data, &orig); err != nil {
		t.Fatal(err)
	}
	if len(orig.CommandBatchesByToken) != 1 || orig.CommandBatchesByToken[0].RetryOf != nil ||
		len(orig.CommandBatchesByToken[0].Retries) != 1 || orig.CommandBatchesByToken[0].Retries[0] != "wire-retry" {
		t.Fatalf("original = %+v", orig.CommandBatchesByToken)
	}
}

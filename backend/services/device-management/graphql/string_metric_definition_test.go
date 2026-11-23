// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
)

// A STRING metric can be declared, and it cannot be calculated: a formula reads and produces
// numbers and booleans, never text.
func TestAStringMetricIsDeclarableButNeverCalculated(t *testing.T) {
	ctx := deviceConfigurationCtx(t) // profile "prof"
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)

	if _, err := api.CreateMetricDefinition(ctx, &model.MetricDefinitionCreateRequest{Token: "mode-def",
		DeviceProfileToken: "prof", MetricKey: "mode", DataType: "STRING"}); err != nil {
		t.Fatalf("a STRING metric is refused: %v", err)
	}

	expr := "1 + 1"
	_, err := api.CreateMetricDefinition(ctx, &model.MetricDefinitionCreateRequest{Token: "calc-def",
		DeviceProfileToken: "prof", MetricKey: "calc", DataType: "STRING", Expression: &expr})
	if err == nil || !strings.Contains(err.Error(), "STRING metric cannot be calculated") {
		t.Fatalf("a calculated STRING metric was not refused: %v", err)
	}
}

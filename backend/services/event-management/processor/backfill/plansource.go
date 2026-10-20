// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backfill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"gorm.io/gorm"
)

// planQuery is the document sent to device-management's calculatedMetricPlan door. It selects
// exactly the fields of model.CalculatedPlanDoc, which a job stores verbatim and the worker
// parses refusing unknown fields, and is validated against that service's served schema by a test.
const planQuery = `query CalculatedMetricPlan($profileToken: String!, $version: Int) {
  calculatedMetricPlan(profileToken: $profileToken, version: $version) {
    versionToken version activeSince
    measured { key dataType }
    calculated { key expression dataType minValue maxValue unit classifier newInVersion changedSinceEarlier }
    constantKeys
  }
}`

// GraphQLPlanSource is the model.BackfillPlanSource over device-management's GraphQL doors,
// called with the same service token the roster uses (device:read). It acts for no user: the
// caller of a start was authorized before it is asked, and the plan it reads is the tenant's
// published profile.
type GraphQLPlanSource struct {
	client *svcclient.Client
	url    string
}

// NewGraphQLPlanSource returns the source reading from the device-management endpoint at url.
func NewGraphQLPlanSource(client *svcclient.Client, url string) *GraphQLPlanSource {
	return &GraphQLPlanSource{client: client, url: url}
}

// Plan implements model.BackfillPlanSource.
func (s *GraphQLPlanSource) Plan(ctx context.Context, tenant, profileToken string, version *int) (*model.CalculatedPlanDoc, json.RawMessage, error) {
	vars := map[string]any{"profileToken": profileToken}
	if version != nil {
		vars["version"] = *version
	}
	var out struct {
		Plan json.RawMessage `json:"calculatedMetricPlan"`
	}
	if err := s.client.Query(ctx, s.url, tenant, planQuery, vars, &out); err != nil {
		return nil, nil, mapDoorError(err, "the plan")
	}
	if len(out.Plan) == 0 || string(out.Plan) == "null" {
		return nil, nil, fmt.Errorf("device-management answered no plan for %q: %w", profileToken, gorm.ErrRecordNotFound)
	}
	doc, err := model.ParseCalculatedPlanDoc(out.Plan)
	if err != nil {
		return nil, nil, fmt.Errorf("device-management answered a plan this service cannot read: %w", err)
	}
	return doc, out.Plan, nil
}

// CheckDevices implements model.BackfillPlanSource by asking the roster door for one entry of the
// named devices: the door refuses, naming it, a token that is not a device of the profile.
func (s *GraphQLPlanSource) CheckDevices(ctx context.Context, tenant, profileToken string, deviceTokens []string) error {
	vars := map[string]any{
		"profileToken": profileToken, "constantKeys": []string{}, "deviceTokens": deviceTokens, "limit": 1,
	}
	var out json.RawMessage
	if err := s.client.Query(ctx, s.url, tenant, rosterQuery, vars, &out); err != nil {
		return mapDoorError(err, "the devices")
	}
	return nil
}

// mapDoorError gives a device-management failure the answer a caller of the backfill API gets.
func mapDoorError(err error, what string) error {
	var gqlErr *svcclient.GraphQLError
	if !errors.As(err, &gqlErr) {
		return &model.BackfillUnavailable{Reason: "device-management could not be reached to read " + what}
	}
	msg := strings.Join(gqlErr.Messages, "; ")
	for _, code := range gqlErr.Codes {
		switch code {
		case "NOT_FOUND":
			return fmt.Errorf("%s: %w", msg, gorm.ErrRecordNotFound)
		case integrity.CodeInvalid:
			return integrity.NewRefusal(integrity.ClassInvalid, msg)
		}
	}
	return fmt.Errorf("device-management refused to give %s: %s", what, msg)
}

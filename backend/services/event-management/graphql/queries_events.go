/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package graphql

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// GraphQL representation of the relationship anchor input.
type EventAnchorInput struct {
	Type string
	Id   string
}

// GraphQL representation of the event search criteria input.
type EventSearchCriteriaInput struct {
	PageNumber int32
	PageSize   int32
	DeviceId   *string
	EventTypes *[]int32
	StartTime  *string
	EndTime    *string
	Anchor     *EventAnchorInput
}

// Convert a GraphQL criteria input into the model search criteria.
func toEventSearchCriteria(in EventSearchCriteriaInput) (model.EventSearchCriteria, error) {
	criteria := model.EventSearchCriteria{
		Pagination: rdb.Pagination{
			PageNumber: in.PageNumber,
			PageSize:   in.PageSize,
		},
	}

	if in.DeviceId != nil {
		id, err := strconv.ParseUint(*in.DeviceId, 0, 64)
		if err != nil {
			return criteria, err
		}
		uid := uint(id)
		criteria.DeviceId = &uid
	}

	if in.EventTypes != nil {
		types := make([]esmodel.EventType, 0, len(*in.EventTypes))
		for _, t := range *in.EventTypes {
			types = append(types, esmodel.EventType(t))
		}
		criteria.EventTypes = types
	}

	if in.StartTime != nil {
		t, err := time.Parse(time.RFC3339, *in.StartTime)
		if err != nil {
			return criteria, err
		}
		criteria.StartTime = &t
	}

	if in.EndTime != nil {
		t, err := time.Parse(time.RFC3339, *in.EndTime)
		if err != nil {
			return criteria, err
		}
		criteria.EndTime = &t
	}

	if in.Anchor != nil {
		if !model.IsAnchorType(in.Anchor.Type) {
			return criteria, fmt.Errorf("unknown anchor type %q", in.Anchor.Type)
		}
		id, err := strconv.ParseUint(in.Anchor.Id, 0, 64)
		if err != nil {
			return criteria, err
		}
		uid := uint(id)
		atype := in.Anchor.Type
		criteria.AnchorType = &atype
		criteria.AnchorId = &uid
	}

	return criteria, nil
}

// List base events that match the given criteria.
func (r *SchemaResolver) Events(ctx context.Context, args struct {
	Criteria EventSearchCriteriaInput
}) (*EventSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	criteria, err := toEventSearchCriteria(args.Criteria)
	if err != nil {
		return nil, err
	}
	found, err := api.Events(ctx, criteria)
	if err != nil {
		return nil, err
	}
	return &EventSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}

// List location events that match the given criteria.
func (r *SchemaResolver) LocationEvents(ctx context.Context, args struct {
	Criteria EventSearchCriteriaInput
}) (*LocationEventSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	criteria, err := toEventSearchCriteria(args.Criteria)
	if err != nil {
		return nil, err
	}
	found, err := api.LocationEvents(ctx, criteria)
	if err != nil {
		return nil, err
	}
	return &LocationEventSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}

// List measurement events that match the given criteria.
func (r *SchemaResolver) MeasurementEvents(ctx context.Context, args struct {
	Criteria EventSearchCriteriaInput
}) (*MeasurementEventSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	criteria, err := toEventSearchCriteria(args.Criteria)
	if err != nil {
		return nil, err
	}
	found, err := api.MeasurementEvents(ctx, criteria)
	if err != nil {
		return nil, err
	}
	return &MeasurementEventSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}

// List alert events that match the given criteria.
func (r *SchemaResolver) AlertEvents(ctx context.Context, args struct {
	Criteria EventSearchCriteriaInput
}) (*AlertEventSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	criteria, err := toEventSearchCriteria(args.Criteria)
	if err != nil {
		return nil, err
	}
	found, err := api.AlertEvents(ctx, criteria)
	if err != nil {
		return nil, err
	}
	return &AlertEventSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}

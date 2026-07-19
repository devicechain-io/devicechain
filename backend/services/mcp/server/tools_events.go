// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- query_measurements ----

type QueryMeasurementsInput struct {
	DeviceToken string `json:"deviceToken" jsonschema:"the device whose measurement history to query"`
	StartTime   string `json:"startTime,omitempty" jsonschema:"inclusive RFC3339 start time (optional)"`
	EndTime     string `json:"endTime,omitempty" jsonschema:"exclusive RFC3339 end time (optional)"`
	PageNumber  int    `json:"pageNumber,omitempty" jsonschema:"1-based page number (default 1)"`
	PageSize    int    `json:"pageSize,omitempty" jsonschema:"events per page (default 25, max 100)"`
}

type MeasurementEvent struct {
	DeviceToken  string   `json:"deviceToken"`
	Name         string   `json:"name"`
	Value        *float64 `json:"value,omitempty"`
	Unit         string   `json:"unit,omitempty"`
	DataType     string   `json:"dataType,omitempty"`
	OccurredTime string   `json:"occurredTime,omitempty"`
}

type QueryMeasurementsOutput struct {
	Measurements []MeasurementEvent `json:"measurements"`
	TotalRecords int                `json:"totalRecords"`
}

const queryMeasurementsQuery = `query QueryMeasurements($criteria: EventSearchCriteria!) {
  measurementEvents(criteria: $criteria) {
    results { deviceToken name value unit dataType occurredTime }
    pagination { totalRecords }
  }
}`

// QueryMeasurements returns raw measurement history for a device (paged, bounded).
// For trends prefer aggregate_measurements — it returns far fewer rows.
func (t *Tools) QueryMeasurements(ctx context.Context, req *mcp.CallToolRequest, in QueryMeasurementsInput) (*mcp.CallToolResult, QueryMeasurementsOutput, error) {
	token, _, err := callerToken(req)
	if err != nil {
		return nil, QueryMeasurementsOutput{}, err
	}
	if in.DeviceToken == "" {
		return nil, QueryMeasurementsOutput{}, fmt.Errorf("deviceToken is required")
	}
	criteria := map[string]any{
		"deviceToken": in.DeviceToken,
		"pageNumber":  clampPageNumber(in.PageNumber),
		"pageSize":    clampPageSize(in.PageSize),
	}
	putIfSet(criteria, "startTime", in.StartTime)
	putIfSet(criteria, "endTime", in.EndTime)

	var resp struct {
		MeasurementEvents struct {
			Results    []MeasurementEvent `json:"results"`
			Pagination struct {
				TotalRecords int `json:"totalRecords"`
			} `json:"pagination"`
		} `json:"measurementEvents"`
	}
	if err := t.gql.Query(ctx, "event-management", token, queryMeasurementsQuery, map[string]any{"criteria": criteria}, &resp); err != nil {
		return nil, QueryMeasurementsOutput{}, err
	}
	return nil, QueryMeasurementsOutput{
		Measurements: resp.MeasurementEvents.Results,
		TotalRecords: resp.MeasurementEvents.Pagination.TotalRecords,
	}, nil
}

// ---- aggregate_measurements ----

type AggregateMeasurementsInput struct {
	DeviceToken     string `json:"deviceToken" jsonschema:"the device whose measurements to aggregate"`
	Name            string `json:"name,omitempty" jsonschema:"optional metric name to aggregate (omit for all)"`
	StartTime       string `json:"startTime,omitempty" jsonschema:"inclusive RFC3339 start time — ALWAYS provide a window: an unbounded aggregate returns every bucket over all history"`
	EndTime         string `json:"endTime,omitempty" jsonschema:"exclusive RFC3339 end time — provide together with startTime to bound the result"`
	IntervalSeconds int    `json:"intervalSeconds" jsonschema:"time-bucket width in seconds (required, e.g. 3600 for hourly). Buckets returned = window / interval, so keep the window and interval proportionate"`
}

type MeasurementBucket struct {
	BucketStart string   `json:"bucketStart"`
	Name        string   `json:"name"`
	Avg         *float64 `json:"avg,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	Sum         *float64 `json:"sum,omitempty"`
	Count       int      `json:"count"`
}

type AggregateMeasurementsOutput struct {
	Buckets []MeasurementBucket `json:"buckets"`
}

const aggregateMeasurementsQuery = `query AggregateMeasurements($criteria: MeasurementAggregationCriteria!) {
  bucketedMeasurements(criteria: $criteria) {
    bucketStart name avg min max sum count
  }
}`

// AggregateMeasurements returns time-bucketed avg/min/max/sum/count per metric
// (ADR-026 rollup). The token-efficient way to read trends over a window.
func (t *Tools) AggregateMeasurements(ctx context.Context, req *mcp.CallToolRequest, in AggregateMeasurementsInput) (*mcp.CallToolResult, AggregateMeasurementsOutput, error) {
	token, _, err := callerToken(req)
	if err != nil {
		return nil, AggregateMeasurementsOutput{}, err
	}
	if in.DeviceToken == "" {
		return nil, AggregateMeasurementsOutput{}, fmt.Errorf("deviceToken is required")
	}
	if in.IntervalSeconds <= 0 {
		return nil, AggregateMeasurementsOutput{}, fmt.Errorf("intervalSeconds must be positive")
	}
	criteria := map[string]any{
		"deviceToken":     in.DeviceToken,
		"intervalSeconds": in.IntervalSeconds,
	}
	putIfSet(criteria, "name", in.Name)
	putIfSet(criteria, "startTime", in.StartTime)
	putIfSet(criteria, "endTime", in.EndTime)

	var resp struct {
		BucketedMeasurements []MeasurementBucket `json:"bucketedMeasurements"`
	}
	if err := t.gql.Query(ctx, "event-management", token, aggregateMeasurementsQuery, map[string]any{"criteria": criteria}, &resp); err != nil {
		return nil, AggregateMeasurementsOutput{}, err
	}
	return nil, AggregateMeasurementsOutput{Buckets: resp.BucketedMeasurements}, nil
}

// ---- alarms ----

type AlarmSummary struct {
	Token            string   `json:"token"`
	OriginatorToken  string   `json:"originatorToken,omitempty"`
	AlarmKey         string   `json:"alarmKey"`
	MetricKey        string   `json:"metricKey,omitempty"`
	State            string   `json:"state"`
	Severity         string   `json:"severity"`
	Acknowledged     bool     `json:"acknowledged"`
	RaisedTime       string   `json:"raisedTime,omitempty"`
	ClearedTime      string   `json:"clearedTime,omitempty"`
	AcknowledgedTime string   `json:"acknowledgedTime,omitempty"`
	AcknowledgedBy   string   `json:"acknowledgedBy,omitempty"`
	LastValue        *float64 `json:"lastValue,omitempty"`
	Message          string   `json:"message,omitempty"`
}

const alarmFields = `token originatorToken alarmKey metricKey state severity acknowledged raisedTime clearedTime acknowledgedTime acknowledgedBy lastValue message`

type ListAlarmsInput struct {
	Originator   string `json:"originator,omitempty" jsonschema:"optional device token to filter alarms to one device"`
	State        string `json:"state,omitempty" jsonschema:"optional alarm state filter: one of ACTIVE, CLEARED (an active alarm is 'ACTIVE', not 'RAISED')"`
	Severity     string `json:"severity,omitempty" jsonschema:"optional severity filter: one of CRITICAL, MAJOR, MINOR, WARNING, INDETERMINATE"`
	AlarmKey     string `json:"alarmKey,omitempty" jsonschema:"optional alarm-key filter"`
	Acknowledged *bool  `json:"acknowledged,omitempty" jsonschema:"optional acknowledged filter"`
	PageNumber   int    `json:"pageNumber,omitempty" jsonschema:"1-based page number (default 1)"`
	PageSize     int    `json:"pageSize,omitempty" jsonschema:"alarms per page (default 25, max 100)"`
}

type ListAlarmsOutput struct {
	Alarms       []AlarmSummary `json:"alarms"`
	TotalRecords int            `json:"totalRecords"`
}

var listAlarmsQuery = `query ListAlarms($criteria: AlarmSearchCriteria!) {
  alarms(criteria: $criteria) {
    results { ` + alarmFields + ` }
    pagination { totalRecords }
  }
}`

// ListAlarms lists alarms in the caller's tenant with optional filters (paged).
func (t *Tools) ListAlarms(ctx context.Context, req *mcp.CallToolRequest, in ListAlarmsInput) (*mcp.CallToolResult, ListAlarmsOutput, error) {
	token, _, err := callerToken(req)
	if err != nil {
		return nil, ListAlarmsOutput{}, err
	}
	criteria := map[string]any{
		"pageNumber": clampPageNumber(in.PageNumber),
		"pageSize":   clampPageSize(in.PageSize),
	}
	putIfSet(criteria, "state", in.State)
	putIfSet(criteria, "severity", in.Severity)
	putIfSet(criteria, "alarmKey", in.AlarmKey)
	if in.Originator != "" {
		criteria["originatorType"] = "device"
		criteria["originator"] = in.Originator
	}
	if in.Acknowledged != nil {
		criteria["acknowledged"] = *in.Acknowledged
	}

	var resp struct {
		Alarms struct {
			Results    []AlarmSummary `json:"results"`
			Pagination struct {
				TotalRecords int `json:"totalRecords"`
			} `json:"pagination"`
		} `json:"alarms"`
	}
	if err := t.gql.Query(ctx, "device-management", token, listAlarmsQuery, map[string]any{"criteria": criteria}, &resp); err != nil {
		return nil, ListAlarmsOutput{}, err
	}
	return nil, ListAlarmsOutput{Alarms: resp.Alarms.Results, TotalRecords: resp.Alarms.Pagination.TotalRecords}, nil
}

type GetAlarmInput struct {
	Tokens []string `json:"tokens" jsonschema:"the alarm tokens to look up (max 50)"`
}

type GetAlarmOutput struct {
	Alarms []AlarmSummary `json:"alarms"`
}

var getAlarmQuery = `query GetAlarm($tokens: [String!]!) {
  alarmsByToken(tokens: $tokens) { ` + alarmFields + ` }
}`

// GetAlarm resolves alarms by token.
func (t *Tools) GetAlarm(ctx context.Context, req *mcp.CallToolRequest, in GetAlarmInput) (*mcp.CallToolResult, GetAlarmOutput, error) {
	token, _, err := callerToken(req)
	if err != nil {
		return nil, GetAlarmOutput{}, err
	}
	if err := requireTokens(in.Tokens); err != nil {
		return nil, GetAlarmOutput{}, err
	}
	var resp struct {
		AlarmsByToken []AlarmSummary `json:"alarmsByToken"`
	}
	if err := t.gql.Query(ctx, "device-management", token, getAlarmQuery, map[string]any{"tokens": in.Tokens}, &resp); err != nil {
		return nil, GetAlarmOutput{}, err
	}
	return nil, GetAlarmOutput{Alarms: resp.AlarmsByToken}, nil
}

// ---- list_commands ----

type ListCommandsInput struct {
	DeviceToken string `json:"deviceToken,omitempty" jsonschema:"optional device token to filter commands to one device"`
	Status      string `json:"status,omitempty" jsonschema:"optional command status filter: one of QUEUED, SENT, SUCCESSFUL, TIMEOUT, EXPIRED, FAILED"`
	PageNumber  int    `json:"pageNumber,omitempty" jsonschema:"1-based page number (default 1)"`
	PageSize    int    `json:"pageSize,omitempty" jsonschema:"commands per page (default 25, max 100)"`
}

type CommandSummary struct {
	Token         string `json:"token"`
	DeviceToken   string `json:"deviceToken"`
	Name          string `json:"name"`
	Status        string `json:"status"`
	QueuedTime    string `json:"queuedTime,omitempty"`
	SentTime      string `json:"sentTime,omitempty"`
	RespondedTime string `json:"respondedTime,omitempty"`
	Error         string `json:"error,omitempty"`
}

type ListCommandsOutput struct {
	Commands     []CommandSummary `json:"commands"`
	TotalRecords int              `json:"totalRecords"`
}

const listCommandsQuery = `query ListCommands($criteria: CommandSearchCriteria!) {
  commands(criteria: $criteria) {
    results { token deviceToken name status queuedTime sentTime respondedTime error }
    pagination { totalRecords }
  }
}`

// ListCommands lists dispatched commands in the caller's tenant with optional
// device/status filters (paged). Delivery lifecycle only; payloads are omitted.
func (t *Tools) ListCommands(ctx context.Context, req *mcp.CallToolRequest, in ListCommandsInput) (*mcp.CallToolResult, ListCommandsOutput, error) {
	token, _, err := callerToken(req)
	if err != nil {
		return nil, ListCommandsOutput{}, err
	}
	criteria := map[string]any{
		"pageNumber": clampPageNumber(in.PageNumber),
		"pageSize":   clampPageSize(in.PageSize),
	}
	putIfSet(criteria, "deviceToken", in.DeviceToken)
	putIfSet(criteria, "status", in.Status)

	var resp struct {
		Commands struct {
			Results    []CommandSummary `json:"results"`
			Pagination struct {
				TotalRecords int `json:"totalRecords"`
			} `json:"pagination"`
		} `json:"commands"`
	}
	if err := t.gql.Query(ctx, "command-delivery", token, listCommandsQuery, map[string]any{"criteria": criteria}, &resp); err != nil {
		return nil, ListCommandsOutput{}, err
	}
	return nil, ListCommandsOutput{Commands: resp.Commands.Results, TotalRecords: resp.Commands.Pagination.TotalRecords}, nil
}

// putIfSet adds a string criterion only when non-empty, so an omitted optional
// filter is absent from the criteria (not sent as "").
func putIfSet(m map[string]any, key, val string) {
	if val != "" {
		m[key] = val
	}
}

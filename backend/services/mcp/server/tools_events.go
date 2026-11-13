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

// MeasurementEvent is one raw reading. The metric name and unit are what the device reported,
// so they sit under Untrusted.
type MeasurementEvent struct {
	DeviceToken  string                `json:"deviceToken" prov:"id"`
	Value        *float64              `json:"value,omitempty"`
	DataType     string                `json:"dataType,omitempty" prov:"platform"`
	OccurredTime string                `json:"occurredTime,omitempty" prov:"platform"`
	Untrusted    *MeasurementUntrusted `json:"untrusted,omitempty"`
}

// measurementEventRaw is the shape event-management returns.
type measurementEventRaw struct {
	DeviceToken  string   `json:"deviceToken"`
	Name         string   `json:"name"`
	Value        *float64 `json:"value"`
	Unit         string   `json:"unit"`
	DataType     string   `json:"dataType"`
	OccurredTime string   `json:"occurredTime"`
}

type QueryMeasurementsOutput struct {
	Measurements []MeasurementEvent `json:"measurements"`
	TotalRecords int                `json:"totalRecords"`
}

var queryMeasurementsQuery = newDocument("event-management", `query QueryMeasurements($criteria: EventSearchCriteria!) {
  measurementEvents(criteria: $criteria) {
    results { deviceToken name value unit dataType occurredTime }
    pagination { totalRecords }
  }
}`)

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
			Results    []measurementEventRaw `json:"results"`
			Pagination struct {
				TotalRecords int `json:"totalRecords"`
			} `json:"pagination"`
		} `json:"measurementEvents"`
	}
	if err := t.gql.Query(ctx, queryMeasurementsQuery, token, map[string]any{"criteria": criteria}, &resp); err != nil {
		return nil, QueryMeasurementsOutput{}, err
	}
	out := QueryMeasurementsOutput{
		Measurements: []MeasurementEvent{},
		TotalRecords: resp.MeasurementEvents.Pagination.TotalRecords,
	}
	for _, m := range resp.MeasurementEvents.Results {
		out.Measurements = append(out.Measurements, MeasurementEvent{
			DeviceToken: m.DeviceToken, Value: m.Value, DataType: m.DataType, OccurredTime: m.OccurredTime,
			Untrusted: newMeasurementUntrusted(m.Name, m.Unit),
		})
	}
	return provenanceResult(out), out, nil
}

// ---- aggregate_measurements ----

type AggregateMeasurementsInput struct {
	DeviceToken     string `json:"deviceToken" jsonschema:"the device whose measurements to aggregate"`
	Name            string `json:"name,omitempty" jsonschema:"optional metric name to aggregate (omit for all). Omitting it returns one row per bucket per metric, so a wide window over many metrics is refused sooner"`
	StartTime       string `json:"startTime" jsonschema:"inclusive RFC3339 start time (required)"`
	EndTime         string `json:"endTime,omitempty" jsonschema:"RFC3339 end time; defaults to now"`
	IntervalSeconds int    `json:"intervalSeconds" jsonschema:"time-bucket width in seconds (required, e.g. 3600 for hourly). A request is capped at 10,000 buckets (window / interval) and at 50,000 rows (buckets x distinct metric names that match the request's filters; 1 name when name is given), so keep the window and interval proportionate"`
}

// MeasurementBucket is one time bucket of one metric. The metric name is what the device
// reported, so it sits under Untrusted.
type MeasurementBucket struct {
	BucketStart string                `json:"bucketStart" prov:"platform"`
	Avg         *float64              `json:"avg,omitempty"`
	Min         *float64              `json:"min,omitempty"`
	Max         *float64              `json:"max,omitempty"`
	Sum         *float64              `json:"sum,omitempty"`
	Count       int                   `json:"count"`
	Untrusted   *MeasurementUntrusted `json:"untrusted,omitempty"`
}

// measurementBucketRaw is the shape event-management returns.
type measurementBucketRaw struct {
	BucketStart string   `json:"bucketStart"`
	Name        string   `json:"name"`
	Avg         *float64 `json:"avg"`
	Min         *float64 `json:"min"`
	Max         *float64 `json:"max"`
	Sum         *float64 `json:"sum"`
	Count       int      `json:"count"`
}

type AggregateMeasurementsOutput struct {
	Buckets []MeasurementBucket `json:"buckets"`
}

var aggregateMeasurementsQuery = newDocument("event-management", `query AggregateMeasurements($criteria: MeasurementAggregationCriteria!) {
  bucketedMeasurements(criteria: $criteria) {
    bucketStart name avg min max sum count
  }
}`)

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
	if in.StartTime == "" {
		return nil, AggregateMeasurementsOutput{}, fmt.Errorf("startTime is required")
	}
	criteria := map[string]any{
		"deviceToken":     in.DeviceToken,
		"intervalSeconds": in.IntervalSeconds,
	}
	putIfSet(criteria, "name", in.Name)
	putIfSet(criteria, "startTime", in.StartTime)
	putIfSet(criteria, "endTime", in.EndTime)

	var resp struct {
		BucketedMeasurements []measurementBucketRaw `json:"bucketedMeasurements"`
	}
	if err := t.gql.Query(ctx, aggregateMeasurementsQuery, token, map[string]any{"criteria": criteria}, &resp); err != nil {
		return nil, AggregateMeasurementsOutput{}, err
	}
	out := AggregateMeasurementsOutput{Buckets: []MeasurementBucket{}}
	for _, b := range resp.BucketedMeasurements {
		out.Buckets = append(out.Buckets, MeasurementBucket{
			BucketStart: b.BucketStart, Avg: b.Avg, Min: b.Min, Max: b.Max, Sum: b.Sum, Count: b.Count,
			Untrusted: newMeasurementUntrusted(b.Name, ""),
		})
	}
	return provenanceResult(out), out, nil
}

// ---- alarms ----

// AlarmSummary is one alarm. The alarm key and metric key come from a rule an operator wrote,
// and the acknowledger is a person, so they sit under Untrusted.
type AlarmSummary struct {
	Token            string          `json:"token" prov:"id"`
	OriginatorToken  string          `json:"originatorToken,omitempty" prov:"id"`
	State            string          `json:"state" prov:"platform"`
	Severity         string          `json:"severity" prov:"platform"`
	Acknowledged     bool            `json:"acknowledged"`
	RaisedTime       string          `json:"raisedTime,omitempty" prov:"platform"`
	ClearedTime      string          `json:"clearedTime,omitempty" prov:"platform"`
	AcknowledgedTime string          `json:"acknowledgedTime,omitempty" prov:"platform"`
	LastValue        *float64        `json:"lastValue,omitempty"`
	Untrusted        *AlarmUntrusted `json:"untrusted,omitempty"`
}

// AlarmUntrusted is the operator-authored text of an alarm.
type AlarmUntrusted struct {
	AlarmKey       *Marked `json:"alarmKey,omitempty" prov:"operator"`
	MetricKey      *Marked `json:"metricKey,omitempty" prov:"operator"`
	AcknowledgedBy *Marked `json:"acknowledgedBy,omitempty" prov:"operator"`
}

// alarmRaw is the shape device-management returns.
type alarmRaw struct {
	Token            string   `json:"token"`
	OriginatorToken  string   `json:"originatorToken"`
	AlarmKey         string   `json:"alarmKey"`
	MetricKey        string   `json:"metricKey"`
	State            string   `json:"state"`
	Severity         string   `json:"severity"`
	Acknowledged     bool     `json:"acknowledged"`
	RaisedTime       string   `json:"raisedTime"`
	ClearedTime      string   `json:"clearedTime"`
	AcknowledgedTime string   `json:"acknowledgedTime"`
	AcknowledgedBy   string   `json:"acknowledgedBy"`
	LastValue        *float64 `json:"lastValue"`
}

func newAlarmSummaries(raw []alarmRaw) []AlarmSummary {
	out := make([]AlarmSummary, 0, len(raw))
	for _, a := range raw {
		u := &AlarmUntrusted{
			AlarmKey:       mark(SourceOperator, a.AlarmKey),
			MetricKey:      mark(SourceOperator, a.MetricKey),
			AcknowledgedBy: mark(SourceOperator, a.AcknowledgedBy),
		}
		if u.AlarmKey == nil && u.MetricKey == nil && u.AcknowledgedBy == nil {
			u = nil
		}
		out = append(out, AlarmSummary{
			Token: a.Token, OriginatorToken: a.OriginatorToken, State: a.State, Severity: a.Severity,
			Acknowledged: a.Acknowledged, RaisedTime: a.RaisedTime, ClearedTime: a.ClearedTime,
			AcknowledgedTime: a.AcknowledgedTime, LastValue: a.LastValue, Untrusted: u,
		})
	}
	return out
}

const alarmFields = `token originatorToken alarmKey metricKey state severity acknowledged raisedTime clearedTime acknowledgedTime acknowledgedBy lastValue`

type ListAlarmsInput struct {
	OriginatorToken string `json:"originatorToken,omitempty" jsonschema:"optional device token to filter alarms to one device"`
	State           string `json:"state,omitempty" jsonschema:"optional alarm state filter: one of ACTIVE, CLEARED (an active alarm is 'ACTIVE', not 'RAISED')"`
	Severity        string `json:"severity,omitempty" jsonschema:"optional severity filter: one of CRITICAL, MAJOR, MINOR, WARNING, INDETERMINATE"`
	AlarmKey        string `json:"alarmKey,omitempty" jsonschema:"optional alarm-key filter"`
	Acknowledged    *bool  `json:"acknowledged,omitempty" jsonschema:"optional acknowledged filter"`
	PageNumber      int    `json:"pageNumber,omitempty" jsonschema:"1-based page number (default 1)"`
	PageSize        int    `json:"pageSize,omitempty" jsonschema:"alarms per page (default 25, max 100)"`
}

type ListAlarmsOutput struct {
	Alarms       []AlarmSummary `json:"alarms"`
	TotalRecords int            `json:"totalRecords"`
}

var listAlarmsQuery = newDocument("device-management", `query ListAlarms($criteria: AlarmSearchCriteria!) {
  alarms(criteria: $criteria) {
    results { `+alarmFields+` }
    pagination { totalRecords }
  }
}`)

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
	if in.OriginatorToken != "" {
		criteria["originatorType"] = "device"
		criteria["originatorToken"] = in.OriginatorToken
	}
	if in.Acknowledged != nil {
		criteria["acknowledged"] = *in.Acknowledged
	}

	var resp struct {
		Alarms struct {
			Results    []alarmRaw `json:"results"`
			Pagination struct {
				TotalRecords int `json:"totalRecords"`
			} `json:"pagination"`
		} `json:"alarms"`
	}
	if err := t.gql.Query(ctx, listAlarmsQuery, token, map[string]any{"criteria": criteria}, &resp); err != nil {
		return nil, ListAlarmsOutput{}, err
	}
	out := ListAlarmsOutput{Alarms: newAlarmSummaries(resp.Alarms.Results), TotalRecords: resp.Alarms.Pagination.TotalRecords}
	return provenanceResult(out), out, nil
}

type GetAlarmInput struct {
	Tokens []string `json:"tokens" jsonschema:"the alarm tokens to look up (max 50)"`
}

type GetAlarmOutput struct {
	Alarms []AlarmSummary `json:"alarms"`
}

var getAlarmQuery = newDocument("device-management", `query GetAlarm($tokens: [String!]!) {
  alarmsByToken(tokens: $tokens) { `+alarmFields+` }
}`)

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
		AlarmsByToken []alarmRaw `json:"alarmsByToken"`
	}
	if err := t.gql.Query(ctx, getAlarmQuery, token, map[string]any{"tokens": in.Tokens}, &resp); err != nil {
		return nil, GetAlarmOutput{}, err
	}
	out := GetAlarmOutput{Alarms: newAlarmSummaries(resp.AlarmsByToken)}
	return provenanceResult(out), out, nil
}

// ---- list_commands ----

type ListCommandsInput struct {
	DeviceToken string `json:"deviceToken,omitempty" jsonschema:"optional device token to filter commands to one device"`
	// 🔴 This enumeration is the ONLY thing an MCP client reads to decide what it may
	// filter on. A status missing from the string is one an agent can never ask for —
	// not an error it can recover from, an invisible capability. It must therefore
	// carry every value in command-delivery's CommandStatus, non-terminal (QUEUED,
	// HELD, SENT, PARKED) then terminal (SUCCESSFUL, FAILED, TIMEOUT, EXPIRED,
	// CANCELLED). HELD, PARKED and CANCELLED are the three an agent most needs the
	// names of: HELD and PARKED are the two different reasons a command has not
	// moved — never dispatched versus dispatched to a device that turned out to be
	// unreachable — and CANCELLED is what a cancel now records instead of the EXPIRED
	// it used to share with TTL death.
	Status     string `json:"status,omitempty" jsonschema:"optional command status filter: one of QUEUED, HELD, SENT, PARKED, SUCCESSFUL, FAILED, TIMEOUT, EXPIRED, CANCELLED"`
	PageNumber int    `json:"pageNumber,omitempty" jsonschema:"1-based page number (default 1)"`
	PageSize   int    `json:"pageSize,omitempty" jsonschema:"commands per page (default 25, max 100)"`
}

// CommandSummary is one dispatched command. Its name is written by whoever issued it (an
// operator today) and its error is whatever the device answered with, so both sit under
// Untrusted.
type CommandSummary struct {
	Token         string            `json:"token" prov:"id"`
	DeviceToken   string            `json:"deviceToken" prov:"id"`
	Status        string            `json:"status" prov:"platform"`
	QueuedTime    string            `json:"queuedTime,omitempty" prov:"platform"`
	SentTime      string            `json:"sentTime,omitempty" prov:"platform"`
	RespondedTime string            `json:"respondedTime,omitempty" prov:"platform"`
	Untrusted     *CommandUntrusted `json:"untrusted,omitempty"`
}

// CommandUntrusted is the text of a dispatched command.
type CommandUntrusted struct {
	Name  *Marked `json:"name,omitempty" prov:"operator"`
	Error *Marked `json:"error,omitempty" prov:"device-reported"`
}

// commandRaw is the shape command-delivery returns.
type commandRaw struct {
	Token         string `json:"token"`
	DeviceToken   string `json:"deviceToken"`
	Name          string `json:"name"`
	Status        string `json:"status"`
	QueuedTime    string `json:"queuedTime"`
	SentTime      string `json:"sentTime"`
	RespondedTime string `json:"respondedTime"`
	Error         string `json:"error"`
}

type ListCommandsOutput struct {
	Commands     []CommandSummary `json:"commands"`
	TotalRecords int              `json:"totalRecords"`
}

var listCommandsQuery = newDocument("command-delivery", `query ListCommands($criteria: CommandSearchCriteria!) {
  commands(criteria: $criteria) {
    results { token deviceToken name status queuedTime sentTime respondedTime error }
    pagination { totalRecords }
  }
}`)

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
			Results    []commandRaw `json:"results"`
			Pagination struct {
				TotalRecords int `json:"totalRecords"`
			} `json:"pagination"`
		} `json:"commands"`
	}
	if err := t.gql.Query(ctx, listCommandsQuery, token, map[string]any{"criteria": criteria}, &resp); err != nil {
		return nil, ListCommandsOutput{}, err
	}
	out := ListCommandsOutput{Commands: []CommandSummary{}, TotalRecords: resp.Commands.Pagination.TotalRecords}
	for _, c := range resp.Commands.Results {
		u := &CommandUntrusted{Name: mark(SourceOperator, c.Name), Error: mark(SourceDevice, c.Error)}
		if u.Name == nil && u.Error == nil {
			u = nil
		}
		out.Commands = append(out.Commands, CommandSummary{
			Token: c.Token, DeviceToken: c.DeviceToken, Status: c.Status, QueuedTime: c.QueuedTime,
			SentTime: c.SentTime, RespondedTime: c.RespondedTime, Untrusted: u,
		})
	}
	return provenanceResult(out), out, nil
}

// putIfSet adds a string criterion only when non-empty, so an omitted optional
// filter is absent from the criteria (not sent as "").
func putIfSet(m map[string]any, key, val string) {
	if val != "" {
		m[key] = val
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxTokens bounds a multi-token lookup so one call can't fan out unboundedly.
const maxTokens = 50

// ---- get_device ----

type GetDeviceInput struct {
	Tokens []string `json:"tokens" jsonschema:"the device tokens to look up (max 50)"`
}

type GetDeviceOutput struct {
	Devices []DeviceSummary `json:"devices"`
}

var getDeviceQuery = newDocument("device-management", `query GetDevice($tokens: [String!]!) {
  devicesByToken(tokens: $tokens) {
    token name description externalId deviceType { token }
  }
}`)

// GetDevice resolves one or more devices by token.
func (t *Tools) GetDevice(ctx context.Context, req *mcp.CallToolRequest, in GetDeviceInput) (*mcp.CallToolResult, GetDeviceOutput, error) {
	token, _, err := callerToken(req)
	if err != nil {
		return nil, GetDeviceOutput{}, err
	}
	if err := requireTokens(in.Tokens); err != nil {
		return nil, GetDeviceOutput{}, err
	}
	var resp struct {
		DevicesByToken []struct {
			Token       string `json:"token"`
			Name        string `json:"name"`
			Description string `json:"description"`
			ExternalId  string `json:"externalId"`
			DeviceType  struct {
				Token string `json:"token"`
			} `json:"deviceType"`
		} `json:"devicesByToken"`
	}
	if err := t.gql.Query(ctx, getDeviceQuery, token, map[string]any{"tokens": in.Tokens}, &resp); err != nil {
		return nil, GetDeviceOutput{}, err
	}
	out := GetDeviceOutput{}
	for _, d := range resp.DevicesByToken {
		out.Devices = append(out.Devices, newDeviceSummary(d.Token, d.Name, d.Description, d.ExternalId, d.DeviceType.Token))
	}
	return provenanceResult(out), out, nil
}

// ---- get_device_state ----

type GetDeviceStateInput struct {
	DeviceTokens []string `json:"deviceTokens" jsonschema:"the device tokens whose live state to read (max 50)"`
}

type DeviceStateSummary struct {
	DeviceToken        string `json:"deviceToken" prov:"id"`
	Active             bool   `json:"active"`
	LastConnectTime    string `json:"lastConnectTime,omitempty" prov:"platform"`
	LastDisconnectTime string `json:"lastDisconnectTime,omitempty" prov:"platform"`
	LastActivityTime   string `json:"lastActivityTime,omitempty" prov:"platform"`
	InactivityTimeout  int    `json:"inactivityTimeout"`
	// PresenceSource says where Active came from: ASSERTED (a transport reported the
	// device's connection state directly) or INFERRED (derived from data arriving, or
	// not arriving, within InactivityTimeout). The two do not support the same
	// conclusion from Active == false, which is why it is reported at all — see the
	// tool description in server.go (ADR-067 decision 3).
	//
	// Deliberately NO omitempty: an empty value means the platform served no presence
	// source, and that must not be indistinguishable from a device the platform told
	// us nothing about. Omitting the key would make "we did not ask" and "the state is
	// inferred" look identical to a reader that only sees the emitted JSON.
	PresenceSource string `json:"presenceSource" prov:"platform"`
}

type GetDeviceStateOutput struct {
	States []DeviceStateSummary `json:"states"`
}

var getDeviceStateQuery = newDocument("device-state", `query GetDeviceState($deviceTokens: [String!]!) {
  deviceStatesByDeviceToken(deviceTokens: $deviceTokens) {
    deviceToken active lastConnectTime lastDisconnectTime lastActivityTime inactivityTimeout presenceSource
  }
}`)

// GetDeviceState reads the live last-known connectivity state per device.
func (t *Tools) GetDeviceState(ctx context.Context, req *mcp.CallToolRequest, in GetDeviceStateInput) (*mcp.CallToolResult, GetDeviceStateOutput, error) {
	token, _, err := callerToken(req)
	if err != nil {
		return nil, GetDeviceStateOutput{}, err
	}
	if err := requireTokens(in.DeviceTokens); err != nil {
		return nil, GetDeviceStateOutput{}, err
	}
	var resp struct {
		DeviceStatesByDeviceToken []DeviceStateSummary `json:"deviceStatesByDeviceToken"`
	}
	if err := t.gql.Query(ctx, getDeviceStateQuery, token, map[string]any{"deviceTokens": in.DeviceTokens}, &resp); err != nil {
		return nil, GetDeviceStateOutput{}, err
	}
	out := GetDeviceStateOutput{States: resp.DeviceStatesByDeviceToken}
	return provenanceResult(out), out, nil
}

// ---- get_latest_measurements ----

type GetLatestMeasurementsInput struct {
	DeviceToken string `json:"deviceToken" jsonschema:"the device token whose latest per-metric values to read"`
}

type LatestMeasurement struct {
	Value        *float64 `json:"value,omitempty"`
	DataType     string   `json:"dataType,omitempty" prov:"platform"`
	OccurredTime string   `json:"occurredTime" prov:"platform"`
	// Untrusted holds the metric name and unit: both come from what the device reports, so
	// they are text a device chose.
	Untrusted *MeasurementUntrusted `json:"untrusted,omitempty"`
}

// MeasurementUntrusted is the text of a measurement a device wrote.
type MeasurementUntrusted struct {
	Name *Marked `json:"name,omitempty" prov:"device-reported"`
	Unit *Marked `json:"unit,omitempty" prov:"device-reported"`
}

func newMeasurementUntrusted(name, unit string) *MeasurementUntrusted {
	u := &MeasurementUntrusted{Name: mark(SourceDevice, name), Unit: mark(SourceDevice, unit)}
	if u.Name == nil && u.Unit == nil {
		return nil
	}
	return u
}

type GetLatestMeasurementsOutput struct {
	Measurements []LatestMeasurement `json:"measurements"`
}

var latestMeasurementsQuery = newDocument("device-state", `query LatestMeasurements($deviceToken: String!) {
  latestMeasurements(deviceToken: $deviceToken) {
    name value unit dataType occurredTime
  }
}`)

// GetLatestMeasurements reads the last-known value of each metric for a device.
func (t *Tools) GetLatestMeasurements(ctx context.Context, req *mcp.CallToolRequest, in GetLatestMeasurementsInput) (*mcp.CallToolResult, GetLatestMeasurementsOutput, error) {
	token, _, err := callerToken(req)
	if err != nil {
		return nil, GetLatestMeasurementsOutput{}, err
	}
	if in.DeviceToken == "" {
		return nil, GetLatestMeasurementsOutput{}, fmt.Errorf("deviceToken is required")
	}
	var resp struct {
		LatestMeasurements []struct {
			Name         string   `json:"name"`
			Value        *float64 `json:"value"`
			Unit         string   `json:"unit"`
			DataType     string   `json:"dataType"`
			OccurredTime string   `json:"occurredTime"`
		} `json:"latestMeasurements"`
	}
	if err := t.gql.Query(ctx, latestMeasurementsQuery, token, map[string]any{"deviceToken": in.DeviceToken}, &resp); err != nil {
		return nil, GetLatestMeasurementsOutput{}, err
	}
	out := GetLatestMeasurementsOutput{Measurements: []LatestMeasurement{}}
	for _, m := range resp.LatestMeasurements {
		out.Measurements = append(out.Measurements, LatestMeasurement{
			Value: m.Value, DataType: m.DataType, OccurredTime: m.OccurredTime,
			Untrusted: newMeasurementUntrusted(m.Name, m.Unit),
		})
	}
	return provenanceResult(out), out, nil
}

// ---- get_device_capabilities ----

type GetDeviceCapabilitiesInput struct {
	DeviceToken string `json:"deviceToken" jsonschema:"the device token whose capability definitions to read"`
}

// MetricCapability is one metric the device profile defines. The key, name and unit are
// authored by a tenant operator, so they sit under Untrusted.
type MetricCapability struct {
	DataType  string                `json:"dataType,omitempty" prov:"platform"`
	Untrusted *MetricCapabilityText `json:"untrusted,omitempty"`
}

// MetricCapabilityText is the operator-authored text of a metric definition.
type MetricCapabilityText struct {
	MetricKey *Marked `json:"metricKey,omitempty" prov:"operator"`
	Name      *Marked `json:"name,omitempty" prov:"operator"`
	Unit      *Marked `json:"unit,omitempty" prov:"operator"`
}

// CommandCapability is one command in the device published vocabulary. Its key, name,
// description and parameter schema are operator-authored.
type CommandCapability struct {
	Untrusted *CommandCapabilityText `json:"untrusted,omitempty"`
}

// CommandCapabilityText is the operator-authored text of a command definition. The description
// and parameter schema come from the published definition, so an agent can describe what a
// command does and what arguments it takes without a second lookup against the profile.
type CommandCapabilityText struct {
	CommandKey      *Marked `json:"commandKey,omitempty" prov:"operator"`
	Name            *Marked `json:"name,omitempty" prov:"operator"`
	Description     *Marked `json:"description,omitempty" prov:"operator"`
	ParameterSchema *Marked `json:"parameterSchema,omitempty" prov:"operator"`
}

// metricDefinitionRaw and commandDefinitionRaw are the shapes the downstream query returns.
type metricDefinitionRaw struct {
	MetricKey string `json:"metricKey"`
	Name      string `json:"name"`
	Unit      string `json:"unit"`
	DataType  string `json:"dataType"`
}

type commandDefinitionRaw struct {
	CommandKey      string `json:"commandKey"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	ParameterSchema string `json:"parameterSchema"`
}

type GetDeviceCapabilitiesOutput struct {
	DeviceToken   string             `json:"deviceToken" prov:"id"`
	DeviceType    string             `json:"deviceType,omitempty" prov:"id"`
	Profile       string             `json:"profile,omitempty" prov:"id"`
	ActiveVersion *int               `json:"activeVersion,omitempty"`
	Metrics       []MetricCapability `json:"metrics"`
	// Commands is the device's PUBLISHED command vocabulary — what it actually
	// accepts, not what has been authored.
	Commands []CommandCapability `json:"commands"`
	// CommandsConstrained reports whether the profile restricts which commands may be
	// sent. When false the platform accepts ANY command key and Commands is empty, so
	// an empty list must NOT be read as "this device takes no commands" — it means the
	// vocabulary is open. This field exists because that is precisely the inference a
	// reader is most likely to get backwards.
	CommandsConstrained bool `json:"commandsConstrained"`
}

// Metrics still read the profile's DRAFT definitions; commands read the device's
// published vocabulary, which device-management resolves against the same snapshot the
// enqueue gate validates against. Both are fetched in one request.
var deviceCapabilitiesQuery = newDocument("device-management", `query DeviceCapabilities($tokens: [String!]!, $deviceToken: String!) {
  devicesByToken(tokens: $tokens) {
    token
    deviceType {
      token
      profile {
        token activeVersion
        metricDefinitions { metricKey name unit dataType }
      }
    }
  }
  deviceCommandVocabulary(deviceToken: $deviceToken) {
    constrained
    commands { commandKey name description parameterSchema }
  }
}`)

// GetDeviceCapabilities reports what a device can measure and what it can be told to do.
//
// COMMANDS are the device's PUBLISHED vocabulary — the same resolution the enqueue gate
// validates against, so a command reported here is one the platform will accept. This
// tool used to report the profile's DRAFT command definitions and warn the caller they
// might not be active, which is a warning an agent cannot act on: it could not tell which
// of the listed commands were real. Reporting what is true beats describing what is
// uncertain.
//
// METRICS are still the profile's DRAFT definitions, so they may name a metric the device
// does not yet report. activeVersion is the published version a device resolves, or null
// when the profile has never been published; the tool description tells the caller to read
// metrics against it. Moving metrics onto the published snapshot is a separate change —
// there is no published-metric listing yet.
func (t *Tools) GetDeviceCapabilities(ctx context.Context, req *mcp.CallToolRequest, in GetDeviceCapabilitiesInput) (*mcp.CallToolResult, GetDeviceCapabilitiesOutput, error) {
	token, _, err := callerToken(req)
	if err != nil {
		return nil, GetDeviceCapabilitiesOutput{}, err
	}
	if in.DeviceToken == "" {
		return nil, GetDeviceCapabilitiesOutput{}, fmt.Errorf("deviceToken is required")
	}
	var resp struct {
		DevicesByToken []struct {
			Token      string `json:"token"`
			DeviceType struct {
				Token   string `json:"token"`
				Profile *struct {
					Token             string                `json:"token"`
					ActiveVersion     *int                  `json:"activeVersion"`
					MetricDefinitions []metricDefinitionRaw `json:"metricDefinitions"`
				} `json:"profile"`
			} `json:"deviceType"`
		} `json:"devicesByToken"`
		DeviceCommandVocabulary *struct {
			Constrained bool                   `json:"constrained"`
			Commands    []commandDefinitionRaw `json:"commands"`
		} `json:"deviceCommandVocabulary"`
	}
	vars := map[string]any{"tokens": []string{in.DeviceToken}, "deviceToken": in.DeviceToken}
	if err := t.gql.Query(ctx, deviceCapabilitiesQuery, token, vars, &resp); err != nil {
		return nil, GetDeviceCapabilitiesOutput{}, err
	}
	if len(resp.DevicesByToken) == 0 {
		return nil, GetDeviceCapabilitiesOutput{}, fmt.Errorf("device %q not found", in.DeviceToken)
	}
	d := resp.DevicesByToken[0]
	out := GetDeviceCapabilitiesOutput{
		DeviceToken: d.Token,
		DeviceType:  d.DeviceType.Token,
		Metrics:     []MetricCapability{},
		Commands:    []CommandCapability{},
	}
	if p := d.DeviceType.Profile; p != nil {
		out.Profile = p.Token
		out.ActiveVersion = p.ActiveVersion
		for _, m := range p.MetricDefinitions {
			out.Metrics = append(out.Metrics, MetricCapability{
				DataType: m.DataType,
				Untrusted: &MetricCapabilityText{
					MetricKey: mark(SourceOperator, m.MetricKey),
					Name:      mark(SourceOperator, m.Name),
					Unit:      mark(SourceOperator, m.Unit),
				},
			})
		}
	}
	// A null vocabulary means device-management could not resolve the token. The device
	// read above already succeeded, so this is a race (deleted mid-query) rather than a
	// bad request: report an open vocabulary and no commands, which is what the enqueue
	// gate would say about a device it cannot find.
	if v := resp.DeviceCommandVocabulary; v != nil {
		out.CommandsConstrained = v.Constrained
		for _, c := range v.Commands {
			out.Commands = append(out.Commands, CommandCapability{Untrusted: &CommandCapabilityText{
				CommandKey:      mark(SourceOperator, c.CommandKey),
				Name:            mark(SourceOperator, c.Name),
				Description:     mark(SourceOperator, c.Description),
				ParameterSchema: mark(SourceOperator, c.ParameterSchema),
			}})
		}
	}
	return provenanceResult(out), out, nil
}

// requireTokens validates a multi-token input: non-empty, within the fan-out cap,
// and with no blank entries (a blank token would only burn a downstream query).
func requireTokens(tokens []string) error {
	if len(tokens) == 0 {
		return fmt.Errorf("at least one token is required")
	}
	if len(tokens) > maxTokens {
		return fmt.Errorf("too many tokens (max %d)", maxTokens)
	}
	for _, tok := range tokens {
		if tok == "" {
			return fmt.Errorf("token must not be empty")
		}
	}
	return nil
}

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

const getDeviceQuery = `query GetDevice($tokens: [String!]!) {
  devicesByToken(tokens: $tokens) {
    token name description externalId deviceType { token }
  }
}`

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
	if err := t.gql.Query(ctx, "device-management", token, getDeviceQuery, map[string]any{"tokens": in.Tokens}, &resp); err != nil {
		return nil, GetDeviceOutput{}, err
	}
	out := GetDeviceOutput{}
	for _, d := range resp.DevicesByToken {
		out.Devices = append(out.Devices, DeviceSummary{
			Token: d.Token, Name: d.Name, Description: d.Description,
			ExternalId: d.ExternalId, DeviceType: d.DeviceType.Token,
		})
	}
	return nil, out, nil
}

// ---- get_device_state ----

type GetDeviceStateInput struct {
	DeviceTokens []string `json:"deviceTokens" jsonschema:"the device tokens whose live state to read (max 50)"`
}

type DeviceStateSummary struct {
	DeviceToken        string `json:"deviceToken"`
	Active             bool   `json:"active"`
	LastConnectTime    string `json:"lastConnectTime,omitempty"`
	LastDisconnectTime string `json:"lastDisconnectTime,omitempty"`
	LastActivityTime   string `json:"lastActivityTime,omitempty"`
	InactivityTimeout  int    `json:"inactivityTimeout"`
}

type GetDeviceStateOutput struct {
	States []DeviceStateSummary `json:"states"`
}

const getDeviceStateQuery = `query GetDeviceState($deviceTokens: [String!]!) {
  deviceStatesByDeviceToken(deviceTokens: $deviceTokens) {
    deviceToken active lastConnectTime lastDisconnectTime lastActivityTime inactivityTimeout
  }
}`

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
	if err := t.gql.Query(ctx, "device-state", token, getDeviceStateQuery, map[string]any{"deviceTokens": in.DeviceTokens}, &resp); err != nil {
		return nil, GetDeviceStateOutput{}, err
	}
	return nil, GetDeviceStateOutput{States: resp.DeviceStatesByDeviceToken}, nil
}

// ---- get_latest_measurements ----

type GetLatestMeasurementsInput struct {
	DeviceToken string `json:"deviceToken" jsonschema:"the device token whose latest per-metric values to read"`
}

type LatestMeasurement struct {
	Name         string   `json:"name"`
	Value        *float64 `json:"value,omitempty"`
	Unit         string   `json:"unit,omitempty"`
	DataType     string   `json:"dataType,omitempty"`
	OccurredTime string   `json:"occurredTime"`
}

type GetLatestMeasurementsOutput struct {
	Measurements []LatestMeasurement `json:"measurements"`
}

const latestMeasurementsQuery = `query LatestMeasurements($deviceToken: String!) {
  latestMeasurements(deviceToken: $deviceToken) {
    name value unit dataType occurredTime
  }
}`

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
		LatestMeasurements []LatestMeasurement `json:"latestMeasurements"`
	}
	if err := t.gql.Query(ctx, "device-state", token, latestMeasurementsQuery, map[string]any{"deviceToken": in.DeviceToken}, &resp); err != nil {
		return nil, GetLatestMeasurementsOutput{}, err
	}
	return nil, GetLatestMeasurementsOutput{Measurements: resp.LatestMeasurements}, nil
}

// ---- get_device_capabilities ----

type GetDeviceCapabilitiesInput struct {
	DeviceToken string `json:"deviceToken" jsonschema:"the device token whose capability definitions to read"`
}

type MetricCapability struct {
	MetricKey string `json:"metricKey"`
	Name      string `json:"name,omitempty"`
	Unit      string `json:"unit,omitempty"`
	DataType  string `json:"dataType,omitempty"`
}

type CommandCapability struct {
	CommandKey string `json:"commandKey"`
	Name       string `json:"name,omitempty"`
	// Description and ParameterSchema come from the published definition, so an agent
	// can describe what a command does and what arguments it takes without a second
	// lookup against the profile.
	Description     string `json:"description,omitempty"`
	ParameterSchema string `json:"parameterSchema,omitempty"`
}

type GetDeviceCapabilitiesOutput struct {
	DeviceToken   string             `json:"deviceToken"`
	DeviceType    string             `json:"deviceType,omitempty"`
	Profile       string             `json:"profile,omitempty"`
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
const deviceCapabilitiesQuery = `query DeviceCapabilities($tokens: [String!]!, $deviceToken: String!) {
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
}`

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
					Token             string             `json:"token"`
					ActiveVersion     *int               `json:"activeVersion"`
					MetricDefinitions []MetricCapability `json:"metricDefinitions"`
				} `json:"profile"`
			} `json:"deviceType"`
		} `json:"devicesByToken"`
		DeviceCommandVocabulary *struct {
			Constrained bool                `json:"constrained"`
			Commands    []CommandCapability `json:"commands"`
		} `json:"deviceCommandVocabulary"`
	}
	vars := map[string]any{"tokens": []string{in.DeviceToken}, "deviceToken": in.DeviceToken}
	if err := t.gql.Query(ctx, "device-management", token, deviceCapabilitiesQuery, vars, &resp); err != nil {
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
		out.Metrics = p.MetricDefinitions
	}
	// A null vocabulary means device-management could not resolve the token. The device
	// read above already succeeded, so this is a race (deleted mid-query) rather than a
	// bad request: report an open vocabulary and no commands, which is what the enqueue
	// gate would say about a device it cannot find.
	if v := resp.DeviceCommandVocabulary; v != nil {
		out.CommandsConstrained = v.Constrained
		if v.Commands != nil {
			out.Commands = v.Commands
		}
	}
	return nil, out, nil
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

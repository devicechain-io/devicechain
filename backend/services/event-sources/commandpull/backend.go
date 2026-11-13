// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package commandpull is the platform side of the HTTP command pull, over the service plane:
// device-management proves a device's access token and says whether its type has opted in to the
// HTTP_PULL command channel, and command-delivery owns the commands the device pulls,
// acknowledges and answers.
package commandpull

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-event-sources/processor"
)

// GraphQLClient is the slice of svcclient.Client the backend needs.
type GraphQLClient interface {
	Query(ctx context.Context, baseURL, tenant, query string, variables map[string]any, out any) error
}

// The operations. Field names are pinned to the two schemas, and graphql-go rejects an unknown
// one, so a drift fails the call loudly rather than reading as an answer.
const (
	authQuery = `query($credentialId: String!) {
  authenticateCommandChannel(credentialId: $credentialId) { deviceToken deviceTypeToken commandChannel }
}`
	pullMutation = `mutation($deviceToken: String!, $limit: Int) {
  pullCommands(deviceToken: $deviceToken, limit: $limit) { token name payload deliveryMode attempt expiresAt }
}`
	ackMutation = `mutation($token: String!, $deviceToken: String!) {
  acknowledgeCommandDelivery(token: $token, deviceToken: $deviceToken)
}`
	respondMutation = `mutation($token: String!, $deviceToken: String!, $success: Boolean!, $payload: String, $error: String) {
  respondToPulledCommand(token: $token, deviceToken: $deviceToken, success: $success, payload: $payload, error: $error)
}`

	// channelHTTPPull is the command channel value that opts a device type in. It is
	// device-management's served string.
	channelHTTPPull = "HTTP_PULL"
)

// Backend is processor.CommandPullBackend over device-management and command-delivery.
type Backend struct {
	client GraphQLClient
	dmURL  string
	cdURL  string
}

var _ processor.CommandPullBackend = (*Backend)(nil)

// New binds a backend to a service-token client (holding device:read and command:claim) and the
// two GraphQL endpoints.
func New(client GraphQLClient, deviceManagementURL, commandDeliveryURL string) *Backend {
	return &Backend{client: client, dmURL: deviceManagementURL, cdURL: commandDeliveryURL}
}

// Authenticate implements processor.CommandPullBackend.
func (b *Backend) Authenticate(ctx context.Context, tenant, accessToken string) (*processor.PullDevice, error) {
	var out struct {
		Auth *struct {
			DeviceToken    string  `json:"deviceToken"`
			CommandChannel *string `json:"commandChannel"`
		} `json:"authenticateCommandChannel"`
	}
	if err := b.client.Query(ctx, b.dmURL, tenant, authQuery, map[string]any{"credentialId": accessToken}, &out); err != nil {
		return nil, err
	}
	if out.Auth == nil {
		return nil, nil
	}
	if out.Auth.DeviceToken == "" {
		return nil, fmt.Errorf("device-management authenticated a token and named no device")
	}
	return &processor.PullDevice{
		Token:   out.Auth.DeviceToken,
		OptedIn: out.Auth.CommandChannel != nil && *out.Auth.CommandChannel == channelHTTPPull,
	}, nil
}

// Pull implements processor.CommandPullBackend.
//
// 🔴 AN ABSENT LIST IS AN ERROR. "Nothing to pull" is the ordinary answer, so a response that
// lost the field would decode to the same thing and a device would wait out its poll for
// commands the platform never reported.
func (b *Backend) Pull(ctx context.Context, tenant, device string, limit int) ([]processor.PulledCommand, error) {
	var out struct {
		Pull *[]struct {
			Token        string  `json:"token"`
			Name         string  `json:"name"`
			Payload      *string `json:"payload"`
			DeliveryMode *string `json:"deliveryMode"`
			Attempt      int     `json:"attempt"`
			ExpiresAt    *string `json:"expiresAt"`
		} `json:"pullCommands"`
	}
	if err := b.client.Query(ctx, b.cdURL, tenant, pullMutation,
		map[string]any{"deviceToken": device, "limit": limit}, &out); err != nil {
		return nil, err
	}
	if out.Pull == nil {
		return nil, fmt.Errorf("command-delivery returned no command list")
	}
	cmds := make([]processor.PulledCommand, 0, len(*out.Pull))
	for _, c := range *out.Pull {
		cmds = append(cmds, processor.PulledCommand{Token: c.Token, Name: c.Name, Payload: c.Payload,
			DeliveryMode: c.DeliveryMode, Attempt: c.Attempt, ExpiresAt: c.ExpiresAt})
	}
	return cmds, nil
}

// Ack implements processor.CommandPullBackend.
func (b *Backend) Ack(ctx context.Context, tenant, device, command string) (processor.PullAnswer, error) {
	var out struct {
		Answer string `json:"acknowledgeCommandDelivery"`
	}
	if err := b.client.Query(ctx, b.cdURL, tenant, ackMutation,
		map[string]any{"token": command, "deviceToken": device}, &out); err != nil {
		return "", err
	}
	if out.Answer == "" {
		return "", fmt.Errorf("command-delivery returned no answer to an acknowledgement")
	}
	return processor.PullAnswer(out.Answer), nil
}

// Respond implements processor.CommandPullBackend.
func (b *Backend) Respond(ctx context.Context, tenant, device, command string, success bool,
	payload, errMsg *string) (processor.PullAnswer, error) {
	var out struct {
		Answer string `json:"respondToPulledCommand"`
	}
	vars := map[string]any{"token": command, "deviceToken": device, "success": success, "payload": payload, "error": errMsg}
	if err := b.client.Query(ctx, b.cdURL, tenant, respondMutation, vars, &out); err != nil {
		return "", err
	}
	if out.Answer == "" {
		return "", fmt.Errorf("command-delivery returned no answer to a response")
	}
	return processor.PullAnswer(out.Answer), nil
}

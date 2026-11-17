// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import (
	"context"
	"fmt"
)

const mRotateIntegrationToken = `mutation ProductRotateIntegrationToken($token:String!){rotateIntegrationToken(token:$token){issuedToken}}`

// RotateIntegrationToken issues a bearer token once, invalidating the previous token.
// The caller must deliver the result without logging or persisting it.
func RotateIntegrationToken(ctx context.Context, c Client, ep Endpoints, token string) (string, error) {
	var out struct {
		RotateIntegrationToken struct {
			IssuedToken string `json:"issuedToken"`
		} `json:"rotateIntegrationToken"`
	}
	if err := c.Query(ctx, ep.DeviceManagement, mRotateIntegrationToken, map[string]any{"token": token}, &out); err != nil {
		return "", err
	}
	if out.RotateIntegrationToken.IssuedToken == "" {
		return "", fmt.Errorf("rotateIntegrationToken returned an empty issued token")
	}
	return out.RotateIntegrationToken.IssuedToken, nil
}

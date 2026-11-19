// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// RestrictedSuppressionWindowToken is opaque and stable across hidden/deleted windows.
const RestrictedSuppressionWindowToken = "restricted"

func (api *Api) VisibleSuppressionWindowToken(ctx context.Context, token string) (string, error) {
	visible, err := api.VisibleSuppressionWindowTokens(ctx, []string{token})
	return visible[token], err
}

// VisibleSuppressionWindowTokens keeps service status suitable for notification
// delivery. Scoped humans must prove both storage and whole-window visibility.
func (api *Api) VisibleSuppressionWindowTokens(ctx context.Context, tokens []string) (map[string]string, error) {
	out := make(map[string]string, len(tokens))
	_, scoped := auth.ScopeFromContext(ctx)
	claims, _ := auth.ClaimsFromContext(ctx)
	if !scoped || (claims != nil && claims.TokenType == auth.TokenTypeService) {
		for _, token := range tokens {
			out[token] = token
		}
		return out, nil
	}
	keys := []string{}
	for _, token := range tokens {
		if token == "" {
			out[token] = ""
			continue
		}
		if _, ok := out[token]; !ok {
			keys = append(keys, token)
		}
		out[token] = RestrictedSuppressionWindowToken
	}
	for start := 0; start < len(keys); start += rdb.MaxLookupKeys {
		var rows []AlarmSuppressionWindow
		if err := api.RDB.DB(ctx).Where("token IN ?", keys[start:min(start+rdb.MaxLookupKeys, len(keys))]).Find(&rows).Error; err != nil {
			return nil, err
		}
		visible, err := api.filterSuppressionWindowsInScope(ctx, rows)
		if err != nil {
			return nil, err
		}
		for _, row := range visible {
			out[row.Token] = row.Token
		}
	}
	return out, nil
}

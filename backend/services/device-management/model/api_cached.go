// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

type CachedApi struct {
	API *Api
}

// Create a new API instance.
func NewCachedApi(api *Api) *CachedApi {
	capi := &CachedApi{
		API: api,
	}
	return capi
}

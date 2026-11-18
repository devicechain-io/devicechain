// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/rs/zerolog/log"
)

const enqueueDeviceIdentities = `query($deviceTokens: [String!]!) {
  devicesByToken(tokens: $deviceTokens) { token id }
}`

// Read identities BEFORE validation: ids only grow, so this identity is never newer
// than the device later validation saw. Recreation between reads stamps the OLD id,
// which the deletion floor refuses; that is the safe direction.
func readEnqueueIdentities(tokens []string, query func(string, map[string]any, any) error) (map[string]uint64, error) {
	var out struct {
		DevicesByToken *[]deviceIdentity `json:"devicesByToken"`
	}
	if err := query(enqueueDeviceIdentities, map[string]any{"deviceTokens": tokens}, &out); err != nil {
		return nil, err
	}
	if out.DevicesByToken == nil {
		return nil, fmt.Errorf("verify: device-management returned no device identities")
	}
	ids := make(map[string]uint64, len(*out.DevicesByToken))
	for _, device := range *out.DevicesByToken {
		id := parseDeviceIncarnation(device)
		if id == 0 {
			return nil, fmt.Errorf("verify: device-management returned no valid identity for %q", device.Token)
		}
		ids[device.Token] = id
	}
	return ids, nil
}

type deviceIdentity struct {
	Token string          `json:"token"`
	ID    json.RawMessage `json:"id"`
}

func parseDeviceIncarnation(device deviceIdentity) uint64 {
	var raw string
	if err := json.Unmarshal(device.ID, &raw); err != nil {
		log.Debug().Str("device", device.Token).Msg("Device incarnation is unavailable; leaving enqueue identity unknown.")
		return 0
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	// The backing column is a signed bigint, as are device-management's sequence ids.
	if err != nil || id == 0 || id > math.MaxInt64 {
		log.Debug().Str("device", device.Token).Msg("Device incarnation is unavailable; leaving enqueue identity unknown.")
		return 0
	}
	return id
}
